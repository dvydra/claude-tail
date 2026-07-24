package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// pending.go — the marker protocol shared with the opt-in Claude Code hooks.
//
// When Claude blocks on an AskUserQuestion or a permission prompt, the deferred
// JSONL flush leaves the transcript dark until the user answers (verified: the
// record lands only afterwards). The hooks write a per-session marker file the
// instant the prompt appears; the live loop stats it and renders the prompt
// immediately, before the flush. See docs/superpowers/specs/2026-07-24-*.

// pendingMarker is one waiting prompt, written by the hook, read by the tail.
// Payload is the tool_input verbatim: for a question, the {questions:[...]}
// object claudeParseQuestions already understands; for a permission, the gated
// tool's {tool_name, tool_input}.
type pendingMarker struct {
	Kind      string          `json:"kind"` // "question" | "permission"
	Payload   json.RawMessage `json:"payload"`
	ToolUseID string          `json:"tool_use_id"`
	Ts        int64           `json:"ts"`
}

// parsePendingMarker decodes a marker file's bytes. ok=false on empty/partial/
// unparseable input, so a half-written file (should not happen — writes are
// atomic — but be safe) is ignored rather than rendered as garbage.
func parsePendingMarker(b []byte) (*pendingMarker, bool) {
	if len(b) == 0 {
		return nil, false
	}
	var m pendingMarker
	if err := json.Unmarshal(b, &m); err != nil || m.Kind == "" {
		return nil, false
	}
	return &m, true
}

// contentKey is a stable dedup key derived from the marker's rendered content,
// namespaced by kind. Both the marker render path and the eventual JSONL card
// compute the SAME key for the same question, so the JSONL card can be
// suppressed once the marker already showed it — independent of whether the
// hook payload carried a tool_use id.
//
// Questions are normalized through claudeParseQuestions/questionsContentKey
// (render.go) rather than hashed as raw bytes: the marker's payload and the
// JSONL record's tool_input are structurally the same {questions:[...]} shape
// but are never byte-identical (the JSONL side carries a tool_use id the hook
// payload doesn't), so a raw-byte hash would never match.
func contentKey(m *pendingMarker) string {
	if m.Kind == "question" {
		return "question:" + questionsContentKey(claudeParseQuestions(m.Payload))
	}
	sum := sha256.Sum256(m.Payload)
	return m.Kind + ":" + hex.EncodeToString(sum[:8])
}

// pendingAction decides whether this tick should render the marker. It renders
// only when a marker is present AND its content key differs from the one last
// rendered (so a marker lingering across ticks — e.g. a slow answer, or a hook
// that failed to clean up — renders exactly once). An absent marker clears the
// remembered key.
func pendingAction(prevKey string, m *pendingMarker, ok bool) (render bool, newKey string) {
	if !ok || m == nil {
		return false, ""
	}
	k := contentKey(m)
	if k == prevKey {
		return false, prevKey
	}
	return true, k
}

// permissionSummary renders a permission marker's payload as "Tool(preview)"
// for the one-line pending notice, matching full-mode's Tool(arg) style
// (adapter_claude.go's claudeToolSummary + render.go's truncateRunes). Falls
// back to the bare tool name when the input yields no preview (unknown tool,
// missing/empty tool_input) — never panics, never renders empty parens.
func permissionSummary(m *pendingMarker) string {
	var p struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	_ = json.Unmarshal(m.Payload, &p)
	arg := claudeToolSummary(p.ToolName, p.ToolInput)
	if arg == "" {
		return p.ToolName
	}
	return p.ToolName + "(" + truncateRunes(arg, 120) + ")"
}

func pendingDir(home string) string {
	return filepath.Join(home, ".claude", "entire-tail", "pending")
}

func pendingMarkerPath(home, sessionID string) string {
	return filepath.Join(pendingDir(home), sessionID+".json")
}

// readPendingMarker reads and parses the marker for a session, if any. Any IO
// error (including not-exist) yields ok=false — the common case each tick.
func readPendingMarker(home, sessionID string) (*pendingMarker, bool) {
	b, err := os.ReadFile(pendingMarkerPath(home, sessionID))
	if err != nil {
		return nil, false
	}
	return parsePendingMarker(b)
}
