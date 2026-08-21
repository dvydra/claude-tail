package main

import (
	"strings"
	"testing"
	"time"
)

const taskNoteEventPayload = `<task-notification>
<task-id>bb8leuqzn</task-id>
<summary>Monitor event: "CI on #3304"</summary>
<event>lint: pass
test: pass
Entire Gates: fail</event>
If this event is something the user would act on now, send a PushNotification. Routine or benign output doesn't need one.
</task-notification>`

const taskNoteDonePayload = `<task-notification>
<task-id>bb8leuqzn</task-id>
<tool-use-id>toolu_01RBb8w5LMoUEr1MoJTrg7fE</tool-use-id>
<output-file>/tmp/tasks/bb8leuqzn.output</output-file>
<status>completed</status>
<summary>Monitor "CI on #3304" stream ended</summary>
</task-notification>`

func TestIsTaskNote(t *testing.T) {
	cases := []struct {
		name                           string
		originKind, promptSource, body string
		want                           bool
	}{
		{"origin kind", "task-notification", "system", taskNoteEventPayload, true},
		// Transcripts predating the origin field: the payload is the only evidence.
		{"payload only", "", "system", taskNoteEventPayload, true},
		{"typed human", "human", "typed", "check the build", false},
		// A human who pastes the tag is still a human — promptSource decides.
		{"human pasting the tag", "human", "typed", "look at this: " + taskNoteEventPayload, false},
		{"system without a payload", "", "system", "keep going", false},
	}
	for _, c := range cases {
		if got := isTaskNote(c.originKind, c.promptSource, c.body); got != c.want {
			t.Errorf("%s: isTaskNote = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTaskNoteLine(t *testing.T) {
	got := taskNoteLine(taskNoteEventPayload)
	want := `Monitor event: "CI on #3304" · lint: pass, test: pass, Entire Gates: fail`
	if got != want {
		t.Errorf("event line = %q, want %q", got, want)
	}
	// The instruction addressed to the agent must not survive: it's not news.
	if strings.Contains(got, "PushNotification") {
		t.Errorf("agent instruction leaked into the marker: %q", got)
	}

	got = taskNoteLine(taskNoteDonePayload)
	if want := `Monitor "CI on #3304" stream ended`; got != want {
		t.Errorf("done line = %q, want %q", got, want)
	}
	// Ids and temp paths are plumbing — nothing a reader can act on.
	for _, leak := range []string{"toolu_", "/tmp/tasks", "bb8leuqzn", "completed"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q leaked into the marker: %q", leak, got)
		}
	}
}

// A settled CI run reports every check at once; the marker stays one line.
func TestTaskNoteLineTruncates(t *testing.T) {
	body := `<task-notification>
<summary>Monitor event: "CI on #3304"</summary>
<event>ALL-SETTLED: {"test":"pass","Cursor Bugbot":"pass","check-licenses / license-check":"pass","test-db":"pass","test-nodb":"pass","lint":"pass"}</event>
</task-notification>`
	got := taskNoteLine(body)
	if n := len([]rune(got)); n > taskNoteMaxRunes+1 { // +1 for the ellipsis
		t.Errorf("marker is %d runes, want <= %d: %q", n, taskNoteMaxRunes+1, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation not marked: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("marker spans lines: %q", got)
	}
}

// A long Monitor title must not crowd out the event. The title repeats on every
// tick; the checks are the only part that changed.
func TestTaskNoteLineBudgetsTitleNotEvent(t *testing.T) {
	body := `<task-notification>
<summary>Monitor event: "CI on #3304 workload-rename — settle then I merge (authorized)"</summary>
<event>integration / integration: pass
test-db: pass</event>
</task-notification>`
	got := taskNoteLine(body)
	for _, want := range []string{"integration / integration: pass", "test-db: pass"} {
		if !strings.Contains(got, want) {
			t.Errorf("event detail %q lost to the title: %q", want, got)
		}
	}
	if !strings.Contains(got, "…") {
		t.Errorf("long title not trimmed: %q", got)
	}

	// A completion notice has no event competing for the line, so it keeps its
	// whole title rather than being trimmed to the summary budget.
	body = `<task-notification>
<status>completed</status>
<summary>Monitor "CI on #3304 workload-rename — settle then I merge (authorized)" stream ended</summary>
</task-notification>`
	if got := taskNoteLine(body); !strings.HasSuffix(got, `(authorized)" stream ended`) {
		t.Errorf("completion notice trimmed with nothing competing: %q", got)
	}
}

// Nothing human-readable in the payload → nothing rendered, rather than an
// empty marker line.
func TestTaskNoteLineEmpty(t *testing.T) {
	if got := taskNoteLine("<task-notification>\n<task-id>x</task-id>\n</task-notification>"); got != "" {
		t.Errorf("taskNoteLine = %q, want empty", got)
	}
}

// The adapter must classify these as TASKNOTE, never as a USER turn — that
// attribution is the whole bug.
func TestNormalizeClaudeTaskNote(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2026-01-02T15:04:14Z","promptSource":"system","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<summary>Monitor \"x\" stream ended</summary>\n</task-notification>"}}`)
	recs := normalizeClaude(line, time.UTC)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(recs), recs)
	}
	if recs[0].Kind != KindTaskNote {
		t.Errorf("Kind = %v, want %v", recs[0].Kind, KindTaskNote)
	}
	if want := `Monitor "x" stream ended`; recs[0].Body != want {
		t.Errorf("Body = %q, want %q", recs[0].Body, want)
	}

	// A typed message with the same timestamp shape stays a USER turn.
	line = []byte(`{"type":"user","timestamp":"2026-01-02T15:04:14Z","promptSource":"typed","origin":{"kind":"human"},"message":{"role":"user","content":"check the build"}}`)
	recs = normalizeClaude(line, time.UTC)
	if len(recs) != 1 || recs[0].Kind != KindUser {
		t.Fatalf("typed message misclassified: %+v", recs)
	}
}

// A record with no payload left to show must not emit a stray marker.
func TestNormalizeClaudeTaskNoteEmpty(t *testing.T) {
	line := []byte(`{"type":"user","promptSource":"system","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>x</task-id>\n</task-notification>"}}`)
	if recs := normalizeClaude(line, time.UTC); len(recs) != 0 {
		t.Errorf("got %d records, want none: %+v", len(recs), recs)
	}
}
