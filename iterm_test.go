package main

import (
	"strings"
	"testing"
)

func TestShQuote(t *testing.T) {
	cases := map[string]string{
		"/tmp/x":         "'/tmp/x'",
		"/has space/dir": "'/has space/dir'",
		"/it's/tricky":   `'/it'\''s/tricky'`,
	}
	for in, want := range cases {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAsEscape(t *testing.T) {
	// A shell command with a single-quoted embedded apostrophe carries a
	// backslash; both it and any double quote must be escaped for AppleScript.
	in := `cd '/it'\''s' && x "y"`
	got := asEscape(in)
	if !strings.Contains(got, `\\`) {
		t.Errorf("backslash not escaped: %q", got)
	}
	if !strings.Contains(got, `\"y\"`) {
		t.Errorf("double quote not escaped: %q", got)
	}
}

func TestWorkspaceScript(t *testing.T) {
	s := workspaceScript("/work/proj", "/usr/local/bin/entire-tail")
	checks := []string{
		`tell application "iTerm2"`,
		"tell current window",                     // reuse current window, don't create one
		"set a to current session",                // current pane becomes A
		"split vertically with default profile",   // → B (right, full height)
		"split horizontally with default profile", // → C (below A)
		"cd '/work/proj' && claude",
		"'/usr/local/bin/entire-tail' --no-pick",
		"select a",
	}
	for _, c := range checks {
		if !strings.Contains(s, c) {
			t.Errorf("workspace script missing %q:\n%s", c, s)
		}
	}
	// It must reuse the current window, not open a new one.
	if strings.Contains(s, "create window") {
		t.Error("workspace should reuse the current window, not create one")
	}
	// The workspace must not run --resume (that's the resume-pair layout).
	if strings.Contains(s, "--resume") {
		t.Error("workspace script should launch a fresh claude, not --resume")
	}
}

func TestResumePairScript(t *testing.T) {
	s := resumePairScript("/work/proj", "/sessions/abc.jsonl", "abc-123", "/bin/entire-tail")
	checks := []string{
		"cd '/work/proj' && claude --resume 'abc-123'",
		"'/bin/entire-tail' '/sessions/abc.jsonl'",
		"split vertically with default profile",
	}
	for _, c := range checks {
		if !strings.Contains(s, c) {
			t.Errorf("resume-pair script missing %q:\n%s", c, s)
		}
	}
	// Two panes only — no second (horizontal) split.
	if strings.Contains(s, "split horizontally") {
		t.Error("resume pair should be two panes (one vertical split)")
	}
}
