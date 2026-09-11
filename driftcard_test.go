package main

import (
	"strings"
	"testing"
)

func driftCardPlain(r driftReport, err error) string {
	return stripANSI(strings.Join(driftCardLines(r, err, Theme{}, 80), "\n"))
}

func TestDriftCardShowsVerdictAndBothLines(t *testing.T) {
	got := driftCardPlain(driftReport{
		Verdict: verdictDrifted,
		Asked:   "a skill that writes a status line",
		Doing:   "designing drift detection in entire-tail",
		Hops:    []string{"statusline options", "apple intelligence", "drift feature"},
	}, nil)

	for _, want := range []string{
		verdictDrifted,
		"a skill that writes a status line",
		"designing drift detection in entire-tail",
		"statusline options", "apple intelligence", "drift feature",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("card missing %q:\n%s", want, got)
		}
	}
}

func TestDriftCardOnTrackHidesTheHopsHeading(t *testing.T) {
	got := driftCardPlain(driftReport{
		Verdict: verdictOnTrack,
		Asked:   "fix the auth bug",
		Doing:   "fixing the auth bug",
	}, nil)
	if !strings.Contains(got, verdictOnTrack) {
		t.Errorf("card missing the verdict:\n%s", got)
	}
	if strings.Contains(got, "How you got here") {
		t.Errorf("no hops, so the heading should be absent:\n%s", got)
	}
}

// The whole point of the fmrun.go rework: a card that cannot answer says why.
func TestDriftCardShowsTheReasonItFailed(t *testing.T) {
	got := driftCardPlain(driftReport{}, fmError("You have not agreed to the apple foundation models cli legal notice"))
	if !strings.Contains(got, "not agreed") {
		t.Errorf("card swallowed the reason:\n%s", got)
	}
}

func TestDriftCardTooShortIsNotAnError(t *testing.T) {
	got := driftCardPlain(driftReport{}, errDriftTooShort)
	if !strings.Contains(strings.ToLower(got), "not enough of this session") {
		t.Errorf("card should explain the session is too young:\n%s", got)
	}
}

func TestDriftCardWrapsToWidth(t *testing.T) {
	long := strings.Repeat("verylongword ", 30)
	for _, line := range driftCardLines(
		driftReport{Verdict: verdictAdjacent, Asked: long, Doing: long, Hops: []string{long}},
		nil, Theme{}, 60,
	) {
		if n := len([]rune(stripANSI(line))); n > 60 {
			t.Errorf("line is %d cols, want <= 60: %q", n, stripANSI(line))
		}
	}
}
