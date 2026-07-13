package main

import (
	"testing"
	"time"
)

func TestNormalizeEntireTranscript(t *testing.T) {
	loc := time.UTC

	// user: content is [{id,text}] blocks joined into the body.
	u := normalizeEntireTranscript([]byte(`{"type":"user","ts":"2026-03-27T06:57:14.146Z","content":[{"id":"x","text":"hello world"}]}`), loc)
	if len(u) != 1 || u[0].Kind != KindUser || u[0].Body != "hello world" {
		t.Fatalf("user → %+v", u)
	}

	// assistant: a text block + a tool_use block (name + input).
	a := normalizeEntireTranscript([]byte(`{"type":"assistant","ts":"2026-03-27T06:57:15Z","content":[{"type":"text","text":"reply"},{"type":"tool_use","id":"t","name":"Bash","input":{"command":"ls -la"}}]}`), loc)
	if len(a) != 2 {
		t.Fatalf("assistant → %d records, want 2: %+v", len(a), a)
	}
	if a[0].Kind != KindAssistant || a[0].Body != "reply" {
		t.Errorf("text block → %+v", a[0])
	}
	if a[1].Kind != KindToolUse || a[1].Name != "Bash" || a[1].Summary != "ls -la" {
		t.Errorf("tool block → %+v", a[1])
	}
}

func TestSniffAgentEntire(t *testing.T) {
	// Entire's transcript format: top-level content + ts, no nested message.
	line := []byte(`{"type":"assistant","ts":"2026-03-27T06:57:15Z","content":[{"type":"text","text":"hi"}]}`)
	if got := sniffAgent(line); got != AgentEntire {
		t.Errorf("sniffAgent(entire) = %v, want AgentEntire", got)
	}
	// A Claude line (nested message) must NOT be mistaken for entire.
	claude := []byte(`{"type":"user","message":{"content":"hi"},"timestamp":"2026-01-01T00:00:00Z"}`)
	if got := sniffAgent(claude); got != AgentClaude {
		t.Errorf("sniffAgent(claude) = %v, want AgentClaude", got)
	}
}
