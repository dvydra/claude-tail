package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanTrailsAssociations(t *testing.T) {
	now := time.Unix(1800000000, 0)
	url := "https://entire.io/et/acme/api/trails/7"
	sessions := []wtfSession{{Agent: AgentClaude, ID: "one", Repo: "et/acme/api", Branch: "fix", Active: true, LastActivity: now.Unix() - 10}, {Agent: AgentAmp, ID: "two", Repo: "gh/other/api", Branch: "fix", LastActivity: now.Unix() - 5}}
	fail := false
	d := trailsScanDeps{Now: func() time.Time { return now },
		Inventory: func(context.Context) ([]wtfSession, error) { return sessions, nil },
		Observe: func(_ context.Context, s wtfSession, c trailsCursor) ([]trailsObservation, trailsCursor, error) {
			return []trailsObservation{{URL: url, Source: "assistant", At: 1800000000 - 20}}, c, nil
		},
		Lookup: func(_ context.Context, repo, selector string, branch bool) (trailsEntry, error) {
			if fail || repo != "et/acme/api" {
				return trailsEntry{}, errors.New("offline")
			}
			return trailsEntry{URL: url, Repo: repo, Branch: "fix", Title: "Fix auth", Status: "open"}, nil
		},
	}
	c, _ := scanTrails(context.Background(), newTrailsCatalog(), d)
	tr := c.Trails[url]
	if len(tr.Associations) != 2 || !tr.Associations["claude:one"].BranchMatched || tr.Associations["amp:two"].BranchMatched {
		t.Fatalf("%+v", tr)
	}
	old := tr.Associations["claude:one"].LastAt
	sessions[0].Branch = "another"
	sessions[0].LastActivity += 100
	now = now.Add(20 * time.Minute)
	fail = true
	c, _ = scanTrails(context.Background(), c, d)
	if c.Trails[url].Associations["claude:one"].LastAt != old {
		t.Fatal("unrelated activity revived trail")
	}
	if c.Trails[url].Title != "Fix auth" || c.Trails[url].MetadataError == "" {
		t.Fatal("lost last-good metadata")
	}
	sessions = nil
	c, _ = scanTrails(context.Background(), c, d)
	if len(c.Sessions) != 2 || c.Sessions["claude:one"].Active {
		t.Fatal("lost stopped session")
	}
}

func TestScanTrailsDegradedInventoryPreservesSessions(t *testing.T) {
	ui, now := trailsUIFixture()
	d := trailsScanDeps{Now: func() time.Time { return now },
		Inventory: func(context.Context) ([]wtfSession, error) { return nil, errors.New("offline") },
		Lookup: func(context.Context, string, string, bool) (trailsEntry, error) {
			return trailsEntry{}, errTrailsDeferred
		},
	}
	c, err := scanTrails(context.Background(), ui.Catalog, d)
	if err != nil || len(c.Errors) != 1 || !c.Sessions["claude:one"].Active || len(selectTrails(c, now, "")) != 1 {
		t.Fatalf("lost degraded snapshot: %+v %v", c, err)
	}
	d.Inventory = func(context.Context) ([]wtfSession, error) { return nil, nil }
	c, err = scanTrails(context.Background(), c, d)
	if err != nil || len(c.Errors) != 0 || c.Sessions["claude:one"].Active {
		t.Fatal("inventory recovery failed", err)
	}
}

func TestTrailsObserveIgnoresNonHumanSources(t *testing.T) {
	for _, line := range []string{
		`{"type":"user","isMeta":true,"message":{"content":"https://entire.io/et/acme/api/trails/7"}}`,
		`{"type":"assistant","isSidechain":true,"message":{"content":"https://entire.io/et/acme/api/trails/7"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"url":"https://entire.io/et/acme/api/trails/7"}}]}}`,
	} {
		c := trailsCursor{}
		if got := trailsLine(AgentClaude, []byte(line), &c); len(got) != 0 {
			t.Fatalf("accepted injected/tool content: %+v", got)
		}
	}
}

func TestTrailsObservePartialAndReplacement(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "session.jsonl")
	line := `{"type":"assistant","timestamp":"2026-10-09T02:00:00Z","message":{"content":[{"type":"text","text":"https://entire.io/gh/acme/api/trails/42"}]}}` + "\n"
	s := wtfSession{Agent: AgentClaude, Transcript: p, LastActivity: 1}
	os.WriteFile(p, []byte(line[:50]), 0600)
	a, cursor, err := observeTrailsFile(s, p, trailsCursor{})
	if err != nil || len(a) != 0 || cursor.Offset != 0 {
		t.Fatalf("partial: %v %+v", err, cursor)
	}
	os.WriteFile(p, []byte(line), 0600)
	a, cursor, err = observeTrailsFile(s, p, cursor)
	if err != nil || len(a) != 1 || a[0].At != 1791511200 {
		t.Fatalf("complete: %+v %v", a, err)
	}
	a, cursor, err = observeTrailsFile(s, p, cursor)
	if err != nil || len(a) != 0 {
		t.Fatal("replayed unchanged file")
	}
	os.Rename(p, p+".old")
	os.WriteFile(p, []byte(line), 0600)
	a, _, err = observeTrailsFile(s, p, cursor)
	if err != nil || len(a) != 1 {
		t.Fatal("replacement skipped")
	}
}

func TestTrailsObserveRewrittenFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	line := `{"type":"user","message":{"content":"https://entire.io/et/acme/api/trails/42"}}` + "\n"
	os.WriteFile(p, []byte(line), 0600)
	s := wtfSession{Agent: AgentClaude, LastActivity: 10}
	_, c, err := observeTrailsFile(s, p, trailsCursor{})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte(strings.ReplaceAll(line, "/42", "/43")), 0600)
	a, _, err := observeTrailsFile(s, p, c)
	if err != nil || len(a) != 1 || !strings.HasSuffix(a[0].URL, "/43") {
		t.Fatalf("rewritten file: %+v %v", a, err)
	}
}

func TestTrailsMetadataRejectsWrongIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	for _, url := range []string{"https://evil.test/et/acme/api/trails/7", "https://entire.io/gh/acme/api/trails/7", "https://entire.io/et/acme/api/trails/8"} {
		data := `{"url":"` + url + `","branch":"fix"}`
		os.WriteFile(filepath.Join(dir, "entire"), []byte("#!/bin/sh\nprintf '%s' '"+data+"'\n"), 0700)
		if _, err := trailsLookup(context.Background(), "et/acme/api", "7", false); err == nil {
			t.Fatal("accepted", url)
		}
	}
}

func TestScanTrailsBranchWithoutMention(t *testing.T) {
	now := time.Now()
	d := trailsScanDeps{Now: func() time.Time { return now }, Inventory: func(context.Context) ([]wtfSession, error) {
		return []wtfSession{{Agent: AgentClaude, ID: "one", Repo: "et/acme/api", Branch: "fix", Active: true, LastActivity: now.Unix()}}, nil
	}, Observe: func(_ context.Context, _ wtfSession, c trailsCursor) ([]trailsObservation, trailsCursor, error) {
		return nil, c, nil
	}, Lookup: func(context.Context, string, string, bool) (trailsEntry, error) {
		return trailsEntry{URL: "https://entire.io/et/acme/api/trails/8", Repo: "et/acme/api", Branch: "fix"}, nil
	}}
	c, err := scanTrails(context.Background(), newTrailsCatalog(), d)
	if err != nil {
		t.Fatal(err)
	}
	rows := selectTrails(c, now, "")
	if len(rows) != 1 || !rows[0].Active {
		t.Fatalf("branch-only: %+v", rows)
	}
}

func TestTrailsObserveAmpCompletion(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "feed.jsonl")
	s := wtfSession{Agent: AgentAmp, LastActivity: 10}
	env := ampEnvelope{Message: ampMessage{Role: "assistant", ProtocolMessageID: "M-1", State: ampMessageState{Type: "streaming"}, Content: []ampBlock{{Type: "text", Text: "https://entire.io/et/acme/api/trails/9"}}}}
	first, _ := json.Marshal(env)
	os.WriteFile(p, append(first, '\n'), 0600)
	a, cursor, err := observeTrailsFile(s, p, trailsCursor{})
	if err != nil || len(a) != 0 {
		t.Fatalf("incomplete: %+v %v", a, err)
	}
	env.Message.State.Type = "complete"
	b, _ := json.Marshal(env)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	f.Write(append(b, '\n'))
	f.Write(append(b, '\n'))
	f.Close()
	a, cursor, err = observeTrailsFile(s, p, cursor)
	if err != nil || len(a) != 1 {
		t.Fatalf("completion %+v %v", a, err)
	}
	encoded, _ := json.Marshal(cursor)
	var restored trailsCursor
	json.Unmarshal(encoded, &restored)
	a, _, err = observeTrailsFile(s, p, restored)
	if err != nil || len(a) != 0 {
		t.Fatal("restart replayed")
	}
}
