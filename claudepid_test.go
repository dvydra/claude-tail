package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeClaudeTranscript puts an empty transcript for id under the profile's project
// dir for cwd, and returns its path.
func writeClaudeTranscript(t *testing.T, configDir, cwd, id string) string {
	t.Helper()
	dir := filepath.Join(configDir, "projects", claudeSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaudeSessionOfReadsTheRegistry(t *testing.T) {
	home := t.TempDir()
	id := "aaaaaaaa-0000-0000-0000-000000000000"
	writeRegistry(t, filepath.Join(home, ".claude", "sessions"), map[string]string{
		"100.json": entry(100, id, "work", "idle", 1000),
	})
	want := writeClaudeTranscript(t, filepath.Join(home, ".claude"), "/tmp/x", id)

	argvCalls := 0
	sessionOf := claudeSessionOf(home, func(int) string { argvCalls++; return "" })
	if got := sessionOf(100); got != want {
		t.Errorf("sessionOf(100) = %q, want %q", got, want)
	}
	if argvCalls != 0 {
		t.Errorf("read argv %d times for a registered pid, want 0", argvCalls)
	}
}

func TestClaudeSessionOfSpansProfiles(t *testing.T) {
	home := t.TempDir()
	id := "bbbbbbbb-0000-0000-0000-000000000000"
	personal := filepath.Join(home, personalConfigDir)
	writeRegistry(t, filepath.Join(personal, "sessions"), map[string]string{
		"200.json": entry(200, id, "mine", "idle", 1000),
	})
	want := writeClaudeTranscript(t, personal, "/tmp/x", id)

	if got := claudeSessionOf(home, func(int) string { return "" })(200); got != want {
		t.Errorf("sessionOf(200) = %q, want %q", got, want)
	}
}

// A claude that hasn't registered yet can still carry its id on the command
// line (the workspace launches with --resume / --session-id).
func TestClaudeSessionOfFallsBackToArgv(t *testing.T) {
	home := t.TempDir()
	id := "cccccccc-0000-0000-0000-000000000000"
	want := writeClaudeTranscript(t, filepath.Join(home, ".claude"), "/tmp/y", id)

	sessionOf := claudeSessionOf(home, func(pid int) string {
		if pid == 300 {
			return "claude --resume " + id
		}
		return "claude"
	})
	if got := sessionOf(300); got != want {
		t.Errorf("sessionOf(300) = %q, want %q", got, want)
	}
	// No registry entry and no id on the command line: nothing to tail. The
	// newest transcript in its folder would be some other session.
	if got := sessionOf(301); got != "" {
		t.Errorf("sessionOf(301) = %q, want empty", got)
	}
}

func TestLiveCwdCounts(t *testing.T) {
	got := liveCwdCounts([]liveSession{{Cwd: "/a"}, {Cwd: "/b"}, {Cwd: "/a"}, {Cwd: ""}})
	if len(got) != 2 || got["/a"] != 2 || got["/b"] != 1 {
		t.Errorf("liveCwdCounts = %v, want map[/a:2 /b:1]", got)
	}
}
