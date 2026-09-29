package main

import (
	"strings"
	"testing"
)

func testWTFSnapshot() wtfSnapshot {
	return wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentAmp, ID: "T-idle", Name: "amp-idle", Repo: "org/other", Cwd: "/home/dan/src/other", Branch: "main", State: "idle", Active: true, LastActivity: 30, Summary: "Waiting for another task."},
		{Agent: AgentClaude, ID: "claude-busy-id", Name: "api-work", Repo: "org/api", Cwd: "/home/dan/src/api/.claude/worktrees/checkpoints", Branch: "feat/checkpoints", State: "busy", Active: true, LastActivity: 40, Summary: "Reworking checkpoint writes; tests are running.", NeedsUser: "Choose whether to preserve the old API."},
		{Agent: AgentClaude, ID: "older-ended", Name: "old-fix", Repo: "org/old", Cwd: "/home/dan/src/old", State: "ended", LastActivity: 10, Summary: "Fixed an older issue."},
		{Agent: AgentAmp, ID: "T-new-ended", Name: "new-fix", Repo: "org/new", Cwd: "/home/dan/src/new", State: "ended", LastActivity: 20, Summary: "Fixed the latest issue."},
	}}
}

func TestRenderWTFSnapshotSectionsOrderingAndContent(t *testing.T) {
	got := renderWTFSnapshot(testWTFSnapshot(), 120, false)
	for _, want := range []string{"2 active · 2 ended today", "Now", "org/api", "org/other", "Recently stopped", "NEEDS DANIEL  Choose whether to preserve the old API."} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
	assertOrder := func(a, b string) {
		t.Helper()
		if strings.Index(got, a) >= strings.Index(got, b) {
			t.Errorf("%q should precede %q:\n%s", a, b, got)
		}
	}
	assertOrder("Now", "Recently stopped")
	assertOrder("api-work", "amp-idle")
	assertOrder("new-fix", "old-fix")
	assertOrder("Reworking checkpoint writes; tests are running.", "NEEDS DANIEL")
}

func TestRenderWTFSnapshotClipsAndDisablesColor(t *testing.T) {
	got := renderWTFSnapshot(testWTFSnapshot(), 48, false)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("plain render contains escape byte: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if width := visWidth(line); width > 48 {
			t.Errorf("line width = %d, want <= 48: %q", width, line)
		}
	}
}

func TestRenderWTFSnapshotEmpty(t *testing.T) {
	if got := renderWTFSnapshot(wtfSnapshot{}, 80, false); !strings.Contains(got, "No sessions active or seen today.") {
		t.Fatalf("empty render = %q", got)
	}
}

func TestRenderWTFMarksOnlySelectedSession(t *testing.T) {
	ui := wtfUI{Snapshot: testWTFSnapshot(), Cursor: 1, Width: 120, Height: 40}
	got := renderWTF(ui, Theme{})
	if strings.Count(got, "▸ ") != 1 || !strings.Contains(got, "▸ A  amp-idle") {
		t.Fatalf("selected render:\n%s", got)
	}
}

func TestUpdateWTFNavigationRefreshQuitAndChoose(t *testing.T) {
	base := wtfUI{Snapshot: testWTFSnapshot(), Cursor: 1, Top: 1}
	if got := updateWTF(base, kUp, 0); got.Cursor != 0 {
		t.Fatalf("up cursor = %d", got.Cursor)
	}
	if got := updateWTF(base, kDown, 0); got.Cursor != 2 {
		t.Fatalf("down cursor = %d", got.Cursor)
	}
	if got := updateWTF(base, kHome, 0); got.Cursor != 0 {
		t.Fatalf("home cursor = %d", got.Cursor)
	}
	if got := updateWTF(base, kEnd, 0); got.Cursor != 3 {
		t.Fatalf("end cursor = %d", got.Cursor)
	}
	if got := updateWTF(base, kRune, 'r'); !got.Refresh {
		t.Fatal("r did not request refresh")
	}
	for _, key := range []struct {
		k treeKey
		r rune
	}{{kRune, 'q'}, {kEsc, 0}} {
		if got := updateWTF(base, key.k, key.r); !got.Quit {
			t.Fatalf("%v did not quit", key)
		}
	}
	got := updateWTF(base, kEnter, 0)
	if got.Chosen == nil || got.Chosen.ID != "T-idle" {
		t.Fatalf("enter chose %+v", got.Chosen)
	}
}
