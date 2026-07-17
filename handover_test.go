package main

import (
	"strings"
	"testing"
	"time"
)

func TestLocalMidnight(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 14, 5, 0, 0, loc).Unix()
	want := time.Date(2026, 7, 17, 0, 0, 0, 0, loc).Unix()
	if got := localMidnight(now, loc); got != want {
		t.Fatalf("localMidnight = %d, want %d", got, want)
	}
}

func TestFlattenTodayKeepsTodayLocalOnly(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, loc).Unix()
	mid := localMidnight(now, loc)
	yesterday := mid - 3600
	today := mid + 3600
	tree := sessionTree{Home: "/h", Folders: []treeFolder{{
		Cwd: "/h/src/repo", Dir: "/h/src/repo",
		Sessions: []treeSession{
			{ID: "a", Path: "/p/a.jsonl", Mtime: today, cwd: "/h/src/repo", Snippet: "work", Live: true},
			{ID: "b", Path: "/p/b.jsonl", Mtime: yesterday, cwd: "/h/src/repo"},
			{ID: "c", Path: "", Mtime: today, cwd: "/h/src/repo"}, // cloud-only: no transcript
		},
	}}}
	got := flattenToday(tree, mid, "/h")
	if len(got) != 1 || got[0].SessionID != "a" {
		t.Fatalf("flattenToday = %+v, want only session a", got)
	}
	if !got[0].Live {
		t.Fatalf("expected session a live")
	}
}

func TestUpdateHandoverPickTagging(t *testing.T) {
	ui := handoverPickUI{Items: make([]handoverItem, 3), Tags: allIndependent(3), Height: 10}
	ui = updateHandoverPick(ui, kRune, '2') // tag row 0 → group 2
	ui = updateHandoverPick(ui, kDown, 0)
	ui = updateHandoverPick(ui, kRune, '2') // tag row 1 → group 2
	ui = updateHandoverPick(ui, kDown, 0)
	ui = updateHandoverPick(ui, kRune, '-') // exclude row 2
	if ui.Tags[0] != 2 || ui.Tags[1] != 2 || ui.Tags[2] != tagExcluded {
		t.Fatalf("tags = %v", ui.Tags)
	}
	ui = updateHandoverPick(ui, kRune, 'x') // row 2 back to independent (cursor still 2)
	if ui.Tags[2] != tagIndependent {
		t.Fatalf("x did not reset, tags = %v", ui.Tags)
	}
}

func TestUpdateHandoverPickDoneQuit(t *testing.T) {
	ui := handoverPickUI{Items: make([]handoverItem, 1), Tags: allIndependent(1), Height: 10}
	if updateHandoverPick(ui, kEnter, 0).Done != true {
		t.Fatal("Enter should set Done")
	}
	if updateHandoverPick(ui, kRune, 'q').Quit != true {
		t.Fatal("q should set Quit")
	}
	if updateHandoverPick(ui, kEsc, 0).Quit != true {
		t.Fatal("Esc should set Quit")
	}
}

func TestBuildGroups(t *testing.T) {
	items := []handoverItem{
		{SessionID: "solo1", Repo: "o/a"},
		{SessionID: "ci1", Repo: "o/b"},
		{SessionID: "ci2", Repo: "o/b"},
		{SessionID: "skip", Repo: "o/c"},
	}
	tags := []handoverTag{tagIndependent, 2, 2, tagExcluded}
	got := buildGroups(items, tags)
	if len(got) != 2 {
		t.Fatalf("want 2 groups, got %d: %+v", len(got), got)
	}
	if got[0].GroupID != "solo-1" || len(got[0].Sessions) != 1 || got[0].Sessions[0].SessionID != "solo1" {
		t.Fatalf("group 0 = %+v", got[0])
	}
	if got[1].GroupID != "g2" || len(got[1].Sessions) != 2 {
		t.Fatalf("group 1 = %+v", got[1])
	}
}

func TestManifestSessionFrom(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, loc).Unix()
	it := handoverItem{SessionID: "a", Repo: "o/r", Cwd: "/c", Title: "work", Live: true, LastActivity: now, Tokens: 1000, Path: "/p/a.jsonl"}
	links := []sessionLink{
		{Kind: "trail", URL: "https://entire.io/gh/o/r/trails/t1/"},
		{Kind: "PR", URL: "https://github.com/o/r/pull/9"},
	}
	ms := manifestSessionFrom(it, links)
	if ms.State != "live" || ms.Agent != "claude" {
		t.Fatalf("state/agent = %q/%q", ms.State, ms.Agent)
	}
	if len(ms.TrailUrls) != 1 || ms.TrailUrls[0] != "https://entire.io/gh/o/r/trails/t1/" {
		t.Fatalf("trails = %v", ms.TrailUrls)
	}
	if len(ms.PrUrls) != 1 || ms.PrUrls[0] != "https://github.com/o/r/pull/9" {
		t.Fatalf("prs = %v", ms.PrUrls)
	}
	if ms.EntireSessionIds == nil {
		t.Fatal("EntireSessionIds must be non-nil (empty slice) for stable JSON")
	}
}

func TestManifestSessionFromDropsPlaceholders(t *testing.T) {
	it := handoverItem{SessionID: "a", Path: "/p"}
	links := []sessionLink{
		{Kind: "trail", Owner: "org", Repo: "repo", ID: "N", URL: "https://entire.io/gh/org/repo/trails/N/"},
		{Kind: "trail", Owner: "entirehq", Repo: "entiredb", ID: "812", URL: "https://entire.io/gh/entirehq/entiredb/trails/812/"},
		{Kind: "PR", Owner: "owner", Repo: "repo", ID: "1", URL: "https://github.com/owner/repo/pull/1"},
	}
	ms := manifestSessionFrom(it, links)
	if len(ms.TrailUrls) != 1 || ms.TrailUrls[0] != "https://entire.io/gh/entirehq/entiredb/trails/812/" {
		t.Fatalf("trails = %v (should drop the org/repo/N placeholder)", ms.TrailUrls)
	}
	if len(ms.PrUrls) != 0 {
		t.Fatalf("prs = %v (should drop the owner/repo placeholder)", ms.PrUrls)
	}
}

func TestBuildManifestGroupsThrough(t *testing.T) {
	groups := []handoverGroup{
		{GroupID: "g1", Sessions: []handoverItem{{SessionID: "a", Path: "/p/a"}, {SessionID: "b", Path: "/p/b"}}},
	}
	m := buildManifest(groups, "/vault/Handover/2026-07-17", "2026-07-17", 0,
		func(string) []sessionLink { return nil })
	if len(m.Groups) != 1 || len(m.Groups[0].Sessions) != 2 || m.Groups[0].GroupID != "g1" {
		t.Fatalf("groups = %+v", m.Groups)
	}
	if m.VaultHandoverDir != "/vault/Handover/2026-07-17" || m.Date != "2026-07-17" {
		t.Fatalf("header = %+v", m)
	}
}

func TestHandoverVaultDirDefaultAndEnv(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, loc).Unix()
	def := handoverVaultDir(func(string) string { return "" }, now, loc)
	if def != "/Users/dvydra/Library/Mobile Documents/iCloud~md~obsidian/Documents/Handover/2026-07-17" {
		t.Fatalf("default = %q", def)
	}
	env := handoverVaultDir(func(k string) string {
		if k == "ENTIRE_TAIL_HANDOVER_VAULT" {
			return "/tmp/v"
		}
		return ""
	}, now, loc)
	if env != "/tmp/v/Handover/2026-07-17" {
		t.Fatalf("env = %q", env)
	}
}

func TestRenderHandoverPickShowsTags(t *testing.T) {
	items := []handoverItem{
		{SessionID: "aaaaaaaa1", Repo: "o/a", Title: "first", LastActivity: 1000},
		{SessionID: "bbbbbbbb2", Repo: "o/b", Title: "second", LastActivity: 1000},
	}
	ui := handoverPickUI{Items: items, Tags: []handoverTag{2, tagExcluded}, Cursor: 0, Width: 120, Height: 10, Now: 2000}
	out := renderHandoverPick(ui)
	if !strings.Contains(out, "[2]") {
		t.Fatalf("expected group tag [2] in:\n%s", out)
	}
	if !strings.Contains(out, "[ ]") {
		t.Fatalf("expected excluded tag [ ] in:\n%s", out)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("expected titles in:\n%s", out)
	}
}

func TestComposeHandoverRowLiveMarker(t *testing.T) {
	live := composeHandoverRow(handoverItem{SessionID: "a", Repo: "o/a", Title: "x", Live: true, LastActivity: 1000}, tagIndependent, 2000)
	ended := composeHandoverRow(handoverItem{SessionID: "b", Repo: "o/b", Title: "y", Live: false, LastActivity: 1000}, tagIndependent, 2000)
	if !strings.Contains(live, "●") {
		t.Fatalf("live row missing ● marker: %q", live)
	}
	if !strings.Contains(ended, "○") {
		t.Fatalf("ended row missing ○ marker: %q", ended)
	}
}

func TestComposeHandoverRowPlaceholder(t *testing.T) {
	row := composeHandoverRow(handoverItem{SessionID: "a", Repo: "o/a", Title: "", LastActivity: 1000}, tagIndependent, 2000)
	if !strings.Contains(row, "i to preview") {
		t.Fatalf("empty title should show a preview placeholder: %q", row)
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"<command-message>progress</command-message> <command-name>progress</command-name>": "/progress",
		"<command-name>/goal</command-name> <command-message>goal</command-message>":         "/goal",
		"<local-command-caveat>Caveat: The messages below were generated…</local-command-caveat> stdout junk": "",
		"Caveat: The messages below were generated by the user":                                              "",
		"  fix the flaky aurora monitor test  ":                                                              "fix the flaky aurora monitor test",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultTagsLiveOnly(t *testing.T) {
	items := []handoverItem{{Live: true}, {Live: false}, {Live: true}}
	tags := defaultTags(items)
	if tags[0] != tagIndependent || tags[1] != tagExcluded || tags[2] != tagIndependent {
		t.Fatalf("defaultTags = %v (want [0 -1 0])", tags)
	}
}

func TestFlattenTodaySortsLiveFirst(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, loc).Unix()
	mid := localMidnight(now, loc)
	tree := sessionTree{Home: "/h", Folders: []treeFolder{{
		Cwd: "/h/r", Dir: "/h/r",
		Sessions: []treeSession{
			{ID: "ended-new", Path: "/p/1", Mtime: mid + 5000, cwd: "/h/r"},
			{ID: "live-old", Path: "/p/2", Mtime: mid + 1000, cwd: "/h/r", Live: true},
		},
	}}}
	got := flattenToday(tree, mid, "/h")
	if len(got) != 2 || got[0].SessionID != "live-old" {
		t.Fatalf("expected live-old first, got %+v", got)
	}
}

func TestUpdateHandoverPickPreview(t *testing.T) {
	ui := handoverPickUI{Items: make([]handoverItem, 1), Tags: allIndependent(1), Height: 10}
	if !updateHandoverPick(ui, kRune, 'i').PreviewReq {
		t.Fatal("i should set PreviewReq")
	}
}

func TestRowTag(t *testing.T) {
	if rowTag(tagIndependent) != "[x]" || rowTag(tagExcluded) != "[ ]" || rowTag(3) != "[3]" {
		t.Fatalf("rowTag mapping wrong: %q %q %q", rowTag(tagIndependent), rowTag(tagExcluded), rowTag(3))
	}
}
