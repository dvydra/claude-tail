package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSubagentsDir(t *testing.T) {
	if got := subagentsDir("/p/proj/abc123.jsonl"); got != "/p/proj/abc123/subagents" {
		t.Errorf("subagentsDir = %q", got)
	}
}

func TestDiscoverSubagents(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(main, []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sess", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two subagents; the second spawns later (later first-record timestamp).
	write := func(id, ts, meta string) {
		if err := os.WriteFile(filepath.Join(sub, "agent-"+id+".jsonl"),
			[]byte(`{"type":"user","timestamp":"`+ts+`","message":{"content":"go"}}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if meta != "" {
			if err := os.WriteFile(filepath.Join(sub, "agent-"+id+".meta.json"), []byte(meta), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("bbb", "2026-07-13T04:00:00Z", `{"agentType":"general-purpose","description":"second agent"}`)
	write("aaa", "2026-07-13T03:00:00Z", `{"agentType":"code-reviewer","description":"first agent"}`)

	got := discoverSubagents(main)
	if len(got) != 2 {
		t.Fatalf("want 2 channels, got %d: %+v", len(got), got)
	}
	// Ordered by spawn time: aaa (03:00) before bbb (04:00).
	if got[0].AgentID != "aaa" || got[0].Description != "first agent" || got[0].AgentType != "code-reviewer" {
		t.Errorf("channel[0] = %+v", got[0])
	}
	if got[1].AgentID != "bbb" || got[1].Description != "second agent" {
		t.Errorf("channel[1] = %+v", got[1])
	}
}

func TestDiscoverSubagentsNone(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sess.jsonl")
	_ = os.WriteFile(main, []byte("{}\n"), 0o644)
	if got := discoverSubagents(main); got != nil {
		t.Errorf("want nil for a session with no subagents, got %+v", got)
	}
}

func TestDiscoverSubagentsMissingMeta(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	_ = os.WriteFile(main, []byte("{}\n"), 0o644)
	sub := filepath.Join(dir, "s", "subagents")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "agent-zz99.jsonl"),
		[]byte(`{"type":"user","timestamp":"2026-07-13T03:00:00Z"}`+"\n"), 0o644)
	got := discoverSubagents(main)
	if len(got) != 1 || got[0].Description == "" {
		t.Fatalf("want a synthesized description, got %+v", got)
	}
}

func TestRecordEpoch(t *testing.T) {
	want, _ := time.Parse(time.RFC3339, "2026-07-13T03:00:00Z")
	if got := recordEpoch([]byte(`{"timestamp":"2026-07-13T03:00:00Z"}`)); got != want.Unix() {
		t.Errorf("recordEpoch = %d, want %d", got, want.Unix())
	}
	if got := recordEpoch([]byte(`{"nope":true}`)); got != 0 {
		t.Errorf("recordEpoch(no ts) = %d, want 0", got)
	}
}
