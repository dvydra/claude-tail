package main

import (
	"strings"
	"testing"
)

func testHelpInfo() helpInfo {
	return helpInfo{
		Agent:       AgentClaude,
		Session:     "~/.claude/projects/-repo/abc.jsonl",
		Theme:       "tokyo-night",
		Backfill:    "all",
		From:        1,
		Total:       3,
		Tools:       toolDots,
		Collapse:    5,
		Wrap:        119,
		TreeEnabled: true,
	}
}

func TestHelpLinesShowsLiveState(t *testing.T) {
	got := strings.Join(helpLines(testHelpInfo()), "\n")
	for _, want := range []string{
		"claude", "~/.claude/projects/-repo/abc.jsonl", "tokyo-night",
		"all (1..3 of 3)", "dots", "user pastes > 5 lines",
		"cycle tool style", "cycle theme", "focus subagents", "this help", "quit",
		"Ctrl-X", "legend", "119 columns", "toggle word wrap",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("help content missing %q:\n%s", want, got)
		}
	}
}

func TestHelpLinesReflectsToggles(t *testing.T) {
	info := testHelpInfo()
	info.Tools, info.Collapse = toolNone, 0
	got := strings.Join(helpLines(info), "\n")
	if !strings.Contains(got, "collapse  off") {
		t.Errorf("collapse=0 should render as off:\n%s", got)
	}
	if !strings.Contains(got, toolNone.label()) {
		t.Errorf("tool style should be the live one (%s):\n%s", toolNone.label(), got)
	}
	// Wrap suspended by `w` has to read as such — otherwise prose running off the
	// right edge looks like a bug rather than a mode you chose.
	info.Wrap = 0
	if got := strings.Join(helpLines(info), "\n"); !strings.Contains(got, "wrap      off") {
		t.Errorf("wrap=0 should render as off:\n%s", got)
	}
}

// Ctrl-X is Claude-only (the tree has nothing to go back to elsewhere), so the
// modal must not advertise it for codex/agy.
func TestHelpLinesHidesTreeKeyWhenDisabled(t *testing.T) {
	info := testHelpInfo()
	info.Agent, info.TreeEnabled = AgentCodex, false
	if got := strings.Join(helpLines(info), "\n"); strings.Contains(got, "Ctrl-X") {
		t.Errorf("Ctrl-X shown for a non-Claude session:\n%s", got)
	}
}

// Every row of the box must be the same visible width, or the border staggers.
func TestDrawHelpBoxIsRectangular(t *testing.T) {
	theme := Theme{DimANSI: "\x1b[2m"}
	frame := drawHelp(helpLines(testHelpInfo()), 0, 100, 40, theme)
	var widths []int
	for l := range strings.SplitSeq(frame, "\n") {
		if s := strings.TrimSpace(stripANSI(l)); s != "" {
			widths = append(widths, visWidth(strings.TrimRight(l, " ")))
		}
	}
	if len(widths) < 3 {
		t.Fatalf("expected a box, got %d non-empty rows", len(widths))
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Errorf("row %d is %d cols wide, want %d:\n%s", i, w, widths[0], frame)
		}
	}
	if widths[0] < helpMinBox {
		t.Errorf("box is %d cols, want at least %d:\n%s", widths[0], helpMinBox, frame)
	}
	if !strings.Contains(frame, "╭─") || !strings.Contains(frame, "╰─") {
		t.Errorf("missing box borders:\n%s", frame)
	}
	if !strings.Contains(frame, "q close") {
		t.Errorf("missing dismiss hint:\n%s", frame)
	}
}

// A terminal narrower than the 80-column minimum shrinks the box to fit rather
// than overflowing it (which would wrap every row and shred the border).
func TestDrawHelpFitsNarrowTerminal(t *testing.T) {
	for _, w := range []int{60, 40, 20} {
		frame := drawHelp(helpLines(testHelpInfo()), 0, w, 40, helpTestTheme())
		for l := range strings.SplitSeq(frame, "\n") {
			if got := visWidth(l); got > w {
				t.Errorf("w=%d: row %q is %d cols", w, stripANSI(l), got)
			}
		}
	}
}

// A short terminal clips the box and offers scrolling instead of overflowing.
func TestDrawHelpScrollsWhenClipped(t *testing.T) {
	lines := helpLines(testHelpInfo())
	frame := drawHelp(lines, 2, 100, 10, helpTestTheme())
	if got := strings.Count(frame, "\n") + 1; got > 10 {
		t.Errorf("frame is %d rows, terminal has 10:\n%s", got, frame)
	}
	if !strings.Contains(frame, "↑↓ scroll") {
		t.Errorf("clipped modal should offer scrolling:\n%s", frame)
	}
	if !strings.Contains(stripANSI(frame), stripANSI(lines[2])) {
		t.Errorf("top=2 should start at line 2 (%q):\n%s", lines[2], frame)
	}
}

func helpTestTheme() Theme { return Theme{DimANSI: "\x1b[2m"} }

func TestHelpKeyIsWired(t *testing.T) {
	if got := keyActionFor('?'); got != keyHelp {
		t.Errorf("'?' → %v, want keyHelp", got)
	}
}

func TestVisWidthSkipsANSI(t *testing.T) {
	if got := visWidth("\x1b[2mab\x1b[0m"); got != 2 {
		t.Errorf("visWidth = %d, want 2", got)
	}
	if got := padVisible("ab", 5); got != "ab   " {
		t.Errorf("padVisible = %q, want %q", got, "ab   ")
	}
	if got := visWidth(padVisible("abcdefg", 4)); got != 4 {
		t.Errorf("padVisible over-long = %d cols, want 4", got)
	}
}
