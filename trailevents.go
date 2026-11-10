package main

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

// trailevents.go — what changed between two fetches of a change, and the
// status line that says so. A line is printed only when there are events, so
// a line in the tail always means something happened.

type trailEvent struct {
	Text string
	Sev  trailSev
}

// settledOutcome is a gate's last finished outcome: its own when finished,
// else the one carried from before the rerun.
func (g trailGate) settledOutcome() string {
	if g.Outcome != "" {
		return g.Outcome
	}
	return g.Last
}

// settleGates carries each gate's last finished outcome into cur, so a gate
// that goes failed → running → passed still reads as a recovery. The tracker
// stores the settled state as the next fetch's prev.
func settleGates(prev *changeState, cur changeState) changeState {
	gs := append([]trailGate(nil), cur.Gates...)
	for i := range gs {
		gs[i].Last = gs[i].Outcome
		if gs[i].Last == "" && prev != nil {
			if p, ok := prev.Gate(gs[i].Key); ok {
				gs[i].Last = p.settledOutcome()
			}
		}
	}
	cur.Gates = gs
	return cur
}

func trailConflicted(s string) bool { return s != "" && s != "clean" && s != "unknown" }

func trailPlural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// trailMergeSummary is the merge box's title on entire.com.
func trailMergeSummary(s changeState) (string, trailSev) {
	switch {
	case s.Status == "merged":
		return "Merged", trailSevNeutral
	case s.Status == "closed":
		return "Closed", trailSevNeutral
	case s.Mergeable:
		return "Ready", trailSevGood
	}
	if g, ok := s.Gate("approvals"); ok && g.Outcome != "passed" {
		return "Needs review", trailSevWarn
	}
	for _, g := range s.Gates {
		if g.Outcome == "" && g.Key != "approvals" && g.Key != "findings" {
			return "Waiting on gates", trailSevWarn
		}
	}
	return "Merge blocked", trailSevBad
}

func trailFindingsSev(fs []trailFinding) trailSev {
	if len(fs) == 0 {
		return trailSevGood
	}
	for _, f := range fs {
		if f.Severity == "high" {
			return trailSevBad
		}
	}
	return trailSevWarn
}

func trailBaseline(s changeState) trailEvent {
	var parts []string
	sev := trailSevNeutral
	if len(s.Agents) > 0 {
		good, worst := 0, trailSevGood
		for _, a := range s.Agents {
			if a.Sev == trailSevGood {
				good++
			}
			worst = max(worst, a.Sev)
		}
		mark := "✔"
		if good < len(s.Agents) {
			mark = "✗"
		}
		parts = append(parts, "agents "+strconv.Itoa(good)+"/"+strconv.Itoa(len(s.Agents))+" "+mark)
		sev = max(sev, worst)
	}
	parts = append(parts, trailPlural(len(s.Findings), "finding"))
	sev = max(sev, trailFindingsSev(s.Findings))
	m, msev := trailMergeSummary(s)
	parts = append(parts, m)
	sev = max(sev, msev)
	return trailEvent{strings.Join(parts, " · "), sev}
}

// trailGateName is how a gate is named in an event.
func trailGateName(key string) (failed, green string) {
	switch key {
	case "checks":
		return "build failed", "build green again"
	case "base_checks":
		return "main build is red", "main green again"
	}
	return key + " failed", key + " green again"
}

// trailEvents lists what changed from prev to cur, in the order of the design
// doc's table. prev == nil is the first fetch: one event summarising cur.
func trailEvents(prev *changeState, cur changeState) []trailEvent {
	if prev == nil {
		return []trailEvent{trailBaseline(cur)}
	}
	var evs []trailEvent
	add := func(text string, sev trailSev) { evs = append(evs, trailEvent{text, sev}) }

	// CI gates: build, main, then plugins.
	ciGate := func(g trailGate) {
		if g.Outcome == "" {
			return
		}
		// A gate missing from prev (registered after the baseline) counts as
		// never finished: first seen failed is still a failure.
		was := ""
		if p, ok := prev.Gate(g.Key); ok {
			was = p.settledOutcome()
		}
		failed, green := trailGateName(g.Key)
		switch {
		case g.Outcome == "failed" && was != "failed":
			add(failed, trailSevBad)
		case g.Outcome == "passed" && was == "failed":
			add(green, trailSevGood)
		}
	}
	for _, key := range []string{"checks", "base_checks"} {
		if g, ok := cur.Gate(key); ok {
			ciGate(g)
		}
	}
	for _, g := range cur.Gates {
		if g.Type == "plugin" {
			ciGate(g)
		}
	}

	if prev.BehindBy == 0 && cur.BehindBy > 0 {
		base := cur.Base
		if base == "" {
			base = "base"
		}
		add("out of date: "+strconv.Itoa(cur.BehindBy)+" behind "+base, trailSevWarn)
	}
	switch was, is := trailConflicted(prev.Conflict), trailConflicted(cur.Conflict); {
	case is && !was:
		add("merge conflict", trailSevBad)
	case was && !is && cur.Conflict == "clean":
		add("conflict resolved", trailSevGood)
	}

	old := map[string]bool{}
	for _, f := range prev.Findings {
		old[f.ID] = true
	}
	var fresh, high int
	for _, f := range cur.Findings {
		if !old[f.ID] {
			fresh++
			if f.Severity == "high" {
				high++
			}
		}
	}
	switch {
	case fresh > 0 && high > 0:
		add(trailPlural(fresh, "new finding")+" ("+strconv.Itoa(high)+" high)", trailSevBad)
	case fresh > 0:
		add(trailPlural(fresh, "new finding"), trailSevWarn)
	case len(prev.Findings) > 0 && len(cur.Findings) == 0:
		add("findings cleared", trailSevGood)
	}

	// An agent line only when its rating changes bucket: a percent agent's
	// verdict is the number itself, and 70% → 71% is not news.
	before := map[string]trailAgent{}
	for _, a := range prev.Agents {
		before[a.Name] = a
	}
	for _, a := range cur.Agents {
		if b, ok := before[a.Name]; ok && b.Verdict != "" && a.Verdict != "" && b.Sev != a.Sev {
			add(a.Name+": "+b.Verdict+" → "+a.Verdict, a.Sev)
		}
	}

	if g, ok := cur.Gate("approvals"); ok && g.Outcome != "" {
		if p, ok := prev.Gate("approvals"); ok && p.settledOutcome() != g.Outcome {
			if g.Outcome == "passed" {
				add("approved", trailSevGood)
			} else {
				add("needs review", trailSevWarn)
			}
		}
	}
	if !prev.Mergeable && cur.Mergeable && cur.Status == "open" {
		add("ready to merge", trailSevGood)
	}
	if prev.Status != cur.Status && (cur.Status == "merged" || cur.Status == "closed") {
		add(cur.Status, trailSevNeutral)
	}
	return evs
}

func trailLineSev(evs []trailEvent) trailSev {
	sev := trailSevNeutral
	for _, e := range evs {
		sev = max(sev, e.Sev)
	}
	return sev
}

// trailLineText is the status line's text: the change and its project trail,
// then the events. links wraps the two ids in OSC 8 hyperlinks (tty only).
func trailLineText(cur changeState, evs []trailEvent, links bool) string {
	change, project := cur.Ref.Label(), "P"+strconv.Itoa(cur.ProjectNumber)
	if links {
		if cur.URL != "" {
			change = osc8(cur.URL, change)
		}
		if cur.ProjectURL != "" {
			project = osc8(cur.ProjectURL, project)
		}
	}
	parts := []string{"⛬ " + change + " (" + project + ")"}
	for _, e := range evs {
		parts = append(parts, e.Text)
	}
	return strings.Join(parts, " · ")
}

var trailEscapes = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x1b\x07]*(?:\x1b\\|\x07)`)

// trailVisibleWidth measures display columns, skipping CSI and OSC sequences
// (visWidth stops an OSC 8 link at the first letter of its URL).
func trailVisibleWidth(s string) int {
	return runewidth.StringWidth(trailEscapes.ReplaceAllString(s, ""))
}

var trailBandBG = map[trailSev]string{
	trailSevBad:     "48;2;150;30;30",
	trailSevWarn:    "48;2;150;100;0",
	trailSevGood:    "48;2;30;110;50",
	trailSevNeutral: "48;2;60;60;60",
}

// trailBand paints the line on a full-width background whose colour is its
// severity. width 0 means no padding.
func trailBand(text string, sev trailSev, width int) string {
	pad := ""
	if w := trailVisibleWidth(text); width > w {
		pad = strings.Repeat(" ", width-w)
	}
	return "\x1b[1;38;2;255;255;255;" + trailBandBG[sev] + "m" + text + pad + "\x1b[0m"
}
