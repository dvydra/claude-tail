package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var storeNow = time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)

func mustCreate(t *testing.T, root, name string) string {
	t.Helper()
	slug, err := createThread(root, name, storeNow)
	if err != nil {
		t.Fatalf("createThread(%q): %v", name, err)
	}
	return slug
}

func TestCreateThreadWritesFiles(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Mirror GitHub checks")
	if slug != "mirror-github-checks" {
		t.Fatalf("slug = %q", slug)
	}
	data, err := os.ReadFile(filepath.Join(root, slug, "thread.md"))
	if err != nil {
		t.Fatal(err)
	}
	meta, _, perr := parseThreadMD(data)
	if perr != nil || meta.Status != threadOpen || meta.Created != "2026-11-04" || meta.Updated != "2026-11-04" || meta.Name != "Mirror GitHub checks" {
		t.Fatalf("meta = %+v err=%v", meta, perr)
	}
	if _, err := os.Stat(filepath.Join(root, slug, "knowledge.md")); err != nil {
		t.Error(err)
	}
	if fi, err := os.Stat(filepath.Join(root, slug, "log")); err != nil || !fi.IsDir() {
		t.Error("log dir missing")
	}
}

func TestCreateThreadRefusesEmptySlug(t *testing.T) {
	if _, err := createThread(t.TempDir(), "✨✨", storeNow); err == nil {
		t.Fatal("expected error for empty slug")
	}
}

func TestCreateThreadRefusesDuplicateSlug(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Dup thing")
	if _, err := createThread(root, "dup THING", storeNow); err == nil {
		t.Fatal("expected duplicate error")
	}
	if err := setThreadStatus(root, slug, threadClosed, "", storeNow); err != nil {
		t.Fatal(err)
	}
	if _, err := createThread(root, "Dup thing", storeNow); err == nil {
		t.Fatal("expected duplicate error against _closed")
	}
}

func TestParkRequiresReason(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Park me")
	if err := setThreadStatus(root, slug, threadParked, "  ", storeNow); err == nil {
		t.Fatal("expected error")
	}
	data, _ := os.ReadFile(filepath.Join(root, slug, "thread.md"))
	if !strings.Contains(string(data), "status: open") {
		t.Error("file changed on refused park")
	}
}

func TestParkWritesFrontmatterAndLog(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Park me")
	if err := setThreadStatus(root, slug, threadParked, "waiting on deploy", storeNow); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, slug, "thread.md"))
	s := string(data)
	if !strings.Contains(s, "status: parked") || !strings.Contains(s, "park_reason: waiting on deploy") {
		t.Errorf("frontmatter:\n%s", s)
	}
	log, err := os.ReadFile(filepath.Join(root, slug, "log", "2026-11-04.md"))
	if err != nil || !strings.Contains(string(log), "- 09:30 parked: waiting on deploy") {
		t.Errorf("log = %q err=%v", log, err)
	}
	if err := setThreadStatus(root, slug, threadOpen, "", storeNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(root, slug, "thread.md"))
	if strings.Contains(string(data), "park_reason") || !strings.Contains(string(data), "status: open") {
		t.Errorf("unpark left state:\n%s", data)
	}
	log, _ = os.ReadFile(filepath.Join(root, slug, "log", "2026-11-04.md"))
	if !strings.Contains(string(log), "- 10:30 unparked") {
		t.Errorf("log = %q", log)
	}
}

func TestCloseMovesToClosedAndReopenMovesBack(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Finish me")
	if err := setThreadStatus(root, slug, threadClosed, "", storeNow); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, slug)); !os.IsNotExist(err) {
		t.Error("still in root")
	}
	data, err := os.ReadFile(filepath.Join(root, "_closed", slug, "thread.md"))
	if err != nil || !strings.Contains(string(data), "status: closed") {
		t.Fatalf("closed file: %v\n%s", err, data)
	}
	if err := setThreadStatus(root, slug, threadOpen, "", storeNow); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "_closed", slug)); !os.IsNotExist(err) {
		t.Error("still in _closed")
	}
	data, _ = os.ReadFile(filepath.Join(root, slug, "thread.md"))
	if !strings.Contains(string(data), "status: open") {
		t.Errorf("reopened:\n%s", data)
	}
}

func TestSetStatusRejectsBadSlugAndParseErr(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"", "a/b", ".x", "_closed", "../x"} {
		if err := setThreadStatus(root, bad, threadClosed, "", storeNow); err == nil {
			t.Errorf("slug %q accepted", bad)
		}
	}
	dir := filepath.Join(root, "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "no frontmatter here\n"
	if err := os.WriteFile(filepath.Join(dir, "thread.md"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setThreadStatus(root, "broken", threadParked, "x", storeNow); err == nil {
		t.Fatal("expected parse error")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "thread.md"))
	if string(got) != orig {
		t.Error("broken thread was rewritten")
	}
	if err := registerSession(root, "broken", "claude", "s1", storeNow); err == nil {
		t.Fatal("expected parse error from registerSession")
	}
	got, _ = os.ReadFile(filepath.Join(dir, "thread.md"))
	if string(got) != orig {
		t.Error("broken thread was rewritten by registerSession")
	}
	if _, ok := lookupSession(root, "claude", "s1"); ok {
		t.Error("registry written for broken thread")
	}
}

func TestSetStatusIgnoresFileSlug(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Real one")
	p := filepath.Join(root, slug, "thread.md")
	data, _ := os.ReadFile(p)
	edited := strings.Replace(string(data), "name: Real one", "name: Real one\nslug: ../escape", 1)
	if err := os.WriteFile(p, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setThreadStatus(root, slug, threadClosed, "", storeNow); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "_closed", slug, "thread.md")); err != nil {
		t.Error(err)
	}
}

func TestRegisterSessionIsIdempotent(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Sess")
	for i := 0; i < 2; i++ {
		if err := registerSession(root, slug, "claude", "aaa", storeNow); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := loadRegistry(root)
	if err != nil || len(reg.Sessions) != 1 || reg.Sessions["claude:aaa"] != slug {
		t.Fatalf("registry = %+v err=%v", reg, err)
	}
	if err := registerSession(root, slug, "amp", "bbb", storeNow); err != nil {
		t.Fatal(err)
	}
	if got, ok := lookupSession(root, "amp", "bbb"); !ok || got != slug {
		t.Errorf("lookup = %q %v", got, ok)
	}
	if _, ok := lookupSession(root, "amp", "nope"); ok {
		t.Error("unexpected hit")
	}
	data, _ := os.ReadFile(filepath.Join(root, slug, "thread.md"))
	meta, _, _ := parseThreadMD(data)
	if len(meta.Sessions) != 2 || meta.Sessions[0] != (threadSessionRef{"amp", "bbb"}) || meta.Sessions[1] != (threadSessionRef{"claude", "aaa"}) {
		t.Errorf("sessions = %+v", meta.Sessions)
	}
}

func TestLoadRegistryMissingIsEmpty(t *testing.T) {
	reg, err := loadRegistry(t.TempDir())
	if err != nil || reg.Sessions == nil {
		t.Fatalf("reg=%+v err=%v", reg, err)
	}
}

func TestLockThreadsSerialises(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	var events []string
	var wg sync.WaitGroup
	for _, n := range []string{"a", "b"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			release, err := lockThreads(root)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			events = append(events, n+"+")
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			events = append(events, n+"-")
			mu.Unlock()
			release()
		}(n)
	}
	wg.Wait()
	if len(events) != 4 || events[0][0] != events[1][0] || events[2][0] != events[3][0] {
		t.Errorf("interleaved: %v", events)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); err != nil {
		t.Error("lock file should persist")
	}
}
