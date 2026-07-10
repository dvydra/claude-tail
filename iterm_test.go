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
	s := workspaceScript("/work/proj", "abc-123", "/sessions/abc-123.jsonl", "/usr/local/bin/entire-tail")
	checks := []string{
		`tell application "iTerm2"`,
		"tell current window",                                    // reuse current window, don't create one
		"set a to current session",                               // current pane becomes A
		"split vertically with default profile",                  // → B (right, full height)
		"split horizontally with default profile",                // → C (below A)
		"cd '/work/proj' && claude --resume 'abc-123'",           // A resumes the picked session
		"'/usr/local/bin/entire-tail' '/sessions/abc-123.jsonl'", // B tails that exact file
		"select a",
	}
	for _, c := range checks {
		if !strings.Contains(s, c) {
			t.Errorf("workspace script missing %q:\n%s", c, s)
		}
	}
	// The single-pane check lives in Go (itermSinglePane); the script itself
	// always reuses the current window and never creates one.
	if strings.Contains(s, "create window") {
		t.Error("workspace script should reuse the current window, not create one")
	}
}
