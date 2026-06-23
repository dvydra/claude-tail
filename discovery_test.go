package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSniffAgent(t *testing.T) {
	cases := []struct {
		line string
		want Agent
	}{
		{`{"type":"session_meta","payload":{"cwd":"/x"}}`, AgentCodex},
		{`{"type":"response_item","payload":{"type":"message"}}`, AgentCodex},
		{`{"type":"USER_INPUT","content":"x"}`, AgentAgy},
		{`{"type":"PLANNER_RESPONSE"}`, AgentAgy},
		{`{"type":"user","message":{"content":"hi"}}`, AgentClaude},
		{`{"type":"assistant"}`, AgentClaude},
		{`not json`, AgentClaude},
	}
	for _, c := range cases {
		if got := sniffAgent([]byte(c.line)); got != c.want {
			t.Errorf("sniffAgent(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsTruthy(t *testing.T) {
	truthy := []string{`"x"`, `""`, `1`, `0`, `[]`, `{}`, `true`}
	for _, v := range truthy {
		if !isTruthy(json.RawMessage(v)) {
			t.Errorf("isTruthy(%q) should be true", v)
		}
	}
	for _, v := range []string{`null`, `false`, ``} {
		if isTruthy(json.RawMessage(v)) {
			t.Errorf("isTruthy(%q) should be false", v)
		}
	}
}

func TestNewestGlobAll(t *testing.T) {
	dir := t.TempDir()
	// Create three files with distinct mtimes.
	names := []string{"a.jsonl", "b.jsonl", "c.jsonl"}
	base := time.Now()
	for i, n := range names {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("x"), 0o644)
		// a oldest, c newest
		mt := base.Add(time.Duration(i) * time.Minute)
		os.Chtimes(p, mt, mt)
	}
	got := newestGlobAll(filepath.Join(dir, "*.jsonl"))
	if len(got) != 3 || filepath.Base(got[0]) != "c.jsonl" || filepath.Base(got[2]) != "a.jsonl" {
		t.Errorf("expected newest-first c,b,a; got %v", got)
	}
	if newestGlob(filepath.Join(dir, "*.jsonl")) != got[0] {
		t.Error("newestGlob should return the newest")
	}
	if newestGlob(filepath.Join(dir, "none-*.jsonl")) != "" {
		t.Error("no match → empty")
	}
}

func TestDetectAgentForFileByPath(t *testing.T) {
	home := "/home/me"
	cases := []struct {
		path string
		want Agent
	}{
		{home + "/.claude/projects/slug/x.jsonl", AgentClaude},
		{home + "/.codex/sessions/2026/06/05/rollout-x.jsonl", AgentCodex},
		{home + "/.gemini/antigravity-cli/brain/id/.system_generated/logs/transcript.jsonl", AgentAgy},
	}
	for _, c := range cases {
		if got := detectAgentForFile(home, c.path); got != c.want {
			t.Errorf("detectAgentForFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestCodexScanner(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "05")
	os.MkdirAll(dir, 0o755)
	// Two rollouts: one for /proj, one for /other; /proj is newer.
	mk := func(name, cwd string, age time.Duration) string {
		p := filepath.Join(dir, name)
		line, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": cwd}})
		os.WriteFile(p, append(line, '\n'), 0o644)
		mt := time.Now().Add(-age)
		os.Chtimes(p, mt, mt)
		return p
	}
	other := mk("rollout-other.jsonl", "/other", 2*time.Minute)
	proj := mk("rollout-proj.jsonl", "/proj", 1*time.Minute)

	s := newCodexScanner(home)
	if got := s.findForCwd("/proj"); got != proj {
		t.Errorf("findForCwd(/proj) = %q, want %q", got, proj)
	}
	if got := s.cwdOf(other); got != "/other" {
		t.Errorf("cwdOf = %q", got)
	}
	// No match → global newest (proj is newest).
	if got := s.findForCwd("/nonexistent"); got != proj {
		t.Errorf("fallback newest = %q, want %q", got, proj)
	}
	if got := s.sessionsForCwd("/other", 5); len(got) != 1 || got[0] != other {
		t.Errorf("sessionsForCwd(/other) = %v", got)
	}
}

func TestFindSessionClaude(t *testing.T) {
	home := t.TempDir()
	pwd := "/Users/me/proj"
	slug := "-Users-me-proj"
	dir := filepath.Join(home, ".claude", "projects", slug)
	os.MkdirAll(dir, 0o755)
	want := filepath.Join(dir, "session.jsonl")
	os.WriteFile(want, []byte("{}"), 0o644)
	if got := findSessionClaude(home, pwd); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// Different pwd with no project dir → global fallback finds the same file.
	if got := findSessionClaude(home, "/other/dir"); got != want {
		t.Errorf("fallback got %q want %q", got, want)
	}
}

func TestClaudeSlug(t *testing.T) {
	cases := map[string]string{
		"/Users/dvydra/src/entirehq/entire.io": "-Users-dvydra-src-entirehq-entire-io",
		"/Users/me/proj":                       "-Users-me-proj",
		"/Users/me/claude-tail":                "-Users-me-claude-tail", // existing '-' preserved
		"/a/b_c/d e":                           "-a-b-c-d-e",            // '_' and space → '-'
	}
	for in, want := range cases {
		if got := claudeSlug(in); got != want {
			t.Errorf("claudeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// mkClaudeSession writes a session file under home's projects/<slug> dir and
// stamps its mtime to base+age. Returns the file path.
func mkClaudeSession(t *testing.T, home, cwd string, base time.Time, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "s.jsonl")
	os.WriteFile(p, []byte("{}"), 0o644)
	mt := base.Add(age)
	os.Chtimes(p, mt, mt)
	return p
}

// TestFindSessionClaudeDottedCwd is the original bug: a cwd containing '.' must
// resolve into the dashed folder Claude actually created, not fall through to
// the global newest.
func TestFindSessionClaudeDottedCwd(t *testing.T) {
	home := t.TempDir()
	base := time.Now()
	want := mkClaudeSession(t, home, "/Users/dvydra/src/entirehq/entire.io", base, 0)
	// A decoy that IS the global newest — the old '/'-only slug would land here.
	mkClaudeSession(t, home, "/Users/dvydra/src/other", base, time.Minute)
	if got := findSessionClaude(home, "/Users/dvydra/src/entirehq/entire.io"); got != want {
		t.Errorf("dotted cwd resolved to %q, want %q", got, want)
	}
}

func TestFindSessionClaudeTree(t *testing.T) {
	base := time.Now()

	t.Run("exact beats a newer ancestor", func(t *testing.T) {
		home := t.TempDir()
		pwd := filepath.Join(home, "src/repo/sub")
		exact := mkClaudeSession(t, home, pwd, base, 0)
		mkClaudeSession(t, home, filepath.Join(home, "src/repo"), base, time.Minute) // newer ancestor
		if got := findSessionClaude(home, pwd); got != exact {
			t.Errorf("exact-cwd should win; got %q want %q", got, exact)
		}
	})

	t.Run("no exact → nearest ancestor", func(t *testing.T) {
		home := t.TempDir()
		pwd := filepath.Join(home, "src/repo/a/b")
		far := mkClaudeSession(t, home, filepath.Join(home, "src/repo"), base, time.Minute) // newer but farther
		near := mkClaudeSession(t, home, filepath.Join(home, "src/repo/a"), base, 0)        // older but nearer
		_ = far
		if got := findSessionClaude(home, pwd); got != near {
			t.Errorf("nearest ancestor should win; got %q want %q", got, near)
		}
	})

	t.Run("no exact, no ancestor → descendant", func(t *testing.T) {
		home := t.TempDir()
		pwd := filepath.Join(home, "src/repo")
		child := mkClaudeSession(t, home, filepath.Join(home, "src/repo/aws/cluster"), base, 0)
		if got := findSessionClaude(home, pwd); got != child {
			t.Errorf("descendant should be found; got %q want %q", got, child)
		}
	})

	t.Run("sibling is out of tree → global", func(t *testing.T) {
		home := t.TempDir()
		pwd := filepath.Join(home, "src/repo/aws")
		sibling := mkClaudeSession(t, home, filepath.Join(home, "src/repo/gcp"), base, 0)
		// Not an ancestor or descendant of pwd, so it's reached only as the
		// global newest (it's the sole session) — not as a tree match.
		if got := findSessionClaude(home, pwd); got != sibling {
			t.Errorf("expected global fallback to sibling; got %q want %q", got, sibling)
		}
		if note := cwdMismatchNote(AgentClaude, sibling, home, pwd, nil); note == "" || !strings.Contains(note, "global latest") {
			t.Errorf("sibling should be reported as global latest, got %q", note)
		}
	})
}

func TestAgyConversationIDAndDiscovery(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	id := "conv-123"
	tdir := filepath.Join(root, "brain", id, ".system_generated", "logs")
	os.MkdirAll(tdir, 0o755)
	transcript := filepath.Join(tdir, "transcript.jsonl")
	os.WriteFile(transcript, []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(root, "cache"), 0o755)
	cache, _ := json.Marshal(map[string]string{"/my/cwd": id})
	os.WriteFile(filepath.Join(root, "cache", "last_conversations.json"), cache, 0o644)

	if got := agyConversationID(root, "/my/cwd"); got != id {
		t.Errorf("got %q", got)
	}
	if got := findSessionAgy(home, "/my/cwd"); got != transcript {
		t.Errorf("findSessionAgy got %q want %q", got, transcript)
	}
}

func TestCwdMismatchNote(t *testing.T) {
	home := t.TempDir()
	// Claude: matching slug → no note.
	slug := "-Users-me-proj"
	sess := filepath.Join(home, ".claude", "projects", slug, "s.jsonl")
	if note := cwdMismatchNote(AgentClaude, sess, home, "/Users/me/proj", nil); note != "" {
		t.Errorf("expected no note, got %q", note)
	}
	// Claude: non-matching → note.
	if note := cwdMismatchNote(AgentClaude, sess, home, "/Users/me/other", nil); note == "" {
		t.Error("expected mismatch note")
	}

	// Same-tree ancestor → note names the enclosing dir, not "global latest".
	ancCwd := filepath.Join(home, "src/repo")
	ancSess := filepath.Join(home, ".claude", "projects", claudeSlug(ancCwd), "s.jsonl")
	note := cwdMismatchNote(AgentClaude, ancSess, home, filepath.Join(home, "src/repo/sub"), nil)
	if !strings.Contains(note, "same-tree dir") || !strings.Contains(note, ancCwd) {
		t.Errorf("ancestor note = %q, want it to name %q", note, ancCwd)
	}

	// Same-tree descendant → subdirectory note.
	descCwd := filepath.Join(home, "src/repo/aws")
	descSess := filepath.Join(home, ".claude", "projects", claudeSlug(descCwd), "s.jsonl")
	if note := cwdMismatchNote(AgentClaude, descSess, home, filepath.Join(home, "src/repo"), nil); !strings.Contains(note, "subdirectory") {
		t.Errorf("descendant note = %q, want a subdirectory note", note)
	}
}
