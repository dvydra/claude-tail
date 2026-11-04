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

func TestThreadSkillsCommitKnowledgeAndLog(t *testing.T) {
	for _, p := range []string{"docs/skills/thread-handover/SKILL.md", "docs/skills/threads-bootstrap/SKILL.md"} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, flag := range []string{"--knowledge <", "--knowledge-base <knowledge_base>", "--log <"} {
			if !strings.Contains(string(data), flag) {
				t.Errorf("%s does not pass %s to commit", p, flag)
			}
		}
	}
}

func TestHandoverEntryPointsRouteToThreads(t *testing.T) {
	for _, p := range []string{"docs/skills/handover-command.md", "docs/skills/amp-handover-skill.md"} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "0. Run `entire-tail threads enabled`. If it exits 0, load and follow the `thread-handover` skill") {
			t.Errorf("%s has no threads step 0", p)
		}
	}
}
