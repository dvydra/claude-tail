package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToSlackMrkdwnInline(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bold", "a **bold** b", "a *bold* b"},
		{"bold underscores", "a __bold__ b", "a *bold* b"},
		{"italic star", "a *soft* b", "a _soft_ b"},
		{"italic underscore stays", "a _soft_ b", "a _soft_ b"},
		{"strike", "a ~~gone~~ b", "a ~gone~ b"},
		{"bold not read as italic", "**x**", "*x*"},
		{"link kept as markdown", "see [the docs](https://x.dev/a) now", "see [the docs](https://x.dev/a) now"},
		{"link label converted", "[**docs**](https://x.dev)", "[*docs*](https://x.dev)"},
		{"link url protected", "[d](https://x.dev/a__b__c)", "[d](https://x.dev/a__b__c)"},
		{"link title dropped", `[d](https://x.dev "t")`, `[d](https://x.dev)`},
		{"quad backticks stay inline", "a ```` ``` ```` b", "a ```` ``` ```` b"},
		{"image", "![alt](https://x.dev/i.png)", "![alt](https://x.dev/i.png)"},
		{"heading", "## Why it broke", "*Why it broke*"},
		{"bullet", "- first", "• first"},
		{"nested bullet", "  - second", "  ◦ second"},
		{"task done", "- [x] shipped", "• ☑ shipped"},
		{"task open", "- [ ] todo", "• :black_square_button: todo"},
		{"numbered untouched", "1. first", "1. first"},
		{"quote untouched", "> quoted", "> quoted"},
		{"arithmetic is not italic", "2 * x * 3", "2 * x * 3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toSlackMrkdwn(c.in); got != c.want {
				t.Errorf("toSlackMrkdwn(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Code is what you paste into a terminal, not into Slack: nothing inside a
// fence or backticks may be rewritten.
func TestToSlackMrkdwnLeavesCodeAlone(t *testing.T) {
	in := "run this:\n\n```sh\ngit log --format=**%h** _x_ | grep -v '~~'\n```\n\nand `*not bold*` inline"
	got := toSlackMrkdwn(in)
	for _, want := range []string{
		"git log --format=**%h** _x_ | grep -v '~~'",
		"`*not bold*`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("code was rewritten, missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "```sh") {
		t.Errorf("language tag should be dropped (mrkdwn ignores it):\n%s", got)
	}
	if n := strings.Count(got, "```"); n != 2 {
		t.Errorf("expected 2 fences, got %d:\n%s", n, got)
	}
}

// Slack needs each ``` on its own line — a one-line ```code``` renders as
// literal backticks, and an earlier version silently dropped the body.
func TestToSlackMrkdwnSplitsOneLineFence(t *testing.T) {
	if got, want := toSlackMrkdwn("```git status```"), "```\ngit status\n```"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Body on the same line as the opening tag still survives.
	if got, want := toSlackMrkdwn("```sh echo hi\nmore\n```"), "```\necho hi\nmore\n```"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// A fence that starts mid-line breaks out onto its own lines too.
	if got, want := toSlackMrkdwn("run: ```kubectl get pods``` now"),
		"run:\n```\nkubectl get pods\n```\nnow"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// And the fences of an ordinary block stay on their own lines.
	got := toSlackMrkdwn("a\n\n```sh\nls -l\n```\n\nb")
	if want := "a\n\n```\nls -l\n```\n\nb"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Slack has no tables — the columns only line up inside a code box.
func TestToSlackMrkdwnFencesTables(t *testing.T) {
	in := "before\n\n| stage | before |\n|-------|--------|\n| plan  | 40s    |\n\nafter"
	want := "before\n\n```\n| stage | before |\n|-------|--------|\n| plan  | 40s    |\n```\n\nafter"
	if got := toSlackMrkdwn(in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// A lone pipe row is prose, not a table.
	if got := toSlackMrkdwn("| not a table |"); got != "| not a table |" {
		t.Errorf("single row got fenced: %q", got)
	}
	// A table inside a fence is left exactly as it is (no double fencing).
	in = "```\n| a | b |\n|---|---|\n```"
	if got := toSlackMrkdwn(in); got != in {
		t.Errorf("table inside a fence was rewritten:\n%s", got)
	}
}

func TestToSlackMrkdwnClosesDanglingFence(t *testing.T) {
	got := toSlackMrkdwn("text\n```\nstill open")
	if n := strings.Count(got, "```"); n != 2 {
		t.Errorf("an unterminated fence must be closed, got %d fences:\n%s", n, got)
	}
}

// The target is the Slack composer, so the API-only forms must NOT appear.
func TestToSlackMrkdwnAvoidsAPIOnlyForms(t *testing.T) {
	got := toSlackMrkdwn("a & b < c > d, see [x](https://y.dev)")
	for _, bad := range []string{"&amp;", "&lt;", "&gt;", "<https://y.dev|"} {
		if strings.Contains(got, bad) {
			t.Errorf("output contains API-only form %q (pastes literally): %s", bad, got)
		}
	}
}

// A golden for the whole fixture in `m` mode — this is what a mouse-drag out of
// the terminal actually yields, so it is worth pinning byte-for-byte.
// Regenerate deliberately with UPDATE_GOLDEN=1 and read the diff.
func TestMrkdwnModeGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "claude_session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	th, err := loadTheme("tokyo-night", "")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	r, err := newRenderer(&b, th, "dots", 5)
	if err != nil {
		t.Fatal(err)
	}
	r.toggleMrkdwn()
	for _, line := range splitLines(data) {
		for _, rec := range normalize(AgentClaude, line, time.UTC) {
			r.emit(rec)
		}
	}
	r.endLine()

	got := stripANSI(b.String())
	golden := filepath.Join("testdata", "claude_mrkdwn.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s:\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

func TestToSlackMrkdwnMultiline(t *testing.T) {
	in := "# Result\n\nIt **works**.\n\n- one\n- two\n"
	want := "*Result*\n\nIt *works*.\n\n• one\n• two\n"
	if got := toSlackMrkdwn(in); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}
