package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func stringReader(s string) *strings.Reader { return strings.NewReader(s) }

func runHookScript(t *testing.T, home, mode, stdin string) {
	t.Helper()
	cmd := exec.Command("bash", "hooks/entire-tail-pending.sh", mode)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = stringReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook %s failed: %v: %s", mode, err, out)
	}
}

func TestHookScriptSetsAndClearsQuestionMarker(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	home := t.TempDir()
	payload := `{"session_id":"sid-1","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Q","header":"H","options":[{"label":"A","description":"d"}]}]}}`
	runHookScript(t, home, "question-set", payload)

	marker := filepath.Join(home, ".claude", "entire-tail", "pending", "sid-1.json")
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	m, ok := parsePendingMarker(b)
	if !ok || m.Kind != "question" {
		t.Fatalf("bad marker: %s", b)
	}

	runHookScript(t, home, "question-clear", `{"session_id":"sid-1"}`)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker should be cleared")
	}
}

func TestHookScriptSetsAndClearsPermissionMarker(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	home := t.TempDir()
	payload := `{"session_id":"sid-2","tool_name":"Bash","tool_input":{"command":"git push"}}`
	runHookScript(t, home, "perm-set", payload)

	marker := filepath.Join(home, ".claude", "entire-tail", "pending", "sid-2.json")
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("permission marker not written: %v", err)
	}
	m, ok := parsePendingMarker(b)
	if !ok || m.Kind != "permission" {
		t.Fatalf("bad marker: %s", b)
	}
	if s := permissionSummary(m); !strings.Contains(s, "Bash") || !strings.Contains(s, "git push") {
		t.Fatalf("permission summary %q missing tool/command", s)
	}

	runHookScript(t, home, "perm-clear", `{"session_id":"sid-2"}`)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("permission marker should be cleared")
	}
}

// perm-set must ignore AskUserQuestion: it already gets a richer question card
// from its Pre/Post hooks, so a permission notice is noise — and, worse, would
// clobber the question marker (both keyed on the same <sid>.json). This is the
// bug found in live testing: the "⏳ waiting: AskUserQuestion(...)" line.
func TestHookScriptPermSkipsAskUserQuestion(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	home := t.TempDir()
	marker := filepath.Join(home, ".claude", "entire-tail", "pending", "sid-3.json")

	// A bare perm-set for AskUserQuestion writes nothing.
	runHookScript(t, home, "perm-set",
		`{"session_id":"sid-3","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Q"}]}}`)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("perm-set must not write a marker for AskUserQuestion")
	}

	// The real sequence: question-set writes the card marker, then perm-set for
	// the same AskUserQuestion must NOT overwrite it.
	runHookScript(t, home, "question-set",
		`{"session_id":"sid-3","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Q","header":"H","options":[{"label":"A"}]}]}}`)
	runHookScript(t, home, "perm-set",
		`{"session_id":"sid-3","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Q"}]}}`)
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("question marker missing after perm-set: %v", err)
	}
	if m, ok := parsePendingMarker(b); !ok || m.Kind != "question" {
		t.Fatalf("marker must remain the question card, got: %s", b)
	}
}
