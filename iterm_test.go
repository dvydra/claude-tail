package main

import (
	"regexp"
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
	s := workspaceScript("/work/proj", "abc-123", "/usr/local/bin/entire-tail", "claude")
	checks := []string{
		`tell application "iTerm2"`,
		"tell current window",                                     // reuse current window, don't create one
		"set a to current session",                                // current pane becomes A
		"split vertically with default profile",                   // → B (right, full height)
		"split horizontally with default profile",                 // → C (below A)
		"cd '/work/proj' && 'claude' --resume 'abc-123'",          // A resumes the picked session
		"'/usr/local/bin/entire-tail' --follow-session 'abc-123'", // B follows by id (survives forks)
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

func TestNewWorkspaceScriptPinsSessionID(t *testing.T) {
	s := newWorkspaceScript("/work/proj", "/usr/local/bin/entire-tail", "11111111-2222-4333-8444-555555555555", "claude")
	checks := []string{
		"'claude' --session-id '11111111-2222-4333-8444-555555555555'",                         // A pins the id
		"'/usr/local/bin/entire-tail' --follow-session '11111111-2222-4333-8444-555555555555'", // B follows that exact id
	}
	for _, c := range checks {
		if !strings.Contains(s, c) {
			t.Errorf("new-workspace script missing %q:\n%s", c, s)
		}
	}
	// --wait-new was the racy predecessor; the pinned script must not use it.
	if strings.Contains(s, "--wait-new") {
		t.Error("new-workspace script should pin --session-id, not race with --wait-new")
	}
}

// The launcher is a preference (default `happy`), so both scripts must run the
// chosen binary in pane A — while pane B keeps running entire-tail itself, never
// the wrapper. A launcher given as a path with spaces has to survive shell
// quoting too.
func TestWorkspaceScriptsHonorClaudeBin(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	self := "/usr/local/bin/entire-tail"

	resume := workspaceScript("/work/proj", id, self, "happy")
	if !strings.Contains(resume, "cd '/work/proj' && 'happy' --resume '"+id+"'") {
		t.Errorf("resume workspace should launch happy:\n%s", resume)
	}
	fresh := newWorkspaceScript("/work/proj", self, id, "happy")
	if !strings.Contains(fresh, "cd '/work/proj' && 'happy' --session-id '"+id+"'") {
		t.Errorf("fresh workspace should launch happy:\n%s", fresh)
	}
	for name, s := range map[string]string{"resume": resume, "fresh": fresh} {
		if strings.Contains(s, "&& 'claude'") {
			t.Errorf("%s workspace still launches claude:\n%s", name, s)
		}
		if !strings.Contains(s, "'"+self+"' --follow-session '"+id+"'") {
			t.Errorf("%s workspace: pane B must still run entire-tail, not the launcher:\n%s", name, s)
		}
	}

	spaced := workspaceScript("/work/proj", id, self, "/opt/my agents/happy")
	if !strings.Contains(spaced, `&& '/opt/my agents/happy' --resume '`+id+`'`) {
		t.Errorf("a launcher path with spaces must stay quoted:\n%s", spaced)
	}
}

func TestNewSessionIDIsV4UUID(t *testing.T) {
	id := newSessionID()
	// 8-4-4-4-12 hex, version nibble 4, variant nibble in {8,9,a,b}.
	re := `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	if m, _ := regexp.MatchString(re, id); !m {
		t.Fatalf("newSessionID() = %q, not a v4 UUID", id)
	}
	if newSessionID() == id {
		t.Fatal("newSessionID() returned the same id twice")
	}
}
