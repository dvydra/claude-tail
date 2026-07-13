package main

import (
	"fmt"
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

func TestSummaryCardMetadataBeforeLinks(t *testing.T) {
	s := treeSession{
		ID: "abc", Repo: "o/r", Mtime: 1_700_000_000,
		Path: "/Users/x/.claude/projects/o/abc.jsonl",
	}
	// 12 PRs → the list is capped at 8 with a "+N more" line.
	var links []sessionLink
	for i := range 12 {
		links = append(links, sessionLink{Kind: "PR", Owner: "o", Repo: "r", ID: fmt.Sprint(i),
			URL: fmt.Sprintf("https://github.com/o/r/pull/%d", i)})
	}
	lines := summaryCardLines(s, aiSummary{}, false, links, 1_700_000_500)
	out := strings.Join(lines, "\n")

	// path + last-updated are present…
	if !strings.Contains(out, "path       /Users/x/.claude/projects/o/abc.jsonl") {
		t.Errorf("path missing:\n%s", out)
	}
	if !strings.Contains(out, "updated    ") {
		t.Errorf("updated missing:\n%s", out)
	}
	// …and appear BEFORE the trails/prs section (survive card clipping).
	idxPath := strings.Index(out, "path       ")
	idxLinks := strings.Index(out, "trails & prs")
	if idxPath < 0 || idxLinks < 0 || idxPath > idxLinks {
		t.Errorf("metadata should precede trails/prs (path@%d links@%d)", idxPath, idxLinks)
	}
	// Link list capped at 8 + a "+4 more" line.
	if got := strings.Count(out, "o/r #"); got != 8 {
		t.Errorf("shown links = %d, want 8", got)
	}
	if !strings.Contains(out, "… +4 more") {
		t.Errorf("missing overflow count:\n%s", out)
	}
}

func TestSplitPaneHeights(t *testing.T) {
	// Roomy terminal: whole card fits, preview gets the rest.
	if c, b := splitPaneHeights(60, 30); c != 30 || b != 27 {
		t.Errorf("splitPaneHeights(60,30) = %d,%d want 30,27", c, b)
	}
	// Tight terminal: card clipped, preview keeps its floor.
	if c, b := splitPaneHeights(20, 40); c != 12 || b != minPreviewRows {
		t.Errorf("splitPaneHeights(20,40) = %d,%d want 12,%d", c, b, minPreviewRows)
	}
	// Preview floor always honored even on a tiny screen.
	if _, b := splitPaneHeights(6, 40); b < 1 {
		t.Errorf("body must stay ≥1, got %d", b)
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
