package main

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func testWTFSnapshot() wtfSnapshot {
	return wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentAmp, ID: "T-idle", Name: "amp-idle", Repo: "org/other", Cwd: "/home/dan/src/other", Branch: "main", State: "idle", Active: true, LastActivity: 30, Summary: "Waiting for another task."},
		{Agent: AgentClaude, ID: "claude-busy-id", Name: "api-work", Repo: "org/api", Cwd: "/home/dan/src/api/.claude/worktrees/checkpoints", Branch: "feat/checkpoints", State: "busy", Active: true, LastActivity: 40, Summary: "Reworking checkpoint writes; tests are running.", NeedsUser: "Choose whether to preserve the old API."},
		{Agent: AgentClaude, ID: "older-ended", Name: "old-fix", Repo: "org/old", Cwd: "/home/dan/src/old", State: "ended", LastActivity: 10, Summary: "Fixed an older issue."},
		{Agent: AgentAmp, ID: "T-new-ended", Name: "new-fix", Repo: "org/new", Cwd: "/home/dan/src/new", State: "ended", LastActivity: 20, Summary: "Fixed the latest issue."},
	}}
}

func TestRenderWTFBadness(t *testing.T) {
	snapshot := wtfSnapshot{
		GeneratedAt: 1_700_000_000,
		Sessions: []wtfSession{
			{Agent: AgentAmp, ID: "cwd-only", Cwd: "/challenger/cwd"},
		},
		Findings: []wtfFinding{
			{ID: "later", Kind: "outside-canonical", Severity: 2, TrailKey: "acme/api#2", Owner: "claude:owner", Challenger: "amp:challenger", Worktrees: []string{"/canonical/two", "/actual/two"}, Explanation: "second finding", Evidence: []string{"session evidence", "git evidence"}, FirstSeen: 20, Active: true},
			{ID: "highest", Kind: "default-branch", Severity: 3, TrailKey: "acme/api#3", Owner: "claude:owner3", Challenger: "amp:challenger3", Worktrees: []string{"/canonical/three"}, Explanation: "highest finding", Evidence: []string{"default evidence"}, FirstSeen: 30, Active: true},
			{ID: "earlier", Kind: "existing-wip-elsewhere", Severity: 2, TrailKey: "acme/api#1", Owner: "claude:owner1", Challenger: "amp:challenger1", Worktrees: []string{"/canonical/one", "/actual/one"}, Explanation: "first finding", Evidence: []string{"earlier evidence"}, FirstSeen: 10, Active: true},
			{ID: "cwd", Kind: "outside-canonical", Severity: 1, TrailKey: "acme/api#5", Challenger: "amp:cwd-only", Active: true},
			{ID: "cleared", Kind: "missing-canonical", Severity: 9, TrailKey: "acme/api#9", Active: false},
		},
		Trails: []wtfTrail{
			{Key: "acme/api#2", CanonicalWorktree: "/canonical/two"},
			{Key: "acme/api#3", CanonicalWorktree: "/canonical/three"},
			{Key: "acme/api#5", CanonicalWorktree: "/canonical/five"},
		},
	}

	got := renderWTFSnapshot(snapshot, 160, false)
	for _, want := range []string{"Badness", "default-branch", "owner claude:owner3", "challenger amp:challenger3", "canonical /canonical/three · actual /canonical/three", "canonical /canonical/two · actual /actual/two", "canonical /canonical/five · actual /challenger/cwd", "default evidence", "delivery not attempted"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
	for _, pair := range [][2]string{{"default-branch", "existing-wip-elsewhere"}, {"existing-wip-elsewhere", "outside-canonical"}} {
		if strings.Index(got, pair[0]) >= strings.Index(got, pair[1]) {
			t.Errorf("%q should precede %q:\n%s", pair[0], pair[1], got)
		}
	}
	if strings.Contains(got, "missing-canonical") {
		t.Fatalf("cleared finding rendered:\n%s", got)
	}
}

func TestRenderWTFWIPTrails(t *testing.T) {
	snapshot := wtfSnapshot{
		Sessions: []wtfSession{{Agent: AgentClaude, ID: "active", Active: true}},
		Trails: []wtfTrail{
			{Key: "acme/api#1", OwnerSession: "claude:owner", CanonicalWorktree: "/wt/active", Associations: []wtfAssociation{{SessionKey: "claude:active", Worktree: "/wt/active"}}},
			{Key: "acme/api#2", OwnerSession: "amp:owner", CanonicalWorktree: "/wt/dirty", Associations: []wtfAssociation{{Worktree: "/wt/dirty"}}},
			{Key: "acme/api#3", OwnerSession: "claude:owner3", CanonicalWorktree: "/wt/unmerged", Associations: []wtfAssociation{{Worktree: "/wt/unmerged"}}},
			{Key: "acme/api#4", OwnerSession: "claude:old", CanonicalWorktree: "/wt/clean", Associations: []wtfAssociation{{Worktree: "/wt/clean"}}},
			{Key: "acme/api#5", OwnerSession: "claude:missing", CanonicalWorktree: "/wt/missing", Associations: []wtfAssociation{{Worktree: "/wt/missing"}}},
			{Key: "acme/api#6", OwnerSession: "claude:error", CanonicalWorktree: "/wt/error", Associations: []wtfAssociation{{Worktree: "/wt/error"}}},
		},
		Worktrees: []wtfWorktree{
			{Path: "/wt/active", Exists: true, DirtyFiles: 0, UnmergedCommits: 0},
			{Path: "/wt/dirty", Exists: true, DirtyFiles: 2, UnmergedCommits: 0},
			{Path: "/wt/unmerged", Exists: true, DirtyFiles: 0, UnmergedCommits: 3},
			{Path: "/wt/clean", Exists: true, DirtyFiles: 0, UnmergedCommits: 0},
			{Path: "/wt/missing", Exists: false, DirtyFiles: 0, UnmergedCommits: -1, GitError: "worktree path missing"},
			{Path: "/wt/error", Exists: true, DirtyFiles: 0, UnmergedCommits: -1, GitError: "git status failed"},
		},
	}

	got := renderWTFSnapshot(snapshot, 160, false)
	for _, want := range []string{"WTF  1 active · 0 ended today · 5 WIP trails · 0 findings", "WIP trails", "acme/api#1", "canonical /wt/active", "owner claude:owner", "active 1", "dirty 0", "unmerged 0", "active session", "acme/api#2", "dirty worktree", "acme/api#3", "unmerged commits", "acme/api#5", "canonical /wt/missing", "dirty unknown", "unmerged unknown", "worktree path missing", "acme/api#6", "git status failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "acme/api#4") {
		t.Fatalf("merged clean history rendered as WIP:\n%s", got)
	}
	for _, pair := range [][2]string{{"\nBadness\n", "\nNow\n"}, {"\nNow\n", "\nWIP trails\n"}, {"\nWIP trails\n", "\nRecently stopped\n"}} {
		if strings.Index(got, pair[0]) >= strings.Index(got, pair[1]) {
			t.Errorf("section %q should precede %q:\n%s", pair[0], pair[1], got)
		}
	}
}

func TestRenderWTFShowsEachWIPWorktreeAndAssociationEvidence(t *testing.T) {
	snapshot := wtfSnapshot{Trails: []wtfTrail{{Key: "acme/api#7", Associations: []wtfAssociation{{Worktree: "/wt/a", Source: "user", Evidence: "api#7"}, {Worktree: "/wt/b", Source: "source branch", Evidence: "feat/7"}}}}, Worktrees: []wtfWorktree{{Path: "/wt/a", Exists: true, DirtyFiles: 2, UnmergedCommits: 0}, {Path: "/wt/b", Exists: true, DirtyFiles: 0, UnmergedCommits: 3}}}
	got := renderWTFSnapshot(snapshot, 180, false)
	for _, want := range []string{"/wt/a · dirty 2 · unmerged 0 · user: api#7", "/wt/b · dirty 0 · unmerged 3 · source branch: feat/7"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestRenderWTFSessionRowsShowSortedTrailKeys(t *testing.T) {
	snapshot := wtfSnapshot{Sessions: []wtfSession{{Agent: AgentClaude, ID: "one", Active: true}}, Trails: []wtfTrail{{Key: "acme/api#2", Associations: []wtfAssociation{{SessionKey: "claude:one"}}}, {Key: "acme/api#1", Associations: []wtfAssociation{{SessionKey: "claude:one"}}}}}
	got := renderWTFSnapshot(snapshot, 120, false)
	if !strings.Contains(got, "acme/api#1, acme/api#2") {
		t.Fatalf("session trail keys missing or unsorted:\n%s", got)
	}
}

func TestRenderWTFScrolledToEndedRetainsSectionHeadings(t *testing.T) {
	snapshot := testWTFSnapshot()
	got := composeWTF(snapshot, wtfRenderOpts{width: 120, top: 2})
	for _, pair := range [][2]string{{"\nBadness\n", "\nNow\n"}, {"\nNow\n", "\nWIP trails\n"}, {"\nWIP trails\n", "\nRecently stopped\n"}} {
		if strings.Index(got, pair[0]) < 0 || strings.Index(got, pair[0]) >= strings.Index(got, pair[1]) {
			t.Errorf("section %q should precede %q:\n%s", pair[0], pair[1], got)
		}
	}
	if strings.Contains(got, "▸ Now") || strings.Contains(got, "▸ WIP trails") {
		t.Fatalf("section heading became selectable:\n%s", got)
	}
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

func TestNormalizeWTFViewportUsesNewDimensionsBeforeRender(t *testing.T) {
	snapshot := wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentClaude, ID: "one", Repo: "repo/a", Active: true, State: "busy", Summary: "one summary"},
		{Agent: AgentAmp, ID: "two", Repo: "repo/a", Active: true, State: "idle", Summary: "two summary"},
		{Agent: AgentClaude, ID: "three", Repo: "repo/b", Active: true, State: "idle", Summary: "three summary"},
	}}
	ui := wtfUI{Snapshot: snapshot, Cursor: 2, Top: 0, Width: 100, Height: 20}

	ui = normalizeWTFViewport(ui, 40, 2)
	if ui.Width != 40 || ui.Height != 2 || ui.Top != ui.Cursor {
		t.Fatalf("resized state width/height/top/cursor = %d/%d/%d/%d, want 40/2/2/2", ui.Width, ui.Height, ui.Top, ui.Cursor)
	}
	if got := renderWTF(ui, Theme{}); !strings.Contains(got, "▸ C  three") {
		t.Fatalf("render after resize hid selected session:\n%s", got)
	}
}

func TestRenderWTFMinimalViewportShowsSelectedAcrossHeaders(t *testing.T) {
	snapshot := wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentClaude, ID: "active", Repo: "repo/a", Active: true, State: "busy", Summary: "active summary", NeedsUser: "active need"},
		{Agent: AgentAmp, ID: "ended", Repo: "repo/b", Active: false, State: "ended", Summary: "ended summary"},
	}}
	ui := wtfUI{Snapshot: snapshot, Cursor: 1, Top: 1, Width: 80, Height: 1}

	got := strings.TrimPrefix(renderWTF(ui, Theme{}), "\x1b[H\x1b[2J")
	if !strings.Contains(got, "WTF  1 active · 1 ended today") {
		t.Fatalf("one-row viewport hid fixed header:\n%s", got)
	}
	if strings.Contains(got, "▸ A  ended") || strings.Contains(got, "Recently stopped") || strings.Contains(got, "repo/b") {
		t.Fatalf("one-row viewport rendered body despite zero body rows:\n%s", got)
	}
}

func TestRenderWTFKeepsTodayHeaderFixedWhileBodyScrolls(t *testing.T) {
	snapshot := wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentClaude, ID: "one", Repo: "repo/a", Active: true, State: "busy", Summary: "one"},
		{Agent: AgentClaude, ID: "two", Repo: "repo/b", Active: true, State: "idle", Summary: "two"},
		{Agent: AgentAmp, ID: "three", Repo: "repo/c", Active: false, State: "ended", Summary: "three"},
	}}
	ui := wtfUI{Snapshot: snapshot, Cursor: 2, Top: 2, Width: 80, Height: 5}

	got := renderWTF(ui, Theme{})
	if !strings.Contains(got, "WTF  2 active · 1 ended today") || !strings.Contains(got, "▸ A  three") {
		t.Fatalf("scrolled render must retain header and selection:\n%s", got)
	}
}

func TestApplyWTFSnapshotPreservesSelectedIdentityAcrossReorder(t *testing.T) {
	ui := wtfUI{Snapshot: wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentClaude, ID: "wanted", Active: true, LastActivity: 10},
		{Agent: AgentAmp, ID: "other", Active: true, LastActivity: 5},
	}}, Cursor: 0, Height: 20}
	next := wtfSnapshot{Sessions: []wtfSession{
		{Agent: AgentAmp, ID: "other", Active: true, LastActivity: 20},
		{Agent: AgentClaude, ID: "wanted", Active: true, LastActivity: 10},
	}}

	ui = applyWTFSnapshot(ui, next)
	ui = updateWTF(ui, kEnter, 0)
	if ui.Chosen == nil || wtfSessionKey(ui.Chosen.Agent, ui.Chosen.ID) != "claude:wanted" {
		t.Fatalf("chosen after reorder = %+v", ui.Chosen)
	}
}

func TestRenderWTFHealth(t *testing.T) {
	for _, test := range []struct {
		name     string
		snapshot wtfSnapshot
		want     string
	}{
		{name: "absent", snapshot: wtfSnapshot{}, want: "monitoring off · run entire wtf install"},
		{name: "running", snapshot: wtfSnapshot{Monitoring: true, Now: 200, Health: wtfHealth{LastSuccessfulScan: 195}}, want: "monitoring on · last successful scan 5s ago"},
		{name: "stale error", snapshot: wtfSnapshot{Monitoring: true, Now: 500, Health: wtfHealth{LastSuccessfulScan: 200, LastError: "git unavailable"}}, want: "last successful scan 5m ago · error: git unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := renderWTF(wtfUI{Snapshot: test.snapshot, Width: 160, Height: 40}, Theme{})
			if !strings.Contains(got, test.want) {
				t.Fatalf("render missing %q:\n%s", test.want, got)
			}
		})
	}
}

func TestRunWTFDaemonAwareRefreshWritesRequestAndKeepsSelection(t *testing.T) {
	keys := make(chan wtfKeyEvent, 2)
	keys <- wtfKeyEvent{key: kRune, r: 'r'}
	keys <- wtfKeyEvent{key: kEnter}
	requests := 0
	snapshot := wtfSnapshot{Sessions: []wtfSession{{Agent: AgentClaude, ID: "selected", Active: true}}}
	chosen, err := runWTFDashboardLoopWithRefresh(wtfUI{Snapshot: snapshot, Width: 100, Height: 20}, nil,
		func(cache map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache) {
			return snapshot, cache
		}, keys,
		func(wtfUI) error { return nil }, func() error { requests++; return nil }, nil)
	if err != nil || requests != 1 || chosen == nil || chosen.ID != "selected" {
		t.Fatalf("chosen=%+v requests=%d err=%v", chosen, requests, err)
	}
}

func TestWTFDashboardLoopHandlesKeysWhileCollectionBlocked(t *testing.T) {
	keys := make(chan wtfKeyEvent, 2)
	renders := make(chan wtfUI, 3)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	collect := func(map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache) {
		once.Do(func() { close(started) })
		<-release
		return wtfSnapshot{}, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ui := wtfUI{Width: 80, Height: 20, Snapshot: wtfSnapshot{Sessions: []wtfSession{
			{Agent: AgentClaude, ID: "one", Active: true},
			{Agent: AgentAmp, ID: "two", Active: true},
		}}}
		_, _ = runWTFDashboardLoop(ui, nil, collect, keys, func(ui wtfUI) error {
			renders <- ui
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collection did not start")
	}
	<-renders
	keys <- wtfKeyEvent{key: kDown}
	if ui := <-renders; ui.Cursor != 1 {
		t.Fatalf("down cursor while collecting = %d, want 1", ui.Cursor)
	}
	keys <- wtfKeyEvent{key: kRune, r: 'q'}
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("q was blocked by collection")
	}
	close(release)
}

func TestWTFDashboardLoopHandlesEscapeWhileCollectionBlocked(t *testing.T) {
	keys := make(chan wtfKeyEvent, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runWTFDashboardLoop(wtfUI{Width: 80, Height: 20}, nil, func(cache map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache) {
			<-release
			return wtfSnapshot{}, cache
		}, keys, func(wtfUI) error { return nil })
	}()
	keys <- wtfKeyEvent{key: kEsc}
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Escape was blocked by collection")
	}
	close(release)
}

func TestWTFDashboardLoopStartsWithLoadedCache(t *testing.T) {
	want := map[string]wtfSummaryCache{"claude:one": {InputHash: "persisted"}}
	seen := make(chan map[string]wtfSummaryCache, 1)
	keys := make(chan wtfKeyEvent, 1)
	keys <- wtfKeyEvent{key: kEsc}

	_, err := runWTFDashboardLoop(wtfUI{Width: 80, Height: 20}, want, func(cache map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache) {
		seen <- cache
		return wtfSnapshot{}, cache
	}, keys, func(wtfUI) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		if got["claude:one"].InputHash != "persisted" {
			t.Fatalf("initial cache = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not receive initial cache")
	}
}

func TestWTFRefreshWorkerStopsWhileWaitingForRequest(t *testing.T) {
	keys := make(chan wtfKeyEvent, 1)
	renders := make(chan struct{}, 2)
	exited := make(chan struct{})
	loopDone := make(chan error, 1)
	go func() {
		_, err := runWTFDashboardLoopWithWorkerExit(wtfUI{Width: 80, Height: 20}, nil, func(cache map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache) {
			return wtfSnapshot{}, cache
		}, keys, func(wtfUI) error {
			renders <- struct{}{}
			return nil
		}, exited)
		loopDone <- err
	}()
	<-renders // initial state
	<-renders // first refresh completed; worker is idle again
	keys <- wtfKeyEvent{key: kEsc}
	if err := <-loopDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("refresh worker remained blocked waiting for a request")
	}
}

type timedWTFReader struct {
	reads int
}

func (r *timedWTFReader) Read([]byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		return 0, io.EOF
	}
	return 0, io.ErrClosedPipe
}

func TestWTFKeyReaderPublishesResizeOnTimedZeroByteRead(t *testing.T) {
	reader := &timedWTFReader{}
	events := make(chan wtfKeyEvent, 2)
	done := make(chan struct{})
	defer close(done)
	readWTFKeyEvents(reader, func() (int, int) {
		return 100, 30
	}, 80, 20, events, done)

	select {
	case event := <-events:
		if event.key != kNone || event.width != 100 || event.height != 30 || event.err != nil {
			t.Fatalf("resize event = %+v", event)
		}
	default:
		t.Fatal("timed zero-byte read did not publish changed dimensions")
	}
}

func TestRenderWTFViewportKeepsHeadersOrderedAndNonSelectable(t *testing.T) {
	ui := wtfUI{Snapshot: testWTFSnapshot(), Cursor: 2, Width: 120, Height: 40}
	got := renderWTF(ui, Theme{})
	if strings.Count(got, "▸ ") != 1 || !strings.Contains(got, "▸ A  new-fix") {
		t.Fatalf("selection must mark exactly one session row:\n%s", got)
	}
	for _, pair := range [][2]string{{"Now", "org/api"}, {"org/api", "api-work"}, {"Recently stopped", "org/new"}, {"org/new", "new-fix"}} {
		if strings.Index(got, pair[0]) < 0 || strings.Index(got, pair[0]) >= strings.Index(got, pair[1]) {
			t.Fatalf("%q should precede %q:\n%s", pair[0], pair[1], got)
		}
	}
}
