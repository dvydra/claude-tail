package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelAge(t *testing.T) {
	now := int64(1_000_000)
	cases := []struct {
		ago  int64
		want string
	}{
		{10, "just now"},
		{44, "just now"},
		{60, "1m ago"},
		{90, "2m ago"},    // rounds (90+30)/60 = 2
		{3600, "60m ago"}, // still minutes (hours branch starts at 5400s)
		{5400, "2h ago"},  // (5400+1800)/3600 = 2 — note "1h ago" is unreachable
		{7200, "2h ago"},
		{86400 * 2, "2d ago"},
		{-100, "just now"}, // future clamps to 0
	}
	for _, c := range cases {
		if got := relAge(now-c.ago, now); got != c.want {
			t.Errorf("relAge(ago=%d) = %q, want %q", c.ago, got, c.want)
		}
	}
}

func TestParsePIDs(t *testing.T) {
	got := parsePIDs([]byte("123\n456\n789\n"))
	want := []int{123, 456, 789}
	if len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
		t.Errorf("got %v", got)
	}
	if len(parsePIDs([]byte(""))) != 0 {
		t.Error("empty → no pids")
	}
}

func TestParseLsofCwd(t *testing.T) {
	out := "p1234\nfcwd\nn/Users/me/project\n"
	if got := parseLsofCwd([]byte(out)); got != "/Users/me/project" {
		t.Errorf("got %q", got)
	}
	if got := parseLsofCwd([]byte("p1\nfcwd\n")); got != "" {
		t.Errorf("expected empty when no n-line, got %q", got)
	}
}

func TestCwShort(t *testing.T) {
	cases := map[string]string{
		"/Users/dvydra/src/dvydra/claude-tail": "dvydra/claude-tail",
		"/Users/me":                            "Users/me",
		"single":                               "single",
	}
	for in, want := range cases {
		if got := cwShort(in); got != want {
			t.Errorf("cwShort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollapsePreview(t *testing.T) {
	cases := map[string]string{
		"hello\tworld\n  multiple   spaces ": "hello world multiple spaces",
		"   trimmed   ":                      "trimmed",
	}
	for in, want := range cases {
		if got := collapsePreview(in); got != want {
			t.Errorf("collapsePreview(%q) = %q, want %q", in, got, want)
		}
	}
	// Truncation to 60 runes (57 + ellipsis).
	long := collapsePreview(strings.Repeat("x", 80))
	if r := []rune(long); len(r) != 58 || string(r[57]) != "…" {
		t.Errorf("expected 57 chars + ellipsis, got %d runes: %q", len(r), long)
	}
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("l1\nl2\nl3\nl4\nl5\n"), 0o644)
	got := tailLines(path, 2)
	if len(got) != 2 || string(got[0]) != "l4" || string(got[1]) != "l5" {
		t.Errorf("got %q", got)
	}
	// n larger than file → all lines.
	if got := tailLines(path, 100); len(got) != 5 {
		t.Errorf("got %d lines", len(got))
	}
}

func TestPreviewCandidate(t *testing.T) {
	// claude user string
	if got := previewCandidate(AgentClaude, []byte(`{"type":"user","message":{"content":"hi there"}}`)); got != "hi there" {
		t.Errorf("claude user: %q", got)
	}
	// codex agent_message
	if got := previewCandidate(AgentCodex, []byte(`{"type":"event_msg","payload":{"type":"agent_message","message":"done"}}`)); got != "done" {
		t.Errorf("codex: %q", got)
	}
	// agy USER_INPUT envelope (last one wins via the lead/trail regex)
	if got := previewCandidate(AgentAgy, []byte(`{"type":"USER_INPUT","content":"x <USER_REQUEST>the ask</USER_REQUEST> y"}`)); got != "the ask" {
		t.Errorf("agy: %q", got)
	}
}
