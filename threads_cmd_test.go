package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCmd(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runThreadsCommand(args, root, storeNow, &out, &errOut)
	return code, out.String(), errOut.String()
}

type beginOut struct {
	Base     string `json:"base"`
	Dir      string `json:"dir"`
	ThreadMD string `json:"thread_md"`
}

func mustBegin(t *testing.T, root, slug string) beginOut {
	t.Helper()
	code, out, errOut := runCmd(t, root, "begin", slug)
	if code != 0 {
		t.Fatalf("begin exit %d: %s", code, errOut)
	}
	var b beginOut
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatalf("begin json: %v: %q", err, out)
	}
	return b
}

func writeTmp(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "new.md")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestThreadsCmdNewAndDuplicate(t *testing.T) {
	root := t.TempDir()
	code, out, _ := runCmd(t, root, "new", "Mirror GitHub checks")
	if code != 0 || strings.TrimSpace(out) != "mirror-github-checks" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if code, _, _ := runCmd(t, root, "new", "Mirror GitHub checks"); code != 2 {
		t.Fatalf("duplicate code = %d", code)
	}
}

func TestThreadsCmdCommitRefusesStaleBase(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	b := mustBegin(t, root, slug)
	a := strings.Replace(b.ThreadMD, "## What it is\n", "## What it is\nfrom A\n", 1)
	if code, _, e := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, a)); code != 0 {
		t.Fatalf("commit A = %d %s", code, e)
	}
	bb := strings.Replace(b.ThreadMD, "## What it is\n", "## What it is\nfrom B\n", 1)
	code, out, _ := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, bb))
	if code != 3 {
		t.Fatalf("commit B = %d", code)
	}
	var cur beginOut
	if err := json.Unmarshal([]byte(out), &cur); err != nil {
		t.Fatalf("stdout not begin json: %q", out)
	}
	if !strings.Contains(cur.ThreadMD, "from A") || strings.Contains(cur.ThreadMD, "from B") {
		t.Fatalf("current = %q", cur.ThreadMD)
	}
}

func TestThreadsCmdCommitCannotChangeStatus(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	b := mustBegin(t, root, slug)
	nw := strings.Replace(b.ThreadMD, "status: open", "status: closed", 1)
	nw = strings.Replace(nw, "name: Alpha", "name: Alpha\nneeds_you: true", 1)
	if code, _, e := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, nw)); code != 0 {
		t.Fatalf("commit = %d %s", code, e)
	}
	data, _ := os.ReadFile(filepath.Join(root, slug, "thread.md"))
	meta, _, err := parseThreadMD(data)
	if err != nil || meta.Status != threadOpen || !meta.NeedsYou {
		t.Fatalf("meta = %+v err=%v", meta, err)
	}
}

func TestThreadsCmdCommitRejectsBadFrontmatter(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	b := mustBegin(t, root, slug)
	code, _, errOut := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, "no frontmatter\n"))
	if code != 4 || errOut == "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	data, _ := os.ReadFile(filepath.Join(root, slug, "thread.md"))
	if string(data) != b.ThreadMD {
		t.Fatal("file changed")
	}
}

func TestThreadsCmdLookup(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	if code, out, _ := runCmd(t, root, "lookup", "claude", "s1"); code != 1 || out != "" {
		t.Fatalf("miss code=%d out=%q", code, out)
	}
	if code, _, e := runCmd(t, root, "register", slug, "claude", "s1"); code != 0 {
		t.Fatalf("register %d %s", code, e)
	}
	code, out, _ := runCmd(t, root, "lookup", "claude", "s1")
	if code != 0 || strings.TrimSpace(out) != `{"slug":"alpha"}` {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestThreadsCmdEnabledAndPath(t *testing.T) {
	root := t.TempDir()
	if code, _, _ := runCmd(t, root, "enabled"); code != 1 {
		t.Fatalf("code = %d", code)
	}
	os.WriteFile(filepath.Join(root, ".enabled"), nil, 0o644)
	if code, _, _ := runCmd(t, root, "enabled"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if _, out, _ := runCmd(t, root, "path"); strings.TrimSpace(out) != root {
		t.Fatalf("path = %q", out)
	}
}

func TestThreadsCmdLifecycleAndList(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	if code, _, e := runCmd(t, root, "park", slug, "waiting", "on", "Bob"); code != 0 {
		t.Fatalf("park %d %s", code, e)
	}
	if code, _, _ := runCmd(t, root, "unpark", slug); code != 0 {
		t.Fatal("unpark")
	}
	runCmd(t, root, "close", slug)
	_, out, _ := runCmd(t, root, "list")
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("list = %q", out)
	}
	_, out, _ = runCmd(t, root, "list", "--all")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0]["status"] != "closed" {
		t.Fatalf("all = %q err=%v", out, err)
	}
	if code, _, _ := runCmd(t, root, "reopen", slug); code != 0 {
		t.Fatal("reopen")
	}
	_, out, _ = runCmd(t, root, "list")
	if !strings.Contains(out, `"slug":"alpha"`) {
		t.Fatalf("list = %q", out)
	}
}

func TestThreadsCmdUsage(t *testing.T) {
	root := t.TempDir()
	if code, _, e := runCmd(t, root, "bogus"); code != 64 || e == "" {
		t.Fatalf("code=%d", code)
	}
	if code, _, _ := runCmd(t, root, "park", "x"); code != 64 {
		t.Fatalf("park usage code=%d", code)
	}
}

func TestParseCLIThreads(t *testing.T) {
	c, action, err := parseCLI(commandArgs("/bin/entire-threads", []string{"list"}), func(string) string { return "" }, savedPrefs{})
	if err != nil || action != ActionThreads || strings.Join(c.ThreadsArgs, " ") != "list" {
		t.Fatal(c, action, err)
	}
}
