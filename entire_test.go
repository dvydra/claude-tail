package main

import "testing"

func TestParseEntireTime(t *testing.T) {
	if got := parseEntireTime("2026-07-10T00:00:00Z"); got != 1783641600 {
		t.Errorf("RFC3339 → %d", got)
	}
	if got := parseEntireTime("not a time"); got != 0 {
		t.Errorf("bad time should be 0, got %d", got)
	}
}

func TestBuildEntireTree(t *testing.T) {
	now := parseEntireTime("2026-07-10T00:00:05Z")
	sessions := []entireSession{
		{SessionID: "a", DisplayName: "Alpha task", Repo: "org/one", LastActivityAt: "2026-07-10T00:00:00Z", CheckpointCount: 3},
		{SessionID: "b", DisplayName: "Bravo", Repo: "org/one", LastActivityAt: "2026-07-08T00:00:00Z"},
		{SessionID: "c", CustomName: "Custom name", DisplayName: "ignored", Repo: "org/two", LastActivityAt: "2026-07-07T00:00:00Z"},
		{SessionID: "old", DisplayName: "Ancient", Repo: "org/one", LastActivityAt: "2026-01-01T00:00:00Z"},
	}
	localIDs := map[string]string{"a": "/claude/a.jsonl"} // only 'a' is on this machine
	tree := buildEntireTree(sessions, localIDs, 7, now)

	if len(tree.Folders) != 2 {
		t.Fatalf("want 2 repo folders (7d window drops 'old'), got %d", len(tree.Folders))
	}
	// org/one has a live session → sorts first.
	one := tree.Folders[0]
	if one.Cwd != "org/one" {
		t.Fatalf("first folder = %q, want org/one", one.Cwd)
	}
	if !one.Expanded {
		t.Error("top folder should start expanded")
	}
	if len(one.Sessions) != 2 {
		t.Fatalf("org/one should have 2 in-window sessions, got %d", len(one.Sessions))
	}
	a := one.Sessions[0]
	if a.ID != "a" || a.Snippet != "Alpha task" {
		t.Errorf("newest session = %+v", a)
	}
	if a.Path != "/claude/a.jsonl" {
		t.Errorf("local session should resolve its path, got %q", a.Path)
	}
	if !a.Live {
		t.Error("session active 5s ago should be live")
	}
	if a.Msgs != 3 {
		t.Errorf("checkpoint count → Msgs = %d, want 3", a.Msgs)
	}
	if one.Live < 1 {
		t.Error("folder with a live session should have Live > 0")
	}

	two := tree.Folders[1]
	c := two.Sessions[0]
	if c.Snippet != "Custom name" {
		t.Errorf("customName should win over displayName, got %q", c.Snippet)
	}
	if c.Path != "" {
		t.Errorf("cloud-only session should have empty Path, got %q", c.Path)
	}
}

func TestBuildEntireTreeEdgeCases(t *testing.T) {
	now := parseEntireTime("2026-07-10T00:00:00Z")
	sessions := []entireSession{
		{SessionID: "good", DisplayName: "Good", Repo: "r", LastActivityAt: "2026-07-09T00:00:00Z"},
		{SessionID: "badts", DisplayName: "Bad timestamp", Repo: "r", LastActivityAt: "garbage"},
		{SessionID: "", DisplayName: "No id", Repo: "r", LastActivityAt: "2026-07-09T00:00:00Z"},
	}
	tree := buildEntireTree(sessions, nil, 7, now)
	if len(tree.Folders) != 1 {
		t.Fatalf("want 1 folder, got %d", len(tree.Folders))
	}
	ids := map[string]bool{}
	for _, s := range tree.Folders[0].Sessions {
		ids[s.ID] = true
	}
	if !ids["good"] {
		t.Error("in-window session dropped")
	}
	if !ids["badts"] {
		t.Error("session with unparseable timestamp must not be silently dropped by the window")
	}
	if ids[""] {
		t.Error("session with empty id should be skipped")
	}
	if n := len(tree.Folders[0].Sessions); n != 2 {
		t.Errorf("want 2 sessions (good + badts), got %d", n)
	}
}

func TestBuildEntireTreeUncapped(t *testing.T) {
	now := parseEntireTime("2026-07-10T00:00:00Z")
	sessions := []entireSession{
		{SessionID: "old", DisplayName: "Ancient", Repo: "org/one", LastActivityAt: "2020-01-01T00:00:00Z"},
	}
	if got := buildEntireTree(sessions, nil, 0, now); len(got.Folders) != 1 {
		t.Fatalf("days=0 should keep everything, got %d folders", len(got.Folders))
	}
}
