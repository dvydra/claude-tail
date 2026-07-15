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
	r.endLine() // settle the deferred trailing newline (as backfill/quit do)
	return b.String()
}

func TestAgentSpawnMarker(t *testing.T) {
	// Rendered in every tool style (it's orchestration, not a routine call).
	for _, style := range []string{"dots", "full", "hidden"} {
		out := renderRecords(style, 0, false,
			Record{Kind: KindAgentSpawn, Ts: "T1", AgentDesc: "Map the model", AgentType: "general-purpose"},
		)
		if !strings.Contains(out, "▸ agent:") || !strings.Contains(out, "Map the model") ||
			!strings.Contains(out, "(general-purpose)") {
			t.Errorf("style %s: marker missing in %q", style, out)
		}
	}
}

func TestQuestionCardLayout(t *testing.T) {
	rec := Record{Kind: KindQuestion, Ts: "T1", QID: "q1", Questions: []QuestionItem{
		{Header: "Scope", Question: "Which approach?", Options: []string{"Fast — quick", "Careful"}},
	}}
	out := renderRecords("dots", 0, false, rec)
	plain := stripANSI(out)
	for _, want := range []string{"⁉ WAITING FOR YOUR ANSWER", "Scope: Which approach?", "1. Fast — quick", "2. Careful"} {
		if !strings.Contains(plain, want) {
			t.Errorf("card missing %q in:\n%s", want, plain)
		}
	}
	// Every border row is the same visible width (box stays aligned).
	var widths []int
	for _, ln := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
		widths = append(widths, len([]rune(ln)))
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Errorf("row %d width %d != %d\n%s", i, w, widths[0], plain)
		}
	}
}

func TestQuestionBellFiresOncePerID(t *testing.T) {
	var b strings.Builder
	r := newRendererWith(&b, testTheme(), "dots", 0, identityRender)
	r.live = true
	q := Record{Kind: KindQuestion, QID: "q1", Questions: []QuestionItem{{Question: "?"}}}
	r.emit(q)
	r.emit(q) // same id re-rendered (e.g. a reload) → must not ring again
	r.emit(Record{Kind: KindQuestion, QID: "q2", Questions: []QuestionItem{{Question: "?"}}})
	if n := strings.Count(b.String(), "\a"); n != 2 {
		t.Errorf("bell count = %d, want 2 (one per distinct question id)", n)
	}
	// Backfill (not live) never rings.
	var b2 strings.Builder
	r2 := newRendererWith(&b2, testTheme(), "dots", 0, identityRender)
	r2.emit(q)
	if strings.Contains(b2.String(), "\a") {
		t.Error("backfill should not ring the bell")
	}
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
	want := "D:[" + reset + // dim open bracket
		dotColor("Read") + "." + reset +
		dotColor("Bash") + "." + reset +
		"D:]" + reset + // dim close bracket
		"\n" + // newline breaks the streak before the header
		"U:" + userHdrBody + reset + " D:T1" + reset + "\n" +
		"x\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestDotsRideAgentTurn(t *testing.T) {
	// In dots mode the tool dots ride the end of the agent's text line (one space
	// join), so short tool streaks don't cost a whole extra line before the marker.
	out := renderRecords("dots", 0, false,
		Record{Kind: KindAssistant, Ts: "T1", Body: "reading files"},
		Record{Kind: KindToolUse, Name: "Read"},
		Record{Kind: KindToolUse, Name: "Read"},
		Record{Kind: KindAssistant, Ts: "T2", Body: "done"},
	)
	want := "C:" + claudeHdrBody + reset + " D:T1" + reset + "\n" +
		"reading files " + "D:[" + reset +
		dotColor("Read") + "." + reset + dotColor("Read") + "." + reset +
		"D:]" + reset + "\n" +
		"D:  ⋯ T2" + reset + "\n" +
		"done\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestDotsAfterEmptyBodyStartFresh(t *testing.T) {
	// No body text to ride → the bracketed streak begins the line, no leading space.
	out := renderRecords("dots", 0, false,
		Record{Kind: KindAssistant, Ts: "T1", Body: ""},
		Record{Kind: KindToolUse, Name: "Read"},
	)
	want := "C:" + claudeHdrBody + reset + " D:T1" + reset + "\n" +
		"D:[" + reset + dotColor("Read") + "." + reset + "D:]" + reset + "\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestDotsDoNotRideUserTurn(t *testing.T) {
	// A tool call after a user turn (e.g. a text-less assistant turn emits no body)
	// must start on its own line — never ride/corrupt the user's text.
	out := renderRecords("dots", 0, false,
		Record{Kind: KindUser, Ts: "T1", Body: "install it"},
		Record{Kind: KindToolUse, Name: "Bash"},
	)
	want := "U:" + userHdrBody + reset + " D:T1" + reset + "\n" +
		"install it\n" +
		"D:[" + reset + dotColor("Bash") + "." + reset + "D:]" + reset + "\n"
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

func TestFullToolUseLabel(t *testing.T) {
	// full mode renders "⏺ Label(arg)"; Edit → Update, path → basename.
	out := renderRecords("full", 0, false,
		Record{Kind: KindToolUse, Name: "Edit", Summary: "/a/b/main.go"},
		Record{Kind: KindToolUse, Name: "Bash", Summary: "go test ./..."},
	)
	if !strings.Contains(out, "⏺"+reset+" Update(main.go)\n") {
		t.Errorf("expected ⏺ Update(main.go), got:\n%q", out)
	}
	if !strings.Contains(out, "⏺"+reset+" Bash(go test ./...)\n") {
		t.Errorf("expected ⏺ Bash(go test ./...), got:\n%q", out)
	}
}

func TestFullToolResultDiff(t *testing.T) {
	res := &ToolResult{
		Summary: "Added 1 line, removed 1 line",
		Diff: []DiffLine{
			{' ', 12, ""},
			{'-', 13, `const version = "0.7.0"`},
			{'+', 13, `const version = "0.7.1"`},
		},
	}
	out := stripANSI(renderRecords("full", 0, false, Record{Kind: KindToolResult, N: 1, Result: res}))
	for _, want := range []string{"⎿  Added 1 line, removed 1 line", "  13 - const version = \"0.7.0\"", "  13 + const version = \"0.7.1\""} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFullToolResultNilOmitted(t *testing.T) {
	// No rich detail (e.g. codex/agy) → no ⎿ line in full mode.
	out := renderRecords("full", 0, false, Record{Kind: KindToolResult, N: 1, Result: nil})
	if out != "" {
		t.Errorf("expected no output for nil result, got %q", out)
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

func TestCycleTools(t *testing.T) {
	var b strings.Builder
	r := newRendererWith(&b, testTheme(), "dots", 5, identityRender) // start: dots

	// dots → hidden → full → dots (cycle order full → dots → hidden).
	if got := r.cycleTools(); got != "tool calls hidden" {
		t.Errorf("from dots, cycle status = %q, want hidden", got)
	}
	at := b.Len()
	r.emit(Record{Kind: KindToolUse, Name: "Read"})
	if b.Len() != at {
		t.Error("tool use should produce no output while hidden")
	}

	if got := r.cycleTools(); got != "tool calls full" {
		t.Errorf("from hidden, cycle status = %q, want full", got)
	}
	at = b.Len()
	r.emit(Record{Kind: KindToolUse, Name: "Bash", Summary: "ls"})
	if !strings.Contains(b.String()[at:], "⏺"+reset+" Bash(ls)") {
		t.Errorf("full mode should render ⏺ Bash(ls), got:\n%q", b.String()[at:])
	}

	if got := r.cycleTools(); got != "tool calls dots" {
		t.Errorf("from full, cycle status = %q, want dots", got)
	}
	at = b.Len()
	r.emit(Record{Kind: KindToolUse, Name: "Read"})
	if b.Len() == at {
		t.Error("dots mode should render a dot again")
	}
}

func TestCycleToolsFromHidden(t *testing.T) {
	var b strings.Builder
	r := newRendererWith(&b, testTheme(), "hidden", 0, identityRender) // alias for none
	if got := r.cycleTools(); got != "tool calls full" {
		t.Errorf("from hidden, first cycle should go to full; got %q", got)
	}
}

func TestToggleCollapse(t *testing.T) {
	var b strings.Builder
	r := newRendererWith(&b, testTheme(), "dots", 2, identityRender)
	long := "l1\nl2\nl3\nl4\nl5"

	r.emit(Record{Kind: KindUser, Ts: "T1", Body: long})
	if !strings.Contains(b.String(), "more lines") {
		t.Error("should collapse initially")
	}

	if got := r.toggleCollapse(); got != "collapse off (full user pastes)" {
		t.Errorf("toggleCollapse status = %q", got)
	}
	b.Reset()
	r.emit(Record{Kind: KindUser, Ts: "T2", Body: long})
	if strings.Contains(b.String(), "more lines") || !strings.Contains(b.String(), "l5") {
		t.Errorf("should show full body when collapse off, got:\n%q", b.String())
	}

	if got := r.toggleCollapse(); got != "collapse on (user pastes > 2 lines)" {
		t.Errorf("toggleCollapse status = %q", got)
	}
	b.Reset()
	r.emit(Record{Kind: KindUser, Ts: "T3", Body: long})
	if !strings.Contains(b.String(), "more lines") {
		t.Error("should collapse again after re-enabling")
	}
}

func TestToggleCollapseFromOffUsesDefault(t *testing.T) {
	var b strings.Builder
	r := newRendererWith(&b, testTheme(), "dots", 0, identityRender) // started --no-collapse
	if got := r.toggleCollapse(); got != "collapse on (user pastes > 5 lines)" {
		t.Errorf("starting off, toggle should enable at the default threshold; got %q", got)
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
