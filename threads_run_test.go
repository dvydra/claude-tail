package main

import (
	"errors"
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

func TestThreadLaunchResumesClaude(t *testing.T) {
	dir := t.TempDir()
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	w, reg, refuse := launchThread(t, []threadSessionRef{{Agent: "claude", ID: id}}, dir, threadFacts{})
	if refuse != "" {
		t.Fatal(refuse)
	}
	if !strings.Contains(w.Agent, "--resume '"+id+"'") || !strings.Contains(w.Agent, dir) {
		t.Fatalf("agent line: %s", w.Agent)
	}
	if reg.ID != id || reg.Agent != "claude" {
		t.Fatalf("register = %+v", reg)
	}
}

func TestThreadLaunchAmp(t *testing.T) {
	dir := t.TempDir()
	w, reg, refuse := launchThread(t, []threadSessionRef{{Agent: "amp", ID: "T-abc"}}, dir, threadFacts{})
	if refuse != "" {
		t.Fatal(refuse)
	}
	if !strings.Contains(w.Agent, "threads continue 'T-abc'") {
		t.Fatalf("agent line: %s", w.Agent)
	}
	if reg.Agent != "amp" || reg.ID != "T-abc" {
		t.Fatalf("register = %+v", reg)
	}
}

func TestThreadLaunchFreshPrompt(t *testing.T) {
	dir := t.TempDir()
	w, reg, refuse := launchThread(t, nil, dir, threadFacts{})
	if refuse != "" {
		t.Fatal(refuse)
	}
	want := shQuote("Read /vault/Threads/foo/thread.md and /vault/Threads/foo/knowledge.md, then continue this thread.")
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
	note := openThreadWith(root, th, threadFacts{}, live, "/bin/et", "claude", nil, func() string { return "x" })
	if note != "already running in /w (pid 7)" || *execs != 0 {
		t.Fatalf("note=%q execs=%d", note, *execs)
	}
}

func TestOpenThreadFailedOsaRegistersNothing(t *testing.T) {
	_, execs := stubThreadSeams(t, errors.New("boom"))
	root, th := openFixture(t)
	note := openThreadWith(root, th, threadFacts{}, nil, "/bin/et", "claude", nil, func() string { return "11111111-2222-4333-8444-555555555555" })
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
	openThreadWith(root, th, threadFacts{}, nil, "/bin/et", "claude", nil, func() string { return id })
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
