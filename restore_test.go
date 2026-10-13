package main

import (
	"bytes"
	"errors"
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

func seedPreviousBoot(t *testing.T, home string, tabs []layoutTab) string {
	t.Helper()
	dir := filepath.Join(layoutRoot(home), "100")
	// A snapshot with no session has nothing to restore (layoutHasSession), so
	// every fixture also carries an Amp tab, which needs no transcript on disk.
	tabs = append(tabs, layoutTab{Agent: AgentAmp, Session: "T-01a131ed-1c68-776f-8255-616e9ccc86b0", Cwd: home, Panes: []string{"agent", "shell", "tail"}})
	snap := layoutSnapshot{Boot: 100, Windows: []layoutWindow{{ITermWindow: 5, Tabs: tabs}}}
	if err := writeJSONAtomic(filepath.Join(dir, "snapshot.json"), snap); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stubRestoreIO(t *testing.T) *[]string {
	t.Helper()
	var scripts []string
	o1, o2, o3, o4, o5 := restoreOsa, restorePS, restoreOwnTTY, layoutBoot, restoreRunning
	t.Cleanup(func() { restoreOsa, restorePS, restoreOwnTTY, layoutBoot, restoreRunning = o1, o2, o3, o4, o5 })
	restoreRunning = func(string) map[string]bool { return map[string]bool{} }
	restoreOsa = func(s string) (string, error) {
		scripts = append(scripts, s)
		if strings.Contains(s, "create window") {
			return "77\n", nil
		}
		if strings.Contains(s, "unique id of s") {
			return "5\t1\tR\t/dev/ttys009\n77\t1\tN\t/dev/ttys010\n", nil
		}
		return "", nil
	}
	restorePS = func() (string, error) { return "  1 ttys009 -zsh\n  2 ttys010 -zsh\n  3 ttys010 claude\n", nil }
	restoreOwnTTY = func() string { return "ttys009" }
	layoutBoot = func() int64 { return 200 }
	return &scripts
}

func TestRunRestoreOpensWindowAndMarks(t *testing.T) {
	home := t.TempDir()
	dir := seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	scripts := stubRestoreIO(t)
	var out bytes.Buffer
	if err := runRestore(nil, Config{}, home, strings.NewReader("n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "✔") {
		t.Errorf("report:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "restored")); err != nil {
		t.Error("restored marker not written")
	}
	for _, s := range *scripts {
		if strings.Contains(s, "close (windows") {
			t.Error("answered n: nothing may be closed")
		}
	}
}

func TestRunRestoreRefusesTwiceWithoutForce(t *testing.T) {
	home := t.TempDir()
	seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	stubRestoreIO(t)
	var out bytes.Buffer
	_ = runRestore(nil, Config{}, home, strings.NewReader("n\n"), &out)
	out.Reset()
	scripts := stubRestoreIO(t)
	if err := runRestore(nil, Config{}, home, strings.NewReader("n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already restored") || len(*scripts) != 0 {
		t.Errorf("second run must refuse: %q, %d scripts", out.String(), len(*scripts))
	}
	out.Reset()
	if err := runRestore([]string{"--force"}, Config{}, home, strings.NewReader("n\n"), &out); err != nil || len(*scripts) == 0 {
		t.Errorf("--force must restore again: %v", err)
	}
}

func TestRunRestoreClosesOnYes(t *testing.T) {
	home := t.TempDir()
	seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	scripts := stubRestoreIO(t)
	var out bytes.Buffer
	if err := runRestore(nil, Config{}, home, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	last := (*scripts)[len(*scripts)-1]
	if !strings.Contains(last, "close (windows whose id is 5)") || strings.Contains(last, "id is 77") {
		t.Errorf("must close only the old idle window 5:\n%s", last)
	}
}

func TestRunRestoreDryRunAndDismiss(t *testing.T) {
	home := t.TempDir()
	dir := seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	scripts := stubRestoreIO(t)
	var out bytes.Buffer
	_ = runRestore([]string{"--dry-run"}, Config{}, home, strings.NewReader(""), &out)
	if len(*scripts) != 0 {
		t.Error("--dry-run must not run osascript")
	}
	if _, err := os.Stat(filepath.Join(dir, "restored")); err == nil {
		t.Error("--dry-run must not mark")
	}
	_ = runRestore([]string{"--dismiss"}, Config{}, home, strings.NewReader(""), &out)
	if _, tabs := pendingRestore(home); tabs != 0 {
		t.Error("--dismiss must clear the pending restore")
	}
}

func TestRunRestoreNothingToDo(t *testing.T) {
	home := t.TempDir()
	stubRestoreIO(t)
	var out bytes.Buffer
	if err := runRestore(nil, Config{}, home, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no saved layout") {
		t.Errorf("out = %q", out.String())
	}
}

func TestRunRestoreNoCloseOfferWhenPSFails(t *testing.T) {
	home := t.TempDir()
	seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	scripts := stubRestoreIO(t)
	restorePS = func() (string, error) { return "", errors.New("ps died") }
	var out bytes.Buffer
	if err := runRestore(nil, Config{}, home, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "close the restored windows") {
		t.Errorf("no process list means no idea what is busy; must not offer to close:\n%s", out.String())
	}
	for _, s := range *scripts {
		if strings.Contains(s, "close (windows") {
			t.Error("closed windows without a process list")
		}
	}
}

func TestRunRestoreNoMarkerWhenEveryWindowFails(t *testing.T) {
	home := t.TempDir()
	dir := seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	stubRestoreIO(t)
	restoreOsa = func(s string) (string, error) {
		if strings.Contains(s, "create window") {
			return "", errors.New("not authorized to send Apple events")
		}
		return "", nil
	}
	var out bytes.Buffer
	_ = runRestore(nil, Config{}, home, strings.NewReader("n\n"), &out)
	if _, err := os.Stat(filepath.Join(dir, "restored")); err == nil {
		t.Error("nothing opened, so the snapshot must stay restorable")
	}
}

func TestPlanTabsDedupesAndRespectsRunning(t *testing.T) {
	env, id := testRestoreEnv(t)
	other := "33333333-3333-4333-8333-333333333333"
	env.Running = func(key string) bool { return key == paneSessionKey(AgentClaude, other) }
	tabs := []layoutTab{
		{Agent: AgentClaude, Session: id, Cwd: "/r/one", Panes: []string{"agent", "shell", "tail"}},
		{Agent: AgentClaude, Session: id, Cwd: "/r/one", Panes: []string{"agent", "shell"}},
		{Agent: AgentClaude, Session: id, Cwd: "/r/one", Panes: []string{"shell", "tail"}},
		{Agent: AgentClaude, Session: other, Cwd: "/r/one", Panes: []string{"agent"}},
	}
	got := planTabs(tabs, env, map[string]bool{})
	if got[0].Skip != "" || got[0].Agent == "" {
		t.Errorf("first agent tab resumes: %#v", got[0])
	}
	if got[1].Skip != "duplicate of an earlier tab" {
		t.Errorf("second agent tab for the same session must not resume it again: %#v", got[1])
	}
	if got[2].Skip != "" || got[2].Agent != "" || !strings.Contains(got[2].Shell, "--follow-session '"+id+"'") {
		t.Errorf("a tail-only tab reopens as a tail only: %#v", got[2])
	}
	if got[3].Skip != "already running" {
		t.Errorf("a session already running must not be resumed: %#v", got[3])
	}
}

func TestRunRestoreDryRunShowsRunning(t *testing.T) {
	home := t.TempDir()
	seedPreviousBoot(t, home, nil)
	stubRestoreIO(t)
	restoreRunning = func(string) map[string]bool {
		return map[string]bool{paneSessionKey(AgentAmp, "T-01a131ed-1c68-776f-8255-616e9ccc86b0"): true}
	}
	var out bytes.Buffer
	_ = runRestore([]string{"--dry-run"}, Config{}, home, strings.NewReader(""), &out)
	if !strings.Contains(out.String(), "skipped: already running") {
		t.Errorf("a dry run must show what a real run would skip:\n%s", out.String())
	}
}
