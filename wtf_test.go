package main

import (
	"testing"
	"time"
)

func TestRunWTFRejectsArguments(t *testing.T) {
	err := runWTF(Config{WTFArgs: []string{"status"}})
	if err == nil || err.Error() != "wtf: unsupported arguments: status" {
		t.Fatalf("runWTF(status) error = %v", err)
	}
}

func TestCollectWTFSessionsMergesExactLiveState(t *testing.T) {
	today := []handoverItem{
		{Agent: AgentClaude, SessionID: "c1", Repo: "o/r", Cwd: "/old", Branch: "old", Title: "old title", LastActivity: 100, Path: "/t/c1.jsonl"},
		{Agent: AgentClaude, SessionID: "c2", Repo: "o/r", Cwd: "/ended", Title: "done", LastActivity: 90, Path: "/t/c2.jsonl"},
		{Agent: AgentAmp, SessionID: "T-a", Repo: "o/a", Cwd: "/amp", Title: "amp", LastActivity: 80, Path: "/t/a.jsonl"},
	}
	live := []liveSession{
		{Agent: AgentClaude, SessionID: "c1", Cwd: "/new", Branch: "feat/x", Name: "api-work", Status: "busy", SocketPath: "/tmp/c1.sock", StartedAt: 10_000, UpdatedAt: 120_000, Path: "/t/c1.jsonl"},
		{Agent: AgentAmp, SessionID: "T-a", Cwd: "/amp", Branch: "feat/a", Name: "amp", Status: "idle", UpdatedAt: 110_000, Path: "/t/a.jsonl"},
		{Agent: AgentAmp, SessionID: "T-remote", Name: "remote", Status: "busy", UpdatedAt: 130_000, Path: "/t/remote.jsonl"},
	}
	deps := wtfInventoryDeps{
		Today: func(string, int64, *time.Location) []handoverItem { return today },
		Live:  func(string) []liveSession { return live },
	}

	got := collectWTFSessions("/h", 200, time.UTC, deps)
	if len(got) != 4 || got[0].ID != "T-remote" || got[1].ID != "c1" || got[2].ID != "T-a" || got[3].ID != "c2" {
		t.Fatalf("sessions = %+v", got)
	}
	if got[0].Cwd != "" || got[0].Repo != "" || !got[0].Active || got[0].State != "busy" || got[0].LastActivity != 130 {
		t.Fatalf("remote amp = %+v", got[0])
	}
	if got[1].Cwd != "/new" || got[1].Branch != "feat/x" || got[1].Name != "api-work" || got[1].State != "busy" || got[1].SocketPath != "/tmp/c1.sock" || got[1].StartedAt != 10 || got[1].LastActivity != 120 {
		t.Fatalf("live overlay = %+v", got[1])
	}
	if got[2].Repo != "o/a" || got[2].State != "idle" || !got[2].Active {
		t.Fatalf("amp overlay = %+v", got[2])
	}
	if got[3].State != "ended" || got[3].Active || got[3].LastActivity != 90 {
		t.Fatalf("ended session = %+v", got[3])
	}
}
