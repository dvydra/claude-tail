package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func withWTFProcessFakes(t *testing.T, pid int, alive func(int) bool, name func(int) string) {
	t.Helper()
	oldPID, oldAlive, oldName := wtfCurrentPID, wtfPIDAlive, wtfProcessName
	wtfCurrentPID = func() int { return pid }
	wtfPIDAlive = alive
	wtfProcessName = name
	t.Cleanup(func() { wtfCurrentPID, wtfPIDAlive, wtfProcessName = oldPID, oldAlive, oldName })
}

func TestWTFLockCoordinatesReplacesStaleAndReleasesOnlyOwner(t *testing.T) {
	home := t.TempDir()
	withWTFProcessFakes(t, 101, func(pid int) bool { return pid == 101 }, func(pid int) string { return "entire-tail wtf daemon" })
	release, ok := acquireWTFLock(home)
	if !ok {
		t.Fatal("first lock failed")
	}
	if _, ok := acquireWTFLock(home); ok {
		t.Fatal("second lock succeeded")
	}
	if err := os.WriteFile(wtfLockPath(home), []byte("202\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release()
	if got, err := os.ReadFile(wtfLockPath(home)); err != nil || string(got) != "202\n" {
		t.Fatalf("release removed another pid's lock: %q, %v", got, err)
	}
	wtfCurrentPID = func() int { return 303 }
	wtfPIDAlive = func(int) bool { return false }
	release, ok = acquireWTFLock(home)
	if !ok {
		t.Fatal("dead pid lock was not replaced")
	}
	release()
	if _, err := os.Stat(wtfLockPath(home)); !os.IsNotExist(err) {
		t.Fatalf("owned lock remains: %v", err)
	}
}

func TestWTFHealthRoundTripsAndChecksProcessIdentity(t *testing.T) {
	home := t.TempDir()
	want := wtfHealth{PID: 42, Version: "test", StartedAt: 10, LastAttemptedScan: 11, LastSuccessfulScan: 12, LastError: "degraded"}
	if err := writeWTFHealth(home, want); err != nil {
		t.Fatal(err)
	}
	got, ok := readWTFHealth(home)
	if !ok || got != want {
		t.Fatalf("health = %#v, %v", got, ok)
	}
	if matches, _ := filepath.Glob(filepath.Join(wtfDir(home), "health-*.tmp")); len(matches) != 0 {
		t.Fatalf("temporary health files: %v", matches)
	}
	withWTFProcessFakes(t, 1, func(pid int) bool { return pid == 42 }, func(int) string { return "entire-tail wtf daemon" })
	if !wtfDaemonRunning(got) {
		t.Fatal("live entire-tail pid reported stopped")
	}
	wtfProcessName = func(int) string { return "another-process" }
	if wtfDaemonRunning(got) {
		t.Fatal("recycled pid reported running")
	}
	for _, command := range []string{"/tmp/not-entire-tail --entire-tail", "helper entire-tail", "/usr/bin/entire-tail-helper"} {
		wtfProcessName = func(int) string { return command }
		if wtfDaemonRunning(got) {
			t.Fatalf("command %q reported as entire-tail", command)
		}
	}
}

func TestWTFLockDoesNotTrustProcessArgumentSubstring(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtfLockPath(home), []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withWTFProcessFakes(t, 101, func(int) bool { return true }, func(int) string { return "helper --name entire-tail" })
	release, ok := acquireWTFLock(home)
	if !ok {
		t.Fatal("argument substring prevented stale lock replacement")
	}
	release()
}

func TestWTFLockConcurrentStaleReplacementKeepsWinner(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtfLockPath(home), []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withWTFProcessFakes(t, 101, func(int) bool { return false }, func(int) string { return "" })
	start := make(chan struct{})
	results := make(chan bool, 2)
	releases := make(chan func(), 2)
	for range 2 {
		go func() {
			<-start
			release, ok := acquireWTFLock(home)
			results <- ok
			if ok {
				releases <- release
			}
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		if <-results {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent winners = %d, want 1", winners)
	}
	if _, err := os.Stat(wtfLockPath(home)); err != nil {
		t.Fatalf("winning lock was removed: %v", err)
	}
	(<-releases)()
}

func TestRequestWTFScanCoalescesAtomicMarker(t *testing.T) {
	home := t.TempDir()
	if err := requestWTFScan(home); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(wtfScanRequestPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if err := requestWTFScan(home); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(wtfScanRequestPath(home))
	if err != nil || !os.SameFile(first, second) {
		t.Fatalf("request did not coalesce: %v", err)
	}
}

func TestRunWTFDaemonSerializesScansAndSurvivesDegradation(t *testing.T) {
	home := t.TempDir()
	withWTFProcessFakes(t, 707, func(pid int) bool { return pid == 707 }, func(int) string { return "entire-tail" })
	ticks := make(chan time.Time, 4)
	requests := make(chan time.Time, 4)
	var nowUnix atomic.Int64
	nowUnix.Store(100)
	var active, maxActive, scans atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	deps := wtfDaemonDeps{
		Load:         loadWTFState,
		Save:         saveWTFState,
		Now:          func() time.Time { return time.Unix(nowUnix.Load(), 0) },
		After:        func(time.Duration) <-chan time.Time { return ticks },
		RequestAfter: func(time.Duration) <-chan time.Time { return requests },
		Scan: func(_ context.Context, _ string, state wtfState, _ wtfScanDeps) (wtfState, error) {
			current := active.Add(1)
			if current > maxActive.Load() {
				maxActive.Store(current)
			}
			n := scans.Add(1)
			state.UpdatedAt = int64(n)
			active.Add(-1)
			if n == 2 {
				return state, errors.New("degraded scan")
			}
			return state, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- runWTFDaemon(ctx, home, deps) }()
	waitForScans(t, &scans, 1)
	if err := requestWTFScan(home); err != nil {
		t.Fatal(err)
	}
	requests <- time.Unix(nowUnix.Add(1), 0)
	waitForScans(t, &scans, 2)
	if err := requestWTFScan(home); err != nil {
		t.Fatal(err)
	}
	if err := requestWTFScan(home); err != nil {
		t.Fatal(err)
	}
	requests <- time.Unix(nowUnix.Add(1), 0)
	waitForScans(t, &scans, 3)
	for want := int32(4); want <= 6; want++ {
		now := nowUnix.Add(1)
		ticks <- time.Unix(now, 0)
		waitForScans(t, &scans, want)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop")
	}
	if maxActive.Load() != 1 {
		t.Fatalf("max concurrent scans = %d", maxActive.Load())
	}
	if _, err := os.Stat(wtfScanRequestPath(home)); !os.IsNotExist(err) {
		t.Fatalf("request remains: %v", err)
	}
	health, ok := readWTFHealth(home)
	if !ok || health.LastAttemptedScan != nowUnix.Load() || health.LastSuccessfulScan != nowUnix.Load() || health.LastError != "" {
		t.Fatalf("health = %#v, %v", health, ok)
	}
	state, err := loadWTFState(home, 0)
	if err != nil || state.UpdatedAt != 6 {
		t.Fatalf("state = %#v, %v", state, err)
	}
}

func waitForScans(t *testing.T, scans *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for scans.Load() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if scans.Load() != want {
		t.Fatalf("scans = %d, want %d", scans.Load(), want)
	}
}
