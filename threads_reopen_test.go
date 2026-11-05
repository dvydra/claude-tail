package main

import (
	"strings"
	"testing"
)

func TestUpdateThreadsEnterOpensRReopens(t *testing.T) {
	if _, a := press(tvUI(), kEnter, 0); a.Kind != "open" || a.Slug != "a" {
		t.Fatalf("⏎: %+v", a)
	}
	if _, a := press(tvUI(), kRune, 'r'); a.Kind != "reopen" || a.Slug != "a" {
		t.Fatalf("r: %+v", a)
	}
}

func TestThreadResumeClaude(t *testing.T) {
	sess := t.TempDir()
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	th := thread{Dir: "/v/foo", Meta: threadMeta{
		Sessions: []threadSessionRef{{Agent: "claude", ID: id}},
		Claims:   []threadClaim{{Kind: "worktree", Path: t.TempDir()}},
	}}
	env := func(agent, got string) (string, string, string) { return "", "", sess }
	w, ref, r := threadResumeWith(th, threadFacts{}, "/bin/et", "claude", env)
	if r != "" || !strings.Contains(w.Agent, "--resume '"+id+"'") || !strings.HasPrefix(w.Agent, "cd "+shQuote(sess)+" && ") {
		t.Fatalf("agent %q refuse %q", w.Agent, r)
	}
	if ref != th.Meta.Sessions[0] {
		t.Fatalf("ref = %+v", ref)
	}
	// The session's folder is gone: fall back to the claim.
	gone := func(agent, got string) (string, string, string) { return "", "", "/gone" }
	w, _, _ = threadResumeWith(th, threadFacts{}, "/bin/et", "claude", gone)
	if !strings.HasPrefix(w.Agent, "cd "+shQuote(th.Meta.Claims[0].Path)+" && ") {
		t.Fatalf("agent %q", w.Agent)
	}
}

func TestThreadResumeAmp(t *testing.T) {
	th := thread{Dir: "/v/foo", Meta: threadMeta{
		Sessions: []threadSessionRef{{Agent: "amp", ID: "T-abc"}},
		Claims:   []threadClaim{{Kind: "worktree", Path: t.TempDir()}},
	}}
	w, ref, r := threadResumeWith(th, threadFacts{}, "/bin/et", "claude", nil)
	if r != "" || !strings.Contains(w.Agent, "threads continue 'T-abc'") || ref.ID != "T-abc" {
		t.Fatalf("agent %q ref %+v refuse %q", w.Agent, ref, r)
	}
}

func TestThreadResumeNeedsASession(t *testing.T) {
	th := thread{Dir: "/v/foo", Meta: threadMeta{Claims: []threadClaim{{Kind: "worktree", Path: t.TempDir()}}}}
	if _, _, r := threadResumeWith(th, threadFacts{}, "/bin/et", "claude", nil); !strings.HasPrefix(r, "no session to reopen") {
		t.Fatalf("refuse = %q", r)
	}
}

// The reported bug: `O` reloaded the facts while its new tabs were running, a
// tab was then closed, and ⏎ still refused "already running". The guard reads
// only the live list now; with that session gone, the thread opens.
func TestOpenThreadAfterItsSessionExited(t *testing.T) {
	_, execs := stubThreadSeams(t, nil)
	root, th := openFixture(t)
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	th.Meta.Sessions = []threadSessionRef{{Agent: "claude", ID: id}}
	running := []liveSession{{PID: 7, SessionID: id, Cwd: "/elsewhere"}}
	if note := openThreadWith(root, "", th, false, running, "/bin/et", "claude", nil, func() string { return "x" }); !strings.Contains(note, "already running") {
		t.Fatalf("while running: note=%q", note)
	}
	if note := openThreadWith(root, "", th, false, nil, "/bin/et", "claude", nil, func() string { return "x" }); note != "" || *execs != 1 {
		t.Fatalf("after exit: note=%q execs=%d", note, *execs)
	}
}

func TestReopenExecsWhenRegisterFails(t *testing.T) {
	_, execs := stubThreadSeams(t, nil)
	var notes []string
	prev := threadsNote
	threadsNote = func(s string) { notes = append(notes, s) }
	t.Cleanup(func() { threadsNote = prev })
	root, th := openFixture(t)
	th.Meta.Sessions = []threadSessionRef{{Agent: "claude", ID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}}
	breakThreadFile(t, th) // after load, so registerSession fails
	note := openThreadWith(root, "", th, true, nil, "/bin/et", "claude", nil, func() string { return "x" })
	if note != "" || *execs != 1 {
		t.Fatalf("note=%q execs=%d", note, *execs)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "could not record the session") {
		t.Fatalf("notes = %q", notes)
	}
}
