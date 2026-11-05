package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func launchThread(t *testing.T, sessions []threadSessionRef, claimPath string, f threadFacts) (workspaceLaunch, threadSessionRef, string) {
	t.Helper()
	th := thread{Dir: "/vault/Threads/foo", Meta: threadMeta{Slug: "foo", Sessions: sessions}}
	if claimPath != "" {
		th.Meta.Claims = []threadClaim{{Kind: "worktree", Path: claimPath}}
	}
	return threadLaunch(th, f, "/bin/entire-tail", "claude", func() string { return "11111111-2222-4333-8444-555555555555" })
}

func TestThreadLaunchRefusesRunning(t *testing.T) {
	f := threadFacts{Running: &liveSession{PID: 42, Cwd: "/x/y"}}
	_, _, refuse := launchThread(t, nil, t.TempDir(), f)
	if refuse != "already running in /x/y (pid 42)" {
		t.Fatalf("refuse = %q", refuse)
	}
}

func TestThreadLaunchNoCwd(t *testing.T) {
	_, _, refuse := launchThread(t, nil, "/does/not/exist", threadFacts{})
	if refuse != "no working directory recorded" {
		t.Fatalf("refuse = %q", refuse)
	}
}

// ⏎ opens a thread as a fresh session even when it has old ones: resuming
// one brought its stale context back, which is not what opening a thread means.
func TestThreadLaunchNeverResumes(t *testing.T) {
	const fresh = "11111111-2222-4333-8444-555555555555"
	for _, old := range []threadSessionRef{
		{Agent: "claude", ID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"},
		{Agent: "amp", ID: "T-abc"},
	} {
		dir := t.TempDir()
		w, reg, refuse := launchThread(t, []threadSessionRef{old}, dir, threadFacts{})
		if refuse != "" {
			t.Fatal(refuse)
		}
		if strings.Contains(w.Agent, "--resume") || strings.Contains(w.Agent, "threads continue") || strings.Contains(w.Agent, old.ID) {
			t.Errorf("%s: resumed the old session: %s", old.Agent, w.Agent)
		}
		if !strings.Contains(w.Agent, "--session-id '"+fresh+"'") || !strings.Contains(w.Agent, dir) {
			t.Errorf("%s: agent line: %s", old.Agent, w.Agent)
		}
		if reg != (threadSessionRef{Agent: "claude", ID: fresh}) {
			t.Errorf("%s: register = %+v", old.Agent, reg)
		}
	}
}

func TestThreadLaunchCwd(t *testing.T) {
	claim, sess := t.TempDir(), t.TempDir()
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	th := thread{Dir: "/v/foo", Meta: threadMeta{Sessions: []threadSessionRef{{Agent: "claude", ID: id}}}}
	env := func(cwd string) threadLaunchEnv {
		return func(agent, id string) (string, string, string) { return "", "", cwd }
	}
	newID := func() string { return "11111111-2222-4333-8444-555555555555" }
	cd := func(dir string) string { return "cd " + shQuote(dir) + " && " }

	if w, _, r := threadLaunchWith(th, threadFacts{}, "/bin/et", "claude", newID, env(sess)); r != "" || !strings.HasPrefix(w.Agent, cd(sess)) {
		t.Errorf("no claim should fall back to the newest session's cwd: %q %q", w.Agent, r)
	}
	claimed := th
	claimed.Meta.Claims = []threadClaim{{Kind: "worktree", Path: "/gone"}, {Kind: "worktree", Path: claim}}
	if w, _, r := threadLaunchWith(claimed, threadFacts{}, "/bin/et", "claude", newID, env(sess)); r != "" || !strings.HasPrefix(w.Agent, cd(claim)) {
		t.Errorf("the first existing claim should win: %q %q", w.Agent, r)
	}
	if _, _, r := threadLaunchWith(th, threadFacts{}, "/bin/et", "claude", newID, env("/gone/too")); r != "no working directory recorded" {
		t.Errorf("refuse = %q", r)
	}
}

// The fresh session runs as the account the thread last used.
func TestThreadLaunchKeepsAccount(t *testing.T) {
	th := thread{Dir: "/v/foo", Meta: threadMeta{
		Sessions: []threadSessionRef{{Agent: "claude", ID: "old"}},
		Claims:   []threadClaim{{Kind: "worktree", Path: t.TempDir()}},
	}}
	env := func(agent, id string) (string, string, string) {
		if id == "old" {
			return "CLAUDE_CONFIG_DIR=/p ", "", ""
		}
		return "", "", ""
	}
	w, _, r := threadLaunchWith(th, threadFacts{}, "/bin/et", "claude", func() string { return "new" }, env)
	if r != "" || !strings.Contains(w.Agent, "CLAUDE_CONFIG_DIR=/p ") {
		t.Fatalf("agent line: %s %q", w.Agent, r)
	}
}

func TestThreadLaunchFreshPrompt(t *testing.T) {
	dir := t.TempDir()
	th := thread{Dir: "/vault/Threads/foo", Meta: threadMeta{Slug: "foo"}}
	w, reg, refuse := launchThread(t, nil, dir, threadFacts{})
	if refuse != "" {
		t.Fatal(refuse)
	}
	want := shQuote(paneSafePrompt(threadReassessPrompt(th)))
	if !strings.HasSuffix(w.Agent, " "+want) {
		t.Fatalf("agent line: %s", w.Agent)
	}
	if reg.ID != "11111111-2222-4333-8444-555555555555" || reg.Agent != "claude" {
		t.Fatalf("register = %+v", reg)
	}
}

func TestNewWorkspaceScriptEmptyPromptUnchanged(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	w := newWorkspaceScript("/work/proj", "/bin/et", id, "claude", "", "", true, "")
	if w.Agent != "cd '/work/proj' && 'claude' --session-id '"+id+"'" {
		t.Fatalf("agent: %s", w.Agent)
	}
	p := newWorkspaceScript("/work/proj", "/bin/et", id, "claude", "", "", true, "hi there")
	if p.Agent != w.Agent+" 'hi there'" {
		t.Fatalf("agent: %s", p.Agent)
	}
	h := newWorkspaceScript("/work/proj", "/bin/et", id, "happy", "", "", true, "x")
	if !strings.HasSuffix(h.Agent, " 'x'") || strings.Contains(h.Agent, "--session-id") {
		t.Fatalf("agent: %s", h.Agent)
	}
}

// An apostrophe in the prompt cut every `O` tab's first message off at it
// ("We\re"): iTerm's command splitter handles one level of quote escaping, not
// the two a prompt inside paneCommand gets. Verified live on iTerm before and
// after the fix.
func TestNewWorkspaceScriptPromptHasNoNestedQuotes(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	w := newWorkspaceScript("/work/proj", "/bin/et", id, "claude", "", "", false, `We're on Dan's \thread`)
	if !strings.HasSuffix(w.Agent, ` 'We’re on Dan’s /thread'`) {
		t.Fatalf("agent: %s", w.Agent)
	}
	if strings.Contains(paneSafePrompt(threadReassessPrompt(thread{Dir: "/v/x", Meta: threadMeta{Name: "it's"}})), "'") {
		t.Fatal("reassess prompt still carries an apostrophe")
	}
}

func stubThreadSeams(t *testing.T, osaErr error) (osaCalls, execCalls *int) {
	t.Helper()
	po, pe := threadsOsa, threadsExec
	o, e := 0, 0
	threadsOsa = func(string) error { o++; return osaErr }
	threadsExec = func(string) error { e++; return nil }
	t.Cleanup(func() { threadsOsa, threadsExec = po, pe })
	return &o, &e
}

func openFixture(t *testing.T) (string, thread) {
	t.Helper()
	root := t.TempDir()
	if _, err := createThread(root, "Open me", storeNow); err != nil {
		t.Fatal(err)
	}
	th := loadThreads(root)[0]
	th.Meta.Claims = []threadClaim{{Kind: "worktree", Path: t.TempDir()}}
	return root, th
}

func TestOpenThreadRefusesRunningWithoutFacts(t *testing.T) {
	_, execs := stubThreadSeams(t, nil)
	root, th := openFixture(t)
	th.Meta.Sessions = []threadSessionRef{{Agent: "claude", ID: "sess-1"}}
	live := []liveSession{{SessionID: "sess-1", PID: 7, Cwd: "/w"}}
	note := openThreadWith(root, "", th, false, live, "/bin/et", "claude", nil, func() string { return "x" })
	if note != "already running in /w (pid 7)" || *execs != 0 {
		t.Fatalf("note=%q execs=%d", note, *execs)
	}
}

func TestOpenThreadRefusesAgentInWorktree(t *testing.T) {
	osas, execs := stubThreadSeams(t, nil)
	root, th := openFixture(t)
	wt := th.Meta.Claims[0].Path
	live := []liveSession{{SessionID: "someone-else", PID: 11, Cwd: wt + "/sub"}}
	note := openThreadWith(root, filepath.Dir(wt), th, false, live, "/bin/et", "claude", nil, func() string { return "x" })
	if note != "an agent is already running in ~/"+filepath.Base(wt)+" (pid 11)" || *osas != 0 || *execs != 0 {
		t.Fatalf("note=%q osas=%d execs=%d", note, *osas, *execs)
	}
}

func TestOpenThreadFailedOsaRegistersNothing(t *testing.T) {
	_, execs := stubThreadSeams(t, errors.New("boom"))
	root, th := openFixture(t)
	note := openThreadWith(root, "", th, false, nil, "/bin/et", "claude", nil, func() string { return "11111111-2222-4333-8444-555555555555" })
	if note != "boom" || *execs != 0 {
		t.Fatalf("note=%q execs=%d", note, *execs)
	}
	if got := loadThreads(root)[0].Meta.Sessions; len(got) != 0 {
		t.Fatalf("registered %v", got)
	}
}

func TestOpenThreadRegistersAfterOsaBeforeExec(t *testing.T) {
	_, execs := stubThreadSeams(t, nil)
	root, th := openFixture(t)
	id := "11111111-2222-4333-8444-555555555555"
	openThreadWith(root, "", th, false, nil, "/bin/et", "claude", nil, func() string { return id })
	if *execs != 1 {
		t.Fatalf("execs=%d", *execs)
	}
	got := loadThreads(root)[0].Meta.Sessions
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("sessions %v", got)
	}
}

func TestThreadLaunchWrapperRegistersNothing(t *testing.T) {
	th := thread{Dir: "/v/foo", Meta: threadMeta{Claims: []threadClaim{{Kind: "worktree", Path: t.TempDir()}}}}
	_, reg, refuse := threadLaunch(th, threadFacts{}, "/bin/et", "happy", func() string { return "x" })
	if refuse != "" || reg.ID != "" {
		t.Fatalf("reg=%+v refuse=%q", reg, refuse)
	}
}
