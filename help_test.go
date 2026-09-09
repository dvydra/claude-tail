package main

import (
	"strconv"
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
		StatusBar:   true,
		TreeEnabled: true,
	}
}

func helpTestTheme() Theme { return Theme{DimANSI: "\x1b[2m"} }

// panelLines is the settings panel's body at a fixed cursor, for the box tests
// below — they care about the frame, not what's in it.
func panelLines(t *testing.T) []string {
	t.Helper()
	env := settingsEnv{
		Context: settingsContext(testHelpInfo()),
		Keys:    settingsKeys(true),
		Legend:  "● read  ● edit",
	}
	lines, _ := settingsLines(env, settingsRowsFor(testHelpInfo(), t.TempDir()), 0, helpTestTheme())
	return lines
}

// Every row of the box must be the same visible width, or the border staggers.
func TestDrawPanelBoxIsRectangular(t *testing.T) {
	frame := drawPanel(panelLines(t), 0, 100, 40, helpTestTheme(), " q close ")
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
func TestDrawPanelFitsNarrowTerminal(t *testing.T) {
	lines := panelLines(t)
	for _, w := range []int{60, 40, 20} {
		frame := drawPanel(lines, 0, w, 40, helpTestTheme(), " q close ")
		for l := range strings.SplitSeq(frame, "\n") {
			if got := visWidth(l); got > w {
				t.Errorf("w=%d: row %q is %d cols", w, stripANSI(l), got)
			}
		}
	}
}

// A short terminal clips the box and says where in the list it is, rather than
// overflowing.
func TestDrawPanelScrollsWhenClipped(t *testing.T) {
	lines := panelLines(t)
	frame := drawPanel(lines, 2, 100, 10, helpTestTheme(), " q close ")
	if got := strings.Count(frame, "\n") + 1; got > 10 {
		t.Errorf("frame is %d rows, terminal has 10:\n%s", got, frame)
	}
	// top=2 with a 10-row terminal shows 6 body rows: "3–8/<total>".
	if want := "3–8/" + strconv.Itoa(len(lines)); !strings.Contains(frame, want) {
		t.Errorf("clipped panel should show its position (%q):\n%s", want, frame)
	}
	if !strings.Contains(frame, "q close") {
		t.Errorf("clipped panel dropped the hint:\n%s", frame)
	}
	if !strings.Contains(stripANSI(frame), stripANSI(lines[2])) {
		t.Errorf("top=2 should start at line 2 (%q):\n%s", lines[2], frame)
	}
}

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
