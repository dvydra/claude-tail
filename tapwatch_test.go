package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func writeSidecar(t *testing.T, home, session string, evs ...tapEvent) {
	t.Helper()
	for _, ev := range evs {
		ev.Session = session
		if err := appendTapEvent(home, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func questionEvents(msgID, text string) []tapEvent {
	input, _ := json.Marshal(map[string]any{"questions": []map[string]any{{
		"question": "Which do I build next?", "header": "Next",
		"options": []map[string]string{{"label": "4a"}, {"label": "3"}},
	}}})
	return []tapEvent{
		{Ts: 1786318800000, Kind: "text", MsgID: msgID, Text: text},
		{Ts: 1786318800100, Kind: "tool_use", MsgID: msgID, Name: "AskUserQuestion", ToolID: "toolu_" + msgID, Input: input},
		{Ts: 1786318800200, Kind: "message_stop", MsgID: msgID, StopReason: "tool_use"},
	}
}

func TestTapWatcherReportsCompletedPromptOnce(t *testing.T) {
	home := t.TempDir()
	writeSidecar(t, home, "s1", tapEvent{Ts: 1, Kind: "text", MsgID: "old", Text: "history"})

	// A watcher starts at end-of-file: events already on disk belong to turns the
	// transcript has long since rendered.
	w := newTapWatcher(home, "s1")
	if got := w.poll(); len(got) != 0 {
		t.Fatalf("pre-existing events must not replay, got %+v", got)
	}

	writeSidecar(t, home, "s1", questionEvents("msg_A", "Preamble text.")...)
	got := w.poll()
	if len(got) != 1 {
		t.Fatalf("want 1 prompt, got %d", len(got))
	}
	if got[0].MsgID != "msg_A" || len(got[0].Preamble) != 1 || got[0].Preamble[0] != "Preamble text." {
		t.Errorf("prompt = %+v", got[0])
	}
	if len(got[0].Questions) != 1 || got[0].Questions[0].Question != "Which do I build next?" {
		t.Errorf("questions = %+v", got[0].Questions)
	}
	if got[0].Ts != 1786318800000 {
		t.Errorf("ts = %d", got[0].Ts)
	}

	// Polling again must not re-report it — the events stay in the file forever.
	if again := w.poll(); len(again) != 0 {
		t.Errorf("re-reported: %+v", again)
	}
}

// A message whose events straddle two polls must still be reported exactly once,
// and never half-rendered.
func TestTapWatcherHandlesSplitMessage(t *testing.T) {
	home := t.TempDir()
	writeSidecar(t, home, "s2")
	w := newTapWatcher(home, "s2")
	evs := questionEvents("msg_B", "Split preamble.")

	writeSidecar(t, home, "s2", evs[0], evs[1]) // text + tool_use, still streaming
	if got := w.poll(); len(got) != 0 {
		t.Fatalf("a message with no message_stop must not report: %+v", got)
	}
	writeSidecar(t, home, "s2", evs[2]) // message_stop
	got := w.poll()
	if len(got) != 1 || got[0].MsgID != "msg_B" {
		t.Fatalf("want msg_B once, got %+v", got)
	}
	if len(got[0].Preamble) != 1 || got[0].Preamble[0] != "Split preamble." {
		t.Errorf("preamble lost across the split: %+v", got[0].Preamble)
	}
}

func TestTapWatcherIgnoresOrdinaryTurns(t *testing.T) {
	home := t.TempDir()
	writeSidecar(t, home, "s3")
	w := newTapWatcher(home, "s3")
	writeSidecar(t, home, "s3",
		tapEvent{Ts: 5, Kind: "text", MsgID: "m1", Text: "just talking"},
		tapEvent{Ts: 6, Kind: "message_stop", MsgID: "m1", StopReason: "end_turn"},
		tapEvent{Ts: 7, Kind: "text", MsgID: "m2", Text: "running a tool"},
		tapEvent{Ts: 8, Kind: "tool_use", MsgID: "m2", Name: "Bash"},
		tapEvent{Ts: 9, Kind: "message_stop", MsgID: "m2", StopReason: "tool_use"},
	)
	if got := w.poll(); len(got) != 0 {
		t.Errorf("ordinary turns must stay with the transcript, got %+v", got)
	}
}

// Following a lineage fork/relocation repoints the watcher without replaying the
// new session's backlog.
func TestTapWatcherRebind(t *testing.T) {
	home := t.TempDir()
	writeSidecar(t, home, "old", questionEvents("msg_old", "old text")...)
	writeSidecar(t, home, "new", tapEvent{Ts: 1, Kind: "text", MsgID: "hist", Text: "already flushed"})

	w := newTapWatcher(home, "old")
	w.rebind(home, "new")
	if got := w.poll(); len(got) != 0 {
		t.Fatalf("rebind must start at end-of-file, got %+v", got)
	}
	writeSidecar(t, home, "new", questionEvents("msg_new", "new text")...)
	got := w.poll()
	if len(got) != 1 || got[0].MsgID != "msg_new" {
		t.Fatalf("want the new session's prompt, got %+v", got)
	}

	// Rebinding to the same session is a no-op (offset preserved, no replay).
	before := w.offset
	w.rebind(home, "new")
	if w.offset != before {
		t.Errorf("same-session rebind reset the offset: %d -> %d", before, w.offset)
	}
}

func TestTapWatcherMissingSidecar(t *testing.T) {
	w := newTapWatcher(t.TempDir(), "never-tapped")
	if got := w.poll(); got != nil {
		t.Errorf("a missing sidecar must poll cleanly, got %+v", got)
	}
}

// ── renderer: early render + exact suppression ───────────────────────────────

func TestTapPreambleThenTranscriptRendersOnce(t *testing.T) {
	var buf bytes.Buffer
	r := newRendererWith(&buf, testTheme(), "dots", 0, identityRender)
	r.live = true

	p := tapPendingPrompt{
		MsgID: "msg_X", Ts: 1786318800000,
		Preamble:  []string{"The plan's cost model was wrong."},
		QID:       "toolu_X",
		Questions: []QuestionItem{{Header: "Next", Question: "Which do I build next?", Options: []string{"4a", "3"}}},
	}
	r.tapPreamble(p, formatTapTS(p.Ts, time.UTC))
	r.endLine()
	early := buf.String()

	if !strings.Contains(early, "The plan's cost model was wrong.") {
		t.Fatalf("preamble not rendered from the tap:\n%s", early)
	}
	if !strings.Contains(early, "Which do I build next?") {
		t.Fatalf("question card not rendered:\n%s", early)
	}
	// Order is the whole point: the reasoning, then the question.
	if strings.Index(early, "cost model") > strings.Index(early, "Which do I build next?") {
		t.Error("preamble must render BEFORE the question card")
	}

	// Now the transcript flushes (after the user answers): the same text block and
	// the same question must not render a second time.
	buf.Reset()
	r.emit(Record{Kind: KindAssistant, Ts: "2026-08-10 09:00:00", Body: "The plan's cost model was wrong.", MsgID: "msg_X"})
	r.emit(Record{Kind: KindQuestion, Ts: "2026-08-10 09:00:00", QID: "toolu_X", Questions: p.Questions})
	r.endLine()
	if dup := buf.String(); strings.TrimSpace(dup) != "" {
		t.Errorf("transcript records duplicated tap output:\n%q", dup)
	}

	// A DIFFERENT text in the same message still renders (only the exact block is
	// suppressed, and only once).
	buf.Reset()
	r.emit(Record{Kind: KindAssistant, Ts: "2026-08-10 09:00:01", Body: "Something else entirely.", MsgID: "msg_X"})
	r.endLine()
	if !strings.Contains(buf.String(), "Something else entirely.") {
		t.Error("unrelated text in the same message must still render")
	}
}

// ── the no-tap ordering fix ──────────────────────────────────────────────────

// Without a tap, the hook marker shows the card first and Claude Code releases
// the preamble only after the user answers. The card must then be redrawn under
// its preamble so the pane reads in wire order.
func TestQuestionCardRedrawnAfterDeferredPreamble(t *testing.T) {
	var buf bytes.Buffer
	r := newRendererWith(&buf, testTheme(), "dots", 0, identityRender)
	r.live = true
	qs := []QuestionItem{{Header: "Next", Question: "Which do I build next?", Options: []string{"4a", "3"}}}

	r.pendingQuestion(qs) // instant alert from the hook marker
	buf.Reset()

	// The user answers; Claude Code flushes the withheld message: preamble text
	// first, then the tool_use record for the question.
	r.emit(Record{Kind: KindAssistant, Ts: "2026-08-10 09:00:00", Body: "The plan's cost model was wrong.", MsgID: "msg_L"})
	r.emit(Record{Kind: KindQuestion, Ts: "2026-08-10 09:00:00", QID: "q-late", Questions: qs})
	r.endLine()
	out := buf.String()

	if !strings.Contains(out, "The plan's cost model was wrong.") {
		t.Fatalf("preamble must render when it finally lands:\n%s", out)
	}
	if !strings.Contains(out, "Which do I build next?") {
		t.Fatalf("card must be redrawn under its preamble:\n%s", out)
	}
	if strings.Index(out, "cost model") > strings.Index(out, "Which do I build next?") {
		t.Error("redrawn card must come after the preamble")
	}
	// One bell only — the live assistant turn's, which fires before the card. The
	// redraw itself must not re-alert a prompt the user already answered.
	if n := strings.Count(out, "\a"); n != 1 {
		t.Errorf("want exactly 1 bell (the assistant turn's), got %d", n)
	}
	if strings.LastIndex(out, "\a") > strings.Index(out, "Which do I build next?") {
		t.Error("the redrawn card must not ring the bell again")
	}
}

// The common case is unchanged: an answered-immediately question, with nothing
// printed between the alert and the record, still shows exactly one card.
func TestQuestionCardNotDuplicatedWhenNothingIntervenes(t *testing.T) {
	var buf bytes.Buffer
	r := newRendererWith(&buf, testTheme(), "dots", 0, identityRender)
	r.live = true
	qs := []QuestionItem{{Header: "Drink", Question: "Tea or coffee?", Options: []string{"Tea"}}}

	r.pendingQuestion(qs)
	buf.Reset()
	r.emit(Record{Kind: KindQuestion, Ts: "2026-08-10 09:00:00", QID: "q1", Questions: qs})
	r.endLine()
	if got := strings.TrimSpace(buf.String()); got != "" {
		t.Errorf("card should stay suppressed, got:\n%q", got)
	}
}

// With the tap, the preamble is rendered BEFORE the card and its transcript twin
// is suppressed — so nothing counts as intervening and the card is not repeated.
func TestTapPathDoesNotRedrawCard(t *testing.T) {
	var buf bytes.Buffer
	r := newRendererWith(&buf, testTheme(), "dots", 0, identityRender)
	r.live = true
	qs := []QuestionItem{{Header: "Next", Question: "Which do I build next?", Options: []string{"4a"}}}

	r.tapPreamble(tapPendingPrompt{MsgID: "msg_T", Preamble: []string{"Reasoning."}, QID: "q-tap", Questions: qs}, "2026-08-10 09:00:00")
	buf.Reset()
	r.emit(Record{Kind: KindAssistant, Ts: "2026-08-10 09:00:00", Body: "Reasoning.", MsgID: "msg_T"})
	r.emit(Record{Kind: KindQuestion, Ts: "2026-08-10 09:00:00", QID: "q-tap", Questions: qs})
	r.endLine()
	if got := strings.TrimSpace(buf.String()); got != "" {
		t.Errorf("tap path already rendered both in order; nothing should repeat:\n%q", got)
	}
}

// A session with BOTH early paths live (hook marker + API tap) must still show
// exactly one card. Observed live as a doubled question block: the tap sees the
// question at message_stop, the hook fires when Claude Code dispatches the tool.
func TestQuestionCardNotDoubledByBothEarlyPaths(t *testing.T) {
	qs := []QuestionItem{{Header: "Next", Question: "Which do I build next?", Options: []string{"4a", "3"}}}
	count := func(s string) int { return strings.Count(s, "Which do I build next?") }

	// Order 1: tap first (the normal case — the wire beats the tool dispatch).
	var buf bytes.Buffer
	r := newRendererWith(&buf, testTheme(), "dots", 0, identityRender)
	r.live = true
	r.tapPreamble(tapPendingPrompt{MsgID: "m1", Preamble: []string{"Reasoning."}, QID: "q1", Questions: qs}, "2026-08-10 09:00:00")
	r.pendingQuestion(qs) // the hook marker, arriving second
	r.endLine()
	if n := count(buf.String()); n != 1 {
		t.Errorf("tap-then-marker: card rendered %d times, want 1", n)
	}
	if !strings.Contains(buf.String(), "Reasoning.") {
		t.Error("tap-then-marker: preamble should still be there")
	}
	if n := strings.Count(buf.String(), "\a"); n != 1 {
		t.Errorf("tap-then-marker: want 1 bell, got %d", n)
	}

	// Order 2: marker first (a slow sidecar write). The card still appears once,
	// and the tap's preamble is not dropped just because the card already showed.
	buf.Reset()
	r2 := newRendererWith(&buf, testTheme(), "dots", 0, identityRender)
	r2.live = true
	r2.pendingQuestion(qs)
	r2.tapPreamble(tapPendingPrompt{MsgID: "m2", Preamble: []string{"Late reasoning."}, QID: "q2", Questions: qs}, "2026-08-10 09:00:00")
	r2.endLine()
	if n := count(buf.String()); n != 1 {
		t.Errorf("marker-then-tap: card rendered %d times, want 1", n)
	}
	if !strings.Contains(buf.String(), "Late reasoning.") {
		t.Error("marker-then-tap: preamble must still render")
	}

	// And the transcript record that eventually lands is still suppressed.
	buf.Reset()
	r2.emit(Record{Kind: KindQuestion, Ts: "2026-08-10 09:01:00", QID: "q2", Questions: qs})
	r2.endLine()
	if n := count(buf.String()); n != 0 {
		t.Errorf("transcript card should stay suppressed, rendered %d times", n)
	}
}

func TestEarlyTextKeyDistinguishesMessages(t *testing.T) {
	if earlyTextKey("m1", "ok") == earlyTextKey("m2", "ok") {
		t.Error("identical text in different messages must not share a key")
	}
	if earlyTextKey("m1", "a") == earlyTextKey("m1", "b") {
		t.Error("different text in one message must not share a key")
	}
}

// A reload/theme swap clears the early-shown set, so a full re-render of the
// transcript shows everything from the file rather than silently dropping the
// blocks the tap had displayed.
func TestResetClearsEarlyShown(t *testing.T) {
	var buf bytes.Buffer
	r := newRendererWith(&buf, testTheme(), "dots", 0, identityRender)
	r.earlyShown[earlyTextKey("m", "body")] = true
	r.reset()
	if len(r.earlyShown) != 0 {
		t.Fatal("reset must clear earlyShown")
	}
	r.emit(Record{Kind: KindAssistant, Ts: "2026-08-10 09:00:00", Body: "body", MsgID: "m"})
	r.endLine()
	if !strings.Contains(buf.String(), "body") {
		t.Error("after reset the transcript record must render")
	}
}

func TestTapSidecarGatingByFile(t *testing.T) {
	home := t.TempDir()
	// The live loop only builds a watcher when a sidecar exists — the cheap way to
	// ask "was this session routed through the tap?".
	if _, err := os.Stat(tapSidecarPath(home, "s")); !os.IsNotExist(err) {
		t.Fatal("expected no sidecar")
	}
	writeSidecar(t, home, "s", tapEvent{Ts: 1, Kind: "text", MsgID: "m", Text: "x"})
	if _, err := os.Stat(tapSidecarPath(home, "s")); err != nil {
		t.Errorf("sidecar should exist after an event: %v", err)
	}
}
