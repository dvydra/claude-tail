package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWTFTranscript(t *testing.T, home, body string) string {
	t.Helper()
	path := filepath.Join(home, ".claude", "projects", "repo", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func stubWTFRunner(t *testing.T, run func(string, string, string) ([]byte, error)) {
	t.Helper()
	old := fmRunner
	fmRunner = run
	t.Cleanup(func() { fmRunner = old })
}

func TestSummarizeWTFSessionCachesByTranscriptInput(t *testing.T) {
	home := t.TempDir()
	path := writeWTFTranscript(t, home, `{"type":"user","message":{"role":"user","content":"add search"}}`+"\n")
	s := wtfSession{Agent: AgentClaude, ID: "session", Name: "Search work", Transcript: path}
	calls := 0
	stubWTFRunner(t, func(schema, instructions, input string) ([]byte, error) {
		calls++
		return []byte(`{"summary":"Added search.","needsUser":""}`), nil
	})

	got, cache, err := summarizeWTFSession(s, home, wtfSummaryCache{})
	if err != nil || got.Summary != "Added search." || len(cache.InputHash) != 64 {
		t.Fatalf("first summary = %+v, cache = %+v, err = %v", got, cache, err)
	}
	got, cache, err = summarizeWTFSession(s, home, cache)
	if err != nil || got.Summary != "Added search." || calls != 1 {
		t.Fatalf("cached summary = %+v, calls = %d, err = %v", got, calls, err)
	}

	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"add filters"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _ = summarizeWTFSession(s, home, cache)
	if calls != 2 {
		t.Fatalf("changed transcript calls = %d, want 2", calls)
	}
}

func TestSummarizeWTFSessionFallsBackOnInvalidOutputAndErrors(t *testing.T) {
	home := t.TempDir()
	path := writeWTFTranscript(t, home, `{"type":"user","message":{"role":"user","content":"fix login"}}`+"\n")
	s := wtfSession{Agent: AgentClaude, ID: "session", Name: "Fix login", Transcript: path}
	outputs := []struct {
		out []byte
		err error
	}{{out: []byte("not json")}, {err: errors.New("fm unavailable")}}
	for _, output := range outputs {
		stubWTFRunner(t, func(string, string, string) ([]byte, error) { return output.out, output.err })
		got, _, _ := summarizeWTFSession(s, home, wtfSummaryCache{})
		if got.Summary != fallbackWTFSummary(s) || got.Summary == "" {
			t.Fatalf("fallback = %q, want %q", got.Summary, fallbackWTFSummary(s))
		}
	}
}

func TestSummarizeWTFSessionPendingQuestionOverridesModel(t *testing.T) {
	home := t.TempDir()
	path := writeWTFTranscript(t, home, `{"type":"user","message":{"role":"user","content":"prepare release"}}`+"\n")
	s := wtfSession{Agent: AgentClaude, ID: "session", Name: "Release", Transcript: path}
	if err := os.MkdirAll(pendingDir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := `{"kind":"question","payload":{"questions":[{"question":"Ship version 2.0?","header":"Release"}]}}`
	if err := os.WriteFile(pendingMarkerPath(home, s.ID), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	stubWTFRunner(t, func(string, string, string) ([]byte, error) {
		return []byte(`{"summary":"Prepared the release.","needsUser":"Review the changelog."}`), nil
	})

	got, _, err := summarizeWTFSession(s, home, wtfSummaryCache{})
	if err != nil {
		t.Fatal(err)
	}
	if got.NeedsUser != "Waiting for Daniel: Ship version 2.0?" {
		t.Fatalf("needs user = %q", got.NeedsUser)
	}
}

func TestDeterministicNeedFormatsPermission(t *testing.T) {
	home := t.TempDir()
	s := wtfSession{Agent: AgentClaude, ID: "session"}
	if err := os.MkdirAll(pendingDir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := `{"kind":"permission","payload":{"tool_name":"Bash","tool_input":{"command":"git push"}}}`
	if err := os.WriteFile(pendingMarkerPath(home, s.ID), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := deterministicNeed(home, s); got != "Waiting for Daniel to approve Bash(git push)." {
		t.Fatalf("permission need = %q", got)
	}
}

func TestWTFSummaryInputDropsToolAndSyntheticRecords(t *testing.T) {
	home := t.TempDir()
	path := writeWTFTranscript(t, home, strings.Join([]string{
		`{"type":"user","promptSource":"typed","origin":{"kind":"human"},"message":{"role":"user","content":"keep this request"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"keep this answer"},{"type":"tool_use","name":"Bash","input":{"command":"secret tool command"}}]}}`,
		`{"type":"user","promptSource":"system","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<summary>secret task update</summary>\n</task-notification>"}}`,
	}, "\n")+"\n")

	got := wtfSummaryInput(path, home)
	for _, want := range []string{"User: keep this request", "Assistant: keep this answer"} {
		if !strings.Contains(got, want) {
			t.Errorf("input missing %q: %q", want, got)
		}
	}
	for _, leak := range []string{"secret tool command", "secret task update"} {
		if strings.Contains(got, leak) {
			t.Errorf("input leaked %q: %q", leak, got)
		}
	}
}

func TestSummarizeWTFSnapshotSkipsRemoteRowsAndKeysCacheByAgent(t *testing.T) {
	snapshot := wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentClaude, ID: "same", Transcript: "/claude", Name: "Claude title"},
		{Agent: AgentAmp, ID: "same", Transcript: "/amp", Name: "Amp title"},
		{Agent: AgentAmp, ID: "remote", Name: "Remote title"},
	}}
	seen := map[string]int{}
	summarize := func(s wtfSession, _ string, cached wtfSummaryCache) (wtfSummary, wtfSummaryCache, error) {
		key := wtfSessionKey(s.Agent, s.ID)
		seen[key]++
		return wtfSummary{Summary: key}, wtfSummaryCache{InputHash: key}, nil
	}

	got, cache := summarizeWTFSnapshot(snapshot, "/home", nil, summarize)
	if len(seen) != 2 || len(cache) != 2 || got.Sessions[0].Summary != "claude:same" || got.Sessions[1].Summary != "amp:same" {
		t.Fatalf("snapshot = %+v, cache = %+v, seen = %+v", got, cache, seen)
	}
	if got.Sessions[2].Summary != "Remote title" || seen["amp:remote"] != 0 {
		t.Fatalf("remote row = %+v, seen = %+v", got.Sessions[2], seen)
	}
}
