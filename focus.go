package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// focus.go is the subagent focus overlay reached with `→` during a live tail.
// It takes over the tty in an alt-screen, renders the selected subagent's
// transcript full-screen, and lets you cycle channels with ←/→ while it
// live-follows the file. Esc/q returns to the scrolling tail underneath.
//
// It runs on the main (render) goroutine while the keyboard goroutine is parked,
// so it's the sole tty reader + writer for its lifetime — no races with the
// live loop.

// focusChrome is the number of fixed rows (header, divider, footer) around the
// scrollable body.
const focusChrome = 3

func focusBody(h int) int { return max(h-focusChrome, 1) }

// runFocus runs the alt-screen focus overlay for a session's subagents on the
// shared tty (the same fd the keyboard goroutine reads — it's parked for the
// overlay's lifetime, so there's a single reader). A no-op (with a one-line
// stderr hint) when there's no tty or no subagents yet.
func runFocus(tty *os.File, mainPath, home string, theme Theme) {
	if tty == nil {
		return
	}
	chans := discoverSubagents(mainPath)
	if len(chans) == 0 {
		fmt.Fprintln(os.Stderr, "entire-tail: no subagents in this session yet")
		return
	}
	// The tty is currently in cbreak (the live-tail mode); switch to raw+timed
	// for the overlay and restore cbreak on the way out.
	saved, ok := setRawTimed(tty)
	if !ok {
		return
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	sel := len(chans) - 1 // default to the most recently spawned
	var lines []string
	lastSig := ""
	top := 0
	atBottom := true

	load := func() {
		lines = renderChannel(chans[sel], home, theme)
		lastSig = fileSig(chans[sel].Path)
	}
	switchTo := func(i int) {
		sel = i
		top, atBottom = 0, true
		load()
	}
	load()

	buf := make([]byte, 16)
	for {
		w, h := termSize(tty)
		if sig := fileSig(chans[sel].Path); sig != lastSig { // followed file grew
			lines = renderChannel(chans[sel], home, theme)
			lastSig = sig
		}
		body := focusBody(h)
		maxTop := max(len(lines)-body, 0)
		if atBottom {
			top = maxTop
		}
		top = min(max(top, 0), maxTop)
		io.WriteString(tty, drawFocus(chans, sel, lines, top, w, h, theme))

		n, err := tty.Read(buf)
		// A raw timed read (MIN 0, TIME 5) reports a 0-byte timeout as
		// (0, io.EOF) via os.File.Read — that's our follow tick, NOT end of
		// input. Only a real read error (tty gone) should abort.
		if err != nil && err != io.EOF {
			return
		}
		if n == 0 {
			continue // follow tick → loop and re-check the file
		}
		// A timed read (MIN 0) can wake on the lone ESC of an arrow sequence
		// before the "[C"/"[D" bytes land. Try once more to complete it; a real
		// Esc keypress just times out (m==0) and stays a bare ESC → exit.
		if n == 1 && buf[0] == 0x1b {
			if m, _ := tty.Read(buf[1:]); m > 0 {
				n += m
			}
		}
		k, r := decodeKey(buf[:n])
		switch {
		case k == kEsc, k == kCtrlC, k == kRune && (r == 'q' || r == 'Q'):
			return
		case k == kLeft, k == kRune && r == 'h':
			if sel > 0 {
				switchTo(sel - 1)
			}
		case k == kRight, k == kRune && r == 'l':
			if sel < len(chans)-1 {
				switchTo(sel + 1)
			}
		case k == kUp, k == kRune && r == 'k':
			top, atBottom = top-1, false
		case k == kDown, k == kRune && r == 'j':
			top++
			atBottom = top >= maxTop
		case k == kPageUp:
			top, atBottom = top-(body-1), false
		case k == kPageDown, k == kRune && r == ' ':
			top += body - 1
			atBottom = top >= maxTop
		case k == kHome, k == kRune && r == 'g':
			top, atBottom = 0, false
		case k == kEnd, k == kRune && r == 'G':
			atBottom = true
		case k == kRune && (r == 'r' || r == 'R'):
			lines = renderChannel(chans[sel], home, theme)
			lastSig = fileSig(chans[sel].Path)
		}
	}
}

// renderChannel renders the tail of a subagent transcript to styled lines via
// the normal renderer (dots + collapse, so a long subagent stays compact).
func renderChannel(ch subagentChannel, home string, theme Theme) []string {
	var buf bytes.Buffer
	rr, err := newRenderer(&buf, theme, "dots", 5)
	if err != nil {
		return []string{"  (cannot render this subagent)"}
	}
	agent := detectAgentForFile(home, ch.Path)
	loc := time.Local
	for _, l := range tailLines(ch.Path, 800) {
		for _, rec := range normalize(agent, l, loc) {
			rr.emit(rec)
		}
	}
	rr.endLine() // close a trailing dot-streak bracket / deferred newline
	out := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(out) == 1 && out[0] == "" {
		return []string{"  (nothing to show yet)"}
	}
	return out
}

// drawFocus renders one frame: header (channel + status), a divider, the visible
// window of body lines, and a footer hint.
func drawFocus(chans []subagentChannel, sel int, lines []string, top, w, h int, theme Theme) string {
	ch := chans[sel]
	running, dur := ch.status(time.Now().Unix())
	state := "✔ done"
	if running {
		state = "▸ running"
	}
	hdr := fmt.Sprintf("  focus %d/%d · %s  %s %s", sel+1, len(chans), ch.Description, state, fmtDur(dur))
	body := focusBody(h)

	var b strings.Builder
	b.WriteString("\x1b[H")
	b.WriteString("\x1b[1m" + ansiClip(hdr, w) + reset + "\x1b[K\n")
	b.WriteString(theme.DimANSI + strings.Repeat("─", max(w, 1)) + reset + "\x1b[K\n")
	shown := 0
	for i := top; i < top+body && i < len(lines); i++ {
		b.WriteString(ansiClip(lines[i], w) + "\x1b[K\n")
		shown++
	}
	for ; shown < body; shown++ {
		b.WriteString("\x1b[K\n")
	}
	foot := fmt.Sprintf("  %d–%d/%d · ←/→ switch · ↑↓ scroll · r reload · q back",
		min(top+1, len(lines)), min(top+body, len(lines)), len(lines))
	b.WriteString("\x1b[2m" + ansiClip(foot, w) + reset + "\x1b[K\x1b[J")
	return b.String()
}

// fileSig is a cheap change signature (size + mtime) for follow detection.
func fileSig(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}

// fmtDur renders a duration compactly: "8m47s", "3m", "42s".
func fmtDur(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	s := int(d.Seconds())
	m, sec := s/60, s%60
	if m == 0 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, sec)
}

// ansiClip truncates s to w visible columns, passing SGR escapes through
// uncounted so color codes aren't sliced mid-sequence. (Local to the overlay;
// the transcript lines it clips carry only CSI/SGR codes, no OSC.)
func ansiClip(s string, w int) string {
	if w <= 0 {
		return s
	}
	var b strings.Builder
	vis := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b {
			for ; i < len(rs); i++ {
				b.WriteRune(rs[i])
				if (rs[i] >= 'a' && rs[i] <= 'z') || (rs[i] >= 'A' && rs[i] <= 'Z') {
					break
				}
			}
			continue
		}
		if vis >= w {
			return b.String() + reset
		}
		b.WriteRune(rs[i])
		vis++
	}
	return b.String()
}
