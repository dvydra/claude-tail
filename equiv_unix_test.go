//go:build !windows

package main

import (
	"io"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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
