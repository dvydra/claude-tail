package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
