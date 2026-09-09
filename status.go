package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// status.go is the bottom status line.
//
// The tail is a STREAMING view — it appends to the terminal's own scrollback and
// never repaints in place — so a line pinned to the bottom of the screen has to
// come from the terminal, not from us redrawing. It does: DECSTBM
// (`ESC [ 1 ; h-1 r`) shrinks the scrolling region to everything above the last
// row, so ordinary output scrolls underneath a row we own outright. The bar is
// then written with the cursor saved and restored around it (`ESC 7` / `ESC 8`),
// which is what lets it coexist with the renderer's deferred trailing newline —
// the cursor may be sitting mid-line inside an open dot streak, and it goes back
// exactly there.
//
// Two consequences that are easy to get wrong:
//
//   - DECSTBM homes the cursor. Setting the region without saving/restoring
//     around it would drop the cursor at the top-left and the next line of the
//     transcript would overwrite the backfill. Every region change here is
//     wrapped in ESC 7 / ESC 8.
//   - The row has to be free BEFORE the region shrinks. After backfill the
//     cursor is usually on the last row (the screen is full), and that row is
//     about to stop scrolling; without making room, output would pile up on the
//     bar. `reserveRow` asks the terminal where the cursor is (DSR, `ESC [ 6 n`)
//     and scrolls one line if it's already at the bottom.
//
// It's a no-op without a tty on both ends (a piped run has no screen to pin to),
// and `--no-status` turns it off.

const (
	// statusMsgTTL is how long a keypress message (t/T/c/y/m) stays up.
	statusMsgTTL = 3 * time.Second
	// statusMinWidth is the narrowest terminal worth drawing a bar on.
	statusMinWidth = 20
	statusYellow   = "\x1b[1;38;2;240;200;110m"
	statusDim      = "\x1b[2m"
)

// statusInfo is the persistent half of the bar — what's true about the session
// right now. Rebuilt from live state on every draw, so it never goes stale.
type statusInfo struct {
	Agent    Agent
	Repo     string // repo/folder basename of the session's cwd
	Session  string // session id (shortened for display)
	Turns    int
	LastAt   time.Time // when the transcript last grew (zero = not yet)
	Pending  bool      // the agent is blocked on a question/permission
	Tools    toolStyleKind
	Theme    string
	Collapse int
	Mrkdwn   bool
	NoWrap   bool // the `w` toggle: wrapping suspended so a drag-select copies clean
}

type statusBar struct {
	tty      *os.File
	w, h     int
	msg      string
	msgUntil time.Time
	drawn    string // last line written, so a tick that changes nothing is silent
	off      bool   // suspended for an alt-screen overlay
}

// newStatusBar reserves the bottom row and returns the bar, or nil when the
// terminal is too small to give a row up. The tty must already be in cbreak/raw
// AND not yet have a reader goroutine on it — reserveRow reads the terminal's
// DSR reply. The caller decides whether a bar is wanted at all (a piped run or
// --no-status never gets here).
func newStatusBar(tty *os.File) *statusBar {
	if tty == nil {
		return nil
	}
	w, h := termSize(tty)
	if w < statusMinWidth || h < 4 {
		return nil
	}
	s := &statusBar{tty: tty, w: w, h: h}
	s.reserveRow()
	s.setRegion()
	return s
}

// reserveRow makes sure the cursor ends up INSIDE the region about to be set,
// not on the row the bar will own. If the terminal doesn't answer the DSR query
// in time it assumes the worst (cursor at the bottom, which is the normal state
// after a full-screen backfill) — a spare blank line is a far cheaper mistake
// than the alternative below.
func (s *statusBar) reserveRow() {
	if row, ok := queryCursorRow(s.tty); ok && row < s.h {
		return
	}
	scrollUpOneRow(s.tty)
}

// scrollUpOneRow frees the bottom row AND leaves the cursor above it.
//
// One newline is not enough and the difference is invisible until it isn't: a
// newline on the bottom row scrolls the screen but leaves the cursor on that
// same bottom row. Reserve with a single "\n" and every subsequent write lands
// on the bar's row, outside the scrolling region — so it never scrolls, and the
// bar's next repaint erases it. The transcript appears to flash and vanish and
// the tail looks dead (which is exactly how it was reported).
//
// Two newlines put two blank rows at the bottom; the cursor-up then parks on the
// first of them, with the bar's row free below.
func scrollUpOneRow(tty *os.File) { io.WriteString(tty, "\n\n\x1b[A") }

func (s *statusBar) setRegion() {
	// ESC 7 … ESC 8 because DECSTBM homes the cursor (see the file comment).
	fmt.Fprintf(s.tty, "\x1b7\x1b[1;%dr\x1b8", s.h-1)
}

func (s *statusBar) clearRegion() {
	io.WriteString(s.tty, "\x1b7\x1b[r\x1b8")
}

// setMessage puts a transient yellow message up for statusMsgTTL.
func (s *statusBar) setMessage(msg string, now time.Time) {
	if s == nil {
		return
	}
	s.msg, s.msgUntil = msg, now.Add(statusMsgTTL)
}

// update redraws the bar if anything it shows has changed, and expires a
// message that has had its three seconds. Called from the live loop's tick,
// after the transcript writer has flushed.
func (s *statusBar) update(info statusInfo, now time.Time) {
	if s == nil || s.off {
		return
	}
	if s.msg != "" && now.After(s.msgUntil) {
		s.msg = ""
	}
	line := statusLine(info, s.msg, s.w, now)
	if line == s.drawn {
		return
	}
	s.drawn = line
	// Save cursor, jump outside the scrolling region, clear the row, write,
	// come back. Nothing else may write to the terminal in between.
	fmt.Fprintf(s.tty, "\x1b7\x1b[%d;1H\x1b[2K%s\x1b8", s.h, line)
}

// resize re-reads the terminal size and re-establishes the region. The row the
// bar lives on has moved, so the old one is cleared first.
func (s *statusBar) resize() {
	if s == nil {
		return
	}
	w, h := termSize(s.tty)
	if w == s.w && h == s.h {
		return
	}
	s.clearRegion()
	grew := h != s.h
	s.w, s.h, s.drawn = w, h, ""
	if s.off {
		return
	}
	// A height change moves the bottom row, and terminals commonly leave the
	// cursor on it after a resize. There's no asking now — the keyboard reader
	// owns the tty and would swallow a DSR reply — so reserve unconditionally.
	// Costs a blank line per resize; the alternative is a dead-looking tail.
	if grew {
		scrollUpOneRow(s.tty)
	}
	s.setRegion()
}

// suspend gives the whole screen back — the alt-screen overlays (help, focus,
// the tree) draw full-height and would be clipped by our region.
func (s *statusBar) suspend() {
	if s == nil || s.off {
		return
	}
	s.off = true
	s.clearRegion()
}

// resume re-claims the bottom row after an overlay returns. The overlay left
// the alt-screen, so the cursor is back where the transcript left it.
func (s *statusBar) resume() {
	if s == nil || !s.off {
		return
	}
	s.off = false
	s.w, s.h = termSize(s.tty)
	s.drawn = ""
	s.setRegion()
}

// close restores the full scrolling region and wipes the bar. Must run before
// the terminal modes are restored on the way out, or the shell inherits a
// terminal that can only scroll h-1 rows.
func (s *statusBar) close() {
	if s == nil {
		return
	}
	fmt.Fprintf(s.tty, "\x1b7\x1b[%d;1H\x1b[2K\x1b8", s.h)
	s.clearRegion()
	s.off = true
}

// statusLine renders one bar (pure — no terminal, no clock beyond `now`).
//
// Left is what session you're looking at, right is how it's being rendered;
// when the terminal is too narrow the right half is dropped first, then the
// left is truncated, because "which session is this" outlives "what theme".
func statusLine(info statusInfo, msg string, w int, now time.Time) string {
	if msg != "" {
		return statusYellow + truncVisible(" "+msg, w) + reset
	}
	lefts, rights := statusLefts(info, now), statusRights(info)
	// Widest pair that fits. Left is the outer loop, so the right half gives way
	// first and only then does the left start shedding fields.
	for _, l := range lefts {
		for _, r := range rights {
			if gap := w - visWidth(l) - visWidth(r); gap >= 2 {
				return statusDim + l + strings.Repeat(" ", gap) + r + reset
			}
		}
	}
	return statusDim + truncVisible(lefts[len(lefts)-1], w) + reset
}

// statusLefts is the "which session am I looking at" half, widest first. The
// state field (idle age, or the blocked-agent flag) survives every trim — it's
// the one thing a tail is watched for.
func statusLefts(info statusInfo, now time.Time) []string {
	id, state := shortID(info.Session), ""
	switch {
	case info.Pending:
		state = "⁉ waiting for you"
	case !info.LastAt.IsZero():
		state = fmtAge(now.Sub(info.LastAt))
	}
	turns := ""
	if info.Turns > 0 {
		turns = plural(info.Turns, "turn")
	}
	var out []string
	for _, fields := range [][]string{
		{string(info.Agent), info.Repo, id, turns, state},
		{info.Repo, id, turns, state},
		{info.Repo, id, state},
		{id, state},
		{id},
	} {
		out = append(out, " "+strings.Join(nonEmpty(fields), " · "))
	}
	return out
}

// statusRights is the "how is it being rendered" half, widest first. `mrkdwn`
// outlives the theme name: it changes what a mouse-copy gives you, and it's the
// mode most easily forgotten.
func statusRights(info statusInfo) []string {
	tools, theme, mrk, collapse := info.Tools.label(), info.Theme, "", ""
	if info.Mrkdwn {
		mrk = "mrkdwn"
	}
	if info.Collapse > 0 {
		collapse = "collapse " + fmt.Sprint(info.Collapse)
	}
	// nowrap rides beside mrkdwn down to the narrowest variant: both are
	// off-normal modes you've toggled into, and prose suddenly running off the
	// right edge is exactly the thing you'd otherwise mistake for a bug.
	nowrap := ""
	if info.NoWrap {
		nowrap = "nowrap"
	}
	var out []string
	for _, fields := range [][]string{
		{tools, theme, collapse, mrk, nowrap, "? settings"},
		{tools, theme, mrk, nowrap, "? settings"},
		{tools, mrk, nowrap, "? settings"},
		{tools, mrk, nowrap, "?"},
		{mrk, nowrap, "?"},
	} {
		out = append(out, strings.Join(nonEmpty(fields), " · ")+" ")
	}
	return out
}

// statusRepo names the folder the session is running in. A worktree shows as
// `<checkout>@<worktree>`: its own basename is a task name ("status-line"),
// which on its own doesn't say which repo you're looking at — and worktrees are
// where most of this work happens. Pure string work, so it still answers for a
// worktree whose directory has since been deleted.
func statusRepo(cwd string) string {
	if cwd == "" {
		return ""
	}
	if parent := worktreeParent(cwd); parent != "" {
		rest := strings.TrimPrefix(cwd[len(parent):], "/.claude/worktrees/")
		name, _, _ := strings.Cut(rest, "/") // a subdir of the worktree still names it
		return filepath.Base(parent) + "@" + name
	}
	return filepath.Base(cwd)
}

// fmtAge renders how long the transcript has been quiet. Not fmtDur (focus.go):
// that one is built for short subagent runtimes and would render a two-hour
// silence as "125m", and it renders anything under a second as the empty
// string — which is exactly the moment the bar first appears.
func fmtAge(d time.Duration) string {
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm ago", int(d.Hours()), int(d.Minutes())%60)
	}
}

func nonEmpty(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// queryCursorRow asks the terminal for the cursor position (DSR 6) and parses
// the `ESC [ row ; col R` reply. The tty must be in a no-echo, non-canonical
// mode and must have no other reader, else the reply is lost or echoed.
func queryCursorRow(tty *os.File) (int, bool) {
	// A timed read (MIN 0, TIME 5 → 0.5s) rather than a deadline: this is the
	// same mechanism the focus overlay follows files with, and it works on a tty
	// where os.File read deadlines are not dependable. Restores cbreak after.
	saved, ok := setRawTimed(tty)
	if !ok {
		return 0, false
	}
	defer restoreCbreak(tty, saved)

	if _, err := io.WriteString(tty, "\x1b[6n"); err != nil {
		return 0, false
	}
	var buf []byte
	tmp := make([]byte, 32)
	for range 2 { // one timeout is tolerated; two means no answer is coming
		n, err := tty.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if row, ok := parseCursorReport(buf); ok {
				return row, true
			}
			continue
		}
		// A 0-byte timed read reports (0, io.EOF); that's a timeout, not EOF.
		if err != nil && err != io.EOF {
			break
		}
	}
	return 0, false
}

// parseCursorReport pulls the row out of a DSR reply (`ESC [ row ; col R`).
func parseCursorReport(b []byte) (int, bool) {
	s := string(b)
	i := strings.LastIndex(s, "\x1b[")
	if i < 0 {
		return 0, false
	}
	j := strings.IndexByte(s[i:], 'R')
	if j < 0 {
		return 0, false
	}
	var row, col int
	if _, err := fmt.Sscanf(s[i:i+j+1], "\x1b[%d;%dR", &row, &col); err != nil {
		return 0, false
	}
	return row, true
}
