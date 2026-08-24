package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// help.go is the `?` help modal reached during a live tail. It re-shows the
// startup banner's context (agent/session/theme/backfill/tools/collapse) plus
// the full key map, as a centered bordered box in an alt-screen — so the
// scrolling transcript underneath is untouched when it closes.
//
// Like the focus overlay it runs on the main (render) goroutine while the
// keyboard goroutine is parked, so it's the sole tty reader + writer for its
// lifetime. The state it shows is sampled when `?` is pressed, not at startup:
// `t`/`T`/`c` can all have moved since the banner was printed.

// helpInfo is the snapshot of live state the modal describes.
type helpInfo struct {
	Agent       Agent
	Session     string // transcript path (tildified for display)
	Theme       string
	Backfill    string // the --backfill spec, e.g. "all"
	From, Total int    // backfill range, as the banner reports it
	Tools       toolStyleKind
	Collapse    int  // current paste-collapse threshold (0 = off)
	Mrkdwn      bool // agent bodies rendering as Slack mrkdwn source
	Wrap        int  // current wrap column limit (0 = unwrapped)
	TreeEnabled bool // Ctrl-X is Claude-only
}

// helpMinBox is the modal's minimum OUTER width. A box that hugs its longest
// line reads as cramped against the transcript it covers, so it always opens at
// 80 columns — wider when a line needs it, narrower only when the terminal is.
const helpMinBox = 80

// helpBorders is the columns the box chrome costs: two border cells plus a
// space of padding on each side.
const helpBorders = 4

// helpLines is the modal's content, one line per row (pure — no box, no
// centering, so it's unit-testable). Blank strings are spacer rows.
func helpLines(info helpInfo) []string {
	kv := func(k, v string) string { return fmt.Sprintf("  %-9s %s", k, v) }

	collapse := "off"
	if info.Collapse > 0 {
		collapse = fmt.Sprintf("user pastes > %d lines", info.Collapse)
	}
	bodyMode := "rendered markdown"
	if info.Mrkdwn {
		bodyMode = "slack mrkdwn source (m)"
	}
	wrap := "off — paragraphs are one line (copies unbroken)"
	if info.Wrap > 0 {
		wrap = fmt.Sprintf("%d columns", info.Wrap)
	}
	L := []string{
		kv("agent", string(info.Agent)),
		kv("session", info.Session),
		kv("theme", info.Theme),
		kv("backfill", fmt.Sprintf("%s (%d..%d of %d)", info.Backfill, info.From, info.Total, info.Total)),
		kv("tools", info.Tools.label()),
		kv("collapse", collapse),
		kv("bodies", bodyMode),
		kv("wrap", wrap),
		"",
		"keys",
		kv("y", "copy the last agent message as Slack mrkdwn"),
		kv("", "(press again within 3s to add the one before it)"),
		kv("m", "toggle agent text ↔ Slack mrkdwn source"),
		kv("w", "toggle word wrap off/on"),
		kv("", "(off = a drag-select copies whole paragraphs)"),
		kv("t", "cycle tool style (full → dots → hidden)"),
		kv("T", "cycle theme"),
		kv("c", "toggle user-paste collapse"),
		kv("", "t/T/c/m/w re-render the history as they go"),
		kv("r", "re-render the history on demand"),
		kv("→", "focus subagents"),
	}
	if info.TreeEnabled {
		L = append(L, kv("Ctrl-X", "back to the tree picker"))
	}
	L = append(L,
		kv("?", "this help"),
		kv("q", "quit (also Ctrl-D, Ctrl-C)"),
		"",
		"legend",
		"  "+strings.TrimSpace(strings.TrimPrefix(bannerLegend(), "  legend:   ")),
	)
	return L
}

// runHelp shows the modal on the shared tty (the same fd the keyboard goroutine
// reads — it's parked for the modal's lifetime, so there's a single reader).
// Returns on q/Esc/Enter/space/?; ↑↓ scroll when the box is taller than the
// terminal. A no-op without a tty.
func runHelp(tty *os.File, info helpInfo, theme Theme) {
	if tty == nil {
		return
	}
	// The tty is in cbreak (the live-tail mode); switch to raw so Esc/Ctrl-C
	// arrive as bytes and the alt-screen is always torn down cleanly.
	saved, ok := setRaw(tty)
	if !ok {
		return
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	lines := helpLines(info)
	top := 0
	buf := make([]byte, 16)
	for {
		w, h := termSize(tty)
		body := helpBodyRows(len(lines), h)
		maxTop := max(len(lines)-body, 0)
		top = min(max(top, 0), maxTop)
		io.WriteString(tty, drawHelp(lines, top, w, h, theme))

		n, err := tty.Read(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		k, r := decodeKey(buf[:n])
		switch {
		case k == kUp, k == kRune && r == 'k':
			top--
		case k == kDown, k == kRune && r == 'j':
			top++
		case k == kPageUp:
			top -= body - 1
		case k == kPageDown:
			top += body - 1
		case k == kHome, k == kRune && r == 'g':
			top = 0
		case k == kEnd, k == kRune && r == 'G':
			top = maxTop
		default: // q, Esc, Enter, space, ? — and any other key: dismiss
			return
		}
	}
}

// helpBodyRows is how many content rows fit: the whole box (2 border rows) plus
// a blank row of breathing space above and below, inside h.
func helpBodyRows(n, h int) int {
	return min(n, max(h-4, 1))
}

// drawHelp renders one frame: the box centered in the terminal, with the title
// in the top border and the dismiss hint in the bottom one.
func drawHelp(lines []string, top, w, h int, theme Theme) string {
	body := helpBodyRows(len(lines), h)
	inner := helpMinBox - helpBorders
	for _, l := range lines {
		inner = max(inner, visWidth(l))
	}
	// Shrink to fit a terminal too narrow for that, keeping a column of margin
	// each side (and never going below a width that can still show something).
	inner = max(min(inner, w-2-helpBorders), 8)
	boxW := inner + helpBorders
	left := strings.Repeat(" ", max((w-boxW)/2, 0))
	pad := max((h-(body+2))/2, 0)

	title := " entire-tail " + version + " "
	hint := " q close "
	if len(lines) > body {
		hint = fmt.Sprintf(" %d–%d/%d · ↑↓ scroll · q close ", top+1, top+body, len(lines))
	}

	dim := func(s string) string { return theme.DimANSI + s + reset }
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	for range pad {
		b.WriteString("\n")
	}
	b.WriteString(left + dim("╭─") + "\x1b[1m" + truncVisible(title, inner) + reset +
		dim(strings.Repeat("─", max(inner-visWidth(title), 0))+"─╮") + "\n")
	for i := top; i < top+body && i < len(lines); i++ {
		b.WriteString(left + dim("│ ") + padVisible(lines[i], inner) + dim(" │") + "\n")
	}
	b.WriteString(left + dim("╰─"+strings.Repeat("─", max(inner-visWidth(hint), 0))+truncVisible(hint, inner)+"─╯"))
	return b.String()
}

// visWidth counts the visible columns of s, skipping ANSI escapes.
func visWidth(s string) int {
	n := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b {
			for ; i < len(rs); i++ {
				if (rs[i] >= 'a' && rs[i] <= 'z') || (rs[i] >= 'A' && rs[i] <= 'Z') {
					break
				}
			}
			continue
		}
		n++
	}
	return n
}

// padVisible pads (or truncates) s to exactly w visible columns.
func padVisible(s string, w int) string {
	if n := visWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return truncVisible(s, w)
}
