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

// lines is a workspace's three pane command lines, one per line, for the
// substring and per-pane checks below.
func (w workspaceLaunch) lines() string { return w.Agent + "\n" + w.Tail + "\n" + w.Shell }

func TestWorkspaceScript(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	w := workspaceScript("/work/proj", "abc-123", "/usr/local/bin/entire-tail", "claude", "", "")
	if w.Agent != "cd '/work/proj' && 'claude' --resume 'abc-123'" { // A resumes the picked session
		t.Errorf("agent = %q", w.Agent)
	}
	if w.Tail != "cd '/work/proj' && '/usr/local/bin/entire-tail' --follow-session 'abc-123'" { // B follows by id (survives forks)
		t.Errorf("tail = %q", w.Tail)
	}
	// A is the pane the picker runs in, exec'd by us — never started by iTerm.
	if !w.InPlace {
		t.Error("the resume workspace always runs its agent in this pane")
	}
	checks := []string{
		`tell application "iTerm2"`,
		"tell current window",      // reuse current window, don't create one
		"set a to current session", // current pane becomes A
		`split vertically with default profile command "` + asEscape(paneCommand("/bin/zsh", w.Tail)) + `"`,    // → B (right, full height)
		`split horizontally with default profile command "` + asEscape(paneCommand("/bin/zsh", w.Shell)) + `"`, // → C (below A)
		"select a",
	}
	for _, c := range checks {
		if !strings.Contains(w.Script, c) {
			t.Errorf("workspace script missing %q:\n%s", c, w.Script)
		}
	}
	// The single-pane check lives in Go (itermSinglePane); the script itself
	// always reuses the current window and never creates one.
	if strings.Contains(w.Script, "create window") {
		t.Error("workspace script should reuse the current window, not create one")
	}
}

func TestAmpWorkspaceScripts(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	resume := ampWorkspaceScript("/work/proj", "T-123", "/usr/local/bin/entire-tail", "/usr/local/bin/amp", true)
	if resume.Agent != "cd '/work/proj' && '/usr/local/bin/amp' threads continue 'T-123'" {
		t.Fatalf("resume agent = %q", resume.Agent)
	}
	if resume.Tail != "cd '/work/proj' && '/usr/local/bin/entire-tail' --agent amp --follow-session 'T-123'" {
		t.Fatalf("resume tail = %q", resume.Tail)
	}
	fresh := ampWorkspaceScript("/work/proj", "", "/usr/local/bin/entire-tail", "/usr/local/bin/amp", false)
	if fresh.Agent != "cd '/work/proj' && '/usr/local/bin/amp'" || !strings.Contains(fresh.Tail, "--agent amp --wait-new") {
		t.Fatalf("fresh = %+v", fresh)
	}
}

// No pane is typed into: `write text` put every command on screen and queued
// A's in the tty's input buffer, behind which anything typed before the agent
// took over would land. A's command must not be in the script when A is this
// pane, and must start the new window when it isn't.
func TestWorkspaceScriptsNeverTypeIntoAPane(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	id := "11111111-2222-4333-8444-555555555555"
	self := "/usr/local/bin/entire-tail"
	resume := workspaceScript("/work/proj", id, self, "claude", "", "")
	here := newWorkspaceScript("/work/proj", self, id, "claude", "", "", true)
	away := newWorkspaceScript("/work/proj", self, id, "claude", "", "", false)
	for name, w := range map[string]workspaceLaunch{"resume": resume, "fresh here": here, "fresh new window": away} {
		if strings.Contains(w.Script, "write text") {
			t.Errorf("%s: a pane is typed into:\n%s", name, w.Script)
		}
	}
	for name, w := range map[string]workspaceLaunch{"resume": resume, "fresh here": here} {
		if strings.Contains(w.Script, "--session-id") || strings.Contains(w.Script, "--resume") {
			t.Errorf("%s: pane A runs here, so its command must not be in the script:\n%s", name, w.Script)
		}
	}
	if away.InPlace {
		t.Error("a new window's agent is started by iTerm, not exec'd here")
	}
	want := `create window with default profile command "` + asEscape(paneCommand("/bin/zsh", away.Agent)) + `"`
	if !strings.Contains(away.Script, want) {
		t.Errorf("new window must be started with the agent:\n%s", away.Script)
	}
}

// A started pane ends when its command does, so each is wrapped to become an
// interactive shell afterwards, and quoted to survive iTerm's shell-style split.
func TestPaneCommand(t *testing.T) {
	got := paneCommand("/bin/zsh", "cd '/it'\\''s' && x")
	want := `'/bin/zsh' -lc 'cd '\''/it'\''\'\'''\''s'\'' && x; exec '\''/bin/zsh'\'' -l'`
	if got != want {
		t.Errorf("paneCommand = %s\nwant           %s", got, want)
	}
}

func TestResumeCommand(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	if got := resumeCommand("/work/proj", id, "claude", ""); got != "cd '/work/proj' && 'claude' --resume '"+id+"'" {
		t.Errorf("resumeCommand = %q", got)
	}
	if got := resumeCommand("", id, "claude", ""); got != "'claude' --resume '"+id+"'" {
		t.Errorf("no cwd: resumeCommand = %q", got)
	}
	acct := accountEnvPrefix(claudeProfile{Name: personalProfile, Dir: "/home/me/.claude-personal"})
	if got := resumeCommand("/work/proj", id, "claude", acct); !strings.Contains(got, "&& "+acct+"'claude' --resume") {
		t.Errorf("a personal session must resume as personal: %q", got)
	}
	if got := resumeCommand("/work/proj", "claude_session", "claude", ""); got != "" {
		t.Errorf("a fixture path is not a session id, want no line, got %q", got)
	}

	// The panel's session section carries it, right under the session, and only
	// when there is one.
	info := testHelpInfo()
	if ctx := strings.Join(settingsContext(info), "\n"); strings.Contains(ctx, "resume") {
		t.Errorf("no resume command, no row:\n%s", ctx)
	}
	info.Resume = "cd '/work/proj' && 'claude' --resume '" + id + "'"
	ctx := settingsContext(info)
	if len(ctx) < 3 || ctx[2] != "resume    "+info.Resume {
		t.Errorf("resume row missing or misplaced:\n%s", strings.Join(ctx, "\n"))
	}
}

func TestNewWorkspaceScriptPinsSessionID(t *testing.T) {
	s := newWorkspaceScript("/work/proj", "/usr/local/bin/entire-tail", "11111111-2222-4333-8444-555555555555", "claude", "", "", true).lines()
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

	resume := workspaceScript("/work/proj", id, self, "happy", "", "").lines()
	if !strings.Contains(resume, "cd '/work/proj' && 'happy' --resume '"+id+"'") {
		t.Errorf("resume workspace should launch happy:\n%s", resume)
	}
	fresh := newWorkspaceScript("/work/proj", self, id, "happy", "", "", true).lines()
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

	spaced := workspaceScript("/work/proj", id, self, "/opt/my agents/happy", "", "").lines()
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

	happy := newWorkspaceScript("/work/proj", self, id, "happy", "", "", true).lines()
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
		s := newWorkspaceScript("/work/proj", self, id, bin, "", "", true).lines()
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
		"resume": workspaceScript("/work/proj", id, self, "claude", "", prefix).lines(),
		"fresh":  newWorkspaceScript("/work/proj", self, id, "claude", "", prefix, true).lines(),
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
		"resume": {workspaceScript("/work/proj", id, self, "claude", "", "").lines(), "cd '/work/proj' && 'claude' --resume '" + id + "'"},
		"fresh":  {newWorkspaceScript("/work/proj", self, id, "claude", "", "", true).lines(), "cd '/work/proj' && 'claude' --session-id '" + id + "'"},
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
