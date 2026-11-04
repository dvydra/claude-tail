package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestThreadHandoverSkillMentionsOnlyRealSubcommands(t *testing.T) {
	data, err := os.ReadFile("docs/skills/thread-handover/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "—") {
		t.Error("skill contains an em dash")
	}
	known := map[string]bool{}
	for _, s := range threadsSubcommands {
		known[s] = true
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`entire-tail threads ([a-z]+)`).FindAllStringSubmatch(text, -1) {
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
