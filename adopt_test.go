package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestItermTab(t *testing.T) {
	cases := map[string]string{
		"w0t3p0:656ECDF4-E2C3-4D2B-A0D2-964E582EB5B5": "w0t3",
		"w0t2p0:51874379-E1BA-43C1-8637-4DB126CE0B8A": "w0t2",
		"w12t0p5:xyz": "w12t0",
		"":            "",
		"garbage":     "",
		"t0p0:x":      "", // no window segment
	}
	for in, want := range cases {
		if got := itermTab(in); got != want {
			t.Errorf("itermTab(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePsEnv(t *testing.T) {
	// A representative `ps eww -o command=` line: argv, then space-joined env.
	line := []byte("claude --session-id 25c6a1e0-c6ad-4054-9837-01ca1331eb9e " +
		"TERM_SESSION_ID=w0t2p0:51874379 ITERM_SESSION_ID=w0t2p0:51874379-E1BA-43C1-8637-4DB126CE0B8A __MISE_SESSION=eAH")
	if got := parsePsEnv(line, "ITERM_SESSION_ID"); got != "w0t2p0:51874379-E1BA-43C1-8637-4DB126CE0B8A" {
		t.Errorf("ITERM_SESSION_ID = %q", got)
	}
	if got := parsePsEnv(line, "NOPE"); got != "" {
		t.Errorf("missing key should be empty, got %q", got)
	}
	// A substring of a real key must not match (prefix is anchored on the token).
	if got := parsePsEnv([]byte("x SESSION_ID=nope ITERM_SESSION_ID=yes"), "ITERM_SESSION_ID"); got != "yes" {
		t.Errorf("prefix confusion: got %q", got)
	}
}

func TestScrapeSessionIDArg(t *testing.T) {
	id := "25c6a1e0-c6ad-4054-9837-01ca1331eb9e"
	cases := map[string]string{
		"claude --session-id " + id:        id,
		"claude --resume " + id:            id,
		"claude --session-id=" + id:        id,
		"claude --resume=" + id:            id,
		"claude":                           "",
		"claude --session-id notauuid":     "",
		"claude --session-id":              "", // dangling flag, no value
		"claude --resume " + id + " --foo": id,
	}
	for argv, want := range cases {
		if got := scrapeSessionIDArg(argv); got != want {
			t.Errorf("scrapeSessionIDArg(%q) = %q, want %q", argv, got, want)
		}
	}
}

func TestSiblingPIDs(t *testing.T) {
	procs := []claudeProc{
		{pid: 100, itermID: "w0t3p0:AAA"}, // our tab
		{pid: 200, itermID: "w0t3p1:BBB"}, // our tab, different pane → sibling
		{pid: 300, itermID: "w0t9p0:CCC"}, // different tab
		{pid: 400, itermID: ""},           // not in iTerm
	}
	got := siblingPIDs("w0t3", procs)
	if len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Errorf("siblingPIDs = %v, want [100 200]", got)
	}
	if got := siblingPIDs("", procs); got != nil {
		t.Errorf("empty ownTab should match nothing, got %v", got)
	}
	if got := siblingPIDs("w9t9", procs); got != nil {
		t.Errorf("no matches should be nil, got %v", got)
	}
}

func TestNewestClear(t *testing.T) {
	const s = int64(sessionCloseWindow)
	a := fileStamp{"a.jsonl", 1000 * s}
	b := fileStamp{"b.jsonl", 1000*s - 3*s} // 3 windows older → clear
	c := fileStamp{"c.jsonl", 1000*s - s/2} // half a window older → ambiguous

	if p, clear := newestClear(nil, s); p != "" || clear {
		t.Errorf("empty = (%q,%v), want (\"\",false)", p, clear)
	}
	if p, clear := newestClear([]fileStamp{a}, s); p != "a.jsonl" || !clear {
		t.Errorf("single = (%q,%v), want (a,true)", p, clear)
	}
	if p, clear := newestClear([]fileStamp{b, a}, s); p != "a.jsonl" || !clear {
		t.Errorf("clear-gap = (%q,%v), want (a,true)", p, clear)
	}
	if p, clear := newestClear([]fileStamp{c, a}, s); p != "a.jsonl" || clear {
		t.Errorf("close = (%q,%v), want (a,false)", p, clear)
	}
	// window 0: any newest is "clear" (used as the idle fallback).
	if p, clear := newestClear([]fileStamp{c, a}, 0); p != "a.jsonl" || !clear {
		t.Errorf("window0 = (%q,%v), want (a,true)", p, clear)
	}
}

func TestResolveClaudeSession(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/x/src/repo"
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "25c6a1e0-c6ad-4054-9837-01ca1331eb9e"
	pinned := filepath.Join(dir, id+".jsonl")
	older := filepath.Join(dir, "11111111-1111-1111-1111-111111111111.jsonl")
	for _, p := range []string{pinned, older} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Make `pinned` the OLDER file, so the argv path and the newest-mtime path
	// resolve to different files and each test is meaningful.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(pinned, old, old); err != nil {
		t.Fatal(err)
	}

	// argv carries the id → resolves that exact file, even though it's older.
	if got := resolveClaudeSession(home, cwd, "claude --session-id "+id); got != pinned {
		t.Errorf("argv id: got %q, want %q", got, pinned)
	}
	// bare claude → newest in the project dir (the one without --session-id).
	if got := resolveClaudeSession(home, cwd, "claude"); got != older {
		t.Errorf("bare claude: got %q, want newest %q", got, older)
	}
	// id on the command line but cwd unknown → found by scanning all project dirs.
	if got := resolveClaudeSession(home, "", "claude --resume "+id); got != pinned {
		t.Errorf("id w/o cwd: got %q, want %q", got, pinned)
	}
	// nothing to go on → "".
	if got := resolveClaudeSession(home, "", "claude"); got != "" {
		t.Errorf("no cwd, no id: got %q, want \"\"", got)
	}
	// cwd with no project dir → "".
	if got := resolveClaudeSession(home, "/Users/x/nowhere", "claude"); got != "" {
		t.Errorf("empty project dir: got %q, want \"\"", got)
	}
}
