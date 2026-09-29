package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// These peer-messaging fields and receipt shapes were reverse-engineered and
// tested against Claude Code 2.1.278. Protocol documentation:
// https://code.claude.com/docs/en/cross-session-messaging
type claudePeerFrame struct {
	MessageVersion int               `json:"msgV"`
	MessageID      string            `json:"msg_id"`
	Type           string            `json:"type"`
	Message        claudePeerMessage `json:"message"`
	Priority       string            `json:"priority"`
	SessionID      string            `json:"session_id"`
	From           string            `json:"from,omitempty"`
}

type claudePeerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudePeerReceipt struct {
	Type          string `json:"type"`
	Action        string `json:"action"`
	Status        string `json:"status"`
	OriginalMsgID string `json:"orig_msg_id"`
	Reason        string `json:"reason"`
}

func claudeWarningFrame(sessionID, messageID, from, text string) []byte {
	frame := claudePeerFrame{
		MessageVersion: 1,
		MessageID:      messageID,
		Type:           "user",
		Message:        claudePeerMessage{Role: "user", Content: text},
		Priority:       "next",
		SessionID:      sessionID,
		From:           from,
	}
	b, _ := json.Marshal(frame)
	return append(b, '\n')
}

func sendClaudeWarning(ctx context.Context, target wtfSession, text string) wtfDeliveryResult {
	failed := func(err error) wtfDeliveryResult {
		return wtfDeliveryResult{State: "failed", Error: err.Error()}
	}
	if target.Agent != AgentClaude || !target.Active {
		return failed(fmt.Errorf("target is not an active Claude session"))
	}
	if target.ID == "" {
		return failed(fmt.Errorf("target session id is empty"))
	}
	if target.SocketPath == "" {
		return failed(fmt.Errorf("target socket path is empty"))
	}
	if err := validateClaudeSocket(target.SocketPath); err != nil {
		return failed(err)
	}

	receiptDir, err := os.MkdirTemp(filepath.Dir(target.SocketPath), ".entire-tail-receipt-")
	if err != nil {
		return failed(fmt.Errorf("create receipt directory: %w", err))
	}
	defer os.RemoveAll(receiptDir)
	if err := os.Chmod(receiptDir, 0o700); err != nil {
		return failed(fmt.Errorf("secure receipt directory: %w", err))
	}
	receiptPath := filepath.Join(receiptDir, "r.sock")
	receipts, err := net.Listen("unix", receiptPath)
	if err != nil {
		return failed(fmt.Errorf("create receipt socket: %w", err))
	}
	defer func() {
		receipts.Close()
		_ = os.Remove(receiptPath)
	}()
	if err := os.Chmod(receiptPath, 0o600); err != nil {
		return failed(fmt.Errorf("secure receipt socket: %w", err))
	}

	messageID := newSessionID()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", target.SocketPath)
	if err != nil {
		return failed(fmt.Errorf("dial Claude socket: %w", err))
	}
	defer conn.Close()
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return failed(fmt.Errorf("Claude socket connection is not Unix"))
	}
	written, writeErr := writeClaudeFrame(ctx, unix, claudeWarningFrame(target.ID, messageID, "uds:"+receiptPath, text))
	if !written {
		return failed(fmt.Errorf("write Claude warning: %w", writeErr))
	}
	if writeErr != nil {
		return wtfDeliveryResult{State: "sent"}
	}

	return waitClaudeReceipt(ctx, receipts, messageID)
}

const claudeWriteTimeout = 2 * time.Second

type claudeWriteConn interface {
	Write([]byte) (int, error)
	SetWriteDeadline(time.Time) error
	CloseWrite() error
}

// writeClaudeFrame reports written only after every byte, including the frame's
// trailing newline, has reached the socket. From that point onward the delivery
// is non-retryable even when the half-close fails.
func writeClaudeFrame(ctx context.Context, conn claudeWriteConn, frame []byte) (written bool, err error) {
	deadline := time.Now().Add(claudeWriteTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return false, err
	}
	stopWatcher := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = conn.SetWriteDeadline(time.Now())
		case <-stopWatcher:
		}
	}()
	defer func() {
		close(stopWatcher)
		<-watcherDone
	}()

	for offset := 0; offset < len(frame); {
		n, writeErr := conn.Write(frame[offset:])
		offset += n
		if offset == len(frame) && writeErr != nil {
			return true, writeErr
		}
		if writeErr != nil {
			return false, writeErr
		}
		if n == 0 {
			return false, io.ErrNoProgress
		}
	}
	if err := conn.CloseWrite(); err != nil {
		return true, err
	}
	return true, nil
}

func validateClaudeSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat Claude socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("Claude socket path is not a socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("Claude socket is not owned by current user")
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("stat Claude socket parent: %w", err)
	}
	if parent.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("Claude socket parent is group or world writable")
	}
	return nil
}

func waitClaudeReceipt(ctx context.Context, listener net.Listener, messageID string) wtfDeliveryResult {
	deadline := time.Now().Add(2 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	for {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return wtfDeliveryResult{State: "sent"}
		}
		acceptDeadline := time.Now().Add(50 * time.Millisecond)
		if deadline.Before(acceptDeadline) {
			acceptDeadline = deadline
		}
		if unix, ok := listener.(*net.UnixListener); ok {
			unix.SetDeadline(acceptDeadline)
		}
		conn, err := listener.Accept()
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			return wtfDeliveryResult{State: "sent"}
		}
		conn.SetReadDeadline(deadline)
		line, readErr := bufio.NewReader(conn).ReadBytes('\n')
		conn.Close()
		if readErr != nil {
			continue
		}
		var receipt claudePeerReceipt
		if json.Unmarshal(line, &receipt) != nil || receipt.Type != "control" || receipt.Action != "peer_message_status" || receipt.OriginalMsgID != messageID {
			continue
		}
		switch receipt.Status {
		case "held":
			return wtfDeliveryResult{State: "held"}
		case "delivered":
			return wtfDeliveryResult{State: "sent"}
		case "denied":
			return wtfDeliveryResult{State: "refused"}
		}
	}
}

type wtfDelivery struct {
	FindingID string
	Channel   string
	Target    string
	Message   string
}

type wtfDeliveryResult struct {
	FindingID string
	Channel   string
	State     string
	Error     string
}

type wtfExec func(ctx context.Context, name string, args ...string) ([]byte, error)

const (
	wtfAmpTimeout          = 15 * time.Second
	wtfNotificationTimeout = 5 * time.Second
)

func sendAmpWarning(ctx context.Context, target wtfSession, text string, run wtfExec) wtfDeliveryResult {
	if target.Agent != AgentAmp || !target.Active {
		return wtfFailedDelivery(errors.New("target is not an active Amp session"), text)
	}
	if !strings.HasPrefix(target.ID, "T-") || len(target.ID) == 2 {
		return wtfFailedDelivery(errors.New("target Amp thread id is invalid"), text)
	}
	if err := ctx.Err(); err != nil {
		return wtfFailedDelivery(err, text)
	}
	commandCtx, cancel := context.WithTimeout(ctx, wtfAmpTimeout)
	defer cancel()
	_, err := run(commandCtx, "amp", "threads", "continue", target.ID, "--execute", text)
	if err == nil {
		return wtfDeliveryResult{State: "sent"}
	}
	var lookupError *exec.Error
	var startError *os.PathError
	if errors.As(err, &lookupError) || errors.As(err, &startError) {
		return wtfFailedDelivery(err, text)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || commandCtx.Err() != nil {
		return wtfDeliveryResult{State: "unknown", Error: wtfSafeError(err, text)}
	}
	return wtfFailedDelivery(err, text)
}

func notificationArgs(trailKey, text string) []string {
	return []string{
		"-e", "on run argv",
		"-e", `display notification (item 1 of argv) with title "entire wtf" subtitle (item 2 of argv)`,
		"-e", "end run",
		"--", text, trailKey,
	}
}

func sendMacNotification(ctx context.Context, trailKey, text string, run wtfExec) wtfDeliveryResult {
	commandCtx, cancel := context.WithTimeout(ctx, wtfNotificationTimeout)
	defer cancel()
	_, err := run(commandCtx, "osascript", notificationArgs(trailKey, text)...)
	if err != nil {
		return wtfFailedDelivery(err, text)
	}
	return wtfDeliveryResult{State: "sent"}
}

func wtfFailedDelivery(err error, sensitive ...string) wtfDeliveryResult {
	return wtfDeliveryResult{State: "failed", Error: wtfSafeError(err, sensitive...)}
}

func wtfSafeError(err error, sensitive ...string) string {
	if err == nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(strings.ReplaceAll(err.Error(), "\r", ""), "\n", 2)[0])
	for _, value := range sensitive {
		if value != "" {
			line = strings.ReplaceAll(line, value, "[warning omitted]")
		}
	}
	if line == "" {
		line = "command failed"
	}
	const limit = 240
	runes := []rune(line)
	if len(runes) > limit {
		line = string(runes[:limit])
	}
	return line
}

func warningText(state wtfState, finding wtfFinding) string {
	trail := state.Trails[finding.TrailKey]
	ownerKey := firstNonEmpty(finding.Owner, trail.OwnerSession)
	owner := state.Sessions[ownerKey]
	challenger := state.Sessions[finding.Challenger]
	ownerPath := firstNonEmpty(owner.Cwd, trail.CanonicalWorktree)
	challengerPath := challenger.Cwd
	claimAt := int64(0)
	if trail.FirstClaim != nil {
		claimAt = trail.FirstClaim.At
	}
	worktree := state.Worktrees[trail.CanonicalWorktree]
	defaultBranch := firstNonEmpty(worktree.DefaultBranch, "the default branch")

	return fmt.Sprintf("entire wtf found a conflict for %s.\n", finding.TrailKey) +
		fmt.Sprintf("It is already owned by %s in %s.\n", wtfWarningSessionLabel(owner, ownerKey), ownerPath) +
		fmt.Sprintf("This session is in %s. Stop before changing this trail or either worktree and check with Daniel.\n", challengerPath) +
		fmt.Sprintf("Evidence: first claim at %s; canonical worktree has %d dirty files and %d commits not on %s.", wtfWarningTime(claimAt), worktree.DirtyFiles, worktree.UnmergedCommits, defaultBranch)
}

func wtfWarningSessionLabel(session wtfSession, key string) string {
	agent := session.Agent
	id := session.ID
	if value, rest, ok := strings.Cut(key, ":"); ok {
		if agent == "" {
			agent = Agent(value)
		}
		if id == "" {
			id = rest
		}
	}
	prefix := "Claude"
	if agent == AgentAmp {
		prefix = "Amp"
	}
	if session.Name != "" {
		return prefix + " session " + session.Name
	}
	return prefix + " " + shortID(id)
}

func wtfWarningTime(unix int64) string {
	if unix == 0 {
		return "unknown time"
	}
	return time.Unix(unix, 0).Local().Format("15:04")
}

func pendingWTFDeliveries(state wtfState, now int64) []wtfDelivery {
	var deliveries []wtfDelivery
	for _, id := range sortedMapKeys(state.Findings) {
		finding := state.Findings[id]
		if !finding.Active {
			continue
		}
		trail := state.Trails[finding.TrailKey]
		ownerKey := firstNonEmpty(finding.Owner, trail.OwnerSession)
		challenger, ok := state.Sessions[finding.Challenger]
		if finding.Challenger == "" || !ok || !challenger.Active || finding.Challenger == ownerKey {
			continue
		}
		message := warningText(state, finding)
		channels := []wtfDelivery{
			{FindingID: id, Channel: "mac", Message: message},
			{FindingID: id, Channel: "session:" + finding.Challenger, Target: finding.Challenger, Message: message},
		}
		for _, delivery := range channels {
			if wtfDeliveryDue(finding.Delivery[delivery.Channel], now) {
				deliveries = append(deliveries, delivery)
			}
		}
	}
	sort.Slice(deliveries, func(i, j int) bool {
		if deliveries[i].FindingID != deliveries[j].FindingID {
			return deliveries[i].FindingID < deliveries[j].FindingID
		}
		return deliveries[i].Channel < deliveries[j].Channel
	})
	return deliveries
}

func wtfDeliveryDue(status wtfDeliveryStatus, now int64) bool {
	switch status.State {
	case "":
		return true
	case "failed":
		delays := [...]int64{5, 30, 120, 600, 3600}
		attempt := status.Attempts
		if attempt < 1 {
			attempt = 1
		}
		index := attempt - 1
		if index >= len(delays) {
			index = len(delays) - 1
		}
		return now >= status.LastAttempt+delays[index]
	default:
		return false
	}
}

func markWTFDeliveryStarted(state *wtfState, delivery wtfDelivery, now int64) {
	finding, ok := state.Findings[delivery.FindingID]
	if !ok {
		return
	}
	if finding.Delivery == nil {
		finding.Delivery = make(map[string]wtfDeliveryStatus)
	}
	status := finding.Delivery[delivery.Channel]
	status.State = "sending"
	status.Attempts++
	status.LastAttempt = now
	status.LastError = ""
	finding.Delivery[delivery.Channel] = status
	state.Findings[delivery.FindingID] = finding
}

func applyWTFDeliveryResult(state *wtfState, result wtfDeliveryResult, now int64) {
	_ = now
	finding, ok := state.Findings[result.FindingID]
	if !ok || finding.Delivery == nil {
		return
	}
	status, ok := finding.Delivery[result.Channel]
	if !ok || status.State != "sending" {
		return
	}
	status.State = result.State
	status.LastError = result.Error
	finding.Delivery[result.Channel] = status
	state.Findings[result.FindingID] = finding
}

func recoverWTFDeliveries(state *wtfState) {
	for id, finding := range state.Findings {
		for channel, status := range finding.Delivery {
			if status.State == "sending" {
				status.State = "unknown"
				finding.Delivery[channel] = status
			}
		}
		state.Findings[id] = finding
	}
}
