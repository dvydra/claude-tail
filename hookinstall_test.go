package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const fixtureSettings = `{
  "permissions": {"allow": ["Read"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/existing/pre.sh"}]}
    ]
  }
}`

func TestMergeHooksPreservesExistingAndAddsOurs(t *testing.T) {
	out, err := mergeHooks([]byte(fixtureSettings), "/opt/et/entire-tail-pending.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !hasHookInstalled(out, "/opt/et/entire-tail-pending.sh") {
		t.Fatal("our hook must be present after merge")
	}
	if !strings.Contains(string(out), "/existing/pre.sh") {
		t.Fatal("existing hook must be preserved")
	}
	// idempotent
	out2, _ := mergeHooks(out, "/opt/et/entire-tail-pending.sh")
	if countOccur(out2, "entire-tail-pending.sh") != countOccur(out, "entire-tail-pending.sh") {
		t.Fatal("merge must be idempotent")
	}
	// valid JSON
	var v map[string]any
	if json.Unmarshal(out, &v) != nil {
		t.Fatal("output must be valid JSON")
	}
}

func TestMergeHooksHandlesNullSettings(t *testing.T) {
	out, err := mergeHooks([]byte("null"), "/opt/et/entire-tail-pending.sh")
	if err != nil {
		t.Fatalf("null settings should merge cleanly, got err: %v", err)
	}
	if !hasHookInstalled(out, "/opt/et/entire-tail-pending.sh") {
		t.Fatal("our hook must be present after merging into null settings")
	}
}

func TestUnmergeHooksRemovesOnlyOurs(t *testing.T) {
	merged, _ := mergeHooks([]byte(fixtureSettings), "/opt/et/entire-tail-pending.sh")
	out, err := unmergeHooks(merged, "/opt/et/entire-tail-pending.sh")
	if err != nil {
		t.Fatal(err)
	}
	if hasHookInstalled(out, "/opt/et/entire-tail-pending.sh") {
		t.Fatal("our hook must be gone")
	}
	if !strings.Contains(string(out), "/existing/pre.sh") {
		t.Fatal("existing hook must survive unmerge")
	}
}

func countOccur(b []byte, sub string) int { return strings.Count(string(b), sub) }

func TestShouldOfferHookInstall(t *testing.T) {
	base := hookOfferInputs{isTTY: true, isClaude: true}
	if !shouldOfferHookInstall(base) {
		t.Fatal("clean interactive Claude run should offer")
	}
	cases := []struct {
		name  string
		mut   func(*hookOfferInputs)
	}{
		{"not tty", func(i *hookOfferInputs) { i.isTTY = false }},
		{"adopted", func(i *hookOfferInputs) { i.adopted = true }},
		{"already installed", func(i *hookOfferInputs) { i.alreadyInstalled = true }},
		{"choice recorded", func(i *hookOfferInputs) { i.choiceRecorded = true }},
		{"--no-hook-install", func(i *hookOfferInputs) { i.noHookInstall = true }},
		{"--follow-session", func(i *hookOfferInputs) { i.followSession = true }},
		{"--no-pick", func(i *hookOfferInputs) { i.noPick = true }},
		{"not claude", func(i *hookOfferInputs) { i.isClaude = false }},
	}
	for _, c := range cases {
		in := base
		c.mut(&in)
		if shouldOfferHookInstall(in) {
			t.Errorf("%s: must NOT offer", c.name)
		}
	}
}
