package main

import (
	"os"
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

func TestRenderWTFSnapshotHonorsAsymmetricNarrowWidth(t *testing.T) {
	got := renderWTFSnapshot(testWTFSnapshot(), 13, false)
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if width := visWidth(line); width > 13 {
			t.Errorf("line width = %d, want <= 13: %q", width, line)
		}
	}
}

func TestRenderWTFSnapshotBoundsNonPositiveWidths(t *testing.T) {
	for _, width := range []int{0, -7} {
		if got := renderWTFSnapshot(testWTFSnapshot(), width, false); got != "" {
			t.Errorf("width %d render = %q, want empty bounded output", width, got)
		}
	}
}

func TestRenderWTFSnapshotColorIsDeterministic(t *testing.T) {
	want := renderWTFSnapshot(testWTFSnapshot(), 120, true)
	_ = renderWTF(wtfUI{Snapshot: testWTFSnapshot(), Width: 120, Height: 40}, Theme{UserANSI: "MUTATED", ClaudeANSI: "MUTATED", DimANSI: "MUTATED"})
	got := renderWTFSnapshot(testWTFSnapshot(), 120, true)
	if got != want {
		t.Fatalf("static render changed after interactive render with another theme")
	}
	if !strings.Contains(got, "\x1b[1;38;2;122;162;247m") {
		t.Fatalf("static color render lacks deterministic palette: %q", got)
	}
}

func TestWTFRenderersDoNotReadHome(t *testing.T) {
	original := os.Getenv("HOME")
	t.Cleanup(func() { _ = os.Setenv("HOME", original) })
	_ = os.Setenv("HOME", "/home/dan")

	snapshot := renderWTFSnapshot(testWTFSnapshot(), 120, false)
	interactive := renderWTF(wtfUI{Snapshot: testWTFSnapshot(), Width: 120, Height: 40}, Theme{})
	for name, got := range map[string]string{"snapshot": snapshot, "interactive": interactive} {
		if strings.Contains(got, "~/src") {
			t.Errorf("%s renderer read HOME:\n%s", name, got)
		}
		if !strings.Contains(got, "/home/dan/src") {
			t.Errorf("%s renderer did not use explicit snapshot cwd:\n%s", name, got)
		}
	}
}

func TestRenderWTFSnapshotEmpty(t *testing.T) {
	if got := renderWTFSnapshot(wtfSnapshot{}, 80, false); !strings.Contains(got, "No sessions active or seen today.") {
		t.Fatalf("empty render = %q", got)
	}
}

func TestRenderWTFEmptyHonorsHeightAndClearPrefix(t *testing.T) {
	got := renderWTF(wtfUI{Width: 80, Height: 2}, Theme{ClaudeANSI: "\x1b[31m"})
	if !strings.HasPrefix(got, "\x1b[H\x1b[2J") {
		t.Fatalf("empty interactive render lacks clear prefix: %q", got)
	}
	body := strings.TrimPrefix(got, "\x1b[H\x1b[2J")
	if lines := strings.Count(strings.TrimSuffix(body, "\n"), "\n") + 1; lines > 2 {
		t.Fatalf("empty interactive render has %d lines, want <= 2: %q", lines, body)
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

func TestUpdateWTFUsesExactRowsAndClampsTopAfterRefresh(t *testing.T) {
	snapshot := wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentClaude, ID: "one", Repo: "repo/a", Active: true, State: "busy", Summary: "summary", NeedsUser: "answer"},
		{Agent: AgentAmp, ID: "two", Repo: "repo/a", Active: true, State: "idle"},
		{Agent: AgentClaude, ID: "three", Repo: "repo/b", Active: true, State: "busy", Summary: "summary"},
		{Agent: AgentAmp, ID: "four", Repo: "repo/c", Active: false, State: "ended", Summary: "summary", NeedsUser: "answer"},
	}}
	ui := wtfUI{Snapshot: snapshot, Width: 80, Height: 9}
	for range 3 {
		ui = updateWTF(ui, kDown, 0)
	}
	if ui.Top != 3 {
		t.Fatalf("top = %d, want exact first visible session 3", ui.Top)
	}
	if got := renderWTF(ui, Theme{}); !strings.Contains(got, "▸ A  four") {
		t.Fatalf("selected session is outside viewport:\n%s", got)
	}

	ui.Snapshot.Sessions = ui.Snapshot.Sessions[:2]
	ui = updateWTF(ui, treeKey(-1), 0)
	if ui.Cursor != 1 || ui.Top < 0 || ui.Top > ui.Cursor {
		t.Fatalf("after refresh cursor/top = %d/%d, want clamped to remaining sessions", ui.Cursor, ui.Top)
	}
	if got := renderWTF(ui, Theme{}); !strings.Contains(got, "▸ A  two") {
		t.Fatalf("selected remaining session is outside viewport:\n%s", got)
	}
}
