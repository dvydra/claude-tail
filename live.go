package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// live.go — `--live`: the sessions that are running RIGHT NOW, one expanded
// block each.
//
// Everywhere else here, "is this session live?" is a guess: `pgrep`+`lsof` finds
// a claude process and its cwd but not which transcript it is writing, so
// buildClaudeTree marks the whole folder and hopes the newest file is the right
// one. Claude Code answers the question itself. Every running session registers
// at `<config-dir>/sessions/<pid>.json` — pid, sessionId, cwd, version and a
// busy/idle status — which is the registry behind its own peer messaging
// (`peerProtocol`). Reading it turns the guess into a fact, with no subprocesses
// at all: pid → session id directly, no mtime race, no argv scraping, no tap.
//
// Three things about that file are load-bearing here.
//
//  1. **It is removed on exit, but only a CLEAN exit.** A SIGKILLed claude
//     leaves its entry behind, so the pid is the liveness test, not the file's
//     presence — `syscall.Kill(pid, 0)`, every tick, free. A pid that has been
//     recycled onto some unrelated process is caught by `liveComm`, which checks
//     the process name ONCE per newly-seen pid and caches it: the expensive
//     check runs on the rare event (a new session), never on the 1s tick.
//  2. **`updatedAt` is not a heartbeat.** Measured on idle sessions it lags
//     113–167s behind wall clock, so liveness can never be a freshness window,
//     and the block says "idle 15m" from `statusUpdatedAt` rather than implying
//     a live pulse.
//  3. **`procStart` is malformed** (a session started 16:54:54 records
//     "Wed Sep 16 06:54:54 2026"), so it is read for nothing. It looks like the
//     obvious pid-reuse guard and it is not one — hence liveComm.
//
// A hand-started `claude` that has not taken a turn yet is absent from the
// registry: the session id is minted with the first turn, which is also when
// the transcript appears. So "no entry" means "nothing to tail", which is the
// answer we want anyway.
//
// Split the usual three ways (as tree.go is): collect + pure render + reducer,
// then a thin tty driver at the bottom.

const (
	liveTailDefault = 6  // transcript lines under each block
	liveTailMax     = 40 // `+` ceiling; past this the list stops being a list
	liveRefresh     = time.Second
	liveCursorMark  = "▸"
	liveIndent      = "    "

	// setRawTimed asks for TIME 5 (500ms), so any 0-byte read that returns much
	// faster than that came from a dead fd rather than an idle terminal. 50 of
	// them in a row ends the view — about 5s of spinning worst case, and
	// unreachable from a terminal a human is sitting at.
	liveReadFloor = 100 * time.Millisecond
	liveMaxSpins  = 50
)

// liveSession is one entry of Claude Code's running-session registry. The json
// tags mirror the file; everything below Raw is ours.
type liveSession struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	StartedAt       int64  `json:"startedAt"`       // ms
	UpdatedAt       int64  `json:"updatedAt"`       // ms
	StatusUpdatedAt int64  `json:"statusUpdatedAt"` // ms
	Version         string `json:"version"`
	Kind            string `json:"kind"`       // interactive | ...
	Entrypoint      string `json:"entrypoint"` // cli | ...
	Name            string `json:"name"`       // derived, e.g. "claude-tail-b3"
	Status          string `json:"status"`     // busy | idle
	SocketPath      string `json:"messagingSocketPath"`

	Raw     string // the file verbatim, for the `j` toggle
	Profile string // "" default account, "personal" (see profile.go)
	Path    string // resolved transcript, "" when it isn't on disk yet
	Branch  string // git branch of Cwd, best-effort
}

// Busy reports whether this session has work in flight.
func (s liveSession) Busy() bool { return s.Status == "busy" }

// Label is what to call the session: Claude's derived name, else the short id.
func (s liveSession) Label() string {
	if s.Name != "" {
		return s.Name
	}
	return shortID(s.SessionID)
}

// liveRoot is one account's registry directory plus the profile that owns it.
type liveRoot struct {
	Dir     string
	Profile string
}

// liveRoots lists every account's registry dir, default first, mirroring
// claudeProfiles. The registry lives beside projects/ under the SAME config dir,
// so a personal session (CLAUDE_CONFIG_DIR=~/.claude-personal) registers in its
// own tree and would be invisible to a single-root read.
func liveRoots(home string) []liveRoot {
	profs := claudeProfiles(home)
	out := make([]liveRoot, 0, len(profs))
	for _, p := range profs {
		out = append(out, liveRoot{Dir: filepath.Join(p.Dir, "sessions"), Profile: p.Name})
	}
	return out
}

// parseLiveSession decodes one registry file. ok=false for anything that isn't a
// complete entry — the directory also holds `.key` files, and the json is
// rewritten in place, so a read can catch it half-written. Both must be skipped
// silently rather than rendered as a blank block.
func parseLiveSession(data []byte) (liveSession, bool) {
	var s liveSession
	if err := json.Unmarshal(data, &s); err != nil {
		return liveSession{}, false
	}
	if s.PID <= 0 || s.SessionID == "" {
		return liveSession{}, false
	}
	s.Raw = string(data)
	return s, true
}

// collectLiveSessions reads every root's registry and keeps the entries whose
// process is still running, busy ones first and then by most recent status
// change. alive is injected so the ordering and filtering are testable without
// real processes.
func collectLiveSessions(roots []liveRoot, alive func(pid int) bool) []liveSession {
	var out []liveSession
	for _, root := range roots {
		names, err := filepath.Glob(filepath.Join(root.Dir, "*.json"))
		if err != nil {
			continue
		}
		for _, p := range names {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			s, ok := parseLiveSession(data)
			if !ok || !alive(s.PID) {
				continue
			}
			s.Profile = root.Profile
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Busy() != out[j].Busy() {
			return out[i].Busy()
		}
		if out[i].StatusUpdatedAt != out[j].StatusUpdatedAt {
			return out[i].StatusUpdatedAt > out[j].StatusUpdatedAt
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// uptime renders a duration in the two largest units that fit: 9s, 1m 04s,
// 3h 12m, 2d 02h. Negative input (clock skew between the registry's ms stamps
// and our seconds) reads as 0 rather than printing a minus sign.
func uptime(secs int64) string {
	if secs < 0 {
		secs = 0
	}
	switch {
	case secs < 60:
		return strconv.FormatInt(secs, 10) + "s"
	case secs < 3600:
		return fmt.Sprintf("%dm %02ds", secs/60, secs%60)
	case secs < 86400:
		return fmt.Sprintf("%dh %02dm", secs/3600, (secs%3600)/60)
	default:
		return fmt.Sprintf("%dd %02dh", secs/86400, (secs%86400)/3600)
	}
}

// worktreeLabel names the worktree a cwd sits in — the name, plus any
// subdirectory the session was started in. "" when the cwd isn't a worktree.
// worktreeParent does the path work (and keeps working for a worktree whose
// directory has already been deleted, which is the normal end state once the
// work merges); this only has to name the segment after it.
func worktreeLabel(cwd string) string {
	parent := worktreeParent(cwd)
	if parent == "" {
		return ""
	}
	return strings.TrimPrefix(cwd[len(parent):], "/.claude/worktrees/")
}

// ── render ───────────────────────────────────────────────────────────────────

type liveBlockOpts struct {
	Now      int64 // epoch seconds
	Home     string
	Width    int
	Theme    Theme
	Selected bool
	ShowJSON bool
}

// liveBlockLines renders one session as its multi-line block. Every line is
// clipped to the width by truncVisible, which counts display columns and skips
// ANSI, so colour never costs layout.
func liveBlockLines(s liveSession, tail []string, o liveBlockOpts) []string {
	w := o.Width
	if w < 20 {
		w = 20
	}
	dim, reset := o.Theme.DimANSI, ""
	if dim != "" {
		reset = "\x1b[0m"
	}
	mark := "  "
	if o.Selected {
		mark = liveCursorMark + " "
	}

	glyph, statusCol := "○", dim
	if s.Busy() {
		glyph, statusCol = "◉", o.Theme.ClaudeANSI
	}
	head := fmt.Sprintf("%s%s %s %s%s%s", mark, glyph, padVisible(s.Label(), 22),
		statusCol, s.Status, reset)
	head += fmt.Sprintf("%s · pid %d · v%s%s", dim, s.PID, s.Version, reset)

	// Truncation can land before a line's trailing reset, which would leave the
	// dim colour running to end-of-line and into the next row. A second reset
	// after the cut is harmless when one already survived.
	clip := func(l string) string { return truncVisible(l, w) + reset }
	lines := []string{clip(head)}
	add := func(body string) { lines = append(lines, clip(liveIndent+body)) }

	kind := strings.TrimSpace(s.Kind + " · " + s.Entrypoint)
	add(dim + s.SessionID + "  " + strings.Trim(kind, " ·") + profileSuffix(s.Profile) + reset)

	// A worktree's own path already contains its checkout, so printing both the
	// full cwd and a separate "worktree of <checkout>" line says the same thing
	// twice and costs a row in a view that shows several blocks at once. Show the
	// checkout once, then the worktree name.
	cwd, wt := tildify(s.Cwd, o.Home), worktreeLabel(s.Cwd)
	if wt != "" {
		cwd = tildify(worktreeParent(s.Cwd), o.Home) + "  ⑂ " + wt
	}
	if s.Branch != "" {
		cwd += "  ⎇ " + s.Branch
	}
	add(cwd)

	// Two ages, and they answer different questions: how long this agent has
	// been up, and how long it has been in its current state. statusUpdatedAt
	// is a real edge (the moment it flipped), unlike updatedAt.
	meta := "up " + uptime(o.Now-s.StartedAt/1000)
	if s.StatusUpdatedAt > 0 {
		meta += " · " + s.Status + " " + uptime(o.Now-s.StatusUpdatedAt/1000)
	}
	if s.SocketPath != "" {
		meta += " · " + s.SocketPath
	}
	add(dim + meta + reset)

	if o.ShowJSON {
		for _, l := range wrapPlain(s.Raw, w-len(liveIndent)-2) {
			add(dim + "· " + l + reset)
		}
	}
	for _, l := range tail {
		add(dim + "│ " + reset + l)
	}
	return lines
}

// tailGlance reduces rendered transcript lines to the last n that carry content.
// Two kinds of line are dropped BEFORE the window rather than clipped after it,
// because the budget here is about six rows and a row spent on chrome is a row
// not spent on what the agent actually said:
//
//   - blank lines, which a markdown body emits freely (visWidth rather than a
//     plain emptiness test, since a "blank" line is usually a lone ANSI reset);
//   - the renderer's "⋯ <ts>" turn markers (render.go), which stand in for a
//     collapsed participant change. In a full transcript they're a useful seam;
//     in a six-line glance three of them can crowd out every line of text.
//
// Everything else — tool dots, the ⧗ task-note line — is content and stays.
func tailGlance(lines []string, n int) []string {
	if n <= 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if visWidth(t) == 0 || isTurnMarker(t) {
			continue
		}
		out = append(out, l)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// isTurnMarker matches the renderer's dim "⋯ 2026-09-17 09:30:42" seam: the
// marker rune leading the visible text, followed only by a timestamp. The width
// bound keeps a body line that merely opens with an ellipsis from being eaten.
func isTurnMarker(trimmed string) bool {
	i := strings.Index(trimmed, "⋯ ")
	// Only whitespace and escapes may precede it — the renderer indents the
	// marker and colours it dim, so both are in front of the rune.
	if i < 0 || visWidth(strings.TrimSpace(trimmed[:i])) != 0 {
		return false // the marker isn't the first thing you'd see on the line
	}
	return visWidth(trimmed) <= 28
}

// profileSuffix marks a personal-account session, matching the tree's pink @.
func profileSuffix(profile string) string {
	if profile == personalProfile {
		return "  @" + personalProfile
	}
	return ""
}

// wrapPlain hard-wraps a string with no ANSI in it (the raw registry json) at w
// columns. Nothing here needs word boundaries — it's a json blob being shown
// verbatim, so breaking mid-token is correct.
func wrapPlain(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var out []string
	for rs := []rune(s); len(rs) > 0; {
		n := min(w, len(rs))
		out = append(out, string(rs[:n]))
		rs = rs[n:]
	}
	return out
}

// liveUI is the whole view state. Sessions and Tails are refreshed by the
// driver; everything else is moved by updateLive.
type liveUI struct {
	Sessions   []liveSession
	Tails      map[string][]string // session id → rendered transcript tail
	Cursor     int
	Width      int
	Height     int
	Now        int64
	Home       string
	Theme      Theme
	TailN      int
	ShowJSON   bool
	NoRegistry bool // no account has a sessions/ dir: claude predates the registry
	Quit       bool
	Refresh    bool       // `r`: rebuild now rather than waiting for the tick
	Result     treeResult // set by Enter / t
}

// renderLive paints the whole screen: every session's block, then a key hint.
func renderLive(ui liveUI) string {
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")

	dim, reset := ui.Theme.DimANSI, ""
	if dim != "" {
		reset = "\x1b[0m"
	}

	switch {
	case ui.NoRegistry:
		b.WriteString("  no session registry under ~/.claude/sessions\r\n\r\n")
		b.WriteString(dim + "  --live reads the registry Claude Code writes for each running\r\n")
		b.WriteString("  session; it needs claude 2.1.273 or newer.\r\n" + reset)
	case len(ui.Sessions) == 0:
		b.WriteString("  no live claude sessions\r\n\r\n")
		b.WriteString(dim + "  watching — a session appears here once it takes its first turn.\r\n" + reset)
	default:
		blocks := make([][]string, len(ui.Sessions))
		heights := make([]int, len(ui.Sessions))
		for i, s := range ui.Sessions {
			blocks[i] = liveBlockLines(s, ui.Tails[s.SessionID], liveBlockOpts{
				Now: ui.Now, Home: ui.Home, Width: ui.Width, Theme: ui.Theme,
				Selected: i == ui.Cursor, ShowJSON: ui.ShowJSON,
			})
			heights[i] = len(blocks[i])
		}
		start, end := liveWindow(heights, ui.Cursor, ui.Height)
		for i := start; i < end; i++ {
			if i > start {
				b.WriteString("\r\n")
			}
			b.WriteString(strings.Join(blocks[i], "\r\n") + "\r\n")
		}
		if start > 0 || end < len(ui.Sessions) {
			b.WriteString(dim + fmt.Sprintf("  (%d–%d of %d)", start+1, end, len(ui.Sessions)) + reset + "\r\n")
		}
	}

	b.WriteString("\r\n" + dim + "  ↑↓ move · ⏎ workspace · t tail · j json · +/- tail · r refresh · q quit" + reset)
	return b.String()
}

// updateLive is the reducer: a key in, the next state out. It never touches the
// terminal and never reads the disk — the driver does both.
func updateLive(ui liveUI, k treeKey, r rune) liveUI {
	ui.Refresh = false
	ui.Result = treeNone
	switch k {
	case kUp:
		ui.Cursor--
	case kDown:
		ui.Cursor++
	case kHome:
		ui.Cursor = 0
	case kEnd:
		ui.Cursor = len(ui.Sessions) - 1
	case kEsc, kCtrlC:
		ui.Quit = true
	case kEnter:
		if len(ui.Sessions) > 0 {
			ui.Result = treeWorkspace
		}
	case kRune:
		switch r {
		case 'q':
			ui.Quit = true
		case 't':
			if len(ui.Sessions) > 0 {
				ui.Result = treeChosen
			}
		case 'j':
			ui.ShowJSON = !ui.ShowJSON
		case 'r':
			ui.Refresh = true
		case '+', '=':
			ui.TailN = min(ui.TailN+2, liveTailMax)
		case '-', '_':
			ui.TailN = max(ui.TailN-2, 0)
		}
	}
	ui.clampLive()
	return ui
}

// clampLive keeps the cursor inside the list. There is no scroll offset to keep
// in sync — the visible window is derived from the cursor at render time (see
// liveWindow), so it can never go stale against a list that changes under it
// every second.
func (ui *liveUI) clampLive() {
	ui.Cursor = max(0, min(ui.Cursor, len(ui.Sessions)-1))
}

// liveWindow picks the run of blocks to draw: the cursor's block always, then as
// many neighbours as the row budget allows, reaching upwards first so the list
// reads top-down until the cursor is pushed off the bottom. Blocks are variable
// height (a worktree line, a `j` json dump, a resized tail), which is why this is
// measured rather than a fixed rows-per-item offset.
func liveWindow(heights []int, cursor, budget int) (start, end int) {
	if len(heights) == 0 {
		return 0, 0
	}
	cursor = max(0, min(cursor, len(heights)-1))
	start, end = cursor, cursor+1
	used := heights[cursor]
	for i := cursor - 1; i >= 0; i-- {
		if used+1+heights[i] > budget {
			break
		}
		used += 1 + heights[i]
		start = i
	}
	for i := cursor + 1; i < len(heights); i++ {
		if used+1+heights[i] > budget {
			break
		}
		used += 1 + heights[i]
		end = i + 1
	}
	return start, end
}

// liveReadStall decides what an EMPTY read means, given how long it took and
// how many empty reads preceded it. It returns the new consecutive count and
// whether the fd looks dead.
//
// This exists because a raw TIMED read reports its 0-byte timeout as
// (0, io.EOF) — the focus.go gotcha — so error alone cannot separate "the user
// isn't typing" from "the terminal is gone". Treating EOF as quit would close
// the view on the first quiet tick; ignoring it spins the loop at full tilt when
// the terminal really has closed. The clock separates them: stty set TIME 5, so
// an idle read costs ~500ms while a dead fd returns at once.
func liveReadStall(elapsed time.Duration, spins int) (int, bool) {
	if elapsed >= liveReadFloor {
		return 0, false
	}
	spins++
	return spins, spins > liveMaxSpins
}

// liveChoice turns a selected row into the treeChoice the rest of the program
// already knows how to act on, so ⏎ and `t` behave exactly as they do in the
// tree (workspace launch, in-place tail, the picker↔tail loop in run).
func liveChoice(ui liveUI) treeChoice {
	if ui.Result == treeNone || len(ui.Sessions) == 0 {
		return treeChoice{Result: treeNone}
	}
	s := ui.Sessions[max(0, min(ui.Cursor, len(ui.Sessions)-1))]
	return treeChoice{
		Result:  ui.Result,
		Path:    s.Path,
		Cwd:     s.Cwd,
		ID:      s.SessionID,
		Account: s.Profile,
	}
}

// ── driver (IO: processes, disk, tty; not unit-tested) ───────────────────────

// pidAlive reports whether a pid is running. Signal 0 checks for the process
// without delivering anything; EPERM means it exists but isn't ours, which for
// our purposes is still "running".
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// liveCommCache remembers, per pid, whether that pid really is a claude. The
// registry entry of a SIGKILLed session outlives it, and the pid can in
// principle be recycled onto something else — but checking a process name costs
// a subprocess, so it runs once when a pid is first seen and never again on the
// refresh tick.
var liveCommCache = map[int]bool{}

// liveIsClaude verifies a pid's process name, cached. Anything that can't be
// asked (no ps, odd platform) is trusted rather than dropped: the registry entry
// plus a live pid is already strong evidence, and silently hiding a running
// session is the worse failure.
func liveIsClaude(pid int) bool {
	if v, ok := liveCommCache[pid]; ok {
		return v
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	v := true
	if err == nil {
		name := strings.TrimSpace(string(out))
		v = filepath.Base(name) == "claude"
	}
	liveCommCache[pid] = v
	return v
}

// liveAlive is the composed liveness test the real view uses.
func liveAlive(pid int) bool { return pidAlive(pid) && liveIsClaude(pid) }

// buildLiveSessions collects the registry and fills in what only the disk knows:
// each session's transcript path and git branch.
func buildLiveSessions(home string) ([]liveSession, bool) {
	roots := liveRoots(home)
	haveRegistry := false
	for _, r := range roots {
		if isDir(r.Dir) {
			haveRegistry = true
		}
	}
	sessions := collectLiveSessions(roots, liveAlive)
	for i := range sessions {
		sessions[i].Path = liveTranscriptPath(home, sessions[i])
		sessions[i].Branch = gitBranchOf(sessions[i].Cwd)
	}
	return sessions, haveRegistry
}

// liveTranscriptPath locates a session's jsonl. The id is known exactly, so this
// is a stat in the cwd's project dir per account — no newest-file heuristic.
func liveTranscriptPath(home string, s liveSession) string {
	for _, projects := range claudeProjectsRoots(home) {
		if p := filepath.Join(projects, claudeSlug(s.Cwd), s.SessionID+".jsonl"); isFile(p) {
			return p
		}
	}
	var patterns []string
	for _, projects := range claudeProjectsRoots(home) {
		patterns = append(patterns, filepath.Join(projects, "*", s.SessionID+".jsonl"))
	}
	return newestAcross(patterns)
}

// gitBranchOf reads a cwd's current branch, best-effort. A worktree whose
// directory is gone simply has no branch.
func gitBranchOf(cwd string) string {
	if cwd == "" || !isDir(cwd) {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// liveTail renders the last n lines of a session's transcript, cached on the
// transcript's mtime so the 1s tick costs a stat per session rather than a
// render. Without that the view would re-read every transcript every second.
type liveTailCache struct {
	mtime int64
	lines []string
}

func refreshTails(sessions []liveSession, n int, home string, theme Theme, cache map[string]liveTailCache) map[string][]string {
	out := make(map[string][]string, len(sessions))
	for _, s := range sessions {
		if s.Path == "" || n <= 0 {
			continue
		}
		mt := fileMtimeNano(s.Path)
		c, ok := cache[s.SessionID]
		if !ok || c.mtime != mt {
			c = liveTailCache{mtime: mt, lines: renderPreviewLines(s.Path, home, theme)}
			cache[s.SessionID] = c
		}
		out[s.SessionID] = tailGlance(c.lines, n)
	}
	// Drop cache entries for sessions that have gone away.
	for id := range cache {
		if _, still := out[id]; !still {
			delete(cache, id)
		}
	}
	return out
}

// runLive is the `--live` entry point. On a tty it runs the interactive view and
// returns the picked session; piped, it prints a static dump and returns
// ok=false so the caller exits without tailing anything.
func runLive(home string, theme Theme) (treeChoice, bool) {
	if ttyUsable() {
		// treeNone here means the alt-screen never opened (no /dev/tty, stty
		// refused). Fall through to the dump rather than exiting silently — the
		// user asked to see what's live, so print it.
		if c := runLiveTUI(home, theme); c.Result != treeNone {
			return c, true
		}
	}
	sessions, haveRegistry := buildLiveSessions(home)
	dumpLive(os.Stdout, sessions, haveRegistry, home, time.Now().Unix())
	return treeChoice{Result: treeNone}, false
}

// dumpLive is the non-tty rendering: one line per live session, in the shape
// --list uses, so `entire-tail --live | grep` is useful.
func dumpLive(w io.Writer, sessions []liveSession, haveRegistry bool, home string, now int64) {
	if !haveRegistry {
		fmt.Fprintln(w, "no session registry under ~/.claude/sessions (needs claude 2.1.273 or newer)")
		return
	}
	if len(sessions) == 0 {
		fmt.Fprintln(w, "no live claude sessions")
		return
	}
	for _, s := range sessions {
		fmt.Fprintf(w, "%-6s %-22s pid %-7d up %-10s %s  %s\n",
			s.Status, s.Label(), s.PID, uptime(now-s.StartedAt/1000),
			shortID(s.SessionID), tildify(s.Cwd, home))
	}
}

// runLiveTUI owns the alt-screen. The read is raw+TIMED (as focus.go is), so a
// keystroke is handled at once while a quiet terminal still returns every 500ms
// to re-poll the registry — that is what makes busy/idle flip in place and an
// exited session disappear.
//
// Gotcha inherited from focus.go: a timed read reports a 0-byte timeout as
// (0, io.EOF). Treating that as end-of-input exits the view instantly.
func runLiveTUI(home string, theme Theme) treeChoice {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return treeChoice{Result: treeNone}
	}
	defer tty.Close()
	saved, ok := setRawTimed(tty)
	if !ok {
		return treeChoice{Result: treeNone}
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return treeChoice{Result: treeNone}
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	ui := liveUI{Home: home, Theme: theme, TailN: liveTailDefault}
	cache := map[string]liveTailCache{}
	buf := make([]byte, 16)
	var last time.Time
	spins := 0 // consecutive suspiciously-fast empty reads; see the loop below

	for {
		if time.Since(last) >= liveRefresh {
			sessions, haveRegistry := buildLiveSessions(home)
			ui.Sessions, ui.NoRegistry = sessions, !haveRegistry
			ui.Tails = refreshTails(ui.Sessions, ui.TailN, home, theme, cache)
			ui.Now = time.Now().Unix()
			last = time.Now()
			ui.clampLive()
		}
		w, h := termSize(tty)
		ui.Width, ui.Height = w, h-2
		if ui.Height < 1 {
			ui.Height = 1
		}
		io.WriteString(tty, renderLive(ui))

		started := time.Now()
		n, err := tty.Read(buf)
		if n == 0 {
			if err != nil && err != io.EOF { // a real read error, not a timeout
				return treeChoice{Result: treeNone}
			}
			var dead bool
			if spins, dead = liveReadStall(time.Since(started), spins); dead {
				return treeChoice{Result: treeNone}
			}
			continue // timeout → next refresh tick
		}
		spins = 0
		k, r := decodeKey(buf[:n])
		ui = updateLive(ui, k, r)
		if ui.Quit {
			return treeChoice{Result: treeQuit}
		}
		if ui.Refresh {
			last = time.Time{}
		}
		if ui.Result != treeNone {
			return liveChoice(ui)
		}
	}
}
