package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestThreadHandoverSkillMentionsOnlyRealSubcommands(t *testing.T) {
	known := map[string]bool{}
	for _, s := range threadsSubcommands {
		known[s] = true
	}
	mention := regexp.MustCompile(`entire-tail threads ([a-z]+)`)
	data, err := os.ReadFile("docs/skills/threads-bootstrap/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "—") {
		t.Error("bootstrap skill contains an em dash")
	}
	for _, m := range mention.FindAllStringSubmatch(string(data), -1) {
		if !known[m[1]] {
			t.Errorf("bootstrap skill mentions unknown subcommand %q", m[1])
		}
	}
	data, err = os.ReadFile("docs/skills/thread-handover/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "—") {
		t.Error("skill contains an em dash")
	}
	seen := map[string]bool{}
	for _, m := range mention.FindAllStringSubmatch(text, -1) {
		if !known[m[1]] {
			t.Errorf("skill mentions unknown subcommand %q", m[1])
		}
		seen[m[1]] = true
	}
	for _, w := range []string{"lookup", "list", "new", "register", "begin", "commit", "unpark"} {
		if !seen[w] {
			t.Errorf("skill never mentions %q", w)
		}
	}
}
