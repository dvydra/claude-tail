package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunWTFRejectsArguments(t *testing.T) {
	err := runWTF(Config{WTFArgs: []string{"status"}})
	if err == nil || err.Error() != "wtf: unsupported arguments: status" {
		t.Fatalf("runWTF(status) error = %v", err)
	}
}

func TestFallbackWTFSummaryUsesStableSessionMetadata(t *testing.T) {
	if got := fallbackWTFSummary(wtfSession{Name: "  Add dashboard  ", ID: "abc"}); got != "Add dashboard" {
		t.Fatalf("named fallback = %q", got)
	}
	if got := fallbackWTFSummary(wtfSession{ID: "abc"}); got != "Session abc" {
		t.Fatalf("id fallback = %q", got)
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

func TestCollectWTFSessionsKeepsSameIDForDifferentAgents(t *testing.T) {
	deps := wtfInventoryDeps{
		Today: func(string, int64, *time.Location) []handoverItem {
			return []handoverItem{
				{Agent: AgentClaude, SessionID: "same", Title: "Claude"},
				{Agent: AgentAmp, SessionID: "same", Title: "Amp"},
			}
		},
		Live: func(string) []liveSession { return nil },
	}

	got := collectWTFSessions(t.TempDir(), 1, time.UTC, deps)
	if len(got) != 2 || wtfSessionKey(got[0].Agent, got[0].ID) == wtfSessionKey(got[1].Agent, got[1].ID) {
		t.Fatalf("sessions = %+v, want distinct Claude and Amp rows", got)
	}
}

func TestCollectWTFSessionsDefaultsEmptyLiveStatusAndResolvesRepo(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "git@github.com:org/project.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	deps := wtfInventoryDeps{
		Today: func(string, int64, *time.Location) []handoverItem { return nil },
		Live: func(string) []liveSession {
			return []liveSession{{Agent: AgentClaude, SessionID: "live-only", Cwd: repo}}
		},
	}

	got := collectWTFSessions(home, 1, time.UTC, deps)
	if len(got) != 1 || got[0].State != "active" || got[0].Repo != "org/project" {
		t.Fatalf("live-only session = %+v", got)
	}
}

func TestWTFTreeChoiceUsesExistingTailResolverShape(t *testing.T) {
	got := wtfTreeChoice(wtfSession{Agent: AgentAmp, ID: "T-one", Transcript: "/tmp/one.jsonl", Cwd: "/repo", Repo: "o/r"})
	if got.Result != treeChosen || got.Agent != AgentAmp || got.ID != "T-one" || got.Path != "/tmp/one.jsonl" || got.Cwd != "/repo" || got.Repo != "o/r" {
		t.Fatalf("choice = %+v", got)
	}
}
