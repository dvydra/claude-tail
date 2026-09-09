package main

import (
	"strings"
	"testing"
)

// widths returns each line's visible width, so a test can assert on the shape of
// a wrapped block without caring about the escapes inside it.
func widths(s string) []int {
	var w []int
	for _, l := range strings.Split(s, "\n") {
		w = append(w, visWidth(l))
	}
	return w
}

// The bug this file exists for: glamour wraps through muesli/reflow, which
// writes a breakpoint rune ('-') to the output without counting it. Every hyphen
// on a line therefore bought a free column, the line overshot the limit, and the
// overshoot was broken off as a one-word stub — "…in the runbook and" / "in" /
// "memory, so the value…" on screen. Hyphens are everywhere in this transcript's
// text, so the orphans were constant.
func TestWrapHyphenatedProseHasNoOrphans(t *testing.T) {
	const limit = 96
	md := "Recorded. `op://partial.to/agent-token-elastic-ci-stack-buildkite/token` is now in " +
		"the runbook and in memory, so the value never needs pasting again.\n"

	th, err := loadTheme("tokyo-night", "")
	if err != nil {
		t.Fatalf("loadTheme: %v", err)
	}
	render, err := newGlamour(th.StyleJSON, limit)
	if err != nil {
		t.Fatalf("newGlamour: %v", err)
	}
	out, err := render(md)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(stripANSI(l)) != "" {
			lines = append(lines, stripANSI(l))
		}
	}
	if len(lines) != 2 {
		t.Fatalf("paragraph should wrap to 2 lines at %d columns, got %d:\n%s",
			limit, len(lines), strings.Join(lines, "\n"))
	}
	for i, l := range lines {
		if w := visWidth(l); w > limit {
			t.Errorf("line %d overshoots the limit (%d > %d): %q", i, w, limit, l)
		}
	}
	// The break has to be the LAST one that fits: a line whose next word would
	// still have fitted is the orphan symptom, seen from the other side.
	first, second := lines[0], strings.Fields(lines[1])[0]
	if visWidth(first)+1+visWidth(second) <= limit {
		t.Errorf("broke early — %q still had room for %q", first, second)
	}
}

// A word longer than the whole column is left whole for the terminal to soft-wrap
// rather than cut in half. reflow breaks at hyphens, which is how a one-line
// `op://…/agent-token-elastic-ci-stack-buildkite/token` came out in two pieces —
// and a command that has been cut doesn't run when it's pasted.
func TestWrapNeverBreaksMidToken(t *testing.T) {
	const url = "op://partial.to/agent-token-elastic-ci-stack-buildkite/token"
	got := wrapANSI("see "+url+" for it", 20)
	if !strings.Contains(got, url) {
		t.Errorf("the token was split:\n%s", got)
	}
}

// Colour has to close at a break and reopen on the continuation, or the rest of
// the line bleeds the previous style to the edge of the screen.
func TestWrapCarriesColourAcrossTheBreak(t *testing.T) {
	const blue = "\x1b[38;2;1;2;3m"
	got := wrapANSI(blue+"alpha bravo charlie delta echo"+reset, 20)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected a wrap, got %q", got)
	}
	for i, l := range lines[:len(lines)-1] {
		if !strings.HasSuffix(l, reset) {
			t.Errorf("line %d leaves a colour open: %q", i, l)
		}
	}
	for i, l := range lines[1:] {
		if !strings.Contains(l, blue) {
			t.Errorf("continuation %d lost its colour: %q", i, l)
		}
	}
	if flat := strings.Join(strings.Fields(stripANSI(got)), " "); flat != "alpha bravo charlie delta echo" {
		t.Errorf("text changed across the wrap: %q", flat)
	}
}

// A block quote repeats its bar down the block (as glamour does); a list item
// indents its continuations under its own text instead of running back to the
// margin.
func TestWrapContinuationPrefixes(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  []string
	}{
		{
			name:  "block quote repeats the bar",
			in:    "│ alpha bravo charlie delta echo foxtrot",
			limit: 20,
			want:  []string{"│ alpha bravo", "│ charlie delta echo", "│ foxtrot"},
		},
		{
			name:  "bullet aligns under its text",
			in:    "• alpha bravo charlie delta echo foxtrot",
			limit: 20,
			want:  []string{"• alpha bravo", "  charlie delta echo", "  foxtrot"},
		},
		{
			name:  "ordered item aligns under its text",
			in:    "12. alpha bravo charlie delta echo",
			limit: 20,
			want:  []string{"12. alpha bravo", "    charlie delta", "    echo"},
		},
		{
			name:  "nested bullet keeps its indent",
			in:    "  • alpha bravo charlie delta echo",
			limit: 20,
			want:  []string{"  • alpha bravo", "    charlie delta", "    echo"},
		},
		{
			name:  "plain text starts at the margin",
			in:    "alpha bravo charlie delta echo",
			limit: 20,
			want:  []string{"alpha bravo charlie", "delta echo"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Split(wrapANSI(c.in, c.limit), "\n")
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("wrapANSI(%q, %d)\n got %q\nwant %q", c.in, c.limit, got, c.want)
			}
		})
	}
}

// Tables and horizontal rules are laid out by column: re-wrapping one would
// shred it, so an over-wide one is passed through for the terminal to soft-wrap.
func TestWrapLeavesBoxDrawingAlone(t *testing.T) {
	for _, in := range []string{
		" alpha aaaaaaaaaaaaaaa │ bravo bbbbbbbbbbbbbbbb │ charlie cccccccccc ",
		"──────────────┼──────────────┼──────────────",
		strings.Repeat("─", 60),
	} {
		if got := wrapANSI(in, 20); got != in {
			t.Errorf("re-wrapped a box-drawn line:\n got %q\nwant %q", got, in)
		}
	}
}

// Below the floor a prefix has eaten so much of the line that wrapping reads
// worse than the terminal's own soft wrap, so the line is passed through.
func TestWrapPassesThroughTooNarrowAColumn(t *testing.T) {
	in := "                    • alpha bravo charlie"
	if got := wrapANSI(in, 24); got != in {
		t.Errorf("wrapped below the floor:\n got %q\nwant %q", got, in)
	}
}

// wrap 0 is the piped / --no-wrap path and the width every golden is rendered
// at: it must hand the bytes straight back.
func TestWrapZeroIsAPassthrough(t *testing.T) {
	in := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima"
	if got := wrapANSI(in, 0); got != in {
		t.Errorf("wrapANSI(_, 0) altered the text:\n got %q\nwant %q", got, in)
	}
}

// Rendering unwrapped means glamour pads nothing, so no line may come back with
// trailing whitespace — it costs the last column, lands in the clipboard on a
// drag-select, and pushes a dot streak riding the end of a turn off the edge.
func TestWrappedRenderHasNoTrailingPadding(t *testing.T) {
	th, err := loadTheme("tokyo-night", "")
	if err != nil {
		t.Fatalf("loadTheme: %v", err)
	}
	render, err := newGlamour(th.StyleJSON, 50)
	if err != nil {
		t.Fatalf("newGlamour: %v", err)
	}
	md := "A paragraph with `inline code` in it that is long enough to wrap more than once.\n\n" +
		"- a bullet with `code` in it too, also long enough to need wrapping\n\n" +
		"```sh\naws ssm put-parameter --name /x/y --type SecureString\n```\n"
	out, err := render(md)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, l := range strings.Split(out, "\n") {
		if plain := stripANSI(l); plain != strings.TrimRight(plain, " \t") {
			t.Errorf("line kept trailing padding: %q", l)
		}
	}
}

// readEscape has to swallow an OSC 8 hyperlink whole — its payload is a URL full
// of characters a CSI scan would stop on.
func TestReadEscapeHandlesOSC8(t *testing.T) {
	link := "\x1b]8;;https://example.com/a-b\x1b\\"
	got, n := readEscape([]rune(link + "text"))
	if got != link || n != len([]rune(link)) {
		t.Errorf("readEscape = (%q, %d), want (%q, %d)", got, n, link, len([]rune(link)))
	}
}

// applySGR tracks only colour: a reset clears the carried state, other SGRs add
// to it, and a cursor move isn't colour at all.
func TestApplySGR(t *testing.T) {
	const blue, bold = "\x1b[38;2;1;2;3m", "\x1b[1m"
	if got := applySGR("", blue); got != blue {
		t.Errorf("applySGR(\"\", blue) = %q", got)
	}
	if got := applySGR(blue, bold); got != blue+bold {
		t.Errorf("applySGR(blue, bold) = %q", got)
	}
	if got := applySGR(blue+bold, reset); got != "" {
		t.Errorf("applySGR(_, reset) = %q, want empty", got)
	}
	if got := applySGR(blue, "\x1b[2K"); got != blue {
		t.Errorf("a cursor move changed the colour state: %q", got)
	}
}
