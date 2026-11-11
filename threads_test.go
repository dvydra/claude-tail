package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestThreadSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Mirror GitHub checks": "mirror-github-checks",
		"customer: dagger/RWX": "customer-dagger-rwx",
		"  ✨ fe3h Android  ":   "fe3h-android",
		"":                     "",
	} {
		if got := threadSlug(in); got != want {
			t.Errorf("threadSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestThreadMDRoundTrip(t *testing.T) {
	meta := threadMeta{
		Name: "Mirror GitHub checks", Slug: "mirror-github-checks", Status: threadParked,
		ParkReason: "waiting", ParkedAt: "2026-11-01", WaitingOn: "bob", NeedsYou: true,
		Customer: "acme", Linear: []string{"ENT-1"}, Trails: []string{"gh/o/r/1"},
		Claims:   []threadClaim{{Kind: "worktree", Path: "/x", Branch: "b", Head: "abc"}, {Kind: "trail", Trail: "gh/o/r/1", Expect: "open"}},
		Sessions: []threadSessionRef{{Agent: "claude", ID: "s1"}},
		Created:  "2026-10-01", Updated: "2026-11-04",
		AsOf: map[string]string{"What it is": "2026-11-04"},
	}
	secs := map[string]string{
		"What it is":        "Line one\n\n- a\n- b",
		"Where it got to":   "Here",
		"What's in the way": "Nothing",
		"People":            "Bob",
		"Recent":            "Yesterday",
	}
	out := formatThreadMD(meta, secs)
	gm, gs, err := parseThreadMD(out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gm, meta) {
		t.Errorf("meta mismatch:\n%+v\n%+v", gm, meta)
	}
	for _, h := range threadSections {
		if gs[h] != secs[h] {
			t.Errorf("section %q = %q, want %q", h, gs[h], secs[h])
		}
	}
	_, gs2, err := parseThreadMD(formatThreadMD(meta, map[string]string{"What it is": "x"}))
	if err != nil || gs2["People"] != "" || gs2["What it is"] != "x" {
		t.Errorf("sparse sections: %v %v", gs2, err)
	}
}

func TestParseThreadMDBadFrontmatter(t *testing.T) {
	_, secs, err := parseThreadMD([]byte("---\nstatus: [\n---\n## What it is\nx"))
	if err == nil {
		t.Fatal("want error")
	}
	if secs["What it is"] != "x" {
		t.Errorf("sections = %v", secs)
	}
}

func TestParseThreadMDCRLF(t *testing.T) {
	in := "---\r\nname: N\r\nstatus: open\r\n---\r\n## What it is\r\nx\r\ny\r\n"
	meta, secs, err := parseThreadMD([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "N" || meta.Status != threadOpen || secs["What it is"] != "x\ny" {
		t.Errorf("meta=%+v secs=%q", meta, secs)
	}
}

func TestParseThreadMDUnknownHeadingStaysInSection(t *testing.T) {
	_, secs, err := parseThreadMD([]byte("---\nname: n\n---\n## What it is\nx\n## Extra\ny\n## People\nz\n"))
	if err != nil {
		t.Fatal(err)
	}
	if secs["What it is"] != "x\n## Extra\ny" || secs["People"] != "z" {
		t.Errorf("sections = %q", secs)
	}
}

func writeThread(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadThreadsSkipsClosedAndDotfiles(t *testing.T) {
	root := t.TempDir()
	writeThread(t, root, "a/thread.md", "---\nname: A\nstatus: open\nupdated: 2026-11-01\n---\n## What it is\nx\n")
	writeThread(t, root, "b/thread.md", "---\nname: B\nstatus: parked\nupdated: 2026-11-03\n---\n")
	writeThread(t, root, "_closed/c/thread.md", "---\nname: C\n---\n")
	writeThread(t, root, ".registry.json", "{}")
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, th := range loadThreads(root) {
		slugs = append(slugs, th.Meta.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"b", "a"}) {
		t.Errorf("slugs = %v", slugs)
	}
}

func TestLoadThreadsOrdersNeedsYouFirst(t *testing.T) {
	root := t.TempDir()
	writeThread(t, root, "a/thread.md", "---\nstatus: open\nupdated: 2026-11-04\n---\n")
	writeThread(t, root, "b/thread.md", "---\nstatus: open\nneeds_you: true\nupdated: 2026-01-01\n---\n")
	got := loadThreads(root)
	if len(got) != 2 || got[0].Meta.Slug != "b" {
		t.Errorf("order = %+v", got)
	}
}

func TestLoadThreadsKeepsUnreadable(t *testing.T) {
	root := t.TempDir()
	writeThread(t, root, "broken/thread.md", "---\nstatus: [\n---\n## What it is\nx")
	got := loadThreads(root)
	if len(got) != 1 || got[0].ParseErr == nil || got[0].Meta.Slug != "broken" {
		t.Errorf("got %+v", got)
	}
}
