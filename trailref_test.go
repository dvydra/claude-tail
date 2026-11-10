package main

import (
	"testing"
	"time"
)

func TestParseTrailRefURL(t *testing.T) {
	cases := []struct {
		url  string
		ok   bool
		want trailRef
	}{
		{"https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10", true, trailRef{trailRefChange, "et", "dvydra", "k3s-config", 10}},
		{"https://entire.com/et/dvydra/k3s-config/-/trails/32/changes/10", true, trailRef{trailRefChange, "et", "dvydra", "k3s-config", 10}},
		{"https://entire.io/gh/entirehq/entiredb/trails/4844", true, trailRef{trailRefChange, "gh", "entirehq", "entiredb", 4844}},
		{"https://entire.com/et/dvydra/-/trails/32", true, trailRef{trailRefProject, "et", "dvydra", "", 32}},
		{"https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10/files", true, trailRef{trailRefChange, "et", "dvydra", "k3s-config", 10}},
		{"https://entire.com/GH/EntireHQ/EntireDB/trails/7", true, trailRef{trailRefChange, "gh", "entirehq", "entiredb", 7}},
		{"http://entire.com/et/dvydra/-/trails/32", false, trailRef{}},
		{"https://github.com/et/dvydra/-/trails/32", false, trailRef{}},
		{"https://entire.com/gl/dvydra/-/trails/32", false, trailRef{}},
		{"https://entire.com/et/dvydra/-/trails/0", false, trailRef{}},
		{"https://entire.com/et/dvydra/-/trails/abc", false, trailRef{}},
		{"https://entire.com/et/dvydra/-/trails", false, trailRef{}},
	}
	for _, c := range cases {
		got, ok := parseTrailRefURL(c.url)
		if ok != c.ok || got != c.want {
			t.Errorf("parseTrailRefURL(%q) = %+v, %v; want %+v, %v", c.url, got, ok, c.want, c.ok)
		}
	}
}

func TestTrailRefKeysNeverCollide(t *testing.T) {
	p := trailRef{Kind: trailRefProject, Forge: "et", Project: "dvydra", Number: 32}
	c := trailRef{Kind: trailRefChange, Forge: "et", Project: "dvydra", Repo: "x", Number: 32}
	if p.Key() == c.Key() {
		t.Fatalf("project and change share key %q", p.Key())
	}
	if p.Key() != "et/dvydra/-/32" || c.Key() != "et/dvydra/x/32" {
		t.Fatalf("keys: %q %q", p.Key(), c.Key())
	}
	if p.Label() != "P32" || c.Label() != "x/32" {
		t.Fatalf("labels: %q %q", p.Label(), c.Label())
	}
	if c.ProjectFlag() != "et/dvydra" {
		t.Fatalf("project flag %q", c.ProjectFlag())
	}
	if _, ok := parseTrailRefURL("https://entire.com/et/dvydra/-/trails/10/changes/-/10"); ok {
		t.Fatalf("a change in repo '-' must be rejected")
	}
}

func TestTrailMentionsInLine(t *testing.T) {
	at := time.Date(2026, 11, 11, 1, 2, 3, 0, time.UTC)
	change := trailRef{trailRefChange, "et", "dvydra", "k3s-config", 10}

	user := []byte(`{"type":"user","timestamp":"2026-11-11T01:02:03.000Z","cwd":"/r","message":{"content":"look at https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10 please"}}`)
	ms := trailMentionsInLine(user)
	if len(ms) != 1 || ms[0].Ref != change || !ms[0].At.Equal(at) || ms[0].Cwd != "/r" {
		t.Fatalf("user url: %+v", ms)
	}

	tool := []byte(`{"type":"assistant","timestamp":"2026-11-11T01:02:03.000Z","cwd":"/r","message":{"content":[{"type":"tool_use","input":{"command":"entire trail finding k3s-config/10 --project et/dvydra"}}]}}`)
	ms = trailMentionsInLine(tool)
	if len(ms) != 1 || ms[0].Ref != change || ms[0].Partial != "" {
		t.Fatalf("tool command: %+v", ms)
	}

	bare := []byte(`{"cwd":"/r","timestamp":"2026-11-11T01:02:03.000Z","x":"entire trail show 32 --json"}`)
	ms = trailMentionsInLine(bare)
	if len(ms) != 1 || ms[0].Partial != "32" || ms[0].Cwd != "/r" || ms[0].Ref != (trailRef{}) {
		t.Fatalf("bare project: %+v", ms)
	}

	co := []byte(`{"x":"entire trail checkout k3s-config/10"}`)
	ms = trailMentionsInLine(co)
	if len(ms) != 1 || ms[0].Partial != "k3s-config/10" {
		t.Fatalf("bare change: %+v", ms)
	}

	for _, l := range []string{`{"x":"entire trail list --project et/dvydra"}`, `{"x":"entire trail create --title y"}`} {
		if ms := trailMentionsInLine([]byte(l)); len(ms) != 0 {
			t.Fatalf("%s: %+v", l, ms)
		}
	}

	twice := []byte(`{"a":"https://entire.com/et/dvydra/-/trails/32 and https://entire.com/et/dvydra/-/trails/32"}`)
	ms = trailMentionsInLine(twice)
	if len(ms) != 1 || !ms[0].At.IsZero() {
		t.Fatalf("dedupe/no timestamp: %+v", ms)
	}
}
