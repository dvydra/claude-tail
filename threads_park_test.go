package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNextWorkday(t *testing.T) {
	for _, c := range []struct{ from, want string }{
		{"2026-11-04", "2026-11-05"}, // Wed → Thu
		{"2026-11-06", "2026-11-09"}, // Fri → Mon
		{"2026-11-07", "2026-11-09"}, // Sat → Mon
		{"2026-11-08", "2026-11-09"}, // Sun → Mon
	} {
		d, _ := time.Parse("2006-01-02", c.from)
		if got := nextWorkday(d).Format("2006-01-02"); got != c.want {
			t.Errorf("nextWorkday(%s) = %s, want %s", c.from, got, c.want)
		}
	}
}

func TestParkKindNotTodayWritesWakeAndLog(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Park me")
	if err := parkThread(root, slug, "not_today", "", storeNow); err != nil {
		t.Fatal(err)
	}
	m := loadThreads(root)[0].Meta
	if m.Status != threadParked || m.ParkKind != "not_today" || m.WakesOn != "2026-11-05" || m.ParkReason != "" {
		t.Fatalf("meta %+v", m)
	}
	log, _ := os.ReadFile(filepath.Join(root, slug, "log", "2026-11-04.md"))
	if !strings.Contains(string(log), "parked: not today") {
		t.Fatalf("log %q", log)
	}
}

func TestParkKindWithNoteAndNoWake(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Park me")
	if err := parkThread(root, slug, "customer", "Acme SSO", storeNow); err != nil {
		t.Fatal(err)
	}
	m := loadThreads(root)[0].Meta
	if m.ParkKind != "customer" || m.WakesOn != "" || m.ParkReason != "Acme SSO" {
		t.Fatalf("meta %+v", m)
	}
	log, _ := os.ReadFile(filepath.Join(root, slug, "log", "2026-11-04.md"))
	if !strings.Contains(string(log), "parked: waiting on customer: Acme SSO") {
		t.Fatalf("log %q", log)
	}
}

func TestParkUnknownKindRefused(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Park me")
	if err := parkThread(root, slug, "bogus", "x", storeNow); err == nil {
		t.Fatal("expected error")
	}
}

func TestReparkChangesKind(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Park me")
	_ = parkThread(root, slug, "not_today", "", storeNow)
	if err := parkThread(root, slug, "someday", "", storeNow); err != nil {
		t.Fatal(err)
	}
	m := loadThreads(root)[0].Meta
	if m.ParkKind != "someday" || m.WakesOn != "" {
		t.Fatalf("meta %+v", m)
	}
}

func TestUnparkClearsKindAndWake(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Park me")
	_ = parkThread(root, slug, "not_today", "x", storeNow)
	_ = setThreadStatus(root, slug, threadOpen, "", storeNow)
	data, _ := os.ReadFile(filepath.Join(root, slug, "thread.md"))
	for _, k := range []string{"park_kind", "wakes_on", "park_reason"} {
		if strings.Contains(string(data), k) {
			t.Errorf("%s left behind:\n%s", k, data)
		}
	}
}

func TestWakeThreadsUnparksDueOnly(t *testing.T) {
	root := t.TempDir()
	due := mustCreate(t, root, "Due")
	later := mustCreate(t, root, "Later")
	other := mustCreate(t, root, "Someday")
	_ = parkThread(root, due, "not_today", "", storeNow)                    // wakes 11-05
	_ = parkThread(root, later, "not_today", "", storeNow.AddDate(0, 0, 2)) // Fri → wakes Mon 11-09
	_ = parkThread(root, other, "someday", "", storeNow)
	if n := wakeThreads(root, storeNow.AddDate(0, 0, 1)); n != 1 {
		t.Fatalf("woke %d", n)
	}
	st := map[string]threadStatus{}
	for _, th := range loadThreads(root) {
		st[threadSlugOf(th)] = th.Meta.Status
	}
	if st[due] != threadOpen || st[later] != threadParked || st[other] != threadParked {
		t.Fatalf("statuses %v", st)
	}
	log, _ := os.ReadFile(filepath.Join(root, due, "log", "2026-11-05.md"))
	if !strings.Contains(string(log), "woke") {
		t.Fatalf("log %q", log)
	}
}

func TestThreadsCLIParkKind(t *testing.T) {
	root := t.TempDir()
	slug := mustCreate(t, root, "Park me")
	var out, errOut strings.Builder
	if code := runThreadsCommand([]string{"park", slug, "--kind", "waiting-on-review"}, root, storeNow, &out, &errOut); code == 0 {
		t.Fatal("an unknown kind must fail")
	}
	errOut.Reset()
	if code := runThreadsCommand([]string{"park", slug, "--kind", "review", "trail", "42"}, root, storeNow, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	m := loadThreads(root)[0].Meta
	if m.ParkKind != "review" || m.ParkReason != "trail 42" {
		t.Fatalf("meta %+v", m)
	}
}

func TestSpliceVisibleKeepsBothSides(t *testing.T) {
	base := "\x1b[31mabcdefghij\x1b[0m"
	got := spliceVisible(base, "XY", 3)
	if stripANSI(got) != "abcXYfghij" {
		t.Fatalf("got %q", stripANSI(got))
	}
	// The right-hand text keeps the colour it had.
	if !strings.Contains(got, "\x1b[31mfghij") {
		t.Fatalf("lost colour: %q", got)
	}
	if stripANSI(spliceVisible("ab", "XY", 4)) != "ab  XY" {
		t.Fatalf("short base: %q", stripANSI(spliceVisible("ab", "XY", 4)))
	}
}

func TestPlanOpenAllFreshSessionsAndSkips(t *testing.T) {
	wt := t.TempDir()
	ok := thread{Dir: "/v/ok", Meta: threadMeta{Name: "ok", Updated: "2026-11-01",
		Claims:   []threadClaim{{Kind: "worktree", Path: wt}},
		Sessions: []threadSessionRef{{Agent: "claude", ID: "old"}}}}
	running := thread{Dir: "/v/run", Meta: threadMeta{Name: "run", Sessions: []threadSessionRef{{Agent: "claude", ID: "live-1"}}}}
	nowhere := thread{Dir: "/v/nowhere", Meta: threadMeta{Name: "nowhere"}}
	live := []liveSession{{SessionID: "live-1", PID: 9, Cwd: "/x"}}
	n := 0
	ids := func() string { n++; return "11111111-2222-4333-8444-55555555555" + string(rune('0'+n)) }
	picks, skipped := planOpenAll([]thread{ok, running, nowhere}, nil, live, "/bin/et", "claude", nil, ids)
	if len(picks) != 1 || picks[0].Slug != "ok" {
		t.Fatalf("picks %+v", picks)
	}
	p := picks[0]
	if strings.Contains(p.Tab.Agent, "--resume") || !strings.Contains(p.Tab.Agent, "--session-id") {
		t.Fatalf("must be a fresh pinned session: %s", p.Tab.Agent)
	}
	if !strings.Contains(p.Tab.Agent, "reassess the current state") || !strings.Contains(p.Tab.Agent, "/v/ok/thread.md") {
		t.Fatalf("prompt missing: %s", p.Tab.Agent)
	}
	if p.Ref.ID == "" || !strings.Contains(p.Tab.Tail, p.Ref.ID) {
		t.Fatalf("ref %+v tail %s", p.Ref, p.Tab.Tail)
	}
	if strings.Join(skipped, "|") != "run (already running)|nowhere (no working directory recorded)" {
		t.Fatalf("skipped %v", skipped)
	}
}

func TestOpenAllWithRegistersAfterWindow(t *testing.T) {
	root, th := openFixture(t)
	slug := threadSlugOf(th)
	var scripts []string
	prev := threadsOsa
	threadsOsa = func(s string) error { scripts = append(scripts, s); return nil }
	t.Cleanup(func() { threadsOsa = prev })
	id := "11111111-2222-4333-8444-555555555555"
	picks, _ := planOpenAll([]thread{th}, nil, nil, "/bin/et", "claude", nil, func() string { return id })
	note := openAllWith(root, picks, []string{"x (already running)"})
	if len(scripts) != 1 || !strings.Contains(scripts[0], "create tab") || strings.Contains(scripts[0], "create window") || strings.Count(scripts[0], "split ") != 2 || !strings.Contains(scripts[0], "select home") {
		t.Fatalf("scripts %v", scripts)
	}
	if note != "opened 1 thread in new tabs · skipped x (already running)" {
		t.Fatalf("note %q", note)
	}
	got := loadThreads(root)[0].Meta.Sessions
	if len(got) != 1 || got[0].ID != id || threadSlugOf(loadThreads(root)[0]) != slug {
		t.Fatalf("sessions %v", got)
	}
}

func TestOpenAllWithFailedWindowRegistersNothing(t *testing.T) {
	root, th := openFixture(t)
	prev := threadsOsa
	threadsOsa = func(string) error { return errors.New("boom") }
	t.Cleanup(func() { threadsOsa = prev })
	picks, _ := planOpenAll([]thread{th}, nil, nil, "/bin/et", "claude", nil, func() string { return "11111111-2222-4333-8444-555555555555" })
	if note := openAllWith(root, picks, nil); note != "could not open the tabs: boom" {
		t.Fatalf("note %q", note)
	}
	if got := loadThreads(root)[0].Meta.Sessions; len(got) != 0 {
		t.Fatalf("registered %v", got)
	}
}
