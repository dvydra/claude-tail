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
	s := workspaceScript("/work/proj", "abc-123", "/usr/local/bin/entire-tail", "claude", "", "")
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
	s := newWorkspaceScript("/work/proj", "/usr/local/bin/entire-tail", "11111111-2222-4333-8444-555555555555", "claude", "", "")
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

	resume := workspaceScript("/work/proj", id, self, "happy", "", "")
	if !strings.Contains(resume, "cd '/work/proj' && 'happy' --resume '"+id+"'") {
		t.Errorf("resume workspace should launch happy:\n%s", resume)
	}
	fresh := newWorkspaceScript("/work/proj", self, id, "happy", "", "")
	if !strings.Contains(fresh, "cd '/work/proj' && 'happy'") {
		t.Errorf("fresh workspace should launch happy:\n%s", fresh)
	}
	for name, s := range map[string]string{"resume": resume, "fresh": fresh} {
		if strings.Contains(s, "&& 'claude'") {
			t.Errorf("%s workspace still launches claude:\n%s", name, s)
		}
		if !strings.Contains(s, "'"+self+"' --") {
			t.Errorf("%s workspace: pane B must still run entire-tail, not the launcher:\n%s", name, s)
		}
	}

	spaced := workspaceScript("/work/proj", id, self, "/opt/my agents/happy", "", "")
	if !strings.Contains(spaced, `&& '/opt/my agents/happy' --resume '`+id+`'`) {
		t.Errorf("a launcher path with spaces must stay quoted:\n%s", spaced)
	}
}

// Only plain `claude` forwards --session-id to the process that writes the
// transcript. happy extracts the flag and, in its local hook mode, spawns claude
// WITHOUT it (dist/index-…mjs: the hookSettingsPath branch pushes --resume only),
// so Claude mints its own id and a pinned tail waits for a file that never
// appears. For any non-claude launcher the fresh workspace must therefore pass no
// id at all and let pane B discover the new session with --wait-new.
func TestNewWorkspaceScriptPinsOnlyForClaude(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	self := "/usr/local/bin/entire-tail"

	happy := newWorkspaceScript("/work/proj", self, id, "happy", "", "")
	if strings.Contains(happy, "--session-id") {
		t.Errorf("happy drops --session-id, so the script must not pass it:\n%s", happy)
	}
	if strings.Contains(happy, "--follow-session") {
		t.Errorf("nothing pins the id under happy, so pane B must not --follow-session:\n%s", happy)
	}
	if !strings.Contains(happy, "'"+self+"' --wait-new") {
		t.Errorf("pane B must discover the new session with --wait-new:\n%s", happy)
	}
	if !strings.Contains(happy, "cd '/work/proj' && 'happy'") {
		t.Errorf("pane A must still launch happy in the picked folder:\n%s", happy)
	}

	// Plain claude keeps the pinned-id contract, by name or by absolute path.
	for _, bin := range []string{"claude", "/opt/homebrew/bin/claude"} {
		s := newWorkspaceScript("/work/proj", self, id, bin, "", "")
		if !strings.Contains(s, shQuote(bin)+" --session-id '"+id+"'") {
			t.Errorf("%s: fresh workspace must still pin the id:\n%s", bin, s)
		}
		if !strings.Contains(s, "'"+self+"' --follow-session '"+id+"'") {
			t.Errorf("%s: pane B must still follow the pinned id:\n%s", bin, s)
		}
		if strings.Contains(s, "--wait-new") {
			t.Errorf("%s: pinned workspace must not fall back to --wait-new:\n%s", bin, s)
		}
	}
}

// The tap is opt-in AND fail-open: with no daemon the launched command must be
// byte-identical to what it was before the tap existed, and with a daemon the
// env assignment must land on pane A only — never on the tail or the shell.
func TestWorkspaceScriptsTapEnv(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	self := "/usr/local/bin/entire-tail"

	if tapEnvPrefix("") != "" {
		t.Fatalf("no daemon must yield no prefix, got %q", tapEnvPrefix(""))
	}
	prefix := tapEnvPrefix("http://127.0.0.1:47391")
	if prefix != "ANTHROPIC_BASE_URL='http://127.0.0.1:47391' ENABLE_TOOL_SEARCH=true " {
		t.Fatalf("prefix = %q", prefix)
	}
	// ENABLE_TOOL_SEARCH is load-bearing, not decoration: a custom base URL makes
	// Claude Code stop deferring MCP tool schemas, which on a large MCP fleet is
	// the difference between a working session and "Prompt is too long". Routing
	// must never silently change how requests are composed.
	if !strings.Contains(prefix, "ENABLE_TOOL_SEARCH=true") {
		t.Error("routing an agent must re-enable tool search")
	}

	for name, s := range map[string]string{
		"resume": workspaceScript("/work/proj", id, self, "claude", "", prefix),
		"fresh":  newWorkspaceScript("/work/proj", self, id, "claude", "", prefix),
	} {
		if !strings.Contains(s, "cd '/work/proj' && "+prefix+"'claude'") {
			t.Errorf("%s: agent pane should carry the tap env:\n%s", name, s)
		}
		// Pane B is entire-tail and pane C is a plain shell; neither talks to the
		// API, and routing them would be noise at best.
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, self) && strings.Contains(line, "ANTHROPIC_BASE_URL") {
				t.Errorf("%s: the tail pane must not get the tap env:\n%s", name, line)
			}
		}
		if n := strings.Count(s, "ANTHROPIC_BASE_URL"); n != 1 {
			t.Errorf("%s: want exactly one tap assignment, got %d:\n%s", name, n, s)
		}
	}

	// Without a daemon, both scripts must match the pre-tap output exactly.
	for name, pair := range map[string][2]string{
		"resume": {workspaceScript("/work/proj", id, self, "claude", "", ""), "cd '/work/proj' && 'claude' --resume '" + id + "'"},
		"fresh":  {newWorkspaceScript("/work/proj", self, id, "claude", "", ""), "cd '/work/proj' && 'claude' --session-id '" + id + "'"},
	} {
		if !strings.Contains(pair[0], pair[1]) {
			t.Errorf("%s: fail-open command changed:\n%s", name, pair[0])
		}
		if strings.Contains(pair[0], "ANTHROPIC_BASE_URL") {
			t.Errorf("%s: no daemon must mean no env assignment:\n%s", name, pair[0])
		}
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
