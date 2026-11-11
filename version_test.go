package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// version.sh prints 1.YYYYMMDD.N: the date of HEAD's commit and how many
// commits on HEAD were made that day, so the same source gives the same version.
func TestVersionScript(t *testing.T) {
	script, err := filepath.Abs("version.sh")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	env := append(os.Environ(), "TZ=UTC", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	git := func(date string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(env, "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	version := func() string {
		t.Helper()
		cmd := exec.Command("bash", script)
		cmd.Dir = repo
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}

	if got := version(); got != "1.0.0-dev" {
		t.Fatalf("outside a repo: %q", got)
	}
	git("2026-11-10T23:00:00Z", "init", "-q")
	git("2026-11-10T23:00:00Z", "commit", "-q", "--allow-empty", "-m", "a")
	if got := version(); got != "1.20261110.1" {
		t.Fatalf("first commit: %q", got)
	}
	git("2026-11-11T09:00:00Z", "commit", "-q", "--allow-empty", "-m", "b")
	git("2026-11-11T10:00:00Z", "commit", "-q", "--allow-empty", "-m", "c")
	if got := version(); got != "1.20261111.2" {
		t.Fatalf("second commit of the day: %q", got)
	}
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("2026-11-11T10:00:00Z", "add", "f")
	if got := version(); got != "1.20261111.2-dirty" {
		t.Fatalf("uncommitted change: %q", got)
	}
}
