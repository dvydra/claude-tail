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
