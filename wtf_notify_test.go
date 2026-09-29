package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSendAmpWarningExactCommand(t *testing.T) {
	target := wtfSession{Agent: AgentAmp, ID: "T-123", Active: true}
	warning := "stop now; don't retry\nsecond line"
	var name string
	var args []string
	got := sendAmpWarning(context.Background(), target, warning, func(_ context.Context, command string, argv ...string) ([]byte, error) {
		name, args = command, append([]string(nil), argv...)
		return []byte("secret command output"), nil
	})
	if got.State != "sent" || got.Error != "" {
		t.Fatalf("result = %#v, want sent", got)
	}
	if name != "amp" || !reflect.DeepEqual(args, []string{"threads", "continue", "T-123", "--execute", warning}) {
		t.Fatalf("command = %q %#v", name, args)
	}
}

func TestSendAmpWarningRejectsInvalidTargetsWithoutExec(t *testing.T) {
	for _, target := range []wtfSession{
		{Agent: AgentAmp, ID: "T-123"},
		{Agent: AgentClaude, ID: "T-123", Active: true},
		{Agent: AgentAmp, Active: true},
		{Agent: AgentAmp, ID: "not-a-thread", Active: true},
		{Agent: AgentAmp, ID: "T- 123", Active: true},
		{Agent: AgentAmp, ID: "T-/123", Active: true},
		{Agent: AgentAmp, ID: "T-123\n456", Active: true},
	} {
		called := false
		got := sendAmpWarning(context.Background(), target, "warning", func(context.Context, string, ...string) ([]byte, error) {
			called = true
			return nil, nil
		})
		if called || got.State != "failed" || got.Error == "" {
			t.Fatalf("target %#v: called = %v, result = %#v", target, called, got)
		}
	}
}

func TestSendAmpWarningFailureMappings(t *testing.T) {
	target := wtfSession{Agent: AgentAmp, ID: "T-123", Active: true}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"lookup", &exec.Error{Name: "amp", Err: exec.ErrNotFound}, "failed"},
		{"start", &os.PathError{Op: "fork/exec", Path: "/missing/amp", Err: os.ErrNotExist}, "failed"},
		{"command failure", errors.New("exit status 1\nwarning body must not leak"), "failed"},
		{"bare timeout", context.DeadlineExceeded, "failed"},
		{"bare cancellation", context.Canceled, "failed"},
		{"started timeout", &wtfExecError{Started: true, Err: context.DeadlineExceeded}, "unknown"},
		{"started cancellation", &wtfExecError{Started: true, Err: context.Canceled}, "unknown"},
		{"started command failure", &wtfExecError{Started: true, Err: errors.New("exit status 1")}, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sendAmpWarning(context.Background(), target, "warning body must not leak", func(context.Context, string, ...string) ([]byte, error) {
				return []byte("private output"), tc.err
			})
			if got.State != tc.want || strings.Contains(got.Error, "warning body") || strings.Contains(got.Error, "private output") || strings.Contains(got.Error, "\n") {
				t.Fatalf("result = %#v, want safe %s", got, tc.want)
			}
		})
	}
}

func TestDefaultWTFExecMissingBinaryIsPreStartError(t *testing.T) {
	_, err := defaultWTFExec(context.Background(), filepath.Join(t.TempDir(), "missing-command"))
	if err == nil {
		t.Fatal("expected missing binary error")
	}
	var executionError *wtfExecError
	if errors.As(err, &executionError) {
		t.Fatalf("pre-start error was wrapped as started: %#v", executionError)
	}
	var pathError *os.PathError
	if !errors.As(err, &pathError) {
		t.Fatalf("error = %T %v, want *os.PathError", err, err)
	}
}

func TestDefaultWTFExecNonzeroExitIsStartedCommandFailure(t *testing.T) {
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("false is not installed")
	}
	_, err = defaultWTFExec(context.Background(), falseBin)
	assertWTFStartedError(t, err)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want non-context command failure", err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("error = %T %v, want wrapped *exec.ExitError", err, err)
	}
}

func TestDefaultWTFExecDeadlineIsStartedDeadlineFailure(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = defaultWTFExec(ctx, sleepBin, "5")
	assertWTFStartedError(t, err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestDefaultWTFExecCompletionBeforeCancellationPreservesExitError(t *testing.T) {
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("false is not installed")
	}
	_, err = defaultWTFExec(completedBeforeCancellationContext{}, falseBin)

	assertWTFStartedError(t, err)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want completed command failure", err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("error = %T %v, want wrapped *exec.ExitError", err, err)
	}
}

type completedBeforeCancellationContext struct{}

func (completedBeforeCancellationContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (completedBeforeCancellationContext) Done() <-chan struct{}       { return nil }
func (completedBeforeCancellationContext) Err() error                  { return context.Canceled }
func (completedBeforeCancellationContext) Value(any) any               { return nil }

func assertWTFStartedError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected command error")
	}
	var executionError *wtfExecError
	if !errors.As(err, &executionError) || !executionError.Started {
		t.Fatalf("error = %T %v, want started execution error", err, err)
	}
}

func TestSendAmpWarningRedactsIndividualWarningLines(t *testing.T) {
	target := wtfSession{Agent: AgentAmp, ID: "T-123", Active: true}
	warning := "first private line\nmiddle private line\nlast private line"
	for _, leaked := range []string{"first private line", "middle private line"} {
		got := sendAmpWarning(context.Background(), target, warning, func(context.Context, string, ...string) ([]byte, error) {
			return nil, fmt.Errorf("executor included %s in its error", leaked)
		})
		if got.State != "failed" || strings.Contains(got.Error, leaked) {
			t.Fatalf("error %q produced %#v", leaked, got)
		}
	}
}

func TestSendAmpWarningDeadlineAndPreCanceledContext(t *testing.T) {
	target := wtfSession{Agent: AgentAmp, ID: "T-123", Active: true}
	now := time.Now()
	got := sendAmpWarning(context.Background(), target, "warning", func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Sub(now) < 14*time.Second || deadline.Sub(now) > 16*time.Second {
			t.Fatalf("deadline = %v, now = %v", deadline, now)
		}
		return nil, nil
	})
	if got.State != "sent" {
		t.Fatalf("result = %#v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	got = sendAmpWarning(ctx, target, "warning", func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if called || got.State != "failed" {
		t.Fatalf("called = %v, result = %#v", called, got)
	}
}

func TestMacNotificationExactCommand(t *testing.T) {
	text := "quote \" slash \\ and\nnewline"
	var name string
	var args []string
	got := sendMacNotification(context.Background(), "owner/repo#42", text, func(_ context.Context, command string, argv ...string) ([]byte, error) {
		name, args = command, append([]string(nil), argv...)
		return nil, nil
	})
	if got.State != "sent" || got.Error != "" {
		t.Fatalf("result = %#v, want sent", got)
	}
	want := []string{"-e", "on run argv", "-e", `display notification (item 1 of argv) with title "entire wtf" subtitle (item 2 of argv)`, "-e", "end run", "--", text, "owner/repo#42"}
	if name != "osascript" || !reflect.DeepEqual(args, want) {
		t.Fatalf("command = %q %#v, want osascript %#v", name, args, want)
	}
}

func TestMacNotificationFailureIsSafe(t *testing.T) {
	got := sendMacNotification(context.Background(), "owner/repo#42", "private warning", func(context.Context, string, ...string) ([]byte, error) {
		return []byte("private output"), errors.New("private warning: osascript failed\nmore details")
	})
	if got.State != "failed" || got.Error != "[warning omitted]: osascript failed" {
		t.Fatalf("result = %#v, want one safe error line", got)
	}
}

func TestMacNotificationRedactsIndividualWarningLines(t *testing.T) {
	warning := "first private line\nmiddle private line\nlast private line"
	for _, leaked := range []string{"first private line", "middle private line"} {
		got := sendMacNotification(context.Background(), "owner/repo#42", warning, func(context.Context, string, ...string) ([]byte, error) {
			return nil, fmt.Errorf("executor included %s in its error", leaked)
		})
		if got.State != "failed" || strings.Contains(got.Error, leaked) {
			t.Fatalf("error %q produced %#v", leaked, got)
		}
	}
}

func TestMacNotificationDeadlinePreCanceledAndStartFailure(t *testing.T) {
	now := time.Now()
	got := sendMacNotification(context.Background(), "owner/repo#42", "warning", func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Sub(now) < 4*time.Second || deadline.Sub(now) > 6*time.Second {
			t.Fatalf("deadline = %v, now = %v", deadline, now)
		}
		return nil, nil
	})
	if got.State != "sent" {
		t.Fatalf("result = %#v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	got = sendMacNotification(ctx, "owner/repo#42", "warning", func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if called || got.State != "failed" {
		t.Fatalf("called = %v, result = %#v", called, got)
	}

	pathErr := &os.PathError{Op: "fork/exec", Path: "/missing/osascript", Err: os.ErrNotExist}
	got = sendMacNotification(context.Background(), "owner/repo#42", "warning", func(context.Context, string, ...string) ([]byte, error) {
		return nil, pathErr
	})
	if got.State != "failed" || !strings.Contains(got.Error, "fork/exec") {
		t.Fatalf("result = %#v", got)
	}
}

func TestWTFSafeErrorNormalizesLinesAndBoundsRunes(t *testing.T) {
	got := wtfSafeError(errors.New(strings.Repeat("界", 300) + "\rsecond line"))
	if strings.ContainsAny(got, "\r\n") || len([]rune(got)) != 240 {
		t.Fatalf("safe error has %d runes: %q", len([]rune(got)), got)
	}
}

func TestClaudeWarningFrame(t *testing.T) {
	got := claudeWarningFrame("target-session-id", "fixed-uuid", "", "warning")
	if len(got) == 0 || got[len(got)-1] != '\n' || strings.Count(string(got), "\n") != 1 {
		t.Fatalf("frame is not one newline-terminated object: %q", got)
	}
	var frame claudePeerFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatal(err)
	}
	want := claudePeerFrame{MessageVersion: 1, MessageID: "fixed-uuid", Type: "user", Message: claudePeerMessage{Role: "user", Content: "warning"}, Priority: "next", SessionID: "target-session-id"}
	if frame != want {
		t.Fatalf("frame = %#v, want %#v", frame, want)
	}
}

func TestSendClaudeWarningReceiptMappings(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{{"held", "held"}, {"delivered", "sent"}, {"denied", "refused"}} {
		t.Run(tc.status, func(t *testing.T) {
			target, stop := fakeClaudeSocket(t, func(frame claudePeerFrame) {
				sendClaudeReceipt(t, frame.From, `{"type":"control","action":"peer_message_status","status":"`+tc.status+`","orig_msg_id":"wrong-id"}`)
				sendClaudeReceipt(t, frame.From, `{"type":"control","action":"peer_message_status","status":"`+tc.status+`","orig_msg_id":"`+frame.MessageID+`","reason":"crossSessionInbound hold"}`)
			})
			defer stop()
			got := sendClaudeWarning(context.Background(), target, "warning")
			if got.State != tc.want || got.Error != "" {
				t.Fatalf("result = %#v, want state %q", got, tc.want)
			}
		})
	}
}

func TestSendClaudeWarningWriteSuccessWithoutReceiptIsSent(t *testing.T) {
	target, stop := fakeClaudeSocket(t, func(claudePeerFrame) {})
	defer stop()
	if got := sendClaudeWarning(context.Background(), target, "warning"); got.State != "sent" || got.Error != "" {
		t.Fatalf("result = %#v, want sent", got)
	}
}

func TestSendClaudeWarningDeadlineAfterWriteIsSent(t *testing.T) {
	target, stop := fakeClaudeSocket(t, func(claudePeerFrame) {})
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got := sendClaudeWarning(ctx, target, "warning"); got.State != "sent" || got.Error != "" {
		t.Fatalf("result = %#v, want sent", got)
	}
}

func TestWriteClaudeFrameFullWriteMakesCloseFailureSent(t *testing.T) {
	conn := &fakeClaudeWriteConn{closeErr: errors.New("forced close failure")}
	written, err := writeClaudeFrame(context.Background(), conn, []byte("frame\n"))
	if !written || err == nil || !strings.Contains(err.Error(), "forced close failure") {
		t.Fatalf("written = %v, err = %v", written, err)
	}
}

func TestWriteClaudeFrameIncompleteWriteIsFailed(t *testing.T) {
	conn := &fakeClaudeWriteConn{writeN: 3, writeErr: errors.New("forced write failure")}
	written, err := writeClaudeFrame(context.Background(), conn, []byte("frame\n"))
	if written || err == nil || conn.closeWrites != 0 {
		t.Fatalf("written = %v, err = %v, close writes = %d", written, err, conn.closeWrites)
	}
}

func TestWriteClaudeFrameFullWriteWithErrorIsSent(t *testing.T) {
	conn := &fakeClaudeWriteConn{writeN: len("frame\n"), writeErr: errors.New("error reported with full write")}
	written, err := writeClaudeFrame(context.Background(), conn, []byte("frame\n"))
	if !written || err == nil || conn.closeWrites != 0 {
		t.Fatalf("written = %v, err = %v, close writes = %d", written, err, conn.closeWrites)
	}
}

func TestWriteClaudeFrameCancellationInterruptsBlockedWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := newBlockingClaudeWriteConn()
	done := make(chan struct{})
	go func() {
		defer close(done)
		written, err := writeClaudeFrame(ctx, conn, []byte("frame\n"))
		if written || err == nil {
			t.Errorf("written = %v, err = %v", written, err)
		}
	}()
	<-conn.started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked write did not stop after cancellation")
	}
	if got := conn.deadlineCalls(); got < 2 {
		t.Fatalf("write deadline calls = %d, want bounded deadline plus cancellation", got)
	}
}

type fakeClaudeWriteConn struct {
	writeN, closeWrites int
	writeErr            error
	closeErr            error
}

func (c *fakeClaudeWriteConn) Write(p []byte) (int, error) {
	if c.writeN == 0 && c.writeErr == nil {
		return len(p), nil
	}
	return c.writeN, c.writeErr
}
func (c *fakeClaudeWriteConn) SetWriteDeadline(time.Time) error { return nil }
func (c *fakeClaudeWriteConn) CloseWrite() error {
	c.closeWrites++
	return c.closeErr
}

type blockingClaudeWriteConn struct {
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
	woken   bool
	wake    chan struct{}
}

func newBlockingClaudeWriteConn() *blockingClaudeWriteConn {
	return &blockingClaudeWriteConn{started: make(chan struct{}), wake: make(chan struct{})}
}
func (c *blockingClaudeWriteConn) Write([]byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	<-c.wake
	return 0, os.ErrDeadlineExceeded
}
func (c *blockingClaudeWriteConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if !c.woken && !deadline.After(time.Now()) {
		c.woken = true
		close(c.wake)
	}
	return nil
}
func (c *blockingClaudeWriteConn) CloseWrite() error { return nil }
func (c *blockingClaudeWriteConn) deadlineCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSendClaudeWarningRejectsInvalidTargets(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sock")
	for _, tc := range []struct {
		name   string
		target wtfSession
	}{
		{"inactive", wtfSession{Agent: AgentClaude, ID: "session", SocketPath: missing}},
		{"wrong agent", wtfSession{Agent: AgentAmp, ID: "session", SocketPath: missing, Active: true}},
		{"empty session", wtfSession{Agent: AgentClaude, SocketPath: missing, Active: true}},
		{"empty socket", wtfSession{Agent: AgentClaude, ID: "session", Active: true}},
		{"missing socket", wtfSession{Agent: AgentClaude, ID: "session", SocketPath: missing, Active: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sendClaudeWarning(context.Background(), tc.target, "warning"); got.State != "failed" || got.Error == "" {
				t.Fatalf("result = %#v, want failed with error", got)
			}
		})
	}
}

func TestSendClaudeWarningRejectsWritableSocketParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	target, stop := fakeClaudeSocketAt(t, shortTempSocketPath(t, dir), func(claudePeerFrame) {})
	defer stop()
	if got := sendClaudeWarning(context.Background(), target, "warning"); got.State != "failed" {
		t.Fatalf("result = %#v, want failed", got)
	}
}

func TestSendClaudeWarningRejectsRegularFileSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(path, []byte("ordinary file"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := wtfSession{Agent: AgentClaude, ID: "session", SocketPath: path, Active: true}
	if got := sendClaudeWarning(context.Background(), target, "warning"); got.State != "failed" || !strings.Contains(got.Error, "not a socket") {
		t.Fatalf("result = %#v, want regular-file rejection", got)
	}
}

func TestSendClaudeWarningReceiptSocketUsesPrivateDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	checked := make(chan string, 1)
	target, stop := fakeClaudeSocketAt(t, shortTempSocketPath(t, dir), func(frame claudePeerFrame) {
		path := strings.TrimPrefix(frame.From, "uds:")
		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Errorf("stat receipt directory: %v", err)
			return
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("receipt directory mode = %o, want 700", got)
		}
		if info, err = os.Stat(path); err != nil {
			t.Errorf("stat receipt socket: %v", err)
		} else if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("receipt socket mode = %o, want 600", got)
		}
		checked <- filepath.Dir(path)
	})
	got := sendClaudeWarning(context.Background(), target, "warning")
	stop()
	if got.State != "sent" {
		t.Fatalf("result = %#v, want sent", got)
	}
	receiptDir := <-checked
	if _, err := os.Stat(receiptDir); !os.IsNotExist(err) {
		t.Fatalf("receipt directory remains after send: %v", err)
	}
}

func fakeClaudeSocket(t *testing.T, reply func(claudePeerFrame)) (wtfSession, func()) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return fakeClaudeSocketAt(t, shortTempSocketPath(t, dir), reply)
}

func shortTempSocketPath(t *testing.T, dir string) string {
	t.Helper()
	alias := filepath.Join("/tmp", "et-"+newSessionID()[:8])
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(alias) })
	return filepath.Join(alias, "c.sock")
}

func fakeClaudeSocketAt(t *testing.T, path string, reply func(claudePeerFrame)) (wtfSession, func()) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var frame claudePeerFrame
		if json.Unmarshal(line, &frame) == nil {
			if frame.SessionID != "target-session-id" {
				t.Errorf("frame session id = %q, want target-session-id", frame.SessionID)
			}
			reply(frame)
		}
	}()
	return wtfSession{Agent: AgentClaude, ID: "target-session-id", SocketPath: path, Active: true}, func() {
		listener.Close()
		<-done
	}
}

func sendClaudeReceipt(t *testing.T, from, receipt string) {
	t.Helper()
	path := strings.TrimPrefix(from, "uds:")
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Errorf("dial receipt socket: %v", err)
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(receipt + "\n")); err != nil {
		t.Errorf("write receipt: %v", err)
	}
}

func TestWarningTextDuplicateClaim(t *testing.T) {
	at := time.Date(2026, 9, 29, 10, 14, 0, 0, time.Local).Unix()
	state := newWTFState(at)
	state.Sessions["claude:owner-id"] = wtfSession{Agent: AgentClaude, ID: "owner-id", Name: "api-refactor-a1", Cwd: "~/src/entiredb/.amp/worktrees/api-1223"}
	state.Sessions["claude:challenger-id"] = wtfSession{Agent: AgentClaude, ID: "challenger-id", Cwd: "~/src/entiredb/.amp/worktrees/checkpoint-fix", Active: true}
	state.Trails["entiredb#1223"] = wtfTrail{
		Key: "entiredb#1223", OwnerSession: "claude:owner-id", CanonicalWorktree: "~/src/entiredb/.amp/worktrees/api-1223",
		FirstClaim: &wtfClaim{SessionKey: "claude:owner-id", Worktree: "~/src/entiredb/.amp/worktrees/api-1223", At: at},
	}
	state.Worktrees["~/src/entiredb/.amp/worktrees/api-1223"] = wtfWorktree{DirtyFiles: 3, UnmergedCommits: 2, DefaultBranch: "origin/main"}
	finding := wtfFinding{ID: "f1", Kind: "duplicate-claim", TrailKey: "entiredb#1223", Owner: "claude:owner-id", Challenger: "claude:challenger-id", Active: true}

	want := "entire wtf found a conflict for entiredb#1223.\n" +
		"It is already owned by Claude session api-refactor-a1 in ~/src/entiredb/.amp/worktrees/api-1223.\n" +
		"This session is in ~/src/entiredb/.amp/worktrees/checkpoint-fix. Stop before changing this trail or either worktree and check with Daniel.\n" +
		"Evidence: first claim at 10:14; canonical worktree has 3 dirty files and 2 commits not on origin/main."
	if got := warningText(state, finding); got != want {
		t.Fatalf("warning text:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestWarningTextFallbackLabelsAndSafetyInstruction(t *testing.T) {
	state := newWTFState(1)
	state.Sessions["amp:T-123456789"] = wtfSession{Agent: AgentAmp, ID: "T-123456789", Cwd: "/tmp/owner"}
	state.Sessions["claude:abcdefghijk"] = wtfSession{Agent: AgentClaude, ID: "abcdefghijk", Cwd: "/tmp/challenger"}
	state.Trails["o/r#1"] = wtfTrail{Key: "o/r#1", OwnerSession: "amp:T-123456789", CanonicalWorktree: "/tmp/owner", FirstClaim: &wtfClaim{At: 1}}
	state.Worktrees["/tmp/owner"] = wtfWorktree{DefaultBranch: "origin/main"}

	got := warningText(state, wtfFinding{TrailKey: "o/r#1", Owner: "amp:T-123456789", Challenger: "claude:abcdefghijk"})
	for _, want := range []string{"Amp T-12345", "Stop", "check with Daniel"} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning %q does not contain %q", got, want)
		}
	}
}

func TestWarningTextClaudeFallbackLabel(t *testing.T) {
	state := newWTFState(1)
	state.Sessions["claude:abcdefghijk"] = wtfSession{Cwd: "/tmp/owner"}
	state.Sessions["amp:challenger"] = wtfSession{Agent: AgentAmp, ID: "challenger", Cwd: "/tmp/challenger"}
	state.Trails["o/r#1"] = wtfTrail{Key: "o/r#1", OwnerSession: "claude:abcdefghijk", CanonicalWorktree: "/tmp/owner", FirstClaim: &wtfClaim{At: 1}}
	state.Worktrees["/tmp/owner"] = wtfWorktree{DefaultBranch: "origin/main"}

	got := warningText(state, wtfFinding{TrailKey: "o/r#1", Challenger: "amp:challenger"})
	if !strings.Contains(got, "Claude abcdefgh") {
		t.Fatalf("warning %q does not contain Claude fallback label", got)
	}
}

func TestPendingWTFDeliveriesActiveChallenger(t *testing.T) {
	state, finding := notificationState()
	got := pendingWTFDeliveries(state, 100)
	if len(got) != 2 {
		t.Fatalf("deliveries = %#v", got)
	}
	if got[0].FindingID != finding.ID || got[0].Channel != "mac" || got[0].Target != "" {
		t.Fatalf("first delivery = %#v", got[0])
	}
	if got[1].Channel != "session:"+finding.Challenger || got[1].Target != finding.Challenger {
		t.Fatalf("second delivery = %#v", got[1])
	}
	for _, delivery := range got {
		if !strings.Contains(delivery.Message, "Stop") || !strings.Contains(delivery.Message, "check with Daniel") {
			t.Fatalf("unsafe delivery = %#v", delivery)
		}
	}
}

func TestPendingWTFDeliveriesOwnerOnlyIsDashboardOnly(t *testing.T) {
	state, finding := notificationState()
	finding.Challenger = ""
	state.Findings[finding.ID] = finding
	if got := pendingWTFDeliveries(state, 100); len(got) != 0 {
		t.Fatalf("owner-only deliveries = %#v", got)
	}
}

func TestPendingWTFDeliveriesInactiveChallengerIsDashboardOnly(t *testing.T) {
	state, finding := notificationState()
	challenger := state.Sessions[finding.Challenger]
	challenger.Active = false
	state.Sessions[finding.Challenger] = challenger
	if got := pendingWTFDeliveries(state, 100); len(got) != 0 {
		t.Fatalf("inactive-challenger deliveries = %#v", got)
	}
}

func TestPendingWTFDeliveriesMissingChallengerIsDashboardOnly(t *testing.T) {
	state, finding := notificationState()
	delete(state.Sessions, finding.Challenger)
	if got := pendingWTFDeliveries(state, 100); len(got) != 0 {
		t.Fatalf("missing-challenger deliveries = %#v", got)
	}
}

func TestPendingWTFDeliveriesSelfConflictIsDashboardOnly(t *testing.T) {
	state, finding := notificationState()
	finding.Owner = ""
	finding.Challenger = state.Trails[finding.TrailKey].OwnerSession
	state.Findings[finding.ID] = finding
	if got := pendingWTFDeliveries(state, 100); len(got) != 0 {
		t.Fatalf("self-conflict deliveries = %#v", got)
	}
}

func TestPendingWTFDeliveriesHistoricalIsDashboardOnly(t *testing.T) {
	state, finding := notificationState()
	finding.Active = false
	state.Findings[finding.ID] = finding
	if got := pendingWTFDeliveries(state, 100); len(got) != 0 {
		t.Fatalf("historical deliveries = %#v", got)
	}
}

func TestPendingWTFDeliveriesTerminalStatesDoNotRetry(t *testing.T) {
	for _, terminal := range []string{"sent", "held", "refused", "unknown", "sending"} {
		t.Run(terminal, func(t *testing.T) {
			state, finding := notificationState()
			finding.Delivery = map[string]wtfDeliveryStatus{
				"mac":                           {State: terminal, Attempts: 1, LastAttempt: 1},
				"session:" + finding.Challenger: {State: terminal, Attempts: 1, LastAttempt: 1},
			}
			state.Findings[finding.ID] = finding
			if got := pendingWTFDeliveries(state, 10_000); len(got) != 0 {
				t.Fatalf("deliveries = %#v", got)
			}
		})
	}
}

func TestPendingWTFDeliveriesFailedRetrySchedule(t *testing.T) {
	delays := []int64{5, 30, 120, 600, 3600, 3600}
	for attempts, delay := range delays {
		state, finding := notificationState()
		finding.Delivery = map[string]wtfDeliveryStatus{
			"mac":                           {State: "sent"},
			"session:" + finding.Challenger: {State: "failed", Attempts: attempts + 1, LastAttempt: 100},
		}
		state.Findings[finding.ID] = finding
		if got := pendingWTFDeliveries(state, 100+delay-1); len(got) != 0 {
			t.Fatalf("attempt %d retried early: %#v", attempts+1, got)
		}
		if got := pendingWTFDeliveries(state, 100+delay); len(got) != 1 || got[0].Channel != "session:"+finding.Challenger {
			t.Fatalf("attempt %d due deliveries = %#v", attempts+1, got)
		}
	}
}

func TestPendingWTFDeliveriesRecurrenceStartsPending(t *testing.T) {
	state, finding := notificationState()
	finding.Delivery = map[string]wtfDeliveryStatus{"mac": {State: "sent"}, "session:" + finding.Challenger: {State: "refused"}}
	prior := map[string]wtfFinding{finding.ID: finding}
	finding.Delivery = nil
	finding.Active = true
	cleared := mergeWTFFindings(prior, nil, 110)
	state.Findings = mergeWTFFindings(cleared, map[string]wtfFinding{finding.ID: finding}, 120)
	recurred := state.Findings[finding.ID]
	if recurred.Occurrence != 2 {
		t.Fatalf("recurrence occurrence = %d, want 2", recurred.Occurrence)
	}
	if len(recurred.Delivery) != 0 {
		t.Fatalf("recurrence delivery state = %#v, want fresh pending channels", recurred.Delivery)
	}
	if got := pendingWTFDeliveries(state, 120); len(got) != 2 || got[0].Channel != "mac" || got[1].Channel != "session:"+finding.Challenger {
		t.Fatalf("recurrence deliveries = %#v", got)
	}
}

func TestWTFDeliveryStateTransitions(t *testing.T) {
	state, finding := notificationState()
	delivery := pendingWTFDeliveries(state, 100)[0]
	markWTFDeliveryStarted(&state, delivery, 100)
	status := state.Findings[finding.ID].Delivery["mac"]
	if status.State != "sending" || status.Attempts != 1 || status.LastAttempt != 100 {
		t.Fatalf("started status = %#v", status)
	}
	applyWTFDeliveryResult(&state, wtfDeliveryResult{FindingID: finding.ID, Channel: "mac", State: "failed", Error: "offline"}, 101)
	status = state.Findings[finding.ID].Delivery["mac"]
	if status.State != "failed" || status.Attempts != 1 || status.LastAttempt != 100 || status.LastError != "offline" {
		t.Fatalf("failed status = %#v", status)
	}
}

func TestWTFDeliveryCrashRecoveryMarksSendingUnknown(t *testing.T) {
	home := t.TempDir()
	state, finding := notificationState()
	finding.Delivery = map[string]wtfDeliveryStatus{"mac": {State: "sending", Attempts: 2, LastAttempt: 90, LastError: "old"}}
	state.Findings[finding.ID] = finding
	if err := saveWTFState(home, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadWTFState(home, 100)
	if err != nil {
		t.Fatal(err)
	}
	status := loaded.Findings[finding.ID].Delivery["mac"]
	if status.State != "unknown" || status.Attempts != 2 || status.LastAttempt != 90 || status.LastError != "old" {
		t.Fatalf("recovered status = %#v", status)
	}
	if got := pendingWTFDeliveries(loaded, 10_000); len(got) != 1 || got[0].Channel != "session:"+finding.Challenger {
		t.Fatalf("crash recovery retried unknown channel: %#v", got)
	}
}

func notificationState() (wtfState, wtfFinding) {
	state := newWTFState(100)
	state.Sessions["claude:owner"] = wtfSession{Agent: AgentClaude, ID: "owner", Name: "owner", Cwd: "/wt/owner", Active: true}
	state.Sessions["amp:challenger"] = wtfSession{Agent: AgentAmp, ID: "challenger", Name: "challenger", Cwd: "/wt/challenger", Active: true}
	state.Trails["o/r#1"] = wtfTrail{Key: "o/r#1", OwnerSession: "claude:owner", CanonicalWorktree: "/wt/owner", FirstClaim: &wtfClaim{At: 50}}
	state.Worktrees["/wt/owner"] = wtfWorktree{DirtyFiles: 1, UnmergedCommits: 2, DefaultBranch: "origin/main"}
	finding := wtfFinding{ID: "finding-1", Kind: "duplicate-claim", TrailKey: "o/r#1", Owner: "claude:owner", Challenger: "amp:challenger", Active: true, Occurrence: 1}
	state.Findings[finding.ID] = finding
	return state, finding
}
