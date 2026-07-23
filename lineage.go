package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

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
