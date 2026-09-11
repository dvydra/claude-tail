package main

import (
	"reflect"
	"testing"
)

func TestIsSyntheticUser(t *testing.T) {
	tests := []struct {
		name      string
		origin    string
		source    string
		meta      bool
		body      string
		wantSynth bool
	}{
		{"typed by the human", "human", "typed", false, "fix the auth bug", false},
		{"queued by the human", "human", "queued", false, "us 716 merged??", false},
		{"older record, no provenance fields", "", "typed", false, "/tmp/aws-node-before.yaml", false},
		{"bare record, no provenance at all", "", "", false, "plain text", false},

		{"injected skill body", "", "", true, "Base directory for this skill: /x/y", true},
		{"local-command caveat", "", "", true, "<local-command-caveat>Caveat: ...", true},
		{"task notification", "task-notification", "system", false, "<task-notification>...", true},
		{"sdk task notification", "task-notification", "sdk", false, "<task-notification>...", true},
		{"auto continuation", "auto-continuation", "system", false, "Goal set: ...", true},
		{"sdk prompt", "", "sdk", false, "Open the handover doc and continue", true},
		{"system prompt, meta", "", "system", true, "Poll for activity on trail 1799", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSyntheticUser(tc.origin, tc.source, tc.meta); got != tc.wantSynth {
				t.Errorf("isSyntheticUser(%q,%q,%v) = %v, want %v [%s]",
					tc.origin, tc.source, tc.meta, got, tc.wantSynth, tc.body)
			}
		})
	}
}

func TestUnwrapCommand(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"slash command with args",
			"<command-message>fix-alert</command-message>\n<command-name>/fix-alert</command-name>\n<command-args>triage item 4</command-args>",
			"/fix-alert triage item 4",
		},
		{
			"slash command without args",
			"<command-message>review</command-message>\n<command-name>/review</command-name>",
			"/review",
		},
		{
			"empty args tag",
			"<command-name>/progress</command-name>\n<command-args></command-args>",
			"/progress",
		},
		{"not a command, passes through", "just a normal message", "just a normal message"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unwrapCommand(tc.in); got != tc.want {
				t.Errorf("unwrapCommand(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A command envelope is the human's intent (they typed the slash command), so it
// must survive normalize as a USER turn with the XML chrome removed.
func TestNormalizeClaudeCommandEnvelopeBecomesUserTurn(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2026-06-15T05:00:00Z","message":{"content":` +
		`"<command-message>fix-alert</command-message>\n<command-name>/fix-alert</command-name>\n<command-args>triage item 4</command-args>"}}`)
	got := normalize(AgentClaude, line, utc)
	want := []Record{{Kind: KindUser, Ts: "2026-06-15 05:00:00", Body: "/fix-alert triage item 4"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

// An isMeta record is injected scaffolding, never the human's turn.
func TestNormalizeClaudeMetaStringDropped(t *testing.T) {
	line := []byte(`{"type":"user","isMeta":true,"timestamp":"2026-06-15T05:00:00Z","message":{"content":"<local-command-caveat>Caveat: ...</local-command-caveat>"}}`)
	if got := normalize(AgentClaude, line, utc); got != nil {
		t.Errorf("got %+v, want nil (meta records are not user turns)", got)
	}
}
