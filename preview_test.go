package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatTokens(t *testing.T) {
	cases := map[int64]string{
		0:          "",
		500:        "500",
		300_000:    "300k",
		800_000:    "800k",
		1_200_000:  "1.2m",
		2_000_000:  "2m",
		28_620_503: "28.6m",
	}
	for n, want := range cases {
		if got := formatTokens(n); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTruncVisible(t *testing.T) {
	// Truncates on visible columns, keeping color codes intact.
	got := truncVisible("\x1b[31mhello\x1b[0m world", 5)
	if v := stripANSI(got); v != "hello" {
		t.Errorf("visible = %q, want %q", v, "hello")
	}
	// Short strings pass through unchanged.
	if got := truncVisible("hi", 40); got != "hi" {
		t.Errorf("short = %q", got)
	}
	// OSC 8 hyperlink wrappers don't count as visible columns and aren't sliced
	// mid-sequence: the label survives whole when it fits.
	link := osc8("https://github.com/o/r/pull/9", "o/r #9")
	if v := stripANSI(truncVisible(link+" x", 80)); v != "o/r #9 x" {
		t.Errorf("osc8 visible = %q, want %q", v, "o/r #9 x")
	}
	// A URL full of letters must not be treated as visible text and truncated.
	if v := stripANSI(truncVisible(osc8("https://github.com/o/r/pull/9", "ab"), 1)); v != "a" {
		t.Errorf("osc8 truncated visible = %q, want %q", v, "a")
	}
}

func TestSummaryCardLinks(t *testing.T) {
	s := treeSession{ID: "abc123", Repo: "dvydra/claude-tail"}
	links := []sessionLink{
		{Kind: "trail", Owner: "entirehq", Repo: "entiredb", ID: "42", URL: "https://entire.io/gh/entirehq/entiredb/trails/42/"},
		{Kind: "PR", Owner: "dvydra", Repo: "claude-tail", ID: "16", URL: "https://github.com/dvydra/claude-tail/pull/16"},
	}
	out := strings.Join(summaryCardLines(s, aiSummary{}, false, links, 0), "\n")
	if !strings.Contains(out, "trails & prs") {
		t.Error("missing trails & prs header")
	}
	// Labels are built from owner/repo/id; the URL is embedded as an OSC 8 target.
	plain := stripANSI(out)
	if !strings.Contains(plain, "trail  entirehq/entiredb · 42") {
		t.Errorf("trail label missing:\n%s", plain)
	}
	if !strings.Contains(plain, "PR     dvydra/claude-tail #16") {
		t.Errorf("pr label missing:\n%s", plain)
	}
	if !strings.Contains(out, "\x1b]8;;https://github.com/dvydra/claude-tail/pull/16\x1b\\") {
		t.Error("PR OSC 8 hyperlink target missing")
	}
	// No links → no section.
	bare := strings.Join(summaryCardLines(s, aiSummary{}, false, nil, 0), "\n")
	if strings.Contains(bare, "trails & prs") {
		t.Error("empty links should not render the section")
	}
}

func TestExtractLinks(t *testing.T) {
	body := strings.Join([]string{
		`opened https://github.com/dvydra/claude-tail/pull/16 just now`,
		`and again https://github.com/dvydra/claude-tail/pull/16 (dup)`,
		`trail https://entire.io/gh/entirehq/entiredb/trails/42 shipped`,
		`doc placeholder https://entire.io/gh/{owner}/{repo}/trails/{number} ignore me`,
		`wrapped (https://github.com/foo/bar/pull/7).`,
	}, "\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := extractLinks(path)
	if len(got) != 3 {
		t.Fatalf("want 3 links, got %d: %+v", len(got), got)
	}
	// Trails come first, PRs after; the #16 dup collapses to one.
	if got[0].Kind != "trail" || got[0].Owner != "entirehq" || got[0].ID != "42" ||
		got[0].URL != "https://entire.io/gh/entirehq/entiredb/trails/42/" {
		t.Errorf("trail = %+v", got[0])
	}
	if got[1].Kind != "PR" || got[1].URL != "https://github.com/dvydra/claude-tail/pull/16" {
		t.Errorf("pr[0] = %+v", got[1])
	}
	if got[2].ID != "7" || got[2].Owner != "foo" {
		t.Errorf("pr[1] = %+v", got[2])
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("aaaa bbbb cccc dddd", 9)
	for _, line := range got {
		if len(line) > 9 {
			t.Errorf("line over width: %q", line)
		}
	}
	if strings.Join(got, " ") != "aaaa bbbb cccc dddd" {
		t.Errorf("words not preserved: %v", got)
	}
}
