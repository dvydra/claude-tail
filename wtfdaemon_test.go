package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestWTFAgentPlist(t *testing.T) {
	plist := wtfAgentPlist("/usr/local/bin/entire-tail", "/Users/d/Library/Application Support/entire-tail/wtf/daemon.log")
	for _, want := range []string{
		"<key>Label</key><string>" + wtfAgentLabel + "</string>",
		"<string>/usr/local/bin/entire-tail</string>",
		"<string>wtf</string>",
		"<string>daemon</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<key>ThrottleInterval</key><integer>10</integer>",
		"<key>StandardOutPath</key><string>/Users/d/Library/Application Support/entire-tail/wtf/daemon.log</string>",
		"<key>StandardErrorPath</key><string>/Users/d/Library/Application Support/entire-tail/wtf/daemon.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
}

func TestWTFAgentPlistEscapesInterpolatedStrings(t *testing.T) {
	plist := wtfAgentPlist("/Applications/A&B/<current>/entire-tail", "/tmp/A&B/<logs>/daemon.log")
	var document any
	if err := xml.Unmarshal([]byte(plist), &document); err != nil {
		t.Fatalf("generated plist is not XML: %v\n%s", err, plist)
	}
	for _, want := range []string{"A&amp;B", "&lt;current&gt;", "&lt;logs&gt;"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing escaped value %q:\n%s", want, plist)
		}
	}
}

func stubWTFAgentLifecycle(t *testing.T, health wtfHealth, healthy bool) (*[]string, *int) {
	t.Helper()
	oldLoad, oldUnload, oldWait, oldDaemon := wtfAgentLoad, wtfAgentUnload, wtfAgentWait, wtfDaemonRun
	var calls []string
	daemonCalls := 0
	wtfAgentLoad = func(path string) error { calls = append(calls, "load "+path); return nil }
	wtfAgentUnload = func(path string) error { calls = append(calls, "unload "+path); return nil }
	wtfAgentWait = func(string, time.Duration) (wtfHealth, bool) { return health, healthy }
	wtfDaemonRun = func(context.Context, string, wtfDaemonDeps) error { daemonCalls++; return nil }
	t.Cleanup(func() {
		wtfAgentLoad, wtfAgentUnload, wtfAgentWait, wtfDaemonRun = oldLoad, oldUnload, oldWait, oldDaemon
	})
	return &calls, &daemonCalls
}

func TestRunWTFCommandInstallReloadsAndReportsPID(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "entire-tail")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	calls, _ := stubWTFAgentLifecycle(t, wtfHealth{PID: 4242}, true)
	var out bytes.Buffer
	if err := runWTFCommand([]string{"install"}, home, &out); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"unload " + wtfAgentPath(home), "load " + wtfAgentPath(home)}
	if strings.Join(*calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls = %v", *calls)
	}
	plist, err := os.ReadFile(wtfAgentPath(home))
	if err != nil || !strings.Contains(string(plist), bin) {
		t.Fatalf("plist = %q, %v", plist, err)
	}
	if !strings.Contains(out.String(), "pid 4242") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunWTFCommandInstallReportsFailedHealth(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "entire-tail"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	stubWTFAgentLifecycle(t, wtfHealth{}, false)
	err := runWTFCommand([]string{"install"}, home, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), wtfLogPath(home)) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunWTFCommandInstallStopsWhenUnloadFails(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "entire-tail"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	calls, _ := stubWTFAgentLifecycle(t, wtfHealth{}, false)
	wtfAgentUnload = func(path string) error {
		*calls = append(*calls, "unload "+path)
		return errors.New("unload failed")
	}
	err := runWTFCommand([]string{"install"}, home, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unload failed") {
		t.Fatalf("error = %v", err)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "unload ") {
		t.Fatalf("calls after unload failure = %v", *calls)
	}
}

func TestRunWTFCommandInstallRemovesStaleHealthBeforeLoadAndPreservesState(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "entire-tail"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	if err := writeWTFHealth(home, wtfHealth{PID: 99}); err != nil {
		t.Fatal(err)
	}
	if err := saveWTFState(home, newWTFState(1)); err != nil {
		t.Fatal(err)
	}
	stubWTFAgentLifecycle(t, wtfHealth{PID: 100}, true)
	assertClean := func(stage string) {
		if _, err := os.Stat(wtfHealthPath(home)); !os.IsNotExist(err) {
			t.Fatalf("%s saw stale health: %v", stage, err)
		}
		if _, err := os.Stat(wtfStatePath(home)); err != nil {
			t.Fatalf("%s lost state.json: %v", stage, err)
		}
	}
	wtfAgentLoad = func(string) error { assertClean("load"); return nil }
	wtfAgentWait = func(string, time.Duration) (wtfHealth, bool) {
		assertClean("wait")
		return wtfHealth{PID: 100}, true
	}
	if err := runWTFCommand([]string{"install"}, home, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunWTFCommandInstallStopsWhenStaleHealthRemovalFails(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "entire-tail"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	if err := os.MkdirAll(filepath.Join(wtfHealthPath(home), "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	calls, _ := stubWTFAgentLifecycle(t, wtfHealth{}, false)
	err := runWTFCommand([]string{"install"}, home, &bytes.Buffer{})
	if err == nil {
		t.Fatal("install succeeded despite health removal failure")
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "unload ") {
		t.Fatalf("calls after remove failure = %v", *calls)
	}
}

func TestRunWTFCommandStatusStates(t *testing.T) {
	for _, test := range []struct {
		name               string
		installed, running bool
		want               string
	}{
		{"not installed", false, false, "not installed"},
		{"stale", true, false, "stale health"},
		{"running", true, true, "running (pid 77)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if test.installed {
				if err := os.MkdirAll(filepath.Dir(wtfAgentPath(home)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(wtfAgentPath(home), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			withWTFProcessFakes(t, 1, func(pid int) bool { return test.running && pid == 77 }, func(int) string { return "entire-tail wtf daemon" })
			if test.installed {
				if err := writeWTFHealth(home, wtfHealth{PID: 77}); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := runWTFCommand([]string{"status"}, home, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), test.want) {
				t.Fatalf("output = %q, want %q", out.String(), test.want)
			}
		})
	}
}

func TestRunWTFCommandUninstallPreservesState(t *testing.T) {
	home := t.TempDir()
	stubWTFAgentLifecycle(t, wtfHealth{}, false)
	if _, err := installWTFAgent(home, "/usr/local/bin/entire-tail", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := writeWTFHealth(home, wtfHealth{PID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := saveWTFState(home, newWTFState(1)); err != nil {
		t.Fatal(err)
	}
	if err := runWTFCommand([]string{"uninstall"}, home, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{wtfAgentPath(home), wtfHealthPath(home)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s remains: %v", path, err)
		}
	}
	if _, err := os.Stat(wtfStatePath(home)); err != nil {
		t.Fatalf("state.json was removed: %v", err)
	}
	if err := runWTFCommand([]string{"uninstall"}, home, &bytes.Buffer{}); err != nil {
		t.Fatalf("repeated uninstall: %v", err)
	}
	if _, err := os.Stat(wtfStatePath(home)); err != nil {
		t.Fatalf("repeated uninstall removed state.json: %v", err)
	}
}

func TestRunWTFCommandDaemonAndUnknown(t *testing.T) {
	home := t.TempDir()
	_, daemonCalls := stubWTFAgentLifecycle(t, wtfHealth{}, false)
	if err := runWTFCommand([]string{"daemon"}, home, &bytes.Buffer{}); err != nil || *daemonCalls != 1 {
		t.Fatalf("daemon calls = %d, err = %v", *daemonCalls, err)
	}
	before, _ := os.ReadDir(home)
	err := runWTFCommand([]string{"bogus"}, home, &bytes.Buffer{})
	after, _ := os.ReadDir(home)
	if err == nil || !strings.Contains(err.Error(), "want install|status|uninstall") || len(before) != len(after) {
		t.Fatalf("error = %v, entries %d -> %d", err, len(before), len(after))
	}
}

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

func TestWTFLockReleaseSerializesWithReplacement(t *testing.T) {
	home := t.TempDir()
	withWTFProcessFakes(t, 101, func(int) bool { return false }, func(int) string { return "" })
	release, ok := acquireWTFLock(home)
	if !ok {
		t.Fatal("lock acquisition failed")
	}

	breaker, err := os.OpenFile(wtfLockPath(home)+".breaker", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(breaker.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		release()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("release did not wait for the breaker lock")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := os.Stat(wtfLockPath(home)); err != nil {
		t.Fatalf("release removed lock while replacement held breaker: %v", err)
	}
	if err := syscall.Flock(int(breaker.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := breaker.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("release remained blocked after breaker unlock")
	}
	if _, err := os.Stat(wtfLockPath(home)); !os.IsNotExist(err) {
		t.Fatalf("owned lock remains: %v", err)
	}
}

func TestWTFLockReleaseRefusesReplacedIdentityAfterWaiting(t *testing.T) {
	home := t.TempDir()
	withWTFProcessFakes(t, 101, func(int) bool { return false }, func(int) string { return "" })
	release, ok := acquireWTFLock(home)
	if !ok {
		t.Fatal("lock acquisition failed")
	}

	breaker, err := os.OpenFile(wtfLockPath(home)+".breaker", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(breaker.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		release()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("release did not wait for replacement")
	case <-time.After(50 * time.Millisecond):
	}
	const replacement = "202 replacement-token\n"
	if err := os.WriteFile(wtfLockPath(home), []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(breaker.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := breaker.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("release remained blocked after replacement")
	}
	got, err := os.ReadFile(wtfLockPath(home))
	if err != nil || string(got) != replacement {
		t.Fatalf("release removed replacement identity: %q, %v", got, err)
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
	for _, command := range []string{
		"another-process",
		"/tmp/not-entire-tail --entire-tail",
		"helper entire-tail",
		"/usr/bin/entire-tail-helper",
		"entire-tail",
		"entire-tail dashboard",
		"entire-tail tap daemon",
		"entire-tail wtf status",
		"entire-tail wtf daemon extra",
	} {
		wtfProcessName = func(int) string { return command }
		if wtfDaemonRunning(got) {
			t.Fatalf("command %q reported as WTF daemon", command)
		}
	}
	wtfProcessName = func(int) string { return "/opt/tools/entire-tail wtf daemon" }
	if !wtfDaemonRunning(got) {
		t.Fatal("exact executable path daemon reported stopped")
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

func TestWTFLockReplacesOtherEntireTailSubcommands(t *testing.T) {
	for _, command := range []string{"entire-tail dashboard", "entire-tail tap daemon", "entire-tail wtf status"} {
		t.Run(command, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(wtfLockPath(home), []byte("42\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			withWTFProcessFakes(t, 101, func(int) bool { return true }, func(int) string { return command })
			release, ok := acquireWTFLock(home)
			if !ok {
				t.Fatalf("command %q prevented stale lock replacement", command)
			}
			release()
		})
	}
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

func TestWTFLockIgnoresPreExistingBreakerFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtfLockPath(home)+".breaker", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	withWTFProcessFakes(t, 101, func(int) bool { return false }, func(int) string { return "" })
	release, ok := acquireWTFLock(home)
	if !ok {
		t.Fatal("pre-existing breaker file blocked lock acquisition")
	}
	release()
}

func TestWTFLockReplacesMalformedContents(t *testing.T) {
	for name, contents := range map[string]string{
		"empty":     "",
		"malformed": "not-a-pid token\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(wtfLockPath(home), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			withWTFProcessFakes(t, 101, func(int) bool { return false }, func(int) string { return "" })
			release, ok := acquireWTFLock(home)
			if !ok {
				t.Fatal("malformed lock was not replaced")
			}
			data, err := os.ReadFile(wtfLockPath(home))
			if err != nil || string(data) == contents {
				t.Fatalf("lock contents = %q, %v", data, err)
			}
			release()
		})
	}
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
