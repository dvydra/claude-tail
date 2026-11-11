package main

import (
	"strings"
	"testing"
)

func withGate(s changeState, key, outcome string) changeState {
	gs := append([]trailGate(nil), s.Gates...)
	found := false
	for i := range gs {
		if gs[i].Key == key {
			gs[i].Outcome = outcome
			found = true
		}
	}
	if !found {
		gs = append(gs, trailGate{Key: key, Type: "plugin", Outcome: outcome})
	}
	s.Gates = gs
	return s
}

func evTexts(evs []trailEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Text)
	}
	return out
}

func TestTrailEvents(t *testing.T) {
	base := fixtureState(t)
	base.Findings = nil
	base = withGate(base, "buildkite", "passed")
	cases := []struct {
		name      string
		prev, cur func(changeState) changeState
		want      []trailEvent
	}{
		{"build failed", nil, func(s changeState) changeState { return withGate(s, "checks", "failed") }, []trailEvent{{"build failed", trailSevBad}}},
		{"build green", func(s changeState) changeState { return withGate(s, "checks", "failed") }, nil, []trailEvent{{"build green again", trailSevGood}}},
		{"main red", nil, func(s changeState) changeState { return withGate(s, "base_checks", "failed") }, []trailEvent{{"main build is red", trailSevBad}}},
		{"main green", func(s changeState) changeState { return withGate(s, "base_checks", "failed") }, nil, []trailEvent{{"main green again", trailSevGood}}},
		{"plugin failed", nil, func(s changeState) changeState { return withGate(s, "buildkite", "failed") }, []trailEvent{{"buildkite failed", trailSevBad}}},
		{"plugin green", func(s changeState) changeState { return withGate(s, "buildkite", "failed") }, nil, []trailEvent{{"buildkite green again", trailSevGood}}},
		{"behind", nil, func(s changeState) changeState { s.BehindBy = 3; return s }, []trailEvent{{"out of date: 3 behind main", trailSevWarn}}},
		{"more behind", func(s changeState) changeState { s.BehindBy = 3; return s }, func(s changeState) changeState { s.BehindBy = 5; return s }, nil},
		{"caught up", func(s changeState) changeState { s.BehindBy = 5; return s }, nil, nil},
		{"conflict", nil, func(s changeState) changeState { s.Conflict = "conflict"; return s }, []trailEvent{{"merge conflict", trailSevBad}}},
		{"conflict resolved", func(s changeState) changeState { s.Conflict = "conflict"; return s }, nil, []trailEvent{{"conflict resolved", trailSevGood}}},
		{"new findings high", nil, func(s changeState) changeState {
			s.Findings = []trailFinding{{ID: "a", Severity: "high"}, {ID: "b", Severity: "low"}}
			return s
		}, []trailEvent{{"2 new findings (1 high)", trailSevBad}}},
		{"new finding low", nil, func(s changeState) changeState { s.Findings = []trailFinding{{ID: "a", Severity: "low"}}; return s }, []trailEvent{{"1 new finding", trailSevWarn}}},
		{"finding resolved", func(s changeState) changeState { s.Findings = []trailFinding{{ID: "a"}, {ID: "b"}}; return s }, func(s changeState) changeState {
			s.Findings = []trailFinding{{ID: "a"}}
			return s
		}, nil},
		{"findings cleared", func(s changeState) changeState { s.Findings = []trailFinding{{ID: "a"}}; return s }, nil, []trailEvent{{"findings cleared", trailSevGood}}},
		{"agent flips", nil, func(s changeState) changeState {
			s.Agents = append([]trailAgent(nil), s.Agents...)
			s.Agents[0].Verdict, s.Agents[0].Sev = "Fail", trailSevBad
			return s
		}, []trailEvent{{"Code review: Pass → Fail", trailSevBad}}},
		{"agent back", func(s changeState) changeState {
			s.Agents = append([]trailAgent(nil), s.Agents...)
			s.Agents[0].Verdict, s.Agents[0].Sev = "Fail", trailSevBad
			return s
		}, nil, []trailEvent{{"Code review: Fail → Pass", trailSevGood}}},
		{"needs review", func(s changeState) changeState { return withGate(s, "approvals", "") }, func(s changeState) changeState { return withGate(s, "approvals", "failed") }, []trailEvent{{"needs review", trailSevWarn}}},
		{"approved", func(s changeState) changeState { return withGate(s, "approvals", "failed") }, nil, []trailEvent{{"approved", trailSevGood}}},
		{"ready", nil, func(s changeState) changeState { s.Mergeable = true; return s }, []trailEvent{{"ready to merge", trailSevGood}}},
		{"merged", nil, func(s changeState) changeState { s.Status = "merged"; return s }, []trailEvent{{"merged", trailSevNeutral}}},
		{"closed", nil, func(s changeState) changeState { s.Status = "closed"; return s }, []trailEvent{{"closed", trailSevNeutral}}},
		{"no change", nil, func(s changeState) changeState { s.FetchedAt = s.FetchedAt.Add(99); return s }, nil},
	}
	for _, c := range cases {
		prev, cur := base, base
		if c.prev != nil {
			prev = c.prev(base)
		}
		if c.cur != nil {
			cur = c.cur(base)
		}
		got := trailEvents(&prev, cur)
		if strings.Join(evTexts(got), "|") != strings.Join(evTexts(c.want), "|") || (len(got) == len(c.want) && len(got) > 0 && got[0].Sev != c.want[0].Sev) {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestTrailEventsAcrossARerun(t *testing.T) {
	base := fixtureState(t)
	failed := withGate(base, "checks", "failed")
	running := settleGates(&failed, withGate(base, "checks", ""))
	if evs := trailEvents(&failed, running); len(evs) != 0 {
		t.Fatalf("a gate restarting is not an event: %+v", evs)
	}
	green := withGate(base, "checks", "passed")
	if evs := trailEvents(&running, green); len(evs) != 1 || evs[0].Text != "build green again" {
		t.Fatalf("recovery across a rerun: %+v", evs)
	}
}

func TestTrailEventsBaseline(t *testing.T) {
	s := fixtureState(t)
	evs := trailEvents(nil, s)
	if len(evs) != 1 {
		t.Fatalf("baseline: %+v", evs)
	}
	if evs[0].Text != "agents 3/3 ✔ · 1 finding · Merge blocked" || evs[0].Sev != trailSevBad {
		t.Fatalf("baseline: %+v", evs[0])
	}
	s.Findings, s.Mergeable = nil, true
	evs = trailEvents(nil, s)
	if evs[0].Text != "agents 3/3 ✔ · 0 findings · Ready" || evs[0].Sev != trailSevGood {
		t.Fatalf("clean baseline: %+v", evs[0])
	}
}

func TestTrailLineText(t *testing.T) {
	s := fixtureState(t)
	evs := []trailEvent{{"build failed", trailSevBad}, {"2 new findings (1 high)", trailSevBad}}
	if got := trailLineText(s, evs, false); got != "⛬ k3s-config/10 (P32) · build failed · 2 new findings (1 high)" {
		t.Fatalf("plain: %q", got)
	}
	got := trailLineText(s, evs, true)
	if !strings.Contains(got, osc8(s.URL, "k3s-config/10")) || !strings.Contains(got, osc8(s.ProjectURL, "P32")) {
		t.Fatalf("links: %q", got)
	}
	if trailLineSev(evs) != trailSevBad || trailLineSev([]trailEvent{{"x", trailSevGood}, {"y", trailSevWarn}}) != trailSevWarn {
		t.Fatal("line sev is the worst event")
	}
}

func TestTrailBand(t *testing.T) {
	text := osc8("https://x", "k3s-config/10") + " · build failed"
	b := trailBand(text, trailSevBad, 40)
	if !strings.HasPrefix(b, "\x1b[1;38;2;255;255;255;48;2;150;30;30m") || !strings.HasSuffix(b, "\x1b[0m") {
		t.Fatalf("band: %q", b)
	}
	if w := trailVisibleWidth(b); w != 40 {
		t.Fatalf("padded width %d", w)
	}
	if w := trailVisibleWidth(trailBand(text, trailSevGood, 0)); w != trailVisibleWidth(text) {
		t.Fatalf("width 0 pads: %d", w)
	}
	for sev, bg := range map[trailSev]string{trailSevWarn: "48;2;150;100;0", trailSevGood: "48;2;30;110;50", trailSevNeutral: "48;2;60;60;60"} {
		if !strings.Contains(trailBand("x", sev, 10), bg) {
			t.Errorf("sev %d lacks %s", sev, bg)
		}
	}
}
