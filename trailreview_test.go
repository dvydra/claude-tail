package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Tests for the whole-branch review's findings.

func TestTrailEventsNewGateAlreadyFailed(t *testing.T) {
	prev := fixtureState(t)
	cur := withGate(prev, "buildkite", "failed")
	if evs := trailEvents(&prev, cur); len(evs) != 1 || evs[0].Text != "buildkite failed" {
		t.Fatalf("a gate first seen failed: %+v", evs)
	}
	if evs := trailEvents(&prev, withGate(prev, "buildkite", "passed")); len(evs) != 0 {
		t.Fatalf("a gate first seen passed is quiet: %+v", evs)
	}
}

func TestTrailEventsPercentOnlyOnBucketChange(t *testing.T) {
	prev := fixtureState(t)
	at := func(v string, sev trailSev) changeState {
		s := prev
		s.Agents = append([]trailAgent(nil), prev.Agents...)
		s.Agents[1].Verdict, s.Agents[1].Sev = v, sev
		return s
	}
	if evs := trailEvents(&prev, at("71%", trailSevGood)); len(evs) != 0 {
		t.Fatalf("70%% → 71%% is noise: %+v", evs)
	}
	if evs := trailEvents(&prev, at("40%", trailSevWarn)); len(evs) != 1 || evs[0].Text != "Coverage: 70% → 40%" || evs[0].Sev != trailSevWarn {
		t.Fatalf("bucket change: %+v", evs)
	}
}

func TestParseChangeStateSanitises(t *testing.T) {
	comments := []byte(`{"comments":[{"id":"x","actor_display_name":"ev\u001b[2Jil","body":"line one\nline two \u001b]52;c;aGk=\u0007end\n\nsecond paragraph","severity":"high","status":"open"}]}`)
	show := bytes.Replace(readTrailFixture(t, "show.json"), []byte(`"Makuuchi archive"`), []byte(`"Ti\u001b[31mtle"`), 1)
	s, err := parseChangeState(fixtureChangeRef, show, readTrailFixture(t, "change.json"), comments)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{s.ProjectTitle, s.Findings[0].Author, s.Findings[0].Body} {
		if strings.ContainsAny(v, "\x1b\x07\n") {
			t.Fatalf("control bytes survived: %q", v)
		}
	}
}

func TestRetargetNotes(t *testing.T) {
	notes := []trailNoteRec{{Path: "old.jsonl", Text: "a"}, {Path: "other.jsonl", Text: "b"}, {Path: "old.jsonl", Text: "c"}}
	retargetNotes(notes, "old.jsonl", "new.jsonl")
	if notes[0].Path != "new.jsonl" || notes[1].Path != "other.jsonl" || notes[2].Path != "new.jsonl" {
		t.Fatalf("relocation keeps notes with the moved file: %+v", notes)
	}
}

// eventful drops the updates that carry state only.
func eventful(us []trailUpdate) []trailUpdate {
	var out []trailUpdate
	for _, u := range us {
		if len(u.Events) > 0 {
			out = append(out, u)
		}
	}
	return out
}

func TestTrackerSendsStateWithoutEvents(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, 0)})
	h.stepAt(0)
	got := h.stepAt(60 * time.Second)
	if len(got) != 1 || len(got[0].Events) != 0 || got[0].State.Ref != fixtureChangeRef {
		t.Fatalf("a quiet refetch still updates the modal's state: %+v", got)
	}
}

func TestTrackerProjectChangeMergesWhileTracked(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	h.f.set(showKey("32"), readTrailFixture(t, "show.json"))
	h.tr.Observe([]trailMention{mentionAt(trailRef{Kind: trailRefProject, Forge: "et", Project: "dvydra", Number: 32}, 0)})
	h.stepAt(0)
	merged := bytes.Replace(readTrailFixture(t, "show.json"), []byte(`"status": "open",
      "url": "https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10"`), []byte(`"status": "merged",
      "url": "https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10"`), 1)
	h.f.set(showKey("32"), merged)
	h.f.set(showKey("k3s-config/10"), merged)
	h.f.set(trailRunKey("", "entire", "api", "-j", "au", "/api/v1/repos/REPO1/trails/10"), bytes.Replace(readTrailFixture(t, "change.json"), []byte(`"status": "open"`), []byte(`"status": "merged"`), 1))
	got := eventful(h.stepAt(60 * time.Second))
	if len(got) != 1 || !got[0].Dropped || got[0].Events[len(got[0].Events)-1].Text != "merged" {
		t.Fatalf("a change merged while tracked through its project prints merged: %+v", got)
	}
}

func TestTrackerUntracksAgedOutChange(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, 0)})
	h.stepAt(0)
	got := h.stepAt(25 * time.Hour)
	if len(got) != 1 || !got[0].Dropped || len(got[0].Events) != 0 {
		t.Fatalf("an aged-out change leaves the modal silently: %+v", got)
	}
}
