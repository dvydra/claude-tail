package main

import (
	"fmt"
	"strings"
)

// help.go holds the alt-screen PANEL: the centered bordered box the `?` settings
// screen (settings.go) draws itself in, and the width helpers that go with it.
// Drawing it over an alt-screen is what leaves the scrolling transcript
// underneath untouched when it closes.
//
// helpInfo is the state snapshot a panel describes. It's sampled when `?` is
// pressed, not at startup: `t`/`T`/`c`/`w` can all have moved since the banner
// was printed, and a panel echoing the stale banner would be worse than none.

// helpInfo is the snapshot of live state the modal describes.
type helpInfo struct {
	Agent       Agent
	Session     string // transcript path (tildified for display)
	Theme       string
	ThemeSwatch string // themeSwatch(theme): the colour strip shown beside the name
	Backfill    string // the --backfill spec, e.g. "all"
	From, Total int    // backfill range, as the banner reports it
	Tools       toolStyleKind
	Collapse    int  // current paste-collapse threshold (0 = off)
	Mrkdwn      bool // agent bodies rendering as Slack mrkdwn source
	Wrap        int  // current wrap column limit (0 = unwrapped)
	StatusBar   bool // the bottom row is currently reserved
	TreeEnabled bool // Ctrl-X is Claude-only
}

// helpMinBox is the modal's minimum OUTER width. A box that hugs its longest
// line reads as cramped against the transcript it covers, so it always opens at
// 80 columns — wider when a line needs it, narrower only when the terminal is.
const helpMinBox = 80

// helpBorders is the columns the box chrome costs: two border cells plus a
// space of padding on each side.
const helpBorders = 4

// helpBodyRows is how many content rows fit: the whole box (2 border rows) plus
// a blank row of breathing space above and below, inside h.
func helpBodyRows(n, h int) int {
	return min(n, max(h-4, 1))
}

// drawPanel renders one frame of an alt-screen panel: the box centered in the
// terminal, with the title in the top border and `hint` in the bottom one. The
// caller owns the hint because the two panels say different things there — the
// settings screen its key legend, a scrolling view its position.
func drawPanel(lines []string, top, w, h int, theme Theme, hint string) string {
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
	if len(lines) > body {
		hint = fmt.Sprintf(" %d–%d/%d ·%s", top+1, top+body, len(lines), hint)
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
