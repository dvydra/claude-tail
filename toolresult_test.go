package main

import "testing"

func TestParseClaudeToolResultDiff(t *testing.T) {
	raw := `{"structuredPatch":[{"oldStart":12,"oldLines":2,"newStart":12,"newLines":2,"lines":[" ctx","-const version = \"0.7.0\"","+const version = \"0.7.1\""]}]}`
	r := parseClaudeToolResult([]byte(raw))
	if r == nil {
		t.Fatal("expected a result")
	}
	if r.Summary != "Added 1 line, removed 1 line" {
		t.Errorf("summary = %q", r.Summary)
	}
	// Context #12, removed old #13, added new #13 (new-file numbering for ctx/add).
	want := []DiffLine{
		{' ', 12, "ctx"},
		{'-', 13, `const version = "0.7.0"`},
		{'+', 13, `const version = "0.7.1"`},
	}
	if len(r.Diff) != 3 {
		t.Fatalf("diff lines = %d", len(r.Diff))
	}
	for i, w := range want {
		if r.Diff[i] != w {
			t.Errorf("diff[%d] = %+v, want %+v", i, r.Diff[i], w)
		}
	}
}

func TestParseClaudeToolResultBashOutput(t *testing.T) {
	r := parseClaudeToolResult([]byte(`{"stdout":"line1\nline2\nline3","stderr":""}`))
	if r == nil || len(r.Output) != 3 || r.Output[0] != "line1" {
		t.Fatalf("got %+v", r)
	}
}

func TestParseClaudeToolResultOutputTruncated(t *testing.T) {
	// 10 lines, cap 8 → 8 + a "more" marker.
	r := parseClaudeToolResult([]byte(`{"stdout":"a\nb\nc\nd\ne\nf\ng\nh\ni\nj"}`))
	if r == nil || len(r.Output) != maxOutputLines+1 {
		t.Fatalf("got %d lines", len(r.Output))
	}
	if last := r.Output[len(r.Output)-1]; last != "… (+2 more lines)" {
		t.Errorf("truncation marker = %q", last)
	}
}

func TestParseClaudeToolResultReadSummary(t *testing.T) {
	r := parseClaudeToolResult([]byte(`{"type":"text","file":{"numLines":1304}}`))
	if r == nil || r.Summary != "Read 1304 lines" {
		t.Fatalf("got %+v", r)
	}
}

func TestParseClaudeToolResultNil(t *testing.T) {
	for _, raw := range []string{``, `[]`, `{"agentId":"x"}`, `"   "`} {
		if r := parseClaudeToolResult([]byte(raw)); r != nil {
			t.Errorf("parseClaudeToolResult(%q) = %+v, want nil", raw, r)
		}
	}
}

func TestDiffSummary(t *testing.T) {
	cases := map[[2]int]string{
		{1, 1}: "Added 1 line, removed 1 line",
		{3, 0}: "Added 3 lines",
		{0, 2}: "Removed 2 lines",
		{0, 0}: "No changes",
	}
	for in, want := range cases {
		if got := diffSummary(in[0], in[1]); got != want {
			t.Errorf("diffSummary(%d,%d) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestClaudeToolLabel(t *testing.T) {
	cases := map[string]string{
		"Edit": "Update", "MultiEdit": "Update", "Write": "Write",
		"Read": "Read", "Grep": "Search", "Bash": "Bash",
		"exec_command": "exec_command", // codex tools keep their name
	}
	for name, want := range cases {
		if got := claudeToolLabel(name); got != want {
			t.Errorf("claudeToolLabel(%q) = %q, want %q", name, got, want)
		}
	}
	if got := baseName("/a/b/main.go"); got != "main.go" {
		t.Errorf("baseName = %q", got)
	}
}
