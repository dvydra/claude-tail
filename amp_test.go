package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAmpListUsesSupportedCLIAndCaches(t *testing.T) {
	home := t.TempDir()
	old := ampRun
	defer func() { ampRun = old }()
	var got []string
	ampRun = func(_ context.Context, args ...string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte(`[{"id":"T-one","title":"One","updated":"2026-09-25T10:00:00Z","tree":"file:///tmp/repo","messageCount":2},{"id":"bad"}]`), nil
	}
	threads, err := ampList(home, false)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads=%+v err=%v", threads, err)
	}
	want := []string{"threads", "list", "--json", "--include-archived"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%q want=%q", got, want)
	}
	ampRun = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("offline") }
	cached, err := ampList(home, false)
	if err == nil || !reflect.DeepEqual(cached, threads) {
		t.Fatalf("cached=%+v err=%v", cached, err)
	}
}

func TestAmpTreeColdStartUsesShortInventoryDeadline(t *testing.T) {
	home := t.TempDir()
	old := ampRun
	t.Cleanup(func() { ampRun = old })
	ampRun = func(ctx context.Context, args ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("Amp inventory command has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining > ampFocusRefreshTimeout+100*time.Millisecond {
			t.Fatalf("Amp inventory deadline = %s, want at most %s", remaining, ampFocusRefreshTimeout)
		}
		return []byte(`[]`), nil
	}

	if _, err := buildAmpTree(home, "/tmp/repo", 7, time.Now().Unix(), false); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAmpInventoryReportsFailureWithoutCache(t *testing.T) {
	old := ampRun
	t.Cleanup(func() { ampRun = old })
	ampRun = func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("amp executable not found")
	}

	err := checkAmpInventory(t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "cannot list Amp threads") {
		t.Fatalf("checkAmpInventory error = %v", err)
	}
}

func TestAmpExportCacheFallback(t *testing.T) {
	home := t.TempDir()
	old := ampRun
	defer func() { ampRun = old }()
	ampRun = func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"v":4,"id":"T-one","title":"One","env":{"initial":{"workingDirectory":"file:///tmp/a%20b"}},"messages":[]}`), nil
	}
	ex, err := ampExportThread(home, "T-one", false)
	if err != nil || ex.V != 4 || ex.cwd() != "/tmp/a b" {
		t.Fatalf("export=%+v cwd=%q err=%v", ex, ex.cwd(), err)
	}
	ampRun = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("offline") }
	cached, err := ampExportThread(home, "T-one", false)
	if err == nil || cached.V != 4 {
		t.Fatalf("cached=%+v err=%v", cached, err)
	}
}

func TestAmpExportHonorsCallerTimeout(t *testing.T) {
	home := t.TempDir()
	old := ampRun
	defer func() { ampRun = old }()
	ampRun = func(ctx context.Context, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	started := time.Now()
	_, err := ampExportThreadWithin(home, "T-slow", false, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestAmpCacheOnlyDoesNotRunCommand(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(ampCacheDir(home), "threads.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"id":"T-cached","updated":"2026-09-25T10:00:00Z"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := ampRun
	defer func() { ampRun = old }()
	ampRun = func(context.Context, ...string) ([]byte, error) { t.Fatal("command called"); return nil, nil }
	threads, err := ampList(home, true)
	if err != nil || len(threads) != 1 || threads[0].ID != "T-cached" {
		t.Fatalf("threads=%+v err=%v", threads, err)
	}
}

func TestAmpFilePathRejectsNonFileURI(t *testing.T) {
	if got := ampFilePath("https://example.com/repo"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestNewestAmpThreadPrefersNewestExactCwd(t *testing.T) {
	threads := []ampThread{
		{ID: "T-global", Updated: "2026-09-25T12:00:00Z", Tree: "file:///elsewhere"},
		{ID: "T-old", Updated: "2026-09-25T10:00:00Z", Tree: "file:///work/repo"},
		{ID: "T-new", Updated: "2026-09-25T11:00:00Z", Tree: "file:///work/repo"},
	}
	if got, ok := newestAmpThread(threads, "/work/repo", false); !ok || got.ID != "T-new" {
		t.Fatalf("exact=%+v ok=%v", got, ok)
	}
	if got, ok := newestAmpThread(threads, "/missing", false); ok || got.ID != "" {
		t.Fatalf("unrelated exact=%+v ok=%v", got, ok)
	}
	if got, ok := newestAmpThread(threads, "/missing", true); !ok || got.ID != "T-global" {
		t.Fatalf("fallback=%+v ok=%v", got, ok)
	}
}

func TestClassifyAmpSnapshot(t *testing.T) {
	prior := []byte("one\ntwo\n")
	if got := classifyAmpSnapshot(prior, append([]byte(nil), prior...)); got != ampSnapshotUnchanged {
		t.Fatalf("unchanged=%v", got)
	}
	if got := classifyAmpSnapshot(prior, []byte("one\ntwo\nthree\n")); got != ampSnapshotAppend {
		t.Fatalf("append=%v", got)
	}
	if got := classifyAmpSnapshot(prior, []byte("one\nchanged\n")); got != ampSnapshotRewrite {
		t.Fatalf("rewrite=%v", got)
	}
}

func TestAmpSnapshotRefreshesOnLocalMessageEvent(t *testing.T) {
	home := t.TempDir()
	logPath := filepath.Join(home, ".cache", "amp", "logs", "threads", "T-fast.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	old := ampRun
	t.Cleanup(func() { ampRun = old })
	var calls atomic.Int32
	ampRun = func(context.Context, ...string) ([]byte, error) {
		call := calls.Add(1)
		text := "first"
		if call > 1 {
			text = "second"
		}
		return []byte(fmt.Sprintf(`{"v":%d,"id":"T-fast","messages":[{"role":"assistant","createdAt":"2026-09-25T10:00:00Z","state":{"type":"complete"},"content":[{"type":"text","text":%q}]}]}`, call, text)), nil
	}

	path, stop, sourceErrors, err := startAmpSnapshot(home, "T-fast", false)
	if err != nil {
		t.Fatal(err)
	}
	defer stopAmpSnapshot(stop, sourceErrors)
	if err := os.WriteFile(logPath, []byte(`{"type":"message_added"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(path); readErr == nil && bytes.Contains(data, []byte("second")) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("snapshot did not refresh within 500ms of the local message event")
}

func TestAmpSnapshotReportsRepeatedRefreshErrorOnce(t *testing.T) {
	home := t.TempDir()
	logPath := filepath.Join(home, ".cache", "amp", "logs", "threads", "T-errors.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	old := ampRun
	t.Cleanup(func() { ampRun = old })
	var calls atomic.Int32
	ampRun = func(context.Context, ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return []byte(`{"v":1,"id":"T-errors","messages":[]}`), nil
		}
		return nil, errors.New("offline")
	}

	_, stop, sourceErrors, err := startAmpSnapshot(home, "T-errors", false)
	if err != nil {
		t.Fatal(err)
	}
	defer stopAmpSnapshot(stop, sourceErrors)
	appendEvent := func() {
		f, openErr := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, _ = f.WriteString(`{"type":"message_added"}` + "\n")
		_ = f.Close()
	}
	appendEvent()
	select {
	case got := <-sourceErrors:
		if got == nil || !strings.Contains(got.Error(), "offline") {
			t.Fatalf("source error = %v", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("refresh error was not reported")
	}
	appendEvent()
	select {
	case got := <-sourceErrors:
		t.Fatalf("repeated refresh error was reported again: %v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func stopAmpSnapshot(stop chan struct{}, sourceErrors <-chan error) {
	close(stop)
	for range sourceErrors {
	}
}

func TestMaterializeAmpExportFile(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(t.TempDir(), "thread.json")
	body := `{"id":"T-file","env":{"initial":{"workingDirectory":"file:///tmp/repo"}},"messages":[{"role":"user","createdAt":"2026-09-25T10:00:00Z","content":[{"type":"text","text":"hello"}]}]}`
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectAgentForFile(home, source); got != AgentAmp {
		t.Fatalf("agent=%q", got)
	}
	path, ex, err := materializeAmpExportFile(home, source)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if ex.ID != "T-file" || ex.cwd() != "/tmp/repo" {
		t.Fatalf("export=%+v", ex)
	}
	lines := splitLines(mustReadFile(t, path))
	if len(lines) != 1 || len(normalizeAmp(lines[0], time.UTC)) != 1 {
		t.Fatalf("materialized lines=%q", lines)
	}
	if got := sniffAgent(lines[0]); got != AgentAmp {
		t.Fatalf("materialized agent=%q", got)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseAmpTopEventToleratesThreadIDAndSkipsMalformed(t *testing.T) {
	threads, ok := parseAmpTopEvent([]byte(`{"updatedAt":"2026-09-25T10:00:00Z","threads":[{"threadID":"T-remote","title":"Deploy","status":"running","executorType":"orb","workingDirectory":"file:///srv/app"},{"id":"bad id"}],"reconnecting":false}`))
	if !ok || len(threads) != 1 {
		t.Fatalf("threads=%+v ok=%v", threads, ok)
	}
	if threads[0].id() != "T-remote" || threads[0].status() != "busy" || threads[0].cwd() != "/srv/app" {
		t.Fatalf("thread=%+v", threads[0])
	}
	if _, ok := parseAmpTopEvent([]byte(`not json`)); ok {
		t.Fatal("malformed event accepted")
	}
	if _, ok := parseAmpTopEvent([]byte(`{"threads":[],"reconnecting":true}`)); ok {
		t.Fatal("reconnecting event should retain the prior active set")
	}
}

func TestParseAmpTopEventToleratesAliases(t *testing.T) {
	threads, ok := parseAmpTopEvent([]byte(`{"threads":[{"threadId":"T-alias","agentState":"working","executor":"runner","treeURI":"file:///srv/alias"}]}`))
	if !ok || len(threads) != 1 {
		t.Fatalf("threads=%+v ok=%v", threads, ok)
	}
	if threads[0].id() != "T-alias" || threads[0].status() != "busy" || threads[0].cwd() != "/srv/alias" || threads[0].Executor != "runner" {
		t.Fatalf("thread=%+v", threads[0])
	}
}

func TestParseAmpTopEventDropsDisconnectedRecentThreads(t *testing.T) {
	threads, ok := parseAmpTopEvent([]byte(`{"threads":[{"id":"T-stale","status":"28m","working":false,"executorConnected":false},{"id":"T-working","status":"working","working":true,"executorConnected":true},{"id":"T-idle","status":"2m","working":false,"executorConnected":true}]}`))
	if !ok || len(threads) != 2 {
		t.Fatalf("threads=%+v ok=%v", threads, ok)
	}
	if threads[0].id() != "T-working" || threads[0].status() != "busy" {
		t.Fatalf("working=%+v", threads[0])
	}
	if threads[1].id() != "T-idle" || threads[1].status() != "idle" {
		t.Fatalf("idle=%+v", threads[1])
	}
}

func TestMergeLiveAmpSessionsKeepsLocalAndAddsRemote(t *testing.T) {
	home := t.TempDir()
	local := []liveSession{{Agent: AgentAmp, PID: 42, SessionID: "T-local", Cwd: "/tmp/local", Status: "idle", Name: "Local"}}
	active := []ampActiveThread{
		{ID: "T-local", State: "streaming", Title: "Local live"},
		{ID: "T-orb", State: "idle", Title: "Remote", ExecutorType: "orb", Tree: "file:///workspace/repo"},
	}
	got := mergeLiveAmpSessions(home, local, active)
	if len(got) != 2 {
		t.Fatalf("sessions=%+v", got)
	}
	if got[0].PID != 42 || got[0].Status != "busy" || got[0].Name != "Local live" {
		t.Fatalf("local=%+v", got[0])
	}
	if got[1].SessionID != "T-orb" || got[1].PID != 0 || got[1].Kind != "remote" || got[1].Cwd != "/workspace/repo" {
		t.Fatalf("remote=%+v", got[1])
	}
}

func TestAmpChildChannelsMaterializeExportedThread(t *testing.T) {
	home := t.TempDir()
	child := `{"id":"T-child","title":"Child export","meta":{"lastKnownAgentState":{"state":"idle"}},"messages":[{"role":"assistant","state":{"type":"complete"},"content":[{"type":"text","text":"child answer"}]}]}`
	if err := writeAmpCache(filepath.Join(ampCacheDir(home), "exports", "T-child.json"), []byte(child)); err != nil {
		t.Fatal(err)
	}
	var parent ampExport
	if err := json.Unmarshal([]byte(`{"id":"T-parent","messages":[{"role":"assistant","createdAt":"2026-09-25T10:00:00Z","content":[{"type":"tool_use","id":"create-1","name":"create_thread","input":{"title":"Investigate"}}]},{"role":"user","content":[{"type":"tool_result","toolUseID":"create-1","run":{"result":{"output":{"threadID":"T-child"}}}}]}]}`), &parent); err != nil {
		t.Fatal(err)
	}
	channels := ampChildChannels(home, parent, true)
	if len(channels) != 1 {
		t.Fatalf("channels=%+v", channels)
	}
	ch := channels[0]
	if ch.Agent != AgentAmp || ch.ThreadID != "T-child" || ch.Description != "Investigate" || ch.State != "idle" || !isFile(ch.Path) {
		t.Fatalf("channel=%+v", ch)
	}
	theme, err := loadTheme("tokyo-night", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := stripANSI(strings.Join(renderChannel(ch, home, theme), "\n")); !strings.Contains(got, "child answer") {
		t.Fatalf("render=%q", got)
	}
}
