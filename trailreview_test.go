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

func TestTrailMentionsIgnoreFileContents(t *testing.T) {
	url := "https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10"
	for _, l := range []string{
		`{"type":"assistant","timestamp":"2026-11-11T01:02:03.000Z","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/x_test.go","content":"want ` + url + `"}}]}}`,
		`{"type":"assistant","timestamp":"2026-11-11T01:02:03.000Z","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/x.go","old_string":"a","new_string":"entire trail show k3s-config/10 --project et/dvydra"}}]}}`,
	} {
		if ms := trailMentionsInLine([]byte(l)); len(ms) != 0 {
			t.Fatalf("file contents an agent writes are not mentions: %+v", ms)
		}
	}
	heredoc := `{"type":"assistant","timestamp":"2026-11-11T01:02:03.000Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat >> x_test.go <<'EOF'\nwant ` + url + `\nEOF"}}]}}`
	if ms := trailMentionsInLine([]byte(heredoc)); len(ms) != 0 {
		t.Fatalf("a URL inside a shell command is not a mention: %+v", ms)
	}
	thinking := `{"type":"assistant","timestamp":"2026-11-11T01:02:03.000Z","message":{"content":[{"type":"thinking","thinking":"maybe ` + url + `"}]}}`
	if ms := trailMentionsInLine([]byte(thinking)); len(ms) != 0 {
		t.Fatalf("thinking is not a mention: %+v", ms)
	}
	both := `{"type":"assistant","timestamp":"2026-11-11T01:02:03.000Z","message":{"content":[{"type":"text","text":"see ` + url + `"},{"type":"tool_use","name":"Bash","input":{"command":"entire trail show 31 --project et/dvydra","description":"https://entire.com/et/dvydra/-/trails/99"}}]}}`
	ms := trailMentionsInLine([]byte(both))
	keys := map[string]bool{}
	for _, m := range ms {
		keys[m.Ref.Key()] = true
	}
	if len(ms) != 2 || !keys["et/dvydra/k3s-config/10"] || !keys["et/dvydra/-/31"] {
		t.Fatalf("assistant text and the shell command still count, nothing else: %+v", ms)
	}
}

func TestTrackerSkipsAlreadyMergedOnFirstLook(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	h.f.set(trailRunKey("", "entire", "api", "-j", "au", "/api/v1/repos/REPO1/trails/10"), bytes.Replace(readTrailFixture(t, "change.json"), []byte(`"status": "open"`), []byte(`"status": "merged"`), 1))
	h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, 0)})
	got := h.stepAt(time.Minute)
	if len(eventful(got)) != 0 {
		t.Fatalf("a change merged before we ever saw it prints nothing: %+v", got)
	}
	before := h.f.count()
	h.stepAt(5 * time.Minute)
	if h.f.count() != before {
		t.Fatal("and is not fetched again")
	}
}

func TestTrackerUntrack(t *testing.T) {
	h := newTrackHarness(t)
	stubFixtureChange(t, h.f)
	h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, 0)})
	h.stepAt(0)
	h.tr.Untrack(fixtureChangeRef.Key())
	h.tr.Observe([]trailMention{mentionAt(fixtureChangeRef, time.Minute)})
	before := h.f.count()
	if got := h.stepAt(5 * time.Minute); len(got) != 0 || h.f.count() != before {
		t.Fatalf("a removed change is neither fetched nor sent again: %+v, %d calls", got, h.f.count()-before)
	}
}

func TestTrackerUntrackPushing(t *testing.T) {
	h := newTrackHarness(t)
	stubRepoCwd(h, "aaa")
	stubFixtureChange(t, h.f)
	h.f.set(trailRunKey("", "entire", "trail", "show", "--branch", "feat-x", "--repo", "et/dvydra/k3s-config", "--json"), readTrailFixture(t, "show.json"))
	h.tr.Follow(writeTranscript(t, "/r"))
	h.stepAt(0)
	h.tr.Untrack(fixtureChangeRef.Key())
	if got := h.stepAt(5 * time.Minute); len(got) != 0 || h.f.calls[showKey("k3s-config/10")] != 1 {
		t.Fatalf("the session's own change can be removed too: %+v", got)
	}
}

func TestTrailModalRows(t *testing.T) {
	s := fixtureState(t)
	other := s
	other.Ref = trailRef{trailRefChange, "et", "dvydra", "midnight-magic", 6}
	lines, rows := trailModalLines([]changeState{other, s}, nil, nil, "", testTheme(), 100, other.Ref.Key())
	if len(rows) != 2 || rows[0].Key != s.Ref.Key() || rows[1].Key != other.Ref.Key() {
		t.Fatalf("one row per change, in display order: %+v", rows)
	}
	if !strings.Contains(lines[rows[1].Line], "▸") || strings.Contains(lines[rows[0].Line], "▸") {
		t.Fatalf("only the selected change is marked:\n%s\n%s", lines[rows[0].Line], lines[rows[1].Line])
	}
}

func TestUpdateTrailModal(t *testing.T) {
	rows := []trailModalRow{{Key: "a", URL: "https://entire.com/a", Line: 1}, {Key: "b", URL: "https://entire.com/b", Line: 20}}
	st := trailModalState{}
	st, act := updateTrailModal(st, rows, 40, 10, kDown, 0)
	if st.Sel != 1 || act != trailModalNone || st.Top != 11 {
		t.Fatalf("down selects the next change and scrolls it into view: %+v %v", st, act)
	}
	if _, act = updateTrailModal(st, rows, 40, 10, kEnter, 0); act != trailModalOpen {
		t.Fatalf("enter opens: %v", act)
	}
	if _, act = updateTrailModal(st, rows, 40, 10, kRune, 'x'); act != trailModalRemove {
		t.Fatalf("x removes: %v", act)
	}
	if _, act = updateTrailModal(st, rows, 40, 10, kRune, 'r'); act != trailModalRefresh {
		t.Fatalf("r refreshes: %v", act)
	}
	if _, act = updateTrailModal(st, rows, 40, 10, kEsc, 0); act != trailModalClose {
		t.Fatalf("esc closes: %v", act)
	}
	st, _ = updateTrailModal(trailModalState{Sel: 1}, rows, 40, 10, kUp, 0)
	if st.Sel != 0 {
		t.Fatalf("up: %+v", st)
	}
	if _, act = updateTrailModal(trailModalState{}, nil, 5, 10, kEnter, 0); act != trailModalNone {
		t.Fatalf("enter with nothing selected does nothing: %v", act)
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
