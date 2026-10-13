package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseItermPanes(t *testing.T) {
	out := "155\t1\tAAA\t/dev/ttys003\n155\t1\tBBB\t/dev/ttys004\n155\t2\tCCC\t\nbad line\n"
	got := parseItermPanes(out)
	want := []itermPane{
		{Window: 155, Tab: 1, UUID: "AAA", TTY: "ttys003"},
		{Window: 155, Tab: 1, UUID: "BBB", TTY: "ttys004"},
		{Window: 155, Tab: 2, UUID: "CCC", TTY: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d panes, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pane %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestParsePSProcs(t *testing.T) {
	out := "  101 ttys003  login\n  102 ttys003  -zsh\n  150 ttys003  /Users/x/My Tools/entire-tail\n  200 ??       launchd\n"
	got := parsePSProcs(out)
	if len(got) != 4 {
		t.Fatalf("got %d procs: %#v", len(got), got)
	}
	if got[2] != (psProc{PID: 150, TTY: "ttys003", Comm: "/Users/x/My Tools/entire-tail"}) {
		t.Errorf("comm with a space parsed as %#v", got[2])
	}
}

func TestPaneRole(t *testing.T) {
	sh := psProc{PID: 2, Comm: "-zsh"}
	cases := []struct {
		procs []psProc
		want  string
	}{
		{nil, "shell"},
		{[]psProc{sh}, "shell"},
		{[]psProc{sh, {PID: 3, Comm: "claude"}}, "agent"},
		{[]psProc{sh, {PID: 3, Comm: "amp"}}, "agent"},
		{[]psProc{sh, {PID: 3, Comm: "/opt/bin/entire-tail"}}, "tail"},
		{[]psProc{sh, {PID: 3, Comm: "vim"}}, "shell"},
	}
	for _, c := range cases {
		if got := paneRole(c.procs); got != c.want {
			t.Errorf("paneRole(%v) = %q, want %q", c.procs, got, c.want)
		}
	}
}

func TestPaneShellPID(t *testing.T) {
	procs := []psProc{{PID: 9, Comm: "claude"}, {PID: 5, Comm: "-zsh"}, {PID: 7, Comm: "zsh"}, {PID: 4, Comm: "login"}}
	if got := paneShellPID(procs); got != 5 {
		t.Errorf("paneShellPID = %d, want 5 (lowest shell pid, login excluded)", got)
	}
	if got := paneShellPID([]psProc{{PID: 9, Comm: "claude"}}); got != 0 {
		t.Errorf("no shell: got %d", got)
	}
}

func TestBuildLayout(t *testing.T) {
	panes := []itermPane{
		{Window: 155, Tab: 1, UUID: "A1", TTY: "ttys001"},
		{Window: 155, Tab: 1, UUID: "B1", TTY: "ttys002"},
		{Window: 155, Tab: 1, UUID: "C1", TTY: "ttys003"},
		{Window: 155, Tab: 2, UUID: "A2", TTY: "ttys004"},
		{Window: 155, Tab: 3, UUID: "A3", TTY: "ttys005"},
		{Window: 155, Tab: 3, UUID: "B3", TTY: "ttys006"},
		{Window: 200, Tab: 1, UUID: "X", TTY: ""},
	}
	byTTY := map[string][]psProc{
		"ttys001": {{PID: 1, Comm: "-zsh"}, {PID: 10, Comm: "claude"}},
		"ttys002": {{PID: 2, Comm: "-zsh"}, {PID: 20, Comm: "entire-tail"}},
		"ttys003": {{PID: 3, Comm: "-zsh"}},
		"ttys004": {{PID: 4, Comm: "-zsh"}},
		"ttys005": {{PID: 5, Comm: "-zsh"}},
		"ttys006": {{PID: 6, Comm: "-zsh"}, {PID: 60, Comm: "entire-tail"}},
	}
	cwds := map[string]string{"C1": "/r/one", "A2": "/r/two", "A3": "/r/three"}
	agents := map[string]string{"A1": "claude:11111111-1111-4111-8111-111111111111"}
	registry := map[string]paneEntry{"B3": {Follow: "T-abc", Agent: AgentAmp, Pid: 60}}

	got := buildLayout(panes, byTTY, cwds, agents, registry)
	if len(got) != 2 {
		t.Fatalf("windows = %d, want 2", len(got))
	}
	w := got[0]
	if w.ITermWindow != 155 || len(w.Tabs) != 3 {
		t.Fatalf("window 0 = %#v", w)
	}
	if tab := w.Tabs[0]; tab.Agent != AgentClaude || tab.Session != "11111111-1111-4111-8111-111111111111" || tab.Cwd != "/r/one" {
		t.Errorf("tab 1 = %#v", tab)
	}
	if got, want := w.Tabs[0].Panes, []string{"agent", "tail", "shell"}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("tab 1 panes = %v", got)
	}
	if tab := w.Tabs[1]; tab.Agent != "" || tab.Session != "" || tab.Cwd != "/r/two" {
		t.Errorf("tab 2 (plain shell) = %#v", tab)
	}
	if tab := w.Tabs[2]; tab.Agent != AgentAmp || tab.Session != "T-abc" || tab.Cwd != "/r/three" {
		t.Errorf("tab 3 (tail only, from registry) = %#v", tab)
	}
	if tab := got[1].Tabs[0]; len(tab.Panes) != 1 || tab.Panes[0] != "shell" {
		t.Errorf("unclassifiable pane must be a shell: %#v", tab)
	}
}

func TestSplitSessionKey(t *testing.T) {
	if a, id := splitSessionKey("claude:abc"); a != AgentClaude || id != "abc" {
		t.Errorf("claude key = %q %q", a, id)
	}
	if a, id := splitSessionKey("amp:T-1"); a != AgentAmp || id != "T-1" {
		t.Errorf("amp key = %q %q", a, id)
	}
	if a, id := splitSessionKey(""); a != "" || id != "" {
		t.Errorf("empty key = %q %q", a, id)
	}
}

func TestPreviousBootDir(t *testing.T) {
	root := t.TempDir()
	write := func(boot string) {
		d := filepath.Join(root, boot)
		_ = os.MkdirAll(d, 0o700)
		_ = os.WriteFile(filepath.Join(d, "snapshot.json"), []byte(`{"boot":1}`), 0o600)
	}
	write("100")
	write("300")
	write("500")                                       // the current boot
	_ = os.MkdirAll(filepath.Join(root, "400"), 0o700) // no snapshot inside
	_ = os.MkdirAll(filepath.Join(root, "junk"), 0o700)

	dir, ok := previousBootDir(root, 500)
	if !ok || filepath.Base(dir) != "300" {
		t.Errorf("previousBootDir = %q %v, want .../300", dir, ok)
	}
	if _, ok := previousBootDir(root, 100); ok {
		t.Error("nothing older than the oldest boot")
	}
}

func TestReadLayoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	snap := layoutSnapshot{Boot: 7, Taken: time.Unix(10, 0).UTC(), Windows: []layoutWindow{{ITermWindow: 1, Tabs: []layoutTab{{Cwd: "/x", Panes: []string{"shell"}}}}}}
	if err := writeJSONAtomic(filepath.Join(dir, "snapshot.json"), snap); err != nil {
		t.Fatal(err)
	}
	got, ok := readLayoutSnapshot(dir)
	if !ok || got.Boot != 7 || len(got.Windows) != 1 || got.Windows[0].Tabs[0].Cwd != "/x" {
		t.Errorf("round trip = %#v %v", got, ok)
	}
	if _, ok := readLayoutSnapshot(t.TempDir()); ok {
		t.Error("missing snapshot must read as not ok")
	}
}
