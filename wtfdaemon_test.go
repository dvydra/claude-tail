package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestWTFAgentPlist(t *testing.T) {
	plist := wtfAgentPlist("/usr/local/bin/entire-tail", "/Users/d/Library/Application Support/entire-tail/wtf/daemon.log", "/Users/d/go/bin:/usr/bin")
	for _, want := range []string{
		"<key>EnvironmentVariables</key>",
		"<key>PATH</key><string>/Users/d/go/bin:/usr/bin</string>",
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
	// launchd starts agents with /usr/bin:/bin:/usr/sbin:/sbin, where neither
	// entire nor amp lives; with no PATH to hand on, the plist says nothing.
	if bare := wtfAgentPlist("/b", "/l", ""); strings.Contains(bare, "EnvironmentVariables") {
		t.Errorf("empty PATH still wrote an environment:\n%s", bare)
	}
}

func TestWTFAgentPlistEscapesInterpolatedStrings(t *testing.T) {
	plist := wtfAgentPlist("/Applications/A&B/<current>/entire-tail", "/tmp/A&B/<logs>/daemon.log", "/opt/A&B/bin")
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

// One broken source (a stale Amp export, one worktree git can't read) used
// to fail every scan, so the footer said "last successful scan never" while
// the daemon was saving fresh state every two seconds.
func TestRecordWTFScan(t *testing.T) {
	partial := &wtfScanError{errors: []error{errors.New("amp transcript T-1: invalid")}, localFailed: true}

	var health wtfHealth
	recordWTFScan(&health, 10, partial, nil)
	if health.LastSuccessfulScan != 10 || health.LastError != "" || !reflect.DeepEqual(health.Degraded, []string{"amp transcript T-1: invalid"}) {
		t.Fatalf("partial scan: %#v", health)
	}

	recordWTFScan(&health, 20, nil, nil)
	if health.LastSuccessfulScan != 20 || health.LastError != "" || health.Degraded != nil {
		t.Fatalf("clean scan: %#v", health)
	}

	recordWTFScan(&health, 30, partial, errors.New("disk full"))
	if health.LastSuccessfulScan != 20 || !strings.Contains(health.LastError, "disk full") {
		t.Fatalf("unsaved scan counted as success: %#v", health)
	}

	recordWTFScan(&health, 40, errors.New("inventory failed"), nil)
	if health.LastSuccessfulScan != 20 || health.LastError != "inventory failed" {
		t.Fatalf("failed scan counted as success: %#v", health)
	}
}

func TestWTFHealthRoundTripsAndChecksProcessIdentity(t *testing.T) {
	home := t.TempDir()
	want := wtfHealth{PID: 42, Version: "test", StartedAt: 10, LastAttemptedScan: 11, LastSuccessfulScan: 12, LastError: "failed", Degraded: []string{"one source"}}
	if err := writeWTFHealth(home, want); err != nil {
		t.Fatal(err)
	}
	got, ok := readWTFHealth(home)
	if !ok || !reflect.DeepEqual(got, want) {
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

func TestReadWTFHealthFileReportsMalformedAndIgnoresMissing(t *testing.T) {
	home := t.TempDir()
	if health, err := readWTFHealthFile(home); err != nil || !reflect.DeepEqual(health, wtfHealth{}) {
		t.Fatalf("missing health = %+v, %v", health, err)
	}
	if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtfHealthPath(home), []byte(`{"pid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if health, err := readWTFHealthFile(home); err == nil || !reflect.DeepEqual(health, wtfHealth{}) || !strings.Contains(err.Error(), "read wtf health") {
		t.Fatalf("malformed health = %+v, %v", health, err)
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

func TestWTFDaemonDelivery(t *testing.T) {
	home := t.TempDir()
	withWTFProcessFakes(t, 708, func(pid int) bool { return pid == 708 }, func(int) string { return "entire-tail" })
	ticks := make(chan time.Time, 4)
	requests := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	base, finding := notificationState()
	var scanCount atomic.Int32
	var notifyCount atomic.Int32
	var inSave atomic.Bool
	var saveBoundary sync.Mutex
	sendingSaveEntered := make(chan struct{})
	releaseSendingSave := make(chan struct{})
	var gateSendingSave sync.Once
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	terminalSnapshots := make(chan wtfDelivery, 4)
	finalTerminalSaveEntered := make(chan struct{})
	releaseFinalTerminalSave := make(chan struct{})
	var gateFinalTerminalSave sync.Once
	var logMu sync.Mutex
	var events []wtfDeliveryTestEvent
	terminalSeen := make(map[string]bool)
	deps := wtfDaemonDeps{
		Load:         func(string, int64) (wtfState, error) { return newWTFState(1), nil },
		Now:          func() time.Time { return time.Unix(100+int64(scanCount.Load()), 0) },
		After:        func(time.Duration) <-chan time.Time { return ticks },
		RequestAfter: func(time.Duration) <-chan time.Time { return requests },
		Scan: func(_ context.Context, _ string, state wtfState, _ wtfScanDeps) (wtfState, error) {
			n := scanCount.Add(1)
			switch n {
			case 1, 2:
				return base, nil
			case 3:
				base.Findings = mergeWTFFindings(base.Findings, nil, 102)
				return base, nil
			case 4:
				base.Findings = mergeWTFFindings(base.Findings, map[string]wtfFinding{finding.ID: finding}, 103)
				return base, nil
			default:
				return state, fmt.Errorf("unexpected scan %d", n)
			}
		},
		Save: func(_ string, state wtfState) error {
			saveBoundary.Lock()
			defer saveBoundary.Unlock()
			if !inSave.CompareAndSwap(false, true) {
				t.Error("concurrent Save calls")
			}
			defer inSave.Store(false)
			logMu.Lock()
			events = append(events, savedWTFDeliveryTestEvent(state, finding.ID))
			current := state.Findings[finding.ID]
			hasSending := false
			for channel, status := range current.Delivery {
				if status.State == "sending" {
					hasSending = true
				}
				key := fmt.Sprintf("%d/%s/%d", current.Occurrence, channel, status.Attempts)
				if status.State == "sent" && !terminalSeen[key] {
					terminalSeen[key] = true
					terminalSnapshots <- wtfDelivery{FindingID: finding.ID, Channel: channel, Occurrence: current.Occurrence, Attempt: status.Attempts}
				}
			}
			logMu.Unlock()
			if current.Occurrence == 2 && current.Delivery["mac"].State == "sent" && current.Delivery["session:"+finding.Challenger].State == "sent" {
				gateFinalTerminalSave.Do(func() {
					close(finalTerminalSaveEntered)
					<-releaseFinalTerminalSave
				})
			}
			if hasSending {
				gateSendingSave.Do(func() {
					close(sendingSaveEntered)
					<-releaseSendingSave
				})
			}
			return nil
		},
		Notify: func(_ context.Context, state wtfState, delivery wtfDelivery) wtfDeliveryResult {
			saveBoundary.Lock()
			if inSave.Load() {
				t.Errorf("notifier started before Save returned: delivery = %#v", delivery)
			}
			status := state.Findings[delivery.FindingID].Delivery[delivery.Channel]
			if status.State != "sending" || status.Attempts != delivery.Attempt {
				t.Errorf("notifier snapshot status = %#v, delivery = %#v", status, delivery)
			}
			logMu.Lock()
			events = append(events, wtfDeliveryTestEvent{kind: "notify", occurrence: delivery.Occurrence, channel: delivery.Channel, attempt: delivery.Attempt})
			logMu.Unlock()
			n := notifyCount.Add(1)
			saveBoundary.Unlock()
			if n == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			return wtfDeliveryResult{State: "sent"}
		},
	}
	done := make(chan error, 1)
	go func() { done <- runWTFDaemon(ctx, home, deps) }()
	select {
	case <-sendingSaveEntered:
	case <-time.After(time.Second):
		t.Fatal("sending save did not start")
	}
	if got := notifyCount.Load(); got != 0 {
		t.Fatalf("notifiers started while sending save was blocked: %d", got)
	}
	close(releaseSendingSave)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}

	// A blocked notifier must not delay the next 2-second scan.
	ticks <- time.Now()
	waitForScans(t, &scanCount, 2)
	logMu.Lock()
	assertWTFDeliveryCallCounts(t, events, finding.ID, 1)
	logMu.Unlock()
	close(releaseFirst)
	waitForWTFTerminalSnapshots(t, terminalSnapshots, 2)
	ticks <- time.Now()
	waitForScans(t, &scanCount, 3)
	ticks <- time.Now()
	waitForScans(t, &scanCount, 4)
	waitForWTFTerminalSnapshots(t, terminalSnapshots, 2)
	select {
	case <-finalTerminalSaveEntered:
	case <-time.After(time.Second):
		t.Fatal("final terminal save did not start")
	}

	cancel()
	select {
	case err := <-done:
		t.Fatalf("daemon returned while final terminal save was blocked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFinalTerminalSave)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop")
	}
	logMu.Lock()
	defer logMu.Unlock()
	if got := notifyCount.Load(); got != 4 {
		t.Fatalf("notifier calls = %d, want 4", got)
	}
	for _, occurrence := range []int{1, 2} {
		for _, channel := range []string{"mac", "session:" + finding.Challenger} {
			assertWTFDeliverySequence(t, events, occurrence, channel, 1, "sent")
			if got := countWTFDeliveryCalls(events, occurrence, channel); got != 1 {
				t.Fatalf("occurrence %d channel %q invoked %d times, want 1", occurrence, channel, got)
			}
		}
	}
}

type wtfDeliveryTestEvent struct {
	kind       string
	occurrence int
	channel    string
	attempt    int
	statuses   map[string]wtfDeliveryStatus
}

func savedWTFDeliveryTestEvent(state wtfState, findingID string) wtfDeliveryTestEvent {
	finding := state.Findings[findingID]
	statuses := make(map[string]wtfDeliveryStatus, len(finding.Delivery))
	for channel, status := range finding.Delivery {
		statuses[channel] = status
	}
	return wtfDeliveryTestEvent{kind: "save", occurrence: finding.Occurrence, statuses: statuses}
}

func assertWTFDeliverySequence(t *testing.T, events []wtfDeliveryTestEvent, occurrence int, channel string, attempt int, terminal string) {
	t.Helper()
	stage := 0
	for _, event := range events {
		if event.occurrence != occurrence {
			continue
		}
		status := event.statuses[channel]
		switch stage {
		case 0:
			if event.kind == "save" && status.State == "" {
				stage++
			}
		case 1:
			if event.kind == "save" && status.State == "sending" && status.Attempts == attempt {
				stage++
			}
		case 2:
			if event.kind == "notify" && event.channel == channel && event.attempt == attempt {
				stage++
			}
		case 3:
			if event.kind == "save" && status.State == terminal && status.Attempts == attempt {
				stage++
			}
		}
	}
	if stage != 4 {
		t.Fatalf("occurrence %d channel %q reached sequence stage %d, events = %#v", occurrence, channel, stage, events)
	}
}

func assertWTFDeliveryCallCounts(t *testing.T, events []wtfDeliveryTestEvent, findingID string, occurrence int) {
	t.Helper()
	for _, channel := range []string{"mac", "session:amp:challenger"} {
		if count := countWTFDeliveryCalls(events, occurrence, channel); count > 1 {
			t.Fatalf("finding %q occurrence %d channel %q invoked %d times while in flight", findingID, occurrence, channel, count)
		}
	}
}

func countWTFDeliveryCalls(events []wtfDeliveryTestEvent, occurrence int, channel string) int {
	count := 0
	for _, event := range events {
		if event.kind == "notify" && event.occurrence == occurrence && event.channel == channel {
			count++
		}
	}
	return count
}

func waitForWTFTerminalSnapshots(t *testing.T, terminals <-chan wtfDelivery, count int) {
	t.Helper()
	for range count {
		select {
		case <-terminals:
		case <-time.After(time.Second):
			t.Fatal("terminal delivery snapshot was not observed")
		}
	}
}

func TestWTFDaemonDeliveryChannelsAreIsolated(t *testing.T) {
	home := t.TempDir()
	withWTFProcessFakes(t, 710, func(pid int) bool { return pid == 710 }, func(int) string { return "entire-tail" })
	state, finding := notificationState()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := make(chan struct{})
	release := make(chan struct{})
	terminals := make(chan wtfDelivery, 2)
	var mu sync.Mutex
	var saved wtfState
	calls := make(map[string]int)
	terminalSeen := make(map[string]bool)
	deps := wtfDaemonDeps{
		Load:         func(string, int64) (wtfState, error) { return newWTFState(1), nil },
		Now:          func() time.Time { return time.Unix(100, 0) },
		After:        func(time.Duration) <-chan time.Time { return make(chan time.Time) },
		RequestAfter: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
		Scan:         func(context.Context, string, wtfState, wtfScanDeps) (wtfState, error) { return state, nil },
		Save: func(_ string, got wtfState) error {
			mu.Lock()
			saved, _ = cloneWTFState(got)
			for channel, status := range got.Findings[finding.ID].Delivery {
				if (status.State == "sent" || status.State == "failed") && !terminalSeen[channel] {
					terminalSeen[channel] = true
					select {
					case terminals <- wtfDelivery{Channel: channel}:
					default:
					}
				}
			}
			mu.Unlock()
			return nil
		},
		Notify: func(_ context.Context, _ wtfState, delivery wtfDelivery) wtfDeliveryResult {
			mu.Lock()
			calls[delivery.Channel]++
			mu.Unlock()
			if delivery.Channel == "mac" {
				close(blocked)
				<-release
				return wtfDeliveryResult{State: "failed", Error: "mac unavailable"}
			}
			return wtfDeliveryResult{State: "sent"}
		},
	}
	done := make(chan error, 1)
	go func() { done <- runWTFDaemon(ctx, home, deps) }()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("blocked channel did not start")
	}
	waitForWTFTerminalSnapshots(t, terminals, 1)
	mu.Lock()
	if got := saved.Findings[finding.ID].Delivery["session:"+finding.Challenger].State; got != "sent" {
		t.Fatalf("unblocked channel state = %q, want sent", got)
	}
	mu.Unlock()
	close(release)
	waitForWTFTerminalSnapshots(t, terminals, 1)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["mac"] != 1 || calls["session:"+finding.Challenger] != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	delivery := saved.Findings[finding.ID].Delivery
	if delivery["mac"].State != "failed" || delivery["mac"].LastError != "mac unavailable" || delivery["session:"+finding.Challenger].State != "sent" {
		t.Fatalf("terminal delivery states = %#v", delivery)
	}
	if current := saved.Findings[finding.ID]; !current.Active || current.Kind != finding.Kind || current.TrailKey != finding.TrailKey {
		t.Fatalf("channel failure hid finding: %#v", current)
	}
	if warning := warningText(saved, saved.Findings[finding.ID]); !strings.Contains(warning, "Stop") || !strings.Contains(warning, "check with Daniel") {
		t.Fatalf("unsafe warning after channel failure: %q", warning)
	}
}

func waitForDeliveries(t *testing.T, deliveries *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for deliveries.Load() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if deliveries.Load() != want {
		t.Fatalf("deliveries = %d, want %d", deliveries.Load(), want)
	}
}

func TestWTFDaemonDeliveryBoundsWorkersAndLeavesQueueFullPending(t *testing.T) {
	home := t.TempDir()
	withWTFProcessFakes(t, 709, func(pid int) bool { return pid == 709 }, func(int) string { return "entire-tail" })
	state, template := notificationState()
	state.Findings = make(map[string]wtfFinding)
	for i := range 6 {
		finding := template
		finding.ID = fmt.Sprintf("finding-%d", i)
		state.Findings[finding.ID] = finding
	}
	ctx, cancel := context.WithCancel(context.Background())
	var active, maximum, started atomic.Int32
	var saved wtfState
	deps := wtfDaemonDeps{
		Load:         func(string, int64) (wtfState, error) { return newWTFState(1), nil },
		Save:         func(_ string, got wtfState) error { saved = got; return nil },
		Now:          func() time.Time { return time.Unix(100, 0) },
		After:        func(time.Duration) <-chan time.Time { return make(chan time.Time) },
		RequestAfter: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
		Scan:         func(context.Context, string, wtfState, wtfScanDeps) (wtfState, error) { return state, nil },
		Notify: func(ctx context.Context, _ wtfState, _ wtfDelivery) wtfDeliveryResult {
			current := active.Add(1)
			for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
			}
			started.Add(1)
			<-ctx.Done()
			active.Add(-1)
			return wtfDeliveryResult{State: "unknown"}
		},
	}
	done := make(chan error, 1)
	go func() { done <- runWTFDaemon(ctx, home, deps) }()
	waitForDeliveries(t, &started, 4)
	if maximum.Load() > 4 {
		t.Fatalf("maximum concurrent notifiers = %d", maximum.Load())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not cancel delivery workers")
	}
	sending, pending := 0, 0
	for _, finding := range saved.Findings {
		for _, channel := range []string{"mac", "session:" + finding.Challenger} {
			if finding.Delivery[channel].State == "sending" {
				sending++
			} else {
				pending++
			}
		}
	}
	if sending != 4 || pending != 8 {
		t.Fatalf("delivery states after full queue: sending=%d pending=%d", sending, pending)
	}
}
