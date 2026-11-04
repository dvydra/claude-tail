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
	Base          string `json:"base"`
	Dir           string `json:"dir"`
	ThreadMD      string `json:"thread_md"`
	KnowledgeBase string `json:"knowledge_base"`
	KnowledgeMD   string `json:"knowledge_md"`
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

func readT(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestThreadsCmdBeginKnowledge(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	os.WriteFile(filepath.Join(root, slug, "knowledge.md"), []byte("k1\n"), 0o644)
	b := mustBegin(t, root, slug)
	if b.KnowledgeMD != "k1\n" || b.KnowledgeBase != sha256Hex([]byte("k1\n")) {
		t.Fatalf("begin = %+v", b)
	}
	os.Remove(filepath.Join(root, slug, "knowledge.md"))
	if b := mustBegin(t, root, slug); b.KnowledgeBase != "" || b.KnowledgeMD != "" {
		t.Fatalf("missing knowledge: %+v", b)
	}
}

func TestThreadsCmdCommitStaleKnowledgeWritesNothing(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	dir := filepath.Join(root, slug)
	b := mustBegin(t, root, slug)
	os.WriteFile(filepath.Join(dir, "knowledge.md"), []byte("written by B\n"), 0o644)
	nw := strings.Replace(b.ThreadMD, "## What it is\n", "## What it is\nfrom A\n", 1)
	code, out, _ := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, nw),
		"--knowledge", writeTmp(t, "from A\n"), "--knowledge-base", b.KnowledgeBase, "--log", writeTmp(t, "- A did a thing\n"))
	if code != 3 {
		t.Fatalf("code = %d", code)
	}
	var cur beginOut
	if err := json.Unmarshal([]byte(out), &cur); err != nil || cur.KnowledgeMD != "written by B\n" {
		t.Fatalf("stdout = %q err=%v", out, err)
	}
	if readT(t, filepath.Join(dir, "thread.md")) != b.ThreadMD {
		t.Error("thread.md written on a stale knowledge base")
	}
	if readT(t, filepath.Join(dir, "knowledge.md")) != "written by B\n" {
		t.Error("knowledge.md overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, "log", "2026-11-04.md")); !os.IsNotExist(err) {
		t.Error("log written on a stale commit")
	}
}

func TestThreadsCmdCommitAppendsLog(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	dir := filepath.Join(root, slug)
	for _, entry := range []string{"- first\n", "- second"} {
		b := mustBegin(t, root, slug)
		if code, _, e := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, b.ThreadMD), "--log", writeTmp(t, entry)); code != 0 {
			t.Fatalf("commit = %d %s", code, e)
		}
	}
	if got := readT(t, filepath.Join(dir, "log", "2026-11-04.md")); got != "- first\n\n- second\n" {
		t.Fatalf("log = %q", got)
	}
	b := mustBegin(t, root, slug)
	if code, _, e := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, b.ThreadMD), "--log", "2026-10-30="+writeTmp(t, "- old day\n")); code != 0 {
		t.Fatalf("dated commit = %d %s", code, e)
	}
	if got := readT(t, filepath.Join(dir, "log", "2026-10-30.md")); got != "- old day\n" {
		t.Fatalf("dated log = %q", got)
	}
}

func TestThreadsCmdCommitThreadKnowledgeAndLog(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	dir := filepath.Join(root, slug)
	b := mustBegin(t, root, slug)
	nw := strings.Replace(b.ThreadMD, "## What it is\n", "## What it is\nnew\n", 1)
	code, _, e := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, nw),
		"--knowledge", writeTmp(t, "## Decisions\n- d\n"), "--knowledge-base", b.KnowledgeBase, "--log", writeTmp(t, "- did it\n"))
	if code != 0 {
		t.Fatalf("commit = %d %s", code, e)
	}
	if !strings.Contains(readT(t, filepath.Join(dir, "thread.md")), "## What it is\nnew\n") {
		t.Error("thread.md not written")
	}
	if readT(t, filepath.Join(dir, "knowledge.md")) != "## Decisions\n- d\n" {
		t.Error("knowledge.md not written")
	}
	if readT(t, filepath.Join(dir, "log", "2026-11-04.md")) != "- did it\n" {
		t.Error("log not written")
	}
	if code, _, _ := runCmd(t, root, "commit", slug, "--base", "x", "--file", "y", "--knowledge", "z"); code != 64 {
		t.Errorf("--knowledge without --knowledge-base = %d, want 64", code)
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

func TestThreadsCmdListUsesDirName(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Bar")
	p := filepath.Join(root, slug, "thread.md")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "name: Bar", "name: Bar\nslug: foo", 1)), 0o644)
	_, out, _ := runCmd(t, root, "list")
	if !strings.Contains(out, `"slug":"bar"`) || strings.Contains(out, "foo") {
		t.Fatalf("list = %q", out)
	}
}

func TestThreadsCmdCommitPinsSlug(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Bar")
	b := mustBegin(t, root, slug)
	nw := strings.Replace(b.ThreadMD, "name: Bar", "name: Renamed\nslug: foo", 1)
	if code, _, e := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, nw)); code != 0 {
		t.Fatalf("commit = %d %s", code, e)
	}
	data, _ := os.ReadFile(filepath.Join(root, slug, "thread.md"))
	meta, _, _ := parseThreadMD(data)
	if meta.Slug != "bar" || meta.Name != "Renamed" {
		t.Fatalf("meta = %+v", meta)
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
