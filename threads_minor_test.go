package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClosedInRootStaysVisibleAndCloseMovesIt(t *testing.T) {
	root := t.TempDir()
	writeThread(t, root, "half/thread.md", "---\nname: Half closed\nstatus: closed\n---\n")
	ts := loadThreads(root)
	if len(ts) != 1 || ts[0].Meta.Status != threadOpen || !ts[0].ClosedInRoot {
		t.Fatalf("loaded %+v", ts)
	}
	if text, tone := threadListStatus(ts[0], threadFacts{}); text != "marked closed but not moved" || tone != "risk" {
		t.Fatalf("status = %q %q", text, tone)
	}
	if open, _ := visibleThreads(threadsUI{Threads: ts}); len(open) != 1 {
		t.Fatal("closed-in-root thread hidden from triage")
	}
	if err := setThreadStatus(root, "half", threadClosed, "", storeNow); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "half")); !os.IsNotExist(err) {
		t.Fatal("still in root")
	}
	closed := filepath.Join(root, threadsClosedDir, "half")
	if got := readT(t, filepath.Join(closed, "log", "2026-11-04.md")); !strings.Contains(got, "closed") {
		t.Fatalf("log = %q", got)
	}
	if got := loadThreads(filepath.Join(root, threadsClosedDir)); len(got) != 1 || got[0].Meta.Status != threadClosed || got[0].ClosedInRoot {
		t.Fatalf("closed = %+v", got)
	}
}

func TestLoadThreadsFolderDecidesClosed(t *testing.T) {
	root := t.TempDir()
	writeThread(t, root, "_closed/c/thread.md", "---\nname: C\nstatus: open\n---\n")
	if got := loadThreads(filepath.Join(root, threadsClosedDir)); len(got) != 1 || got[0].Meta.Status != threadClosed {
		t.Fatalf("got %+v", got)
	}
}

func TestUnreadableThreadShowsSlug(t *testing.T) {
	root := t.TempDir()
	writeThread(t, root, "broken-one/thread.md", "---\nstatus: [\n---\n")
	if err := os.MkdirAll(filepath.Join(root, "dir-not-file", "thread.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := loadThreads(root)
	if len(ts) != 2 {
		t.Fatalf("a thread.md read error other than not-exist must keep the row: %+v", ts)
	}
	for _, th := range ts {
		if th.ParseErr == nil || threadDisplayName(th) != filepath.Base(th.Dir) {
			t.Errorf("thread %s: err=%v name=%q", th.Dir, th.ParseErr, threadDisplayName(th))
		}
	}
	ui := tvUI()
	ui.Threads = ts
	if out := renderThreads(ui, idRender, nil); !strings.Contains(out, "broken-one") || !strings.Contains(out, "dir-not-file") {
		t.Fatalf("render lacks slugs:\n%s", out)
	}
	_, out, _ := runCmd(t, root, "list")
	if !strings.Contains(out, `"name":"broken-one"`) {
		t.Fatalf("list = %s", out)
	}
}

func TestRegisterSessionLeavesUpdated(t *testing.T) {
	root := t.TempDir()
	writeThread(t, root, "old/thread.md", "---\nname: Old\nstatus: open\nupdated: 2026-10-01\n---\n")
	if err := registerSession(root, "old", "claude", "s1"); err != nil {
		t.Fatal(err)
	}
	if got := loadThreads(root)[0].Meta.Updated; got != "2026-10-01" {
		t.Fatalf("updated = %q", got)
	}
}

func breakThreadFile(t *testing.T, th thread) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(th.Dir, "thread.md"), []byte("---\nstatus: [\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenThreadResumeExecsWhenRegisterFails(t *testing.T) {
	_, execs := stubThreadSeams(t, nil)
	var notes []string
	prev := threadsNote
	threadsNote = func(s string) { notes = append(notes, s) }
	t.Cleanup(func() { threadsNote = prev })
	root, th := openFixture(t)
	th.Meta.Sessions = []threadSessionRef{{Agent: "claude", ID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}}
	breakThreadFile(t, th) // after load, so registerSession fails
	note := openThreadWith(root, "", th, threadFacts{}, nil, "/bin/et", "claude", nil, func() string { return "x" })
	if note != "" || *execs != 1 {
		t.Fatalf("note=%q execs=%d", note, *execs)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "could not record the session") {
		t.Fatalf("notes = %q", notes)
	}
}

func TestOpenThreadFreshStopsWhenRegisterFails(t *testing.T) {
	_, execs := stubThreadSeams(t, nil)
	root, th := openFixture(t)
	breakThreadFile(t, th)
	note := openThreadWith(root, "", th, threadFacts{}, nil, "/bin/et", "claude", nil, func() string { return "11111111-2222-4333-8444-555555555555" })
	if note == "" || *execs != 0 {
		t.Fatalf("note=%q execs=%d", note, *execs)
	}
}

func TestThreadsListClaimJSONKeys(t *testing.T) {
	root := t.TempDir()
	writeThread(t, root, "a/thread.md", "---\nname: A\nstatus: open\nclaims:\n  - kind: worktree\n    path: /w\n    branch: b\n  - kind: trail\n    trail: gh/o/r/1\n    expect: open\n---\n")
	_, out, _ := runCmd(t, root, "list")
	var rows []struct {
		Claims []map[string]string `json:"claims"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("list = %s err=%v", out, err)
	}
	c := rows[0].Claims
	if len(c) != 2 || c[0]["kind"] != "worktree" || c[0]["path"] != "/w" || c[0]["branch"] != "b" || c[1]["trail"] != "gh/o/r/1" || c[1]["expect"] != "open" {
		t.Fatalf("claims = %v", c)
	}
}

func TestThreadsCmdCommitBrokenDiskExit5(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	p := filepath.Join(root, slug, "thread.md")
	if err := os.WriteFile(p, []byte("---\nstatus: [\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := mustBegin(t, root, slug)
	code, _, errOut := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, "---\nname: A\n---\n"))
	if code != 5 || !strings.Contains(errOut, "thread.md on disk is broken; tell Daniel") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if readT(t, p) != "---\nstatus: [\n---\n" {
		t.Fatal("broken file overwritten")
	}
}

func TestThreadsCmdNewCreated(t *testing.T) {
	root := t.TempDir()
	code, out, e := runCmd(t, root, "new", "--created", "2026-10-28", "Old", "work")
	if code != 0 || strings.TrimSpace(out) != "old-work" {
		t.Fatalf("code=%d out=%q err=%q", code, out, e)
	}
	if got := loadThreads(root)[0].Meta; got.Created != "2026-10-28" || got.Name != "Old work" {
		t.Fatalf("meta = %+v", got)
	}
	if code, _, _ := runCmd(t, root, "new", "--created", "last week", "X"); code != 64 {
		t.Fatalf("bad date code = %d", code)
	}
}

func TestThreadsCmdCommitKeepsSessionsAndCreated(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Alpha")
	if err := registerSession(root, slug, "claude", "s1"); err != nil {
		t.Fatal(err)
	}
	b := mustBegin(t, root, slug)
	nw := "---\nname: Alpha\nstatus: open\ncreated: 1999-01-01\nsessions:\n  - agent: amp\n    id: T-x\n---\n"
	if code, _, e := runCmd(t, root, "commit", slug, "--base", b.Base, "--file", writeTmp(t, nw)); code != 0 {
		t.Fatalf("commit = %d %s", code, e)
	}
	meta, _, err := parseThreadMD([]byte(readT(t, filepath.Join(root, slug, "thread.md"))))
	if err != nil || meta.Created != "2026-11-04" || len(meta.Sessions) != 1 || meta.Sessions[0] != (threadSessionRef{"claude", "s1"}) {
		t.Fatalf("meta = %+v err=%v", meta, err)
	}
}
