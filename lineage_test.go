package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRelocatedSession covers following a session across a worktree cwd switch:
// the same <id>.jsonl reappears in a different project dir (newer mtime), and
// relocatedSession adopts it. Complements the new-id fork path (lineageChild).
func TestRelocatedSession(t *testing.T) {
	root := t.TempDir()
	id := "a3079d56-8720-4977-8480-e280e171cc7a"
	dirA := filepath.Join(root, "-Users-x-repo")
	dirB := filepath.Join(root, "-Users-x-repo--claude-worktrees-wt")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fileA := filepath.Join(dirA, id+".jsonl")
	fileB := filepath.Join(dirB, id+".jsonl")
	for _, f := range []string{fileA, fileB} {
		if err := os.WriteFile(f, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	older, newer := time.Unix(1000, 0), time.Unix(2000, 0)
	_ = os.Chtimes(fileA, older, older)
	_ = os.Chtimes(fileB, newer, newer)

	// From the older file → adopt the newer same-id file in the other dir.
	if got := relocatedSession(root, id, fileA); got != fileB {
		t.Fatalf("from older: want %s, got %q", fileB, got)
	}
	// From the newest file → nothing newer elsewhere → no relocation.
	if got := relocatedSession(root, id, fileB); got != "" {
		t.Fatalf("from newest: want \"\", got %q", got)
	}
	// curPath gone (moved away) → still finds the newest same-id file.
	gone := filepath.Join(root, "-nonexistent", id+".jsonl")
	if got := relocatedSession(root, id, gone); got != fileB {
		t.Fatalf("gone curPath: want %s, got %q", fileB, got)
	}
	// A non-UUID id is never globbed.
	if got := relocatedSession(root, "not-a-uuid", fileA); got != "" {
		t.Fatalf("invalid id: want \"\", got %q", got)
	}
}

func TestSessionIDFromPath(t *testing.T) {
	got := sessionIDFromPath("/x/y/-Users-dvydra-src/9ed9607a-f1bf.jsonl")
	if want := "9ed9607a-f1bf"; got != want {
		t.Fatalf("sessionIDFromPath = %q, want %q", got, want)
	}
}

func TestForkPointer(t *testing.T) {
	// A real-shaped forked session: mode, then the worktree-state record whose
	// worktreeSession.sessionId points at the session it forked from.
	head := []byte(`{"type":"mode","mode":"normal","sessionId":"9ed9607a"}
{"type":"worktree-state","worktreeSession":{"originalCwd":"/x","worktreePath":"/x/.claude/worktrees/wt","sessionId":"f4d95ea2"}}
{"type":"file-history-snapshot","messageId":"m1"}`)
	if got := forkPointer(head); got != "f4d95ea2" {
		t.Fatalf("forkPointer = %q, want f4d95ea2", got)
	}

	// A plain session with no worktree-state has no fork pointer.
	plain := []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`)
	if got := forkPointer(plain); got != "" {
		t.Fatalf("forkPointer(plain) = %q, want empty", got)
	}
}

func TestForkPointerClear(t *testing.T) {
	// A /clear does NOT keep the same file — it mints a new <id>.jsonl in the
	// same project dir whose worktree-state carries worktreeSession.sessionId =
	// the pre-clear session (verified live: baa1307f forked from 8a853bad on a
	// /clear). It is the SAME field a worktree re-enter writes, just with the
	// full worktree metadata alongside, so lineage catches /clear for free.
	head := []byte(`{"type":"mode","mode":"normal","sessionId":"baa1307f"}
{"type":"worktree-state","sessionId":"baa1307f","worktreeSession":{"originalCwd":"/x","preEnterOriginalCwd":"/x","worktreePath":"/x/.claude/worktrees/wt","worktreeName":"wt","worktreeBranch":"wt-branch","originalBranch":"main","originalHeadCommit":"ec0221c","sessionId":"8a853bad"}}
{"type":"file-history-snapshot"}`)
	if got := forkPointer(head); got != "8a853bad" {
		t.Fatalf("forkPointer(/clear) = %q, want 8a853bad", got)
	}
}

func TestValidSessionID(t *testing.T) {
	valid := []string{
		"8a853bad-54f1-4abc-a8e9-3a7a609c7dcf",
		"baa1307f-ca7f-4965-92fe-344736d907c6",
		"BAA1307F-CA7F-4965-92FE-344736D907C6", // upper-case hex is fine
	}
	for _, id := range valid {
		if !validSessionID(id) {
			t.Errorf("validSessionID(%q) = false, want true", id)
		}
	}
	invalid := []string{
		"",
		"../../../../etc/passwd",
		"..",
		"8a853bad",                               // too short
		"8a853bad-54f1-4abc-a8e9-3a7a609c7dcf/x", // path separator
		"8a853bad-54f1-4abc-a8e9-3a7a609c7dcf.bak", // trailing junk
		"zzzzzzzz-54f1-4abc-a8e9-3a7a609c7dcf",     // non-hex
	}
	for _, id := range invalid {
		if validSessionID(id) {
			t.Errorf("validSessionID(%q) = true, want false", id)
		}
	}
}

func TestLineageChild(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "f4d95ea2.jsonl")
	child := filepath.Join(dir, "9ed9607a.jsonl")
	stranger := filepath.Join(dir, "aaaa1111.jsonl") // a concurrent, unrelated session
	strangerChild := filepath.Join(dir, "bbbb2222.jsonl")

	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(cur, `{"type":"user","message":{"role":"user","content":"show me the aurora ones"}}`+"\n")
	write(child, `{"type":"worktree-state","worktreeSession":{"sessionId":"f4d95ea2"}}`+"\n")
	write(stranger, `{"type":"user","message":{"role":"user","content":"unrelated"}}`+"\n")
	// A fork of the stranger — points at aaaa1111, NOT our lineage; must be ignored.
	write(strangerChild, `{"type":"worktree-state","worktreeSession":{"sessionId":"aaaa1111"}}`+"\n")

	lineage := map[string]bool{"f4d95ea2": true}
	gotPath, gotID := lineageChild(dir, cur, lineage)
	if gotPath != child || gotID != "9ed9607a" {
		t.Fatalf("lineageChild = (%q,%q), want (%q,9ed9607a)", gotPath, gotID, child)
	}

	// Once the child is in the lineage, there is no further child to adopt —
	// the stranger's fork must never be picked up.
	lineage["9ed9607a"] = true
	if p, _ := lineageChild(dir, child, lineage); p != "" {
		t.Fatalf("lineageChild adopted a stranger's fork: %q", p)
	}
}

func TestMarkContinuation(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "f4d95ea2-1111-4111-8111-111111111111.jsonl")
	// A real-shaped stopped session whose LAST line lacks a uuid (a
	// file-history-snapshot): the marker must chain off the last uuid-bearing
	// record above it, not orphan itself on a null parent.
	body := `{"type":"user","message":{"role":"user","content":"hi"},"uuid":"aaaa1111-2222-4333-8444-555566667777","sessionId":"f4d95ea2-1111-4111-8111-111111111111","cwd":"/x/y","version":"2.1.211","gitBranch":"main"}` + "\n" +
		`{"type":"file-history-snapshot","messageId":"m1"}` + "\n"
	if err := os.WriteFile(old, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	newID := "9ed9607a-8888-4999-8aaa-bbbbccccdddd"

	markContinuation(old, newID)

	lines := strings.Split(strings.TrimRight(readFileString(t, old), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines after marking, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &rec); err != nil {
		t.Fatalf("marker line is not valid JSON: %v", err)
	}
	// Renders in Claude Code's transcript but is never replayed to the model.
	if rec["type"] != "system" || rec["subtype"] != "informational" {
		t.Errorf("marker type/subtype = %v/%v, want system/informational", rec["type"], rec["subtype"])
	}
	if c, _ := rec["content"].(string); !strings.Contains(c, newID) {
		t.Errorf("marker content %q does not name the new id %q", c, newID)
	}
	// Chains off the last record so it renders as the thread's leaf on resume.
	if rec["parentUuid"] != "aaaa1111-2222-4333-8444-555566667777" {
		t.Errorf("parentUuid = %v, want the last record's uuid", rec["parentUuid"])
	}
	// Copies the session's context fields so the record matches its siblings.
	for k, want := range map[string]string{"sessionId": "f4d95ea2-1111-4111-8111-111111111111", "cwd": "/x/y", "version": "2.1.211", "gitBranch": "main"} {
		if rec[k] != want {
			t.Errorf("marker %s = %v, want %q", k, rec[k], want)
		}
	}

	// Idempotent: a second flip to the same child adds nothing.
	markContinuation(old, newID)
	if lines := strings.Split(strings.TrimRight(readFileString(t, old), "\n"), "\n"); len(lines) != 3 {
		t.Fatalf("second markContinuation stacked a duplicate: %d lines", len(lines))
	}

	// Best-effort: a missing file is a silent no-op, never a panic.
	markContinuation(filepath.Join(dir, "does-not-exist.jsonl"), newID)
}

func readFileString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLineageChildEmptyFileIgnored(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "f4d95ea2.jsonl")
	child := filepath.Join(dir, "9ed9607a.jsonl")
	os.WriteFile(cur, []byte("{}\n"), 0o644)
	os.WriteFile(child, []byte(""), 0o644) // forked file announced but not yet written
	if p, _ := lineageChild(dir, cur, map[string]bool{"f4d95ea2": true}); p != "" {
		t.Fatalf("adopted an empty child: %q", p)
	}
}
