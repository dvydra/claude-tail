package main

import (
	"strings"
	"testing"
	"time"
)

// The resize re-render waits for the SIGWINCH stream to go quiet: a window-edge
// drag emits them continuously, and each re-render walks the whole transcript.
func TestWinchSettled(t *testing.T) {
	now := time.Date(2026, 1, 3, 2, 4, 5, 0, time.UTC)
	if winchSettled(time.Time{}, now) {
		t.Error("no resize owed, but reported settled")
	}
	if winchSettled(now.Add(-winchSettle/2), now) {
		t.Error("a drag still in flight reported settled")
	}
	if !winchSettled(now.Add(-winchSettle), now) {
		t.Error("a quiet resize reported unsettled")
	}
}

// The `w` toggle suspends wrapping for a copy. It's a separate flag from
// cfg.NoWrap so it composes: with either set, the width is 0.
func TestWrapWidthToggleComposesWithFlag(t *testing.T) {
	for _, c := range []struct {
		name         string
		flag, toggle bool
	}{
		{"--no-wrap", true, false},
		{"the w toggle", false, true},
		{"both", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			// wrapWidth reads the real stdout, which isn't a tty under `go test`,
			// so exercise the composition the live loop performs.
			if got := wrapWidthFor(120, !(c.flag || c.toggle)); got != 0 {
				t.Errorf("%s left wrapping on (width %d)", c.name, got)
			}
		})
	}
	if got := wrapWidthFor(120, true); got == 0 {
		t.Error("neither flag nor toggle set, but wrapping is off")
	}
}

// wrap_test.go covers word wrapping: the width calculation, that a wrapped body
// breaks between words rather than mid-word, and that wrap 0 (piped output,
// --no-wrap) still emits one logical line per paragraph.

func TestWrapWidthFor(t *testing.T) {
	cases := []struct {
		name string
		cols int
		tty  bool
		want int
	}{
		// Piped output keeps the pre-wrap behaviour — which is also how every
		// golden is generated, so this case is what keeps them stable.
		{"piped", 120, false, 0},
		// One short of the terminal: a line filling the last column makes the
		// terminal wrap the cursor itself, showing as a phantom blank line.
		{"tty", 120, true, 119},
		{"narrow tty still wraps", 40, true, 39},
		// Below the floor, glamour's indents leave too little room to wrap well.
		{"tiny tty falls back to soft wrap", 12, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wrapWidthFor(c.cols, c.tty); got != c.want {
				t.Errorf("wrapWidthFor(%d, %v) = %d, want %d", c.cols, c.tty, got, c.want)
			}
		})
	}
}

// prose is a long single paragraph of short words, so every break has a space
// available inside the limit and none needs to split a word.
var prose = strings.TrimSpace(strings.Repeat("the quick brown fox jumps over a lazy dog ", 8))

// renderProse renders one assistant body at the given wrap width and returns the
// ANSI-stripped output.
func renderProse(t *testing.T, wrap int, text string) string {
	t.Helper()
	th, err := loadTheme("claude", "")
	if err != nil {
		t.Fatalf("loadTheme: %v", err)
	}
	var b strings.Builder
	r, err := newRenderer(&b, th, "dots", 0, wrap)
	if err != nil {
		t.Fatalf("newRenderer: %v", err)
	}
	r.emit(Record{Kind: KindAssistant, Body: text, Ts: "10:00:00"})
	r.endLine()
	return stripANSI(b.String())
}

// A wrapped body must break BETWEEN words: the whole point of turning wrap on is
// that the terminal's own soft wrap chops mid-word at the column edge.
func TestBodyWrapsAtWordBoundaries(t *testing.T) {
	const wrap = 40
	out := renderProse(t, wrap, prose)

	var bodyLines int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " ")
		// The box header is a fixed-width string whose dash count is pinned by
		// the goldens; wrapping applies to bodies, not to the chrome.
		if line == "" || strings.HasPrefix(line, "───") {
			continue
		}
		if w := len([]rune(line)); w > wrap {
			t.Errorf("line exceeds wrap width %d (%d): %q", wrap, w, line)
		}
		bodyLines++
	}
	if bodyLines < 3 {
		t.Fatalf("expected the paragraph to wrap over several lines, got %d", bodyLines)
	}
	// The words have to survive the wrap intact — collapsing the whitespace must
	// reproduce the source exactly, which it can't if a break split a word.
	if flat := strings.Join(strings.Fields(out), " "); !strings.Contains(flat, prose) {
		t.Errorf("wrapping split a word; got %q", flat)
	}
}

// Unwrapped (wrap 0) stays one logical line per paragraph — the terminal
// soft-wraps it and rejoins it on copy. That's what --no-wrap and every piped
// run get, and the width the goldens are rendered at.
func TestBodyUnwrappedIsOneLine(t *testing.T) {
	out := renderProse(t, 0, prose)
	if flat := strings.Join(strings.Fields(out), " "); !strings.Contains(flat, prose) {
		t.Fatalf("body not rendered intact: %q", flat)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "quick brown fox") && len([]rune(line)) < len([]rune(prose)) {
			t.Errorf("body was wrapped at wrap=0: %q", line)
		}
	}
}

// Glamour right-pads every line out to the wrap width. On prose that padding
// costs the last column, lands in the clipboard on a drag-select, and pushes the
// dot streak that rides the end of an agent turn past the terminal edge.
func TestWrappedBodyHasNoTrailingPadding(t *testing.T) {
	out := renderProse(t, 50, prose)
	for _, line := range strings.Split(out, "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("wrapped line kept trailing padding: %q", line)
		}
	}
}

// setWrap reports whether the width actually moved, so a height-only SIGWINCH
// (dragging the bottom edge) doesn't re-render the whole transcript. A theme
// swap has to preserve the current width rather than reset it.
func TestSetWrap(t *testing.T) {
	th, err := loadTheme("claude", "")
	if err != nil {
		t.Fatalf("loadTheme: %v", err)
	}
	var b strings.Builder
	r, err := newRenderer(&b, th, "dots", 0, 80)
	if err != nil {
		t.Fatalf("newRenderer: %v", err)
	}
	if r.setWrap(80) {
		t.Error("setWrap(same width) reported a change")
	}
	if !r.setWrap(60) {
		t.Error("setWrap(new width) reported no change")
	}
	if r.wrap != 60 {
		t.Errorf("wrap = %d, want 60", r.wrap)
	}

	next, err := loadTheme("dracula", "")
	if err != nil {
		t.Fatalf("loadTheme: %v", err)
	}
	if err := r.applyTheme(next); err != nil {
		t.Fatalf("applyTheme: %v", err)
	}
	if r.wrap != 60 {
		t.Errorf("wrap after applyTheme = %d, want 60", r.wrap)
	}

	var out strings.Builder
	r.w = &out
	r.emit(Record{Kind: KindAssistant, Body: prose, Ts: "10:00:00"})
	r.endLine()
	for _, line := range strings.Split(stripANSI(out.String()), "\n") {
		if w := len([]rune(strings.TrimRight(line, " "))); w > 60 {
			t.Errorf("line exceeds the post-applyTheme wrap width 60 (%d): %q", w, line)
		}
	}
}
