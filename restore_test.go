package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRestoreEnv(t *testing.T) (restoreEnv, string) {
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "-r-one")
	_ = os.MkdirAll(proj, 0o700)
	id := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(proj, id+".jsonl")
	_ = os.WriteFile(path, []byte(`{"type":"user","cwd":"/r/one","gitBranch":"main","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	return restoreEnv{
		Home: home, Self: "/bin/entire-tail", ClaudeBin: "/bin/claude", AmpBin: "/bin/amp",
		PathForID: func(s string) string {
			if s == id {
				return path
			}
			return ""
		},
		DirExists: func(d string) bool { return d == "/r/one" || d == "/r/two" },
	}, id
}

func TestPlanTabClaude(t *testing.T) {
	env, id := testRestoreEnv(t)
	got := planTab(layoutTab{Agent: AgentClaude, Session: id, Cwd: "/r/one"}, env)
	if got.Skip != "" {
		t.Fatalf("skipped: %q", got.Skip)
	}
	if !strings.Contains(got.Agent, "--resume '"+id+"'") || !strings.Contains(got.Tail, "--follow-session '"+id+"'") {
		t.Errorf("agent=%q tail=%q", got.Agent, got.Tail)
	}
	if got.Shell == "" || !strings.Contains(got.Label, shortID(id)) {
		t.Errorf("shell=%q label=%q", got.Shell, got.Label)
	}
}

func TestPlanTabMissingTranscriptSkips(t *testing.T) {
	env, _ := testRestoreEnv(t)
	got := planTab(layoutTab{Agent: AgentClaude, Session: "22222222-2222-4222-8222-222222222222", Cwd: "/r/one"}, env)
	if got.Skip != "transcript missing" {
		t.Errorf("skip = %q", got.Skip)
	}
}

func TestPlanTabAmpAndShell(t *testing.T) {
	env, _ := testRestoreEnv(t)
	amp := planTab(layoutTab{Agent: AgentAmp, Session: "T-01a131ed-1c68-776f-8255-616e9ccc86b0", Cwd: "/r/two"}, env)
	if amp.Skip != "" || !strings.Contains(amp.Agent, "threads continue") || !strings.Contains(amp.Tail, "--agent amp") {
		t.Errorf("amp = %#v", amp)
	}
	sh := planTab(layoutTab{Cwd: "/gone"}, env)
	if sh.Skip != "" || sh.Agent != "" || sh.Tail != "" || sh.Shell != "cd "+shQuote(env.Home) {
		t.Errorf("shell tab with a deleted cwd must open in home: %#v", sh)
	}
}

func TestRestoreWindowScript(t *testing.T) {
	tabs := []restoreTab{
		{Agent: "cd '/r/it'\\''s' && claude --resume 'x'", Tail: "tail \"q\"", Shell: "cd '/r'"},
		{Skip: "transcript missing"},
		{Shell: "cd '/r/two'"},
	}
	s := restoreWindowScript("/bin/zsh", tabs)
	if strings.Count(s, "create window") != 1 || strings.Count(s, "create tab") != 1 {
		t.Errorf("want one window and one extra tab:\n%s", s)
	}
	if strings.Count(s, "split vertically") != 1 || strings.Count(s, "split horizontally") != 1 {
		t.Errorf("only the workspace tab is split:\n%s", s)
	}
	if !strings.Contains(s, asEscape(paneCommand("/bin/zsh", `tail "q"`))) {
		t.Errorf("double quotes must be AppleScript-escaped:\n%s", s)
	}
	if !strings.Contains(s, "return id of w") {
		t.Errorf("script must return the new window id:\n%s", s)
	}
	if restoreWindowScript("/bin/zsh", []restoreTab{{Skip: "x"}}) != "" {
		t.Error("all tabs skipped → no script")
	}
}

func TestIdleWindows(t *testing.T) {
	panes := []itermPane{
		{Window: 1, Tab: 1, UUID: "a", TTY: "ttys001"},
		{Window: 1, Tab: 1, UUID: "b", TTY: "ttys002"},
		{Window: 2, Tab: 1, UUID: "c", TTY: "ttys003"},
		{Window: 3, Tab: 1, UUID: "d", TTY: "ttys004"},
	}
	byTTY := map[string][]psProc{
		"ttys001": {{PID: 1, Comm: "login"}, {PID: 2, Comm: "-zsh"}},
		"ttys002": {{PID: 3, Comm: "-zsh"}, {PID: 4, Comm: "entire-tail"}}, // the restore itself
		"ttys003": {{PID: 5, Comm: "-zsh"}, {PID: 6, Comm: "vim"}},
		"ttys004": {{PID: 7, Comm: "-zsh"}},
	}
	got := idleWindows(panes, byTTY, "ttys002")
	if len(got) != 2 || got[0] != 3 || got[1] != 1 {
		t.Errorf("idleWindows = %v, want [3 1] (window 2 busy, own window last)", got)
	}
}

func TestCloseWindowsScript(t *testing.T) {
	s := closeWindowsScript([]int{3, 1})
	if !strings.Contains(s, "close (windows whose id is 3)") || strings.Index(s, "id is 3") > strings.Index(s, "id is 1") {
		t.Errorf("close script keeps order:\n%s", s)
	}
}
