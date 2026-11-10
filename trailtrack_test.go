package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var trackT0 = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

type trackHarness struct {
	t   *testing.T
	f   *fakeTrailRun
	now time.Time
	out chan trailUpdate
	tr  *trailTracker
}

func newTrackHarness(t *testing.T) *trackHarness {
	h := &trackHarness{t: t, f: newFakeTrailRun(), now: trackT0, out: make(chan trailUpdate, 64)}
	h.tr = newTrailTracker(trailTrackerDeps{Run: h.f.run, Now: func() time.Time { return h.now }, Home: t.TempDir()}, h.out)
	return h
}

func (h *trackHarness) stepAt(d time.Duration) []trailUpdate {
	h.now = trackT0.Add(d)
	h.tr.step(context.Background())
	var got []trailUpdate
	for {
		select {
		case u := <-h.out:
			got = append(got, u)
		default:
			return got
		}
	}
}

func showKey(label string) string {
	return trailRunKey("", "entire", "trail", "show", label, "--project", "et/dvydra", "--json")
}

func mentionAt(r trailRef, d time.Duration) trailMention {
	return trailMention{Ref: r, At: trackT0.Add(d)}
}

// stubRepoCwd makes /r a checkout of et/dvydra/k3s-config on feat-x.
func stubRepoCwd(h *trackHarness, head string) {
	h.f.set(trailRunKey("/r", "git", "rev-parse", "HEAD"), []byte(head+"\n"))
	h.f.set(trailRunKey("/r", "git", "branch", "--show-current"), []byte("feat-x\n"))
	h.f.set(trailRunKey("/r", "git", "remote", "get-url", "origin"), []byte("entire://x.entire.io/et/dvydra/k3s-config\n"))
}

func writeTranscript(t *testing.T, cwd string) string {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"user","cwd":"`+cwd+`","message":{"role":"user","content":"hi"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTrackerPushingBranch(t *testing.T) {
	h := newTrackHarness(t)
	stubRepoCwd(h, "aaa")
	stubFixtureChange(t, h.f)
	h.f.set(trailRunKey("", "entire", "trail", "show", "--branch", "feat-x", "--repo", "et/dvydra/k3s-config", "--json"), readTrailFixture(t, "show.json"))
	h.tr.Follow(writeTranscript(t, "/r"))
	got := h.stepAt(0)
	if len(got) != 1 || got[0].State.Ref != fixtureChangeRef || len(got[0].Events) != 1 {
		t.Fatalf("pushing baseline: %+v", got)
	}
}

func TestTrackerMentionOnce(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	for i := 0; i < 50; i++ {
		h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, time.Duration(i)*time.Second)})
	}
	n := len(eventful(h.stepAt(time.Minute))) + len(eventful(h.stepAt(2*time.Minute+time.Second)))
	if n != 1 {
		t.Fatalf("updates: %d", n)
	}
}

func TestTrackerChangeAndProjectMention(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	h.f.set(showKey("32"), readTrailFixture(t, "show.json"))
	h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, 0), mentionAt(trailRef{Kind: trailRefProject, Forge: "et", Project: "dvydra", Number: 32}, 0)})
	got := h.stepAt(time.Minute)
	if len(got) != 1 || h.f.calls[showKey("k3s-config/10")] != 1 {
		t.Fatalf("one tracked change expected: %d updates, %d fetches", len(got), h.f.calls[showKey("k3s-config/10")])
	}
}

func TestTrackerIgnoresOldMentions(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	h.tr.Observe([]trailMention{{Ref: fixtureChangeRef, At: trackT0.Add(-25 * time.Hour)}, {Ref: fixtureChangeRef}})
	if got := h.stepAt(0); len(got) != 0 || h.f.count() != 0 {
		t.Fatalf("old mention tracked: %+v, %d calls", got, h.f.count())
	}
}

func TestTrackerResolvesPartial(t *testing.T) {
	h := newTrackHarness(t)
	stubRepoCwd(h, "aaa")
	stubFixtureChange(t, h.f)
	h.tr.Observe([]trailMention{{Partial: "k3s-config/10", Cwd: "/r", At: trackT0}})
	if got := h.stepAt(0); len(got) != 1 || got[0].State.Ref != fixtureChangeRef {
		t.Fatalf("partial: %+v", got)
	}
}

func TestTrackerCap(t *testing.T) {
	h := newTrackHarness(t)
	stubRepoCwd(h, "aaa")
	stubFixtureChange(t, h.f)
	h.f.set(trailRunKey("", "entire", "trail", "show", "--branch", "feat-x", "--repo", "et/dvydra/k3s-config", "--json"), readTrailFixture(t, "show.json"))
	h.tr.Follow(writeTranscript(t, "/r"))
	var ms []trailMention
	for i := 1; i <= 12; i++ {
		ms = append(ms, mentionAt(trailRef{trailRefChange, "et", "dvydra", "other", i}, time.Duration(i)*time.Minute))
	}
	h.tr.Observe(ms)
	h.stepAt(13 * time.Minute)
	if h.f.calls[showKey("k3s-config/10")] != 1 {
		t.Fatal("pushing change dropped by the cap")
	}
	fetched := 0
	for i := 1; i <= 12; i++ {
		if h.f.calls[showKey("other/"+strconv.Itoa(i))] > 0 {
			fetched++
			if i <= 3 {
				t.Errorf("old mention other/%d fetched", i)
			}
		}
	}
	if fetched != 9 {
		t.Fatalf("mentioned changes fetched: %d, want 9", fetched)
	}
}

func TestTrackerDropsMerged(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, 0)})
	h.stepAt(0)
	merged := bytes.Replace(readTrailFixture(t, "change.json"), []byte(`"status": "open"`), []byte(`"status": "merged"`), 1)
	h.f.set(trailRunKey("", "entire", "api", "-j", "au", "/api/v1/repos/REPO1/trails/10"), merged)
	got := h.stepAt(60 * time.Second)
	if len(got) != 1 || !got[0].Dropped || got[0].Events[len(got[0].Events)-1].Text != "merged" {
		t.Fatalf("merge: %+v", got)
	}
	before := h.f.count()
	h.stepAt(5 * time.Minute)
	if h.f.count() != before {
		t.Fatal("dropped change fetched again")
	}
}

func TestTrackerCadence(t *testing.T) {
	h := newTrackHarness(t)
	stubRepoCwd(h, "aaa")
	stubFixtureChange(t, h.f)
	h.f.set(trailRunKey("", "entire", "trail", "show", "--branch", "feat-x", "--repo", "et/dvydra/k3s-config", "--json"), readTrailFixture(t, "show.json"))
	h.tr.Follow(writeTranscript(t, "/r"))
	fetches := func() int { return h.f.calls[showKey("k3s-config/10")] }
	h.stepAt(0)
	h.stepAt(10 * time.Second)
	h.stepAt(50 * time.Second)
	if fetches() != 1 {
		t.Fatalf("settled refetched early: %d", fetches())
	}
	h.stepAt(60 * time.Second)
	if fetches() != 2 {
		t.Fatalf("settled not refetched at 60s: %d", fetches())
	}
	stubRepoCwd(h, "bbb")
	h.stepAt(70 * time.Second)
	if fetches() != 3 {
		t.Fatalf("new HEAD did not refetch: %d", fetches())
	}
	running := bytes.Replace(readTrailFixture(t, "change.json"), []byte(`"status": "succeeded"`), []byte(`"status": "running"`), 1)
	h.f.set(trailRunKey("", "entire", "api", "-j", "au", "/api/v1/repos/REPO1/trails/10"), running)
	h.stepAt(130 * time.Second)
	h.stepAt(140 * time.Second)
	if fetches() != 5 {
		t.Fatalf("running not refetched at 10s: %d", fetches())
	}
}

func TestTrackerSilentOnErrors(t *testing.T) {
	h := newTrackHarness(t)
	h.f.err[showKey("k3s-config/10")] = errors.New("not logged in")
	h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, 0)})
	for i := 0; i <= 7; i++ {
		if got := h.stepAt(time.Duration(i) * 10 * time.Second); len(got) != 0 {
			t.Fatalf("update on error: %+v", got)
		}
	}
	if h.tr.Errors()[fixtureChangeRef.Key()] == "" {
		t.Fatal("error not recorded")
	}
	if n := h.f.calls[showKey("k3s-config/10")]; n != 2 {
		t.Fatalf("retries: %d, want 2 (t=0, t=60s)", n)
	}
}
