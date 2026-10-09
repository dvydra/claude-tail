package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
