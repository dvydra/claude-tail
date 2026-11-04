package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type fakeRun struct {
	mu    sync.Mutex
	out   map[string]string // argv joined by space -> stdout
	fail  map[string]bool
	calls []string
}

func (f *fakeRun) run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	if f.fail[key] {
		return nil, errors.New("exit 1")
	}
	if v, ok := f.out[key]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("unexpected: " + key)
}

func wtDeps(f *fakeRun, live []liveSession) threadCheckDeps {
	return threadCheckDeps{
		Run:    f.run,
		Live:   func() []liveSession { return live },
		Exists: func(string) bool { return true },
		Now:    func() int64 { return 1000 },
	}
}

func joinStr(ss []string) string { return strings.Join(ss, " | ") }

func TestCheckThreadWorktreeGone(t *testing.T) {
	f := &fakeRun{}
	deps := wtDeps(f, nil)
	deps.Exists = func(string) bool { return false }
	th := thread{Meta: threadMeta{Claims: []threadClaim{{Kind: "worktree", Path: "/x/gone-wt", Branch: "b"}}}}
	got := checkThread(context.Background(), th, deps)
	if !strings.Contains(joinStr(got.Stale), "worktree gone-wt gone") {
		t.Fatalf("stale = %v", got.Stale)
	}
	if len(f.calls) != 0 {
		t.Fatalf("git called: %v", f.calls)
	}
}

func TestCheckThreadUnpushed(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRun{
		out: map[string]string{
			"git -C " + dir + " rev-parse --abbrev-ref HEAD": "feat\n",
			"git -C " + dir + " rev-parse HEAD":              "abc123\n",
			"git -C " + dir + " status --porcelain":          " M a.go\n",
			"git -C " + dir + " rev-list --count @{u}..HEAD": "2\n",
		},
	}
	th := thread{Meta: threadMeta{Claims: []threadClaim{{Kind: "worktree", Path: dir, Branch: "feat", Head: "abc123"}}}}
	got := checkThread(context.Background(), th, wtDeps(f, nil))
	if !strings.Contains(joinStr(got.Stale), "feat not pushed") {
		t.Fatalf("stale = %v", got.Stale)
	}
	if len(got.Risks) != 0 {
		t.Fatalf("has upstream, no laptop-only risk: %v", got.Risks)
	}
}

func TestCheckThreadNoUpstreamDirtyRisk(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRun{
		out: map[string]string{
			"git -C " + dir + " rev-parse --abbrev-ref HEAD": "feat\n",
			"git -C " + dir + " rev-parse HEAD":              "abc\n",
			"git -C " + dir + " status --porcelain":          " M a.go\n",
		},
		fail: map[string]bool{"git -C " + dir + " rev-list --count @{u}..HEAD": true},
	}
	th := thread{Meta: threadMeta{Claims: []threadClaim{{Kind: "worktree", Path: dir, Branch: "other"}}}}
	got := checkThread(context.Background(), th, wtDeps(f, nil))
	if !strings.Contains(joinStr(got.Stale), "branch is feat, thread says other") {
		t.Fatalf("stale = %v", got.Stale)
	}
	if !strings.Contains(joinStr(got.Risks), "work exists only on this laptop") {
		t.Fatalf("risks = %v", got.Risks)
	}
}

func TestCheckThreadTrailMerged(t *testing.T) {
	// A project trail ("-" in the repo slot) is read by project; a repo-scoped
	// trail number is a different number space and must go through the repo's
	// API path, because `trail show <n> --repo` ignores the repo and returns
	// the PROJECT trail with that number.
	f := &fakeRun{out: map[string]string{
		"entire trail show 7 --project gh/o --json": `{"number":7,"status":"merged"}`,
		"entire api /api/v1/trails/et/o/r/9":        `{"number":9,"status":"open"}`,
	}}
	th := thread{Meta: threadMeta{Claims: []threadClaim{
		{Kind: "trail", Trail: "gh/o/-/7", Expect: "open"},
		{Kind: "trail", Trail: "et/o/r/9", Expect: "open"},
		{Kind: "linear", Linear: "ENT-1"},
	}}}
	got := checkThread(context.Background(), th, wtDeps(f, nil))
	if len(got.Stale) != 1 || got.Stale[0] != "trail 7 is merged, thread says open" {
		t.Fatalf("stale = %v", got.Stale)
	}
	if !strings.Contains(joinStr(got.Details), "ENT-1") {
		t.Fatalf("details = %v", got.Details)
	}
}

// The active login's jurisdiction may not hold the repo (an au login 404s
// entirehq's us repos), so a failed repo read retries in us before giving up.
func TestCheckThreadTrailRepoFallsBackToUS(t *testing.T) {
	f := &fakeRun{
		fail: map[string]bool{"entire api /api/v1/trails/gh/o/r/50": true},
		out:  map[string]string{"entire api -j us /api/v1/trails/gh/o/r/50": `{"number":50,"status":"merged"}`},
	}
	th := thread{Meta: threadMeta{Claims: []threadClaim{{Kind: "trail", Trail: "gh/o/r/50", Expect: "merged"}}}}
	got := checkThread(context.Background(), th, wtDeps(f, nil))
	if len(got.Stale) != 0 || !strings.Contains(joinStr(got.Details), "trail 50 is merged") {
		t.Fatalf("stale = %v, details = %v", got.Stale, got.Details)
	}
}

// A repo read that fails everywhere is unknown, never stale.
func TestCheckThreadTrailUnavailableIsNotStale(t *testing.T) {
	f := &fakeRun{fail: map[string]bool{
		"entire api /api/v1/trails/gh/o/r/1":       true,
		"entire api -j us /api/v1/trails/gh/o/r/1": true,
	}}
	th := thread{Meta: threadMeta{Claims: []threadClaim{{Kind: "trail", Trail: "gh/o/r/1", Expect: "open"}}}}
	got := checkThread(context.Background(), th, wtDeps(f, nil))
	if len(got.Stale) != 0 || !strings.Contains(joinStr(got.Details), "trail 1: status unavailable") {
		t.Fatalf("stale = %v, details = %v", got.Stale, got.Details)
	}
}

func TestCheckThreadRunning(t *testing.T) {
	th := thread{Meta: threadMeta{Sessions: []threadSessionRef{{Agent: "claude", ID: "s1"}}}}
	live := []liveSession{{SessionID: "other", Cwd: "/a"}, {SessionID: "s1", Cwd: "/b/c", PID: 5}}
	got := checkThread(context.Background(), th, wtDeps(&fakeRun{}, live))
	if got.Running == nil || got.Running.PID != 5 {
		t.Fatalf("running = %+v", got.Running)
	}
	if !strings.Contains(joinStr(got.Risks), "running now in /b/c") {
		t.Fatalf("risks = %v", got.Risks)
	}
}

func TestCheckThreadAgentInWorktree(t *testing.T) {
	th := thread{Meta: threadMeta{Claims: []threadClaim{{Kind: "worktree", Path: "/h/src/wt"}}}}
	deps := wtDeps(&fakeRun{}, nil)
	deps.Home = "/h"
	deps.Live = func() []liveSession {
		return []liveSession{{SessionID: "x", Cwd: "/h/src/wt2", PID: 8}, {SessionID: "y", Cwd: "/h/src/wt/sub", PID: 9}}
	}
	got := checkThread(context.Background(), th, deps)
	if !strings.Contains(joinStr(got.Risks), "an agent is already running in ~/src/wt (pid 9)") {
		t.Fatalf("risks = %v", got.Risks)
	}
	deps.Live = func() []liveSession { return []liveSession{{SessionID: "x", Cwd: "/h/src/wt2", PID: 8}} }
	if got := checkThread(context.Background(), th, deps); len(got.Risks) != 0 {
		t.Fatalf("sibling dir matched: %v", got.Risks)
	}
}

func TestCheckThreadsConcurrent(t *testing.T) {
	deps := wtDeps(&fakeRun{}, nil)
	var ts []thread
	for _, s := range []string{"a", "b", "c", "d"} {
		ts = append(ts, thread{Dir: "/t/" + s, Meta: threadMeta{Slug: "ignored", Sessions: []threadSessionRef{{ID: s}}}})
	}
	deps.Live = func() []liveSession { return []liveSession{{SessionID: "c", Cwd: "/c"}} }
	m := checkThreads(context.Background(), ts, deps)
	if len(m) != 4 || m["c"].Running == nil || m["a"].Running != nil {
		t.Fatalf("map = %+v", m)
	}
}

func TestThreadListStatusPrecedence(t *testing.T) {
	cases := []struct {
		name string
		th   thread
		f    threadFacts
		text string
		tone string
	}{
		{"parse", thread{ParseErr: errors.New("bad"), Meta: threadMeta{WaitingOn: "x"}}, threadFacts{Stale: []string{"s"}}, "unreadable: bad", "stale"},
		{"stale", thread{Meta: threadMeta{WaitingOn: "x", NeedsYou: true}}, threadFacts{Stale: []string{"s"}, Risks: []string{"r"}}, "stale: s", "stale"},
		{"risk", thread{Meta: threadMeta{WaitingOn: "x", NeedsYou: true}}, threadFacts{Risks: []string{"r"}}, "r", "risk"},
		{"you", thread{Meta: threadMeta{WaitingOn: "review", NeedsYou: true}}, threadFacts{}, "waiting on you: review", "you"},
		{"plain", thread{Meta: threadMeta{WaitingOn: "ci"}}, threadFacts{}, "ci", "plain"},
		{"none", thread{}, threadFacts{}, "no status written", "plain"},
	}
	for _, c := range cases {
		text, tone := threadListStatus(c.th, c.f)
		if text != c.text || tone != c.tone {
			t.Errorf("%s: got %q/%q want %q/%q", c.name, text, tone, c.text, c.tone)
		}
	}
}

func TestCheckThreadsCallsLiveOnce(t *testing.T) {
	deps := wtDeps(&fakeRun{}, nil)
	var mu sync.Mutex
	n := 0
	deps.Live = func() []liveSession { mu.Lock(); n++; mu.Unlock(); return nil }
	ts := []thread{{Dir: "/t/a"}, {Dir: "/t/b"}, {Dir: "/t/c"}}
	checkThreads(context.Background(), ts, deps)
	if n != 1 {
		t.Fatalf("Live called %d times", n)
	}
}
