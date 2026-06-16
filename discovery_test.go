package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSniffAgent(t *testing.T) {
	cases := []struct {
		line string
		want Agent
	}{
		{`{"type":"session_meta","payload":{"cwd":"/x"}}`, AgentCodex},
		{`{"type":"response_item","payload":{"type":"message"}}`, AgentCodex},
		{`{"type":"USER_INPUT","content":"x"}`, AgentAgy},
		{`{"type":"PLANNER_RESPONSE"}`, AgentAgy},
		{`{"type":"user","message":{"content":"hi"}}`, AgentClaude},
		{`{"type":"assistant"}`, AgentClaude},
		{`not json`, AgentClaude},
	}
	for _, c := range cases {
		if got := sniffAgent([]byte(c.line)); got != c.want {
			t.Errorf("sniffAgent(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsTruthy(t *testing.T) {
	truthy := []string{`"x"`, `""`, `1`, `0`, `[]`, `{}`, `true`}
	for _, v := range truthy {
		if !isTruthy(json.RawMessage(v)) {
			t.Errorf("isTruthy(%q) should be true", v)
		}
	}
	for _, v := range []string{`null`, `false`, ``} {
		if isTruthy(json.RawMessage(v)) {
			t.Errorf("isTruthy(%q) should be false", v)
		}
	}
}

func TestNewestGlobAll(t *testing.T) {
	dir := t.TempDir()
	// Create three files with distinct mtimes.
	names := []string{"a.jsonl", "b.jsonl", "c.jsonl"}
	base := time.Now()
	for i, n := range names {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("x"), 0o644)
		// a oldest, c newest
		mt := base.Add(time.Duration(i) * time.Minute)
		os.Chtimes(p, mt, mt)
	}
	got := newestGlobAll(filepath.Join(dir, "*.jsonl"))
	if len(got) != 3 || filepath.Base(got[0]) != "c.jsonl" || filepath.Base(got[2]) != "a.jsonl" {
		t.Errorf("expected newest-first c,b,a; got %v", got)
	}
	if newestGlob(filepath.Join(dir, "*.jsonl")) != got[0] {
		t.Error("newestGlob should return the newest")
	}
	if newestGlob(filepath.Join(dir, "none-*.jsonl")) != "" {
		t.Error("no match → empty")
	}
}

func TestDetectAgentForFileByPath(t *testing.T) {
	home := "/home/me"
	cases := []struct {
		path string
		want Agent
	}{
		{home + "/.claude/projects/slug/x.jsonl", AgentClaude},
		{home + "/.codex/sessions/2026/06/05/rollout-x.jsonl", AgentCodex},
		{home + "/.gemini/antigravity-cli/brain/id/.system_generated/logs/transcript.jsonl", AgentAgy},
	}
	for _, c := range cases {
		if got := detectAgentForFile(home, c.path); got != c.want {
			t.Errorf("detectAgentForFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestCodexScanner(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "05")
	os.MkdirAll(dir, 0o755)
	// Two rollouts: one for /proj, one for /other; /proj is newer.
	mk := func(name, cwd string, age time.Duration) string {
		p := filepath.Join(dir, name)
		line, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": cwd}})
		os.WriteFile(p, append(line, '\n'), 0o644)
		mt := time.Now().Add(-age)
		os.Chtimes(p, mt, mt)
		return p
	}
	other := mk("rollout-other.jsonl", "/other", 2*time.Minute)
	proj := mk("rollout-proj.jsonl", "/proj", 1*time.Minute)

	s := newCodexScanner(home)
	if got := s.findForCwd("/proj"); got != proj {
		t.Errorf("findForCwd(/proj) = %q, want %q", got, proj)
	}
	if got := s.cwdOf(other); got != "/other" {
		t.Errorf("cwdOf = %q", got)
	}
	// No match → global newest (proj is newest).
	if got := s.findForCwd("/nonexistent"); got != proj {
		t.Errorf("fallback newest = %q, want %q", got, proj)
	}
	if got := s.sessionsForCwd("/other", 5); len(got) != 1 || got[0] != other {
		t.Errorf("sessionsForCwd(/other) = %v", got)
	}
}

func TestFindSessionClaude(t *testing.T) {
	home := t.TempDir()
	pwd := "/Users/me/proj"
	slug := "-Users-me-proj"
	dir := filepath.Join(home, ".claude", "projects", slug)
	os.MkdirAll(dir, 0o755)
	want := filepath.Join(dir, "session.jsonl")
	os.WriteFile(want, []byte("{}"), 0o644)
	if got := findSessionClaude(home, pwd); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// Different pwd with no project dir → global fallback finds the same file.
	if got := findSessionClaude(home, "/other/dir"); got != want {
		t.Errorf("fallback got %q want %q", got, want)
	}
}

func TestAgyConversationIDAndDiscovery(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	id := "conv-123"
	tdir := filepath.Join(root, "brain", id, ".system_generated", "logs")
	os.MkdirAll(tdir, 0o755)
	transcript := filepath.Join(tdir, "transcript.jsonl")
	os.WriteFile(transcript, []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(root, "cache"), 0o755)
	cache, _ := json.Marshal(map[string]string{"/my/cwd": id})
	os.WriteFile(filepath.Join(root, "cache", "last_conversations.json"), cache, 0o644)

	if got := agyConversationID(root, "/my/cwd"); got != id {
		t.Errorf("got %q", got)
	}
	if got := findSessionAgy(home, "/my/cwd"); got != transcript {
		t.Errorf("findSessionAgy got %q want %q", got, transcript)
	}
}

func TestCwdMismatchNote(t *testing.T) {
	home := t.TempDir()
	// Claude: matching slug → no note.
	slug := "-Users-me-proj"
	sess := filepath.Join(home, ".claude", "projects", slug, "s.jsonl")
	if note := cwdMismatchNote(AgentClaude, sess, home, "/Users/me/proj", nil); note != "" {
		t.Errorf("expected no note, got %q", note)
	}
	// Claude: non-matching → note.
	if note := cwdMismatchNote(AgentClaude, sess, home, "/Users/me/other", nil); note == "" {
		t.Error("expected mismatch note")
	}
}
