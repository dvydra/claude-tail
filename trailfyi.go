package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"
)

// Trail FYIs: when a trail this tail watches changes, tell the Claude session
// being tailed, so it doesn't have to poll or sleep on CI. The message goes
// over Claude Code's peer-message socket (the same path wtf warnings take,
// wtf_notify.go). A busy session reads it between tool calls, so a running
// tool is never cut off. An idle session would start a new turn on it, so an
// idle session gets nothing: idle means it handed control back to the human,
// and a turn starting under their prompt is the interruption this avoids.

// trailFYIDedupWindow is how long one FYI text is claimed for a session. Every
// tail on a session runs its own tracker, so without the claim each would send
// the same line.
const trailFYIDedupWindow = 10 * time.Minute

type trailFYIDeps struct {
	Lookup func(sessionID string) (liveSession, bool)
	Send   func(ctx context.Context, target wtfSession, text string) wtfDeliveryResult
	Claim  func(sessionID, text string) bool
}

// trailFYIText is the message the session receives: plain text, no escapes,
// with the change's link so the agent can look closer if it cares.
func trailFYIText(u trailUpdate) string {
	text := "FYI from entire-tail, no action needed unless it changes your plan: " + trailLineText(u.State, u.Events, false)
	if u.State.URL != "" {
		text += " " + u.State.URL
	}
	return text
}

// deliverTrailFYI sends one update to the session if it should get it, and
// says what happened. The startup baseline is not news, so it is never sent.
func deliverTrailFYI(ctx context.Context, d trailFYIDeps, sessionID string, u trailUpdate) string {
	if u.Baseline {
		return "baseline"
	}
	if len(u.Events) == 0 {
		return "no events"
	}
	s, ok := d.Lookup(sessionID)
	if !ok || s.SocketPath == "" {
		return "no session"
	}
	if !s.Busy() {
		return "idle"
	}
	text := trailFYIText(u)
	if !d.Claim(sessionID, text) {
		return "duplicate"
	}
	target := wtfSession{Agent: AgentClaude, ID: sessionID, Active: true, SocketPath: s.SocketPath}
	return d.Send(ctx, target, text).State
}

func trailFYIDir(home string) string {
	return filepath.Join(home, ".claude", "entire-tail", "trailfyi")
}

// claimTrailFYI reports whether this process should send text to the session:
// true unless another tail claimed the same text within the window. The claim
// is a file whose mtime is the claim time.
func claimTrailFYI(dir, sessionID, text string, now time.Time) bool {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + text))
	path := filepath.Join(dir, hex.EncodeToString(sum[:12]))
	if fi, err := os.Stat(path); err == nil {
		if now.Sub(fi.ModTime()) < trailFYIDedupWindow {
			return false
		}
		_ = os.Remove(path)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false // another tail got there first
	}
	f.Close()
	_ = os.Chtimes(path, now, now)
	pruneTrailFYIClaims(dir, now)
	return true
}

func pruneTrailFYIClaims(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > 24*time.Hour {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// startTrailFYI runs deliveries off the render goroutine: a send waits up to two
// seconds for Claude's receipt. A full queue drops the FYI rather than block.
func startTrailFYI(ctx context.Context, d trailFYIDeps) func(sessionID string, u trailUpdate) {
	type job struct {
		id string
		u  trailUpdate
	}
	jobs := make(chan job, 16)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-jobs:
				sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				deliverTrailFYI(sendCtx, d, j.id, j.u)
				cancel()
			}
		}
	}()
	return func(sessionID string, u trailUpdate) {
		select {
		case jobs <- job{sessionID, u}:
		default:
		}
	}
}

func defaultTrailFYIDeps(home string) trailFYIDeps {
	return trailFYIDeps{
		Lookup: func(id string) (liveSession, bool) {
			for _, s := range collectLiveSessions(liveRoots(home), liveAlive) {
				if s.SessionID == id {
					return s, true
				}
			}
			return liveSession{}, false
		},
		Send: sendClaudeWarning,
		Claim: func(id, text string) bool {
			return claimTrailFYI(trailFYIDir(home), id, text, time.Now())
		},
	}
}
