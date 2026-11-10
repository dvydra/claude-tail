package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTrailRun answers commands by their joined argv ("dir|name args…"), and
// counts calls. An unknown command fails, so a test sees exactly what ran.
type fakeTrailRun struct {
	mu    sync.Mutex
	out   map[string][]byte
	err   map[string]error
	calls map[string]int
	all   int
}

func newFakeTrailRun() *fakeTrailRun {
	return &fakeTrailRun{out: map[string][]byte{}, err: map[string]error{}, calls: map[string]int{}}
}

func trailRunKey(dir, name string, args ...string) string {
	return dir + "|" + name + " " + strings.Join(args, " ")
}

func (f *fakeTrailRun) set(key string, out []byte) { f.mu.Lock(); f.out[key] = out; f.mu.Unlock() }

func (f *fakeTrailRun) run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := trailRunKey(dir, name, args...)
	f.calls[k]++
	f.all++
	if e, ok := f.err[k]; ok {
		return nil, e
	}
	if o, ok := f.out[k]; ok {
		return o, nil
	}
	return nil, errors.New("unexpected command: " + k)
}

func (f *fakeTrailRun) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.all }

func readTrailFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "trailstatus", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var fixtureChangeRef = trailRef{trailRefChange, "et", "dvydra", "k3s-config", 10}

// stubFixtureChange makes the runner answer the three calls for k3s-config/10.
func stubFixtureChange(t *testing.T, f *fakeTrailRun) {
	f.set(trailRunKey("", "entire", "trail", "show", "k3s-config/10", "--project", "et/dvydra", "--json"), readTrailFixture(t, "show.json"))
	f.set(trailRunKey("", "entire", "api", "-j", "au", "/api/v1/repos/REPO1/trails/10"), readTrailFixture(t, "change.json"))
	f.set(trailRunKey("", "entire", "api", "-j", "au", "/api/v1/trails/et/dvydra/k3s-config/10/reviews/comments"), readTrailFixture(t, "comments.json"))
}

func fixtureState(t *testing.T) changeState {
	t.Helper()
	s, err := parseChangeState(fixtureChangeRef, readTrailFixture(t, "show.json"), readTrailFixture(t, "change.json"), readTrailFixture(t, "comments.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseChangeState(t *testing.T) {
	s := fixtureState(t)
	if s.ProjectNumber != 32 || s.ProjectTitle != "Makuuchi archive" || s.ProjectURL != "https://entire.com/et/dvydra/-/trails/32" {
		t.Fatalf("project: %+v", s)
	}
	if s.URL != "https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10" || s.Branch != "feat-x" || s.Base != "main" || s.Status != "open" {
		t.Fatalf("change: %+v", s)
	}
	if len(s.Agents) != 3 {
		t.Fatalf("agents: %+v", s.Agents)
	}
	want := []struct {
		name, verdict string
		sev           trailSev
	}{{"Code review", "Pass", trailSevGood}, {"Coverage", "70%", trailSevGood}, {"Size", "Low", trailSevGood}}
	for i, w := range want {
		a := s.Agents[i]
		if a.Name != w.name || a.Verdict != w.verdict || a.Sev != w.sev {
			t.Errorf("agent %d: %+v want %+v", i, a, w)
		}
	}
	if h := s.Agents[0].History; len(h) != 2 || h[0] != trailSevBad || h[1] != trailSevGood {
		t.Errorf("history: %v", h)
	}
	if _, ok := s.Gate("up_to_date"); ok {
		t.Errorf("disabled gate kept")
	}
	if g, ok := s.Gate("checks"); !ok || g.Outcome != "passed" {
		t.Errorf("checks gate: %+v", g)
	}
	if _, ok := s.Gate("base_checks"); !ok {
		t.Errorf("base_checks missing")
	}
	if len(s.Findings) != 1 {
		t.Fatalf("findings: %+v", s.Findings)
	}
	f := s.Findings[0]
	if f.File != "trailtrack.go" || f.Line != 42 || f.Author != "reviewer" || f.Severity != "high" || f.Body != "Nil map write on first fetch." {
		t.Errorf("finding: %+v", f)
	}
	if s.Conflict != "clean" || s.Mergeable || s.BehindBy != 0 {
		t.Errorf("mergeability: %+v", s)
	}
	if s.Running() {
		t.Errorf("settled fixture reads as running")
	}
}

func TestAgentVerdict(t *testing.T) {
	b := func(v bool) *bool { return &v }
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		vt, pol string
		r       trailReading
		verdict string
		sev     trailSev
	}{
		{"boolean", "lower_is_better", trailReading{Bool: b(true)}, "Fail", trailSevBad},
		{"boolean", "lower_is_better", trailReading{Bool: b(false)}, "Pass", trailSevGood},
		{"boolean", "higher_is_better", trailReading{Bool: b(true)}, "Pass", trailSevGood},
		{"percent", "higher_is_better", trailReading{Pct: f(70)}, "70%", trailSevGood},
		{"percent", "higher_is_better", trailReading{Pct: f(50)}, "50%", trailSevWarn},
		{"percent", "higher_is_better", trailReading{Pct: f(20)}, "20%", trailSevBad},
		{"percent", "lower_is_better", trailReading{Pct: f(20)}, "20%", trailSevGood},
		{"size", "lower_is_better", trailReading{Size: "small"}, "Low", trailSevGood},
		{"size", "lower_is_better", trailReading{Size: "medium"}, "Med", trailSevWarn},
		{"size", "lower_is_better", trailReading{Size: "large"}, "High", trailSevBad},
		{"boolean", "lower_is_better", trailReading{}, "", trailSevNeutral},
	}
	for _, c := range cases {
		v, s := agentVerdict(c.vt, c.pol, c.r)
		if v != c.verdict || s != c.sev {
			t.Errorf("%s/%s %+v = %q %v; want %q %v", c.vt, c.pol, c.r, v, s, c.verdict, c.sev)
		}
	}
}

func TestParseChangeStateRejectsOtherChange(t *testing.T) {
	other := trailRef{trailRefChange, "et", "dvydra", "k3s-config", 11}
	if _, err := parseChangeState(other, readTrailFixture(t, "show.json"), readTrailFixture(t, "change.json"), readTrailFixture(t, "comments.json")); err == nil {
		t.Fatal("expected an identity error")
	}
}

func TestFetchChangeStateUsesCache(t *testing.T) {
	home := t.TempDir()
	f := newFakeTrailRun()
	stubFixtureChange(t, f)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if _, err := fetchChangeState(context.Background(), f.run, fixtureChangeRef, home, t0); err != nil {
		t.Fatal(err)
	}
	if f.count() != 3 {
		t.Fatalf("first fetch ran %d commands", f.count())
	}
	if _, err := fetchChangeState(context.Background(), f.run, fixtureChangeRef, home, t0.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if f.count() != 3 {
		t.Fatalf("cached fetch ran commands: %d", f.count())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "entire-tail", "trailstatus")); err != nil {
		t.Fatalf("cache dir: %v", err)
	}
	s, err := fetchChangeState(context.Background(), f.run, fixtureChangeRef, home, t0.Add(61*time.Second))
	if err != nil || f.count() != 6 {
		t.Fatalf("stale fetch: %v, %d", err, f.count())
	}
	if s.ProjectNumber != 32 || len(s.Agents) != 3 {
		t.Fatalf("state after refetch: %+v", s)
	}
}

func TestFetchBranchChangeNoTrail(t *testing.T) {
	f := newFakeTrailRun()
	k := trailRunKey("", "entire", "trail", "show", "--branch", "feat-x", "--repo", "et/dvydra/k3s-config", "--json")
	f.err[k] = &exec.ExitError{Stderr: []byte("Error: no discoverable project trail for branch feat-x\n")}
	ref, ok, err := fetchBranchChange(context.Background(), f.run, "et/dvydra/k3s-config", "feat-x")
	if ok || err != nil || ref != (trailRef{}) {
		t.Fatalf("no trail: %+v %v %v", ref, ok, err)
	}
	f.err[k] = errors.New("keychain not accessible")
	if _, _, err := fetchBranchChange(context.Background(), f.run, "et/dvydra/k3s-config", "feat-x"); err == nil {
		t.Fatal("other errors must be returned")
	}
	delete(f.err, k)
	f.set(k, readTrailFixture(t, "show.json"))
	ref, ok, err = fetchBranchChange(context.Background(), f.run, "et/dvydra/k3s-config", "feat-x")
	if !ok || err != nil || ref != fixtureChangeRef {
		t.Fatalf("found: %+v %v %v", ref, ok, err)
	}
}

func TestFetchProjectChanges(t *testing.T) {
	f := newFakeTrailRun()
	f.set(trailRunKey("", "entire", "trail", "show", "32", "--project", "et/dvydra", "--json"), readTrailFixture(t, "show.json"))
	refs, err := fetchProjectChanges(context.Background(), f.run, trailRef{Kind: trailRefProject, Forge: "et", Project: "dvydra", Number: 32})
	if err != nil || len(refs) != 1 || refs[0] != fixtureChangeRef {
		t.Fatalf("open changes: %+v %v", refs, err)
	}
}
