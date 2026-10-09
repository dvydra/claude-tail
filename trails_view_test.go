package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func trailsUIFixture() (trailsUI, time.Time) {
	now := time.Date(2026, 10, 9, 14, 0, 0, 0, time.Local)
	c := newTrailsCatalog()
	c.UpdatedAt = now.Unix()
	c.Sessions["claude:one"] = wtfSession{Agent: AgentClaude, ID: "one", Name: "Fix auth", Repo: "et/acme/api", Branch: "auth", Active: true, State: "busy"}
	c.Sessions["amp:two"] = wtfSession{Agent: AgentAmp, ID: "two", Name: "Review auth", Repo: "et/acme/api", Branch: "auth"}
	u := "https://entire.io/et/acme/api/trails/7"
	c.Trails[u] = trailsEntry{URL: u, Repo: "et/acme/api", Title: "Fix login", Branch: "auth", Status: "open", Associations: map[string]trailsAssociation{"claude:one": {LastAt: now.Unix() - 30, Branch: "auth", BranchMatched: true}, "amp:two": {LastAt: now.Unix() - 90}}}
	return trailsUI{Catalog: c, Width: 80, Height: 24}, now
}

func TestTrailsUISearchAndNavigation(t *testing.T) {
	ui, now := trailsUIFixture()
	ui, _ = updateTrails(ui, kNone, 0, now)
	selected := ui.SelectedKey
	ui, _ = updateTrails(ui, kRune, '/', now)
	for _, r := range "LOGIN" {
		ui, _ = updateTrails(ui, kRune, r, now)
	}
	if !strings.Contains(renderTrails(ui, now), "Fix login") || ui.SelectedKey != selected {
		t.Fatal("lost search result")
	}
	ui, _ = updateTrails(ui, kEnter, 0, now)
	ui, action := updateTrails(ui, kEnter, 0, now)
	if action.OpenURL != selected {
		t.Fatal(action)
	}
	ui, _ = updateTrails(ui, kRune, 's', now)
	if len(ui.SessionKeys) != 2 {
		t.Fatal("no session choice")
	}
	ui, _ = updateTrails(ui, kDown, 0, now)
	ui, action = updateTrails(ui, kEnter, 0, now)
	if action.SessionKey != "amp:two" || ui.SelectedKey != selected || ui.Query != "LOGIN" {
		t.Fatal(ui, action)
	}
	ui, _ = updateTrails(ui, kRune, '/', now)
	ui, _ = updateTrails(ui, kRune, 'z', now)
	if !strings.Contains(renderTrails(ui, now), "No matches") {
		t.Fatal("missing no-match state")
	}
}

func TestTrailsRenderBoundsAndUntrustedText(t *testing.T) {
	ui, now := trailsUIFixture()
	for key, tr := range ui.Catalog.Trails {
		tr.Title = "Fix\x1b[2J\nmalicious"
		ui.Catalog.Trails[key] = tr
	}
	ui.Width, ui.Height = 40, 12
	ui, _ = updateTrails(ui, kNone, 0, now)
	out := renderTrails(ui, now)
	if !strings.Contains(out, "q quit") {
		t.Fatal("narrow view hides quit hint")
	}
	if strings.Contains(out, "\x1b[2J") || strings.Count(out, "\n") >= 12 {
		t.Fatalf("unsafe/overflow: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if visWidth(line) > 40 {
			t.Fatalf("wide line %q", line)
		}
	}
}

func TestTrailsDispatch(t *testing.T) {
	c, action, err := parseCLI(commandArgs("/bin/entire-trails", []string{"status"}), func(string) string { return "" }, savedPrefs{})
	if err != nil || action != ActionTrails || strings.Join(c.TrailsArgs, " ") != "status" {
		t.Fatal(c, action, err)
	}
}

func TestTrailsSessionAvailability(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	s := wtfSession{Agent: AgentClaude, Transcript: p}
	if _, err := trailsTailArgs(s); err == nil {
		t.Fatal("accepted missing transcript")
	}
	if err := os.WriteFile(p, nil, 0600); err != nil {
		t.Fatal(err)
	}
	args, err := trailsTailArgs(s)
	if err != nil || args[len(args)-1] != p || !strings.Contains(strings.Join(args, " "), "--no-hook-install --no-pane-link") {
		t.Fatal(args, err)
	}
	if _, err := trailsTailArgs(wtfSession{Agent: AgentAmp, ID: "invalid"}); err == nil {
		t.Fatal("accepted invalid thread")
	}
}
