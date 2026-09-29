package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWTFStateMissingReturnsInitializedState(t *testing.T) {
	home := t.TempDir()
	state, err := loadWTFState(home, 1_700_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != wtfStateVersion || state.UpdatedAt != 1_700_000_000 {
		t.Fatalf("state metadata = %+v", state)
	}
	if state.Sessions == nil || state.Trails == nil || state.Worktrees == nil || state.Findings == nil || state.SummaryCache == nil {
		t.Fatalf("state maps are not initialized: %+v", state)
	}
}

func TestWTFStateRoundTripKeepsHistoryAndUsesAtomicModes(t *testing.T) {
	home := t.TempDir()
	state := newWTFState(1_700_000_000)
	state.Trails["o/r#7"] = wtfTrail{Key: "o/r#7", Owner: "o", Repo: "r", Number: 7, FirstSeen: 10, LastSeen: 20}
	state.Worktrees["/gone"] = wtfWorktree{Repo: "o/r", Path: "/gone", Exists: false, FirstSeen: 11, LastSeen: 21}
	state.SummaryCache["claude:s1"] = wtfSummaryCache{InputHash: "abc", Value: wtfSummary{Summary: "cached"}}

	if err := saveWTFState(home, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadWTFState(home, 1_700_000_001)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Trails["o/r#7"].FirstSeen != 10 || loaded.Worktrees["/gone"].Exists || loaded.SummaryCache["claude:s1"].Value.Summary != "cached" {
		t.Fatalf("round trip = %+v", loaded)
	}
	if matches, err := filepath.Glob(filepath.Join(wtfDir(home), "*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, err = %v", matches, err)
	}
	if info, err := os.Stat(wtfDir(home)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state directory mode = %v, err = %v", infoMode(info), err)
	}
	if info, err := os.Stat(wtfStatePath(home)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v, err = %v", infoMode(info), err)
	}
}

func TestWTFStateCorruptFileIsPreserved(t *testing.T) {
	home := t.TempDir()
	now := int64(1_700_000_123)
	if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"version":`)
	if err := os.WriteFile(wtfStatePath(home), bad, 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := loadWTFState(home, now)
	if err == nil || !strings.Contains(err.Error(), "recovered corrupt wtf state") {
		t.Fatalf("load error = %v", err)
	}
	var syntaxErr *os.PathError
	if errors.As(err, &syntaxErr) {
		t.Fatalf("wanted explicit recovery error, got path error: %v", err)
	}
	if state.Version != wtfStateVersion || state.Sessions == nil || state.Trails == nil {
		t.Fatalf("recovered state = %+v", state)
	}
	corrupt := filepath.Join(wtfDir(home), "state.corrupt-1700000123.json")
	if got, readErr := os.ReadFile(corrupt); readErr != nil || string(got) != string(bad) {
		t.Fatalf("corrupt copy = %q, err = %v", got, readErr)
	}
	if _, statErr := os.Stat(wtfStatePath(home)); !os.IsNotExist(statErr) {
		t.Fatalf("state path still exists: %v", statErr)
	}
}

func TestWTFStateCorruptRecoveryDoesNotOverwriteExistingCopy(t *testing.T) {
	home := t.TempDir()
	now := int64(1_700_000_123)
	if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	first := []byte("first corrupt state")
	existing := filepath.Join(wtfDir(home), "state.corrupt-1700000123.json")
	if err := os.WriteFile(existing, first, 0o600); err != nil {
		t.Fatal(err)
	}
	second := []byte(`{"version":`)
	if err := os.WriteFile(wtfStatePath(home), second, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadWTFState(home, now)
	if err == nil || !strings.Contains(err.Error(), "recovered corrupt wtf state") {
		t.Fatalf("load error = %v", err)
	}
	if got, readErr := os.ReadFile(existing); readErr != nil || string(got) != string(first) {
		t.Fatalf("first corrupt copy = %q, err = %v", got, readErr)
	}
	next := filepath.Join(wtfDir(home), "state.corrupt-1700000123-1.json")
	if got, readErr := os.ReadFile(next); readErr != nil || string(got) != string(second) {
		t.Fatalf("second corrupt copy = %q, err = %v", got, readErr)
	}
}

func TestWTFStateRejectsUnsupportedVersionsWithoutMovingFile(t *testing.T) {
	for _, test := range []struct {
		name string
		json string
		want string
	}{
		{name: "missing", json: `{}`, want: "missing version"},
		{name: "older", json: "{\"version\":0}", want: "unsupported version 0"},
		{name: "newer", json: "{\"version\":2}", want: "unsupported version 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(wtfStatePath(home), []byte(test.json), 0o600); err != nil {
				t.Fatal(err)
			}

			state, err := loadWTFState(home, 123)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load error = %v, want %q", err, test.want)
			}
			if state.Version != wtfStateVersion || state.Sessions == nil {
				t.Fatalf("replacement state = %+v", state)
			}
			if got, readErr := os.ReadFile(wtfStatePath(home)); readErr != nil || string(got) != test.json {
				t.Fatalf("state file = %q, err = %v", got, readErr)
			}
			if copies, globErr := filepath.Glob(filepath.Join(wtfDir(home), "state.corrupt-*.json")); globErr != nil || len(copies) != 0 {
				t.Fatalf("corrupt copies = %v, err = %v", copies, globErr)
			}
		})
	}
}

func TestExpireWTFSessionsKeepsAssociations(t *testing.T) {
	state := newWTFState(200)
	state.Sessions["claude:old"] = wtfSession{Agent: AgentClaude, ID: "old", State: "ended", LastActivity: 99}
	state.Sessions["claude:active"] = wtfSession{Agent: AgentClaude, ID: "active", State: "busy", Active: true, LastActivity: 50}
	state.Sessions["amp:today"] = wtfSession{Agent: AgentAmp, ID: "today", State: "ended", LastActivity: 100}
	state.Trails["o/r#1"] = wtfTrail{
		Key:          "o/r#1",
		FirstClaim:   &wtfClaim{SessionKey: "claude:old", At: 60},
		Associations: []wtfAssociation{{SessionKey: "claude:old", At: 70, Source: "transcript"}},
	}

	expireWTFSessions(&state, 100)
	if _, ok := state.Sessions["claude:old"]; ok {
		t.Fatal("yesterday's ended session was not expired")
	}
	if _, ok := state.Sessions["claude:active"]; !ok {
		t.Fatal("active session was expired")
	}
	if _, ok := state.Sessions["amp:today"]; !ok {
		t.Fatal("session ending at midnight was expired")
	}
	trail := state.Trails["o/r#1"]
	if trail.FirstClaim == nil || trail.FirstClaim.SessionKey != "claude:old" || len(trail.Associations) != 1 || trail.Associations[0].SessionKey != "claude:old" {
		t.Fatalf("trail history changed: %+v", trail)
	}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

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
