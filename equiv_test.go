package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"
)

// ansiRe matches SGR sequences and OSC strings so comparisons focus on rendered
// text + layout rather than exact colors (those are covered by unit tests).
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]|\x1b\\][0-9];[^\x1b\x07]*(?:\x1b\\\\|\x07)")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

type fixtureCase struct {
	name      string
	agent     Agent
	file      string
	toolStyle string
	collapse  int
}

var fixtureCases = []fixtureCase{
	{"claude_dots", AgentClaude, "claude_session.jsonl", "dots", 5},
	{"claude_lines", AgentClaude, "claude_session.jsonl", "lines", 5},
	{"codex_dots", AgentCodex, "codex_session.jsonl", "dots", 5},
	{"agy_dots", AgentAgy, "agy_session.jsonl", "dots", 5},
}

// renderFixture renders a whole fixture through the real glamour renderer.
func renderFixture(t *testing.T, fc fixtureCase, loc *time.Location) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fc.file))
	if err != nil {
		t.Fatal(err)
	}
	th, err := loadTheme("tokyo-night", "")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	r, err := newRenderer(&b, th, fc.toolStyle, fc.collapse)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range splitLines(data) {
		for _, rec := range normalize(fc.agent, line, loc) {
			r.emit(rec)
		}
	}
	r.endLine() // settle the deferred trailing newline (as backfill/quit do)
	return b.String()
}

// TestGolden renders each fixture and compares the ANSI-stripped output to a
// committed golden file. Run with UPDATE_GOLDEN=1 to regenerate. Uses UTC so
// the timestamps are machine-independent.
func TestGolden(t *testing.T) {
	for _, fc := range fixtureCases {
		t.Run(fc.name, func(t *testing.T) {
			got := stripANSI(renderFixture(t, fc, time.UTC))
			golden := filepath.Join("testdata", fc.name+".golden")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("output differs from golden %s:\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
			}
		})
	}
}

// TestEquivalenceVsBash renders each fixture through both the bash oracle and
// the Go renderer and asserts the ANSI-stripped output matches. Gated on the
// oracle + its deps (jq/glow) being present; skipped otherwise (e.g. in CI).
//
// The Go renderer now intentionally diverges from bash in every mode, so there
// is nothing left to byte-compare — the harness is kept only for manual A/B use:
//   - lines: tool summaries differ (bash mangles them through markdown).
//   - dots:  tool dots ride the end of the agent turn instead of a standalone
//     line, and (Claude) questions/subagent spawns render as cards/markers.
//
// The goldens (regenerated deliberately) are the parity contract now.
func TestEquivalenceVsBash(t *testing.T) {
	if os.Getenv("RUN_ORACLE") != "1" {
		t.Skip("set RUN_ORACLE=1 to run the bash-oracle parity test (spawns bash+glow)")
	}
	oracle := "entire-tail.bash"
	if _, err := os.Stat(oracle); err != nil {
		t.Skip("bash oracle entire-tail.bash not present")
	}
	for _, tool := range []string{"bash", "jq", "glow", "awk", "base64"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("oracle dependency %q not on PATH", tool)
		}
	}

	for _, fc := range fixtureCases {
		t.Run(fc.name, func(t *testing.T) {
			// Every mode now diverges intentionally (see the doc comment); no fixture
			// is byte-comparable against the oracle anymore. The comparison below is
			// kept for manual A/B inspection of Go vs the bash reference.
			t.Skip("retired: Go renderer intentionally diverges from the bash oracle in every mode")
			goOut := stripANSI(renderFixture(t, fc, time.Local))
			bashOut := stripANSI(string(runOracleBackfill(t, oracle, fc, 5*time.Second)))
			if bashOut != goOut {
				t.Errorf("bash vs go differ for %s:\n--- bash ---\n%q\n--- go ---\n%q", fc.name, bashOut, goOut)
			}
		})
	}
}

// runOracleBackfill runs the bash oracle and captures its backfill output. The
// oracle follows the file forever after backfill, and its pipeline children
// (tail -F, glow) keep stdout open — so we run it in its own process group and
// kill the whole group once the deadline fires, then collect what was written.
func runOracleBackfill(t *testing.T, oracle string, fc fixtureCase, d time.Duration) []byte {
	t.Helper()
	cmd := exec.Command("bash", oracle,
		"--agent", string(fc.agent), "--no-pick", "--backfill", "all",
		"-t", "tokyo-night", "--tool-style", fc.toolStyle,
		filepath.Join("testdata", fc.file))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid // group leader, since Setpgid
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(stdout)
		done <- b
	}()
	var out []byte
	select {
	case out = <-done:
	case <-time.After(d):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		out = <-done
	}
	_ = cmd.Wait()
	// Sweep any stragglers (tail -F is stubborn) that outlived the first kill.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return out
}
