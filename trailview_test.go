package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func modalText(lines []string) string {
	return trailEscapes.ReplaceAllString(strings.Join(lines, "\n"), "")
}

func TestTrailModalLines(t *testing.T) {
	s := fixtureState(t)
	s.Findings = append(s.Findings, trailFinding{ID: "F3", Severity: "low", Author: "bot", File: "a.go", Line: 7, Body: "Low one."})
	other := s
	other.Ref = trailRef{trailRefChange, "et", "dvydra", "midnight-magic", 6}
	other.Findings = nil
	notes := []trailNoteRec{{Text: "⛬ k3s-config/10 (P32) · first"}, {Text: "⛬ k3s-config/10 (P32) · second"}}
	got := modalText(trailModalLines([]changeState{s, other}, notes, nil, "feat-x", testTheme(), 100))

	if strings.Count(got, "P32") != 1+2 { // one project header, plus the two note lines
		t.Fatalf("one header for two changes in one project:\n%s", got)
	}
	order := []string{"P32", "Makuuchi archive", "k3s-config/10", "feat-x → main", "Code review", "Pass", "Coverage", "70%", "Size", "Low", "approvals", "checks", "Merge blocked", "High", "reviewer", "trailtrack.go:42", "Nil map write", "a.go:7", "midnight-magic/6", "second", "first"}
	at := 0
	for _, w := range order {
		i := strings.Index(got[at:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order after offset %d:\n%s", w, at, got)
		}
		at += i + len(w)
	}
	if strings.Contains(got, "up_to_date") {
		t.Fatal("disabled gate shown")
	}
}

func TestTrailModalEmpty(t *testing.T) {
	got := modalText(trailModalLines(nil, nil, nil, "feat", testTheme(), 80))
	if !strings.Contains(got, "No trail for this session") || !strings.Contains(got, "branch feat checked") {
		t.Fatalf("empty:\n%s", got)
	}
}

func TestTrailModalError(t *testing.T) {
	s := fixtureState(t)
	errs := map[string]string{s.Ref.Key(): "Error: not logged in", "et/dvydra/other/3": "Error: 404"}
	got := modalText(trailModalLines([]changeState{s}, nil, errs, "", testTheme(), 80))
	if !strings.Contains(got, "not logged in") || !strings.Contains(got, "other/3") || !strings.Contains(got, "404") {
		t.Fatalf("errors:\n%s", got)
	}
}

func TestKeyActionForTrails(t *testing.T) {
	if keyActionFor('g') != keyTrails || keyActionFor('G') != keyTrails {
		t.Fatal("g is the trail modal")
	}
}

func TestTrackerRefreshWakesRun(t *testing.T) {
	h := newTrackHarness(t)
	h.tr.deps.Now = time.Now
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.tr.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for h.f.count() == 0 && h.tr.Steps() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	before := h.tr.Steps()
	h.tr.Refresh()
	for h.tr.Steps() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.tr.Steps() == before {
		t.Fatal("Refresh did not wake the tracker before its 10s tick")
	}
}
