package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// sessionIDRe matches a UUID-shaped session id (8-4-4-4-12 hex). Claude session
// ids are v4 UUIDs and newSessionID mints the same shape.
var sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validSessionID reports whether id is UUID-shaped. It gates ids that get joined
// into a <id>.jsonl path (--follow-session, the pinned workspace's resume id):
// rejecting anything else keeps a crafted id like "../../etc/x" — which
// filepath.Join would clean into an escape from the project dir — out.
func validSessionID(id string) bool {
	return sessionIDRe.MatchString(id)
}

// lineage.go follows a Claude session across a fork. Claude Code mints a NEW
// session id (a new <id>.jsonl in the same project dir) when it re-enters a
// worktree; the fresh file records the id it forked FROM in its worktree-state
// record (worktreeSession.sessionId). A plain tail latched onto the old file
// would freeze at the fork point (see the drift-noise orphaning). So the live
// loop keeps a lineage set — the ids it considers "ours" — and rolls over to a
// sibling whose fork pointer is in that set. Matching by the explicit pointer
// (not "newest file") means a concurrent, unrelated Claude in the same repo is
// never adopted.

// sessionIDFromPath returns a session file's id (its basename without .jsonl).
func sessionIDFromPath(p string) string {
	return strings.TrimSuffix(filepath.Base(p), ".jsonl")
}

// forkPointer scans the opening records of a Claude session file for a worktree
// fork pointer — worktreeSession.sessionId, the id of the session this one was
// forked from on a worktree re-enter. A /clear writes the SAME field (a new
// <id>.jsonl whose worktreeSession.sessionId is the pre-clear session, alongside
// the full worktree metadata), so lineage rollover follows /clear for free.
// Returns "" if there is none.
func forkPointer(head []byte) string {
	for _, line := range splitLines(head) {
		var rec struct {
			WorktreeSession *struct {
				SessionID string `json:"sessionId"`
			} `json:"worktreeSession"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.WorktreeSession != nil && rec.WorktreeSession.SessionID != "" {
			return rec.WorktreeSession.SessionID
		}
	}
	return ""
}

// rolloverIdleTicks is how many quiet poll ticks (pollInterval each, so ~1s)
// must pass before the live loop scans for a forked child. Waiting for the
// current file to fall silent avoids rolling over mid-turn.
const rolloverIdleTicks = 4

// headScanBytes caps how much of a candidate file we read to find its fork
// pointer. The worktree-state record is the second line of a forked session, so
// a few KB is always enough — and it keeps the per-tick sibling scan cheap even
// when a project dir holds many multi-MB transcripts.
const headScanBytes = 16 * 1024

// readHead returns up to n leading bytes of path (fewer if the file is shorter).
func readHead(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	m, _ := io.ReadFull(f, buf) // short files return ErrUnexpectedEOF; m is still valid
	return buf[:m]
}

// readTail returns up to the last n bytes of path (fewer if the file is
// shorter). The first line of the returned slice may be partial when the file
// exceeds n — callers that parse per-line JSON skip it as unparseable.
func readTail(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if fi.Size() > int64(n) {
		start = fi.Size() - int64(n)
	}
	buf := make([]byte, fi.Size()-start)
	m, _ := f.ReadAt(buf, start)
	return buf[:m]
}

// continuationMarker is the content prefix entire-tail writes into a stopped
// session file at a lineage flip. It is namespaced so a reader (and Claude on
// resume) reads it as an external annotation, never an instruction, and so a
// later run can recognize its own prior marker for idempotency.
const continuationMarker = "entire-tail · session continued in "

// markContinuation appends a Claude-Code-native system/informational record to a
// stopped session file (oldPath), noting the session id it continued into
// (newID). On disk the old file just stops with no forward pointer; this leaves
// one. Claude Code renders system/informational records as dim transcript lines
// and does NOT replay them to the model, so the pointer shows on resume without
// steering Claude. Only the stopped side is touched — the forked child is live
// (Claude is writing it) and already records the backward worktreeSession
// pointer.
//
// Best-effort: every failure path is a silent no-op, because a failed annotation
// must never disrupt the tail. Idempotent: skips when the tail already holds a
// marker for newID, so repeated runs don't stack duplicates.
func markContinuation(oldPath, newID string) {
	tail := readTail(oldPath, headScanBytes)
	if len(tail) == 0 || strings.Contains(string(tail), continuationMarker+newID) {
		return
	}
	// Chain off — and copy context from — the last complete record that carries a
	// uuid, so the marker attaches as that leaf's child and renders on resume
	// (Claude Code walks parentUuid; a null parent would orphan it). Trailing
	// records like file-history-snapshot lack a uuid, so skip them.
	var last map[string]any
	for _, line := range splitLines(tail) {
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if id, ok := m["uuid"].(string); ok && id != "" {
			last = m
		}
	}
	if last == nil {
		return
	}
	rec := map[string]any{
		"parentUuid":  last["uuid"],
		"isSidechain": false,
		"type":        "system",
		"subtype":     "informational",
		"content":     continuationMarker + newID,
		"isMeta":      false,
		"level":       "info",
		"timestamp":   time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"uuid":        newSessionID(),
		"userType":    "external",
	}
	// Carry the session's own context fields so the record matches its siblings.
	for _, k := range []string{"sessionId", "cwd", "version", "gitBranch"} {
		if v, ok := last[k]; ok {
			rec[k] = v
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(oldPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// lineageChild scans dir for a sibling session file (other than curPath) that
// forked from a session in `lineage` and has content, returning its path and
// id. Empty path means no child yet. Deterministic: filepath.Glob is sorted, so
// the earliest-named match wins if somehow two point at the same lineage.
func lineageChild(dir, curPath string, lineage map[string]bool) (string, string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, m := range matches {
		if m == curPath {
			continue
		}
		id := sessionIDFromPath(m)
		if lineage[id] {
			continue // already in our lineage (the current or a prior file)
		}
		fi, err := os.Stat(m)
		if err != nil || fi.Size() == 0 {
			continue
		}
		if parent := forkPointer(readHead(m, headScanBytes)); parent != "" && lineage[parent] {
			return m, id
		}
	}
	return "", ""
}
