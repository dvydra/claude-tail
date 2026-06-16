package main

import (
	"strings"
	"testing"
)

// identityRender echoes the markdown unchanged, so tests exercise the layout
// state machine without glamour's color-dependent output.
func identityRender(s string) (string, error) { return s, nil }

func testTheme() Theme {
	return Theme{Name: "t", UserANSI: "U:", ClaudeANSI: "C:", DimANSI: "D:"}
}

func renderRecords(toolStyle string, collapse int, live bool, recs ...Record) string {
	var b strings.Builder
	r := newRendererWith(&b, testTheme(), toolStyle, collapse, identityRender)
	r.live = live
	for _, rec := range recs {
		r.emit(rec)
	}
	return b.String()
}

func TestHeaderDifferentKinds(t *testing.T) {
	out := renderRecords("dots", 0, false,
		Record{Kind: KindUser, Ts: "T1", Body: "hi"},
		Record{Kind: KindAssistant, Ts: "T2", Body: "yo"},
	)
	want := "U:" + userHdrBody + reset + " D:T1" + reset + "\n" +
		"hi\n" +
		"C:" + claudeHdrBody + reset + " D:T2" + reset + "\n" +
		"yo\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestHeaderSameKindCollapses(t *testing.T) {
	out := renderRecords("dots", 0, false,
		Record{Kind: KindAssistant, Ts: "T1", Body: "a"},
		Record{Kind: KindAssistant, Ts: "T2", Body: "b"},
	)
	want := "C:" + claudeHdrBody + reset + " D:T1" + reset + "\n" +
		"a\n" +
		"D:  ⋯ T2" + reset + "\n" + // continuation marker, not a fresh box
		"b\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestDotStreakThenHeaderBreaksWithNewline(t *testing.T) {
	out := renderRecords("dots", 0, false,
		Record{Kind: KindToolUse, Name: "Read"},
		Record{Kind: KindToolUse, Name: "Bash"},
		Record{Kind: KindUser, Ts: "T1", Body: "x"},
	)
	want := dotColor("Read") + "." + reset +
		dotColor("Bash") + "." + reset +
		"\n" + // newline breaks the streak before the header
		"U:" + userHdrBody + reset + " D:T1" + reset + "\n" +
		"x\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestToolStyleNoneDropsTools(t *testing.T) {
	out := renderRecords("none", 0, false,
		Record{Kind: KindToolUse, Name: "Read", Summary: "f"},
		Record{Kind: KindToolResult, N: 1},
		Record{Kind: KindUser, Ts: "T1", Body: "x"},
	)
	want := "U:" + userHdrBody + reset + " D:T1" + reset + "\n" + "x\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestToolStyleLines(t *testing.T) {
	out := renderRecords("lines", 0, false,
		Record{Kind: KindToolUse, Name: "Bash", Summary: "ls -la"},
		Record{Kind: KindToolResult, N: 3},
	)
	want := "D:  ⚙ Bash  ls -la" + reset + "\n" +
		"D:  ↩ tool_result (×3)" + reset + "\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestToolResultDroppedInDotsMode(t *testing.T) {
	// In dots mode tool_result is not shown (it would double-count tool_use).
	out := renderRecords("dots", 0, false, Record{Kind: KindToolResult, N: 1})
	if out != "" {
		t.Errorf("expected no output for tool_result in dots mode, got %q", out)
	}
}

func TestUserBodyCollapses(t *testing.T) {
	body := "l1\nl2\nl3\nl4\nl5"
	out := renderRecords("dots", 2, false, Record{Kind: KindUser, Ts: "T", Body: body})
	if !strings.Contains(out, "3 more lines — re-run with --no-collapse to expand") {
		t.Errorf("expected collapse marker, got:\n%q", out)
	}
	if strings.Contains(out, "l3") {
		t.Errorf("expected lines beyond 2 to be collapsed, got:\n%q", out)
	}
}

func TestAssistantBodyNotCollapsed(t *testing.T) {
	body := "l1\nl2\nl3\nl4\nl5"
	out := renderRecords("dots", 2, false, Record{Kind: KindAssistant, Ts: "T", Body: body})
	if !strings.Contains(out, "l5") {
		t.Errorf("assistant bodies must not collapse, got:\n%q", out)
	}
}

func TestLiveBellOnAssistantOnly(t *testing.T) {
	live := renderRecords("dots", 0, true, Record{Kind: KindAssistant, Ts: "T", Body: "x"})
	if !strings.HasPrefix(live, "\a") {
		t.Errorf("expected BEL prefix on live assistant turn, got %q", live)
	}
	backfill := renderRecords("dots", 0, false, Record{Kind: KindAssistant, Ts: "T", Body: "x"})
	if strings.Contains(backfill, "\a") {
		t.Errorf("backfill must not ring the bell, got %q", backfill)
	}
	user := renderRecords("dots", 0, true, Record{Kind: KindUser, Ts: "T", Body: "x"})
	if strings.Contains(user, "\a") {
		t.Errorf("user turns must not ring the bell, got %q", user)
	}
}

func TestBodyStripsLeadingTrailingBlankLines(t *testing.T) {
	out := renderRecords("dots", 0, false, Record{Kind: KindUser, Ts: "T", Body: "\n\nhi\n\n"})
	want := "U:" + userHdrBody + reset + " D:T" + reset + "\n" + "hi\n"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestBodySqueezesConsecutiveBlankLines(t *testing.T) {
	// Runs of blank lines inside a body collapse to a single blank (matching
	// the bash backfill awk's held_blank behavior).
	out := renderRecords("dots", 0, false, Record{Kind: KindUser, Ts: "T", Body: "a\n\n\n\nb"})
	want := "U:" + userHdrBody + reset + " D:T" + reset + "\n" + "a\n\nb\n"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestStripTerminalNoise(t *testing.T) {
	cases := map[string]string{
		"a\x1b]11;rgb:0000/0000/0000\x07b": "ab",
		"a\x1b]10;?\x1b\\b":                "ab",
		"x\x1b[12;34Ry":                    "xy",
		"plain":                            "plain",
	}
	for in, want := range cases {
		if got := stripTerminalNoise(in); got != want {
			t.Errorf("stripTerminalNoise(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDotColor(t *testing.T) {
	cases := map[string]string{
		"Read":         "\x1b[38;2;125;185;235m",
		"Bash":         "\x1b[38;2;240;200;110m",
		"Grep":         "\x1b[38;2;220;140;230m",
		"Edit":         "\x1b[38;2;125;215;145m",
		"mcp__foo_bar": "\x1b[38;2;255;180;130m",
		"unknown_tool": "\x1b[38;2;200;200;200m",
	}
	for name, want := range cases {
		if got := dotColor(name); got != want {
			t.Errorf("dotColor(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 3); got != "hel" {
		t.Errorf("got %q", got)
	}
	if got := truncateRunes("héllo", 2); got != "hé" { // rune-aware, not byte
		t.Errorf("got %q", got)
	}
	if got := truncateRunes("hi", 10); got != "hi" {
		t.Errorf("got %q", got)
	}
}
