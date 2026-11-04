package main

import (
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
	want := shQuote("Read /vault/Threads/foo/thread.md and knowledge.md, then continue this thread.")
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
