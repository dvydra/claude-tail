package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtractTrailEvidence(t *testing.T) {
	tests := []struct {
		name, text, current string
		known               []string
		wantKey             string
		wantResolved        bool
	}{
		{"url", "see https://entire.io/gh/acme/api/trails/12", "x/y", nil, "acme/api#12", true},
		{"qualified", "acme/api#13", "x/y", nil, "acme/api#13", true},
		{"repo shorthand current", "api#14", "acme/api", nil, "acme/api#14", true},
		{"repo shorthand unique known", "api#15", "", []string{"acme/api"}, "acme/api#15", true},
		{"repo shorthand ambiguous", "api#16", "", []string{"acme/api", "other/api"}, "", false},
		{"bare current", "trail #17", "acme/api", nil, "acme/api#17", true},
		{"bare no current", "trail 18", "", []string{"acme/api"}, "", false},
		{"ordinary hash", "color #123", "acme/api", nil, "", false},
		{"email boundary", "xapi#19@example.com", "acme/api", nil, "", false},
		{"url boundary", "xhttps://entire.io/gh/acme/api/trails/20", "", nil, "", false},
		{"numeric boundary", "acme/api#21x", "", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTrailEvidence([]trailTextEvent{{At: 42, Source: "user", Text: tt.text}}, trailContext{CurrentRepo: tt.current, KnownRepos: tt.known})
			if tt.wantKey == "" && tt.wantResolved && len(got) == 0 {
				t.Fatal("expected evidence")
			}
			if !tt.wantResolved && tt.wantKey == "" && tt.name != "repo shorthand ambiguous" && tt.name != "bare no current" {
				if len(got) != 0 {
					t.Fatalf("got false-positive evidence: %#v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d evidence entries, want 1: %#v", len(got), got)
			}
			if got[0].Key != tt.wantKey || got[0].Resolved != tt.wantResolved {
				t.Fatalf("got key=%q resolved=%v, want key=%q resolved=%v", got[0].Key, got[0].Resolved, tt.wantKey, tt.wantResolved)
			}
			if got[0].Matched == "" || got[0].Source != "user" || got[0].At != 42 || got[0].Resolution == "" {
				t.Fatalf("evidence metadata not preserved: %#v", got[0])
			}
		})
	}
}

func TestExtractTrailEvidenceDeduplicatesByEarliestTimestamp(t *testing.T) {
	got := extractTrailEvidence([]trailTextEvent{
		{At: 200, Source: "assistant", Text: "ACME/API#7"},
		{At: 100, Source: "user", Text: "https://entire.io/gh/acme/api/trails/7"},
	}, trailContext{})
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].At != 100 || got[0].Source != "user" || got[0].Matched != "https://entire.io/gh/acme/api/trails/7" {
		t.Fatalf("did not preserve earliest evidence: %#v", got[0])
	}
}

func TestClaudeTrailEvents(t *testing.T) {
	got := claudeTrailEvents(filepath.Join("testdata", "wtf", "claude-trails.jsonl"), 999)
	want := []trailTextEvent{
		{At: 1790672400, Source: "user", Text: "user acme/api#1"},
		{At: 1790672401, Source: "assistant", Text: "assistant acme/api#2"},
		{At: 1790672402, Source: "tool input", Text: "input acme/api#3"},
		{At: 1790672403, Source: "tool result", Text: "result acme/api#4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestAmpTrailEvents(t *testing.T) {
	data := mustReadFile(t, filepath.Join("testdata", "wtf", "amp-trails.json"))
	var export ampExport
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatal(err)
	}
	got := ampTrailEvents(export, 999)
	want := []trailTextEvent{
		{At: 1790672400, Source: "user", Text: "user acme/api#5"},
		{At: 1790672401, Source: "assistant", Text: "assistant acme/api#6"},
		{At: 1790672402, Source: "tool input", Text: "input acme/api#7"},
		{At: 1790672403, Source: "tool result", Text: "result acme/api#8"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n got: %#v\nwant: %#v", got, want)
	}
}
