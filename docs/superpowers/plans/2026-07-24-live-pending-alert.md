# Live Pending-Question / Permission Alert Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface an `AskUserQuestion` card (and a one-line permission-prompt notice) in the tail pane the instant Claude blocks on the user, before the deferred JSONL flush.

**Architecture:** Opt-in Claude Code hooks write a per-session marker file when a question/permission prompt appears; entire-tail's live loop stats that marker each tick and renders the pending prompt immediately, reusing the existing question-card renderer. When the real JSONL record lands, a content-key dedup suppresses the second card.

**Tech Stack:** Go (single `package main`), stdlib only. A vendored POSIX shell hook script (needs `jq`, already the project's probe dependency). Tests: Go `testing` + golden files.

## Global Constraints

- Single `package main`; no new runtime dependencies in the binary (the hook script may use `jq`, but the binary must not require it at runtime).
- entire-tail stays **read-only on transcripts**. The only writes this feature makes are: the opt-in hook entry in `~/.claude/settings.json`, the vendored hook script, and marker files under `~/.claude/entire-tail/`.
- Live rendering happens ONLY on the render goroutine (the `select` loop in `tailSession`). The marker check runs inside the `<-ticker.C` case — never from a separate goroutine.
- Committed goldens are the rendering contract. Regenerate deliberately with `UPDATE_GOLDEN=1 go test -run TestGolden ./...` and eyeball the diff.
- Gate the review bar: `go vet ./... && go test -race ./...` must pass.
- Marker dir: `~/.claude/entire-tail/pending/`. Hook-choice file: `~/.claude/entire-tail/hook-choice`. Hook script installed at `~/.claude/entire-tail/entire-tail-pending.sh`.
- Feature is Claude-only (codex/agy have no such hooks); all watch logic gates on `agent == AgentClaude`.

---

## File Structure

- **Create `pending.go`** — the marker model: `pendingMarker` struct, JSON (de)serialize, `contentKey`, `pendingAction` decision function, and `pendingMarkerPath`. Pure; no IO beyond a single file read helper.
- **Create `pending_test.go`** — round-trip, content-key stability, `pendingAction` table test.
- **Create `hookinstall.go`** — `mergeHooks` (pure settings.json merge), `unmergeHooks`, `shouldOfferHookInstall` (pure gate predicate), and the `installHooks`/`uninstallHooks`/`offerHookInstall` IO wrappers + subcommand entry points.
- **Create `hookinstall_test.go`** — merge preserves existing + adds entries; unmerge removes only ours; gate predicate matrix.
- **Create `hooks/entire-tail-pending.sh`** — vendored hook script (set/clear markers).
- **Create `hook_script_test.go`** — exec the script with synthetic stdin under a temp `$HOME`, assert marker written/removed.
- **Modify `render.go`** — add `pendingShown` set + `contentKey`-based suppression in `question()`; add `pendingQuestion` and `pendingPermission` render methods; clear `pendingShown` in `reset()`; init the set in `newRendererWith`.
- **Modify `main.go`** — marker watch in the `<-ticker.C` case of `tailSession`; first-run hook offer near the top of `run`; subcommand dispatch for `install-hooks`/`uninstall-hooks`.
- **Modify `config.go`** — `--no-hook-install` flag; `ActionInstallHooks`/`ActionUninstallHooks` actions.
- **Modify `README.md` / `CLAUDE.md`** — document the feature, hooks, opt-in flow.
- **Create goldens** under `testdata/` for the pending-question and pending-permission renders.

---

## Task 1: Marker model (`pending.go`)

**Files:**
- Create: `pending.go`
- Test: `pending_test.go`

**Interfaces:**
- Produces:
  - `type pendingMarker struct { Kind string; Payload json.RawMessage; ToolUseID string; Ts int64 }`
  - `func contentKey(m *pendingMarker) string` — stable dedup key, namespaced by kind.
  - `func parsePendingMarker(b []byte) (*pendingMarker, bool)` — ok=false on unparseable/partial.
  - `func pendingAction(prevKey string, m *pendingMarker, ok bool) (render bool, newKey string)` — decides whether this tick should render.
  - `func pendingMarkerPath(home, sessionID string) string`
  - `func pendingDir(home) string`

- [ ] **Step 1: Write the failing test**

```go
package main

import "testing"

func TestParsePendingMarkerRoundTrip(t *testing.T) {
	raw := []byte(`{"kind":"question","payload":{"questions":[{"question":"Tea or coffee?","header":"Drink","options":[{"label":"Tea","description":"leaf"}]}]},"tool_use_id":"toolu_1","ts":1784862331}`)
	m, ok := parsePendingMarker(raw)
	if !ok {
		t.Fatal("expected ok")
	}
	if m.Kind != "question" || m.ToolUseID != "toolu_1" {
		t.Fatalf("bad parse: %+v", m)
	}
}

func TestParsePendingMarkerRejectsGarbage(t *testing.T) {
	if _, ok := parsePendingMarker([]byte(`{not json`)); ok {
		t.Fatal("expected !ok on garbage")
	}
	if _, ok := parsePendingMarker([]byte(``)); ok {
		t.Fatal("expected !ok on empty")
	}
}

func TestContentKeyStableAndNamespaced(t *testing.T) {
	q1, _ := parsePendingMarker([]byte(`{"kind":"question","payload":{"questions":[{"question":"Q","header":"H","options":[{"label":"A"}]}]}}`))
	q2, _ := parsePendingMarker([]byte(`{"kind":"question","payload":{"questions":[{"question":"Q","header":"H","options":[{"label":"A"}]}]}}`))
	if contentKey(q1) != contentKey(q2) {
		t.Fatal("same content must yield same key")
	}
	p, _ := parsePendingMarker([]byte(`{"kind":"permission","payload":{"tool_name":"Bash","tool_input":{"command":"ls"}}}`))
	if contentKey(p) == contentKey(q1) {
		t.Fatal("kinds must be namespaced apart")
	}
}

func TestPendingActionRendersOnlyOnChange(t *testing.T) {
	m, _ := parsePendingMarker([]byte(`{"kind":"question","payload":{"questions":[{"question":"Q","header":"H"}]}}`))
	render, key := pendingAction("", m, true)
	if !render || key == "" {
		t.Fatal("first sighting must render")
	}
	render2, _ := pendingAction(key, m, true)
	if render2 {
		t.Fatal("same marker must not re-render")
	}
	render3, key3 := pendingAction(key, nil, false)
	if render3 || key3 != "" {
		t.Fatal("absent marker must clear the key and not render")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestParsePendingMarker|TestContentKey|TestPendingAction' ./...`
Expected: FAIL — `undefined: parsePendingMarker` etc.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
func contentKey(m *pendingMarker) string {
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

var _ = strconv.Itoa // keep strconv if unused after edits; remove if the linter flags it
```

Remove the `strconv` import + the `var _` line if unused (they're only there as a placeholder if a later helper needs number formatting; delete before commit if not).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run 'TestParsePendingMarker|TestContentKey|TestPendingAction' ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pending.go pending_test.go
git commit -m "feat: pending-marker model (parse, content-key, tick decision)"
```

---

## Task 2: Hook script (`hooks/entire-tail-pending.sh`)

**Files:**
- Create: `hooks/entire-tail-pending.sh`
- Test: `hook_script_test.go`

**Interfaces:**
- Produces: an executable script invoked as `entire-tail-pending.sh <mode>` where mode ∈ `question-set|question-clear|perm-set|perm-clear`. Reads hook JSON on stdin. Writes/removes `~/.claude/entire-tail/pending/<session_id>.json`. Honors `$HOME`.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runHookScript(t *testing.T, home, mode, stdin string) {
	t.Helper()
	cmd := exec.Command("bash", "hooks/entire-tail-pending.sh", mode)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = stringReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook %s failed: %v: %s", mode, err, out)
	}
}

func TestHookScriptSetsAndClearsQuestionMarker(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	home := t.TempDir()
	payload := `{"session_id":"sid-1","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Q","header":"H","options":[{"label":"A","description":"d"}]}]}}`
	runHookScript(t, home, "question-set", payload)

	marker := filepath.Join(home, ".claude", "entire-tail", "pending", "sid-1.json")
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	m, ok := parsePendingMarker(b)
	if !ok || m.Kind != "question" {
		t.Fatalf("bad marker: %s", b)
	}

	runHookScript(t, home, "question-clear", `{"session_id":"sid-1"}`)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker should be cleared")
	}
}
```

Add this helper once (in a shared `_test.go` if not already present):

```go
import "strings"

func stringReader(s string) *strings.Reader { return strings.NewReader(s) }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestHookScript ./...`
Expected: FAIL — script file does not exist.

- [ ] **Step 3: Write minimal implementation**

Create `hooks/entire-tail-pending.sh`:

```bash
#!/usr/bin/env bash
# entire-tail pending-prompt hook.
# Writes/removes a per-session marker so entire-tail can surface an
# AskUserQuestion card or a permission prompt the instant it appears, before
# Claude Code's deferred transcript flush. Installed + wired by
# `entire-tail install-hooks`. Reads the Claude Code hook JSON on stdin.
#
# Usage: entire-tail-pending.sh <question-set|question-clear|perm-set|perm-clear>
set -euo pipefail

mode="${1:-}"
dir="${HOME}/.claude/entire-tail/pending"
in="$(cat)"

sid="$(printf '%s' "$in" | jq -r '.session_id // empty')"
[ -z "$sid" ] && exit 0
marker="${dir}/${sid}.json"

case "$mode" in
  question-set)
    mkdir -p "$dir"
    tmp="$(mktemp "${marker}.XXXXXX")"
    printf '%s' "$in" | jq -c '{kind:"question", payload:.tool_input, tool_use_id:(.tool_use_id // null), ts:(now|floor)}' > "$tmp"
    mv -f "$tmp" "$marker"
    ;;
  perm-set)
    mkdir -p "$dir"
    tmp="$(mktemp "${marker}.XXXXXX")"
    printf '%s' "$in" | jq -c '{kind:"permission", payload:{tool_name:.tool_name, tool_input:.tool_input}, tool_use_id:(.tool_use_id // null), ts:(now|floor)}' > "$tmp"
    mv -f "$tmp" "$marker"
    ;;
  question-clear|perm-clear)
    rm -f "$marker"
    ;;
  *)
    exit 0
    ;;
esac
exit 0
```

Make it executable:

```bash
chmod +x hooks/entire-tail-pending.sh
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestHookScript ./...`
Expected: PASS (or SKIP if jq absent — acceptable, note it).

- [ ] **Step 5: Commit**

```bash
git add hooks/entire-tail-pending.sh hook_script_test.go
git commit -m "feat: vendored hook script to write/clear pending markers"
```

---

## Task 3: Settings merge (`hookinstall.go` part 1)

**Files:**
- Create: `hookinstall.go`
- Test: `hookinstall_test.go`

**Interfaces:**
- Produces:
  - `func mergeHooks(settings []byte, scriptPath string) ([]byte, error)` — returns settings.json bytes with our 4 hook entries added (idempotent).
  - `func unmergeHooks(settings []byte, scriptPath string) ([]byte, error)` — removes only entries whose command references `scriptPath`.
  - `func hasHookInstalled(settings []byte, scriptPath string) bool`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const fixtureSettings = `{
  "permissions": {"allow": ["Read"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/existing/pre.sh"}]}
    ]
  }
}`

func TestMergeHooksPreservesExistingAndAddsOurs(t *testing.T) {
	out, err := mergeHooks([]byte(fixtureSettings), "/opt/et/entire-tail-pending.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !hasHookInstalled(out, "/opt/et/entire-tail-pending.sh") {
		t.Fatal("our hook must be present after merge")
	}
	if !strings.Contains(string(out), "/existing/pre.sh") {
		t.Fatal("existing hook must be preserved")
	}
	// idempotent
	out2, _ := mergeHooks(out, "/opt/et/entire-tail-pending.sh")
	if countOccur(out2, "entire-tail-pending.sh") != countOccur(out, "entire-tail-pending.sh") {
		t.Fatal("merge must be idempotent")
	}
	// valid JSON
	var v map[string]any
	if json.Unmarshal(out, &v) != nil {
		t.Fatal("output must be valid JSON")
	}
}

func TestUnmergeHooksRemovesOnlyOurs(t *testing.T) {
	merged, _ := mergeHooks([]byte(fixtureSettings), "/opt/et/entire-tail-pending.sh")
	out, err := unmergeHooks(merged, "/opt/et/entire-tail-pending.sh")
	if err != nil {
		t.Fatal(err)
	}
	if hasHookInstalled(out, "/opt/et/entire-tail-pending.sh") {
		t.Fatal("our hook must be gone")
	}
	if !strings.Contains(string(out), "/existing/pre.sh") {
		t.Fatal("existing hook must survive unmerge")
	}
}

func countOccur(b []byte, sub string) int { return strings.Count(string(b), sub) }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestMergeHooks|TestUnmergeHooks' ./...`
Expected: FAIL — `undefined: mergeHooks`.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// hookinstall.go — install/remove the opt-in pending-prompt hooks in the user's
// ~/.claude/settings.json, and the first-run offer gate. The merge is a pure
// function over the settings bytes so it is unit-tested without touching disk;
// the IO wrappers (backup, write) are the thin untested edge.

// hookSpec is one (event, matcher, mode-arg) our script needs wired.
type hookSpec struct {
	event   string
	matcher string
	mode    string
}

// pendingHookSpecs is the full set: a question opens/closes on the tool's
// Pre/Post; a permission opens on PermissionRequest and closes on
// PermissionDenied (deny) or the gated tool's PostToolUse (grant → tool ran).
func pendingHookSpecs() []hookSpec {
	return []hookSpec{
		{"PreToolUse", "AskUserQuestion", "question-set"},
		{"PostToolUse", "AskUserQuestion", "question-clear"},
		{"PermissionRequest", "", "perm-set"},
		{"PermissionDenied", "", "perm-clear"},
	}
}

func hookCommand(scriptPath, mode string) string {
	return scriptPath + " " + mode
}

// mergeHooks adds our hook entries to settings, preserving everything else, and
// is idempotent (an entry already referencing scriptPath+mode is not
// duplicated). Missing objects/arrays are created.
func mergeHooks(settings []byte, scriptPath string) ([]byte, error) {
	root := map[string]any{}
	if len(strings.TrimSpace(string(settings))) > 0 {
		if err := json.Unmarshal(settings, &root); err != nil {
			return nil, fmt.Errorf("settings.json is not valid JSON: %w", err)
		}
	}
	hooks := asObj(root, "hooks")
	for _, s := range pendingHookSpecs() {
		cmd := hookCommand(scriptPath, s.mode)
		arr := asArr(hooks, s.event)
		if hookGroupExists(arr, cmd) {
			continue
		}
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}
		if s.matcher != "" {
			entry["matcher"] = s.matcher
		}
		hooks[s.event] = append(arr, entry)
	}
	root["hooks"] = hooks
	return json.MarshalIndent(root, "", "  ")
}

// unmergeHooks removes any hook group whose command references scriptPath,
// leaving all other entries intact. Empty event arrays are dropped.
func unmergeHooks(settings []byte, scriptPath string) ([]byte, error) {
	root := map[string]any{}
	if err := json.Unmarshal(settings, &root); err != nil {
		return nil, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return json.MarshalIndent(root, "", "  ")
	}
	for event, v := range hooks {
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(arr))
		for _, g := range arr {
			if !groupRefsScript(g, scriptPath) {
				kept = append(kept, g)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}
	return json.MarshalIndent(root, "", "  ")
}

func hasHookInstalled(settings []byte, scriptPath string) bool {
	root := map[string]any{}
	if json.Unmarshal(settings, &root) != nil {
		return false
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return false
	}
	for _, v := range hooks {
		if arr, ok := v.([]any); ok {
			for _, g := range arr {
				if groupRefsScript(g, scriptPath) {
					return true
				}
			}
		}
	}
	return false
}

// ── small any-tree helpers ──

func asObj(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	o := map[string]any{}
	m[k] = o
	return o
}

func asArr(m map[string]any, k string) []any {
	if v, ok := m[k].([]any); ok {
		return v
	}
	return nil
}

func hookGroupExists(arr []any, cmd string) bool {
	for _, g := range arr {
		if groupHasCommand(g, cmd) {
			return true
		}
	}
	return false
}

func groupHasCommand(group any, cmd string) bool {
	g, ok := group.(map[string]any)
	if !ok {
		return false
	}
	hs, ok := g["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range hs {
		if hm, ok := h.(map[string]any); ok {
			if c, _ := hm["command"].(string); c == cmd {
				return true
			}
		}
	}
	return false
}

func groupRefsScript(group any, scriptPath string) bool {
	g, ok := group.(map[string]any)
	if !ok {
		return false
	}
	hs, ok := g["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range hs {
		if hm, ok := h.(map[string]any); ok {
			if c, _ := hm["command"].(string); strings.Contains(c, scriptPath) {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run 'TestMergeHooks|TestUnmergeHooks' ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add hookinstall.go hookinstall_test.go
git commit -m "feat: pure settings.json merge/unmerge for pending hooks"
```

---

## Task 4: Install-offer gate predicate (`hookinstall.go` part 2)

**Files:**
- Modify: `hookinstall.go`
- Test: `hookinstall_test.go`

**Interfaces:**
- Consumes: `mergeHooks`, `hasHookInstalled` (Task 3).
- Produces: `func shouldOfferHookInstall(g hookOfferInputs) bool` where
  `type hookOfferInputs struct { isTTY, adopted, alreadyInstalled, choiceRecorded, noHookInstall, followSession, noPick, isClaude bool }`.

- [ ] **Step 1: Write the failing test**

```go
func TestShouldOfferHookInstall(t *testing.T) {
	base := hookOfferInputs{isTTY: true, isClaude: true}
	if !shouldOfferHookInstall(base) {
		t.Fatal("clean interactive Claude run should offer")
	}
	cases := []struct {
		name  string
		mut   func(*hookOfferInputs)
	}{
		{"not tty", func(i *hookOfferInputs) { i.isTTY = false }},
		{"adopted", func(i *hookOfferInputs) { i.adopted = true }},
		{"already installed", func(i *hookOfferInputs) { i.alreadyInstalled = true }},
		{"choice recorded", func(i *hookOfferInputs) { i.choiceRecorded = true }},
		{"--no-hook-install", func(i *hookOfferInputs) { i.noHookInstall = true }},
		{"--follow-session", func(i *hookOfferInputs) { i.followSession = true }},
		{"--no-pick", func(i *hookOfferInputs) { i.noPick = true }},
		{"not claude", func(i *hookOfferInputs) { i.isClaude = false }},
	}
	for _, c := range cases {
		in := base
		c.mut(&in)
		if shouldOfferHookInstall(in) {
			t.Errorf("%s: must NOT offer", c.name)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestShouldOfferHookInstall ./...`
Expected: FAIL — `undefined: shouldOfferHookInstall`.

- [ ] **Step 3: Write minimal implementation**

Append to `hookinstall.go`:

```go
// hookOfferInputs are the pure inputs to the first-run offer decision.
type hookOfferInputs struct {
	isTTY            bool
	adopted          bool
	alreadyInstalled bool
	choiceRecorded   bool
	noHookInstall    bool
	followSession    bool
	noPick           bool
	isClaude         bool
}

// shouldOfferHookInstall reports whether the one-time "add the live-question
// hook?" prompt should appear. It fires ONLY on a clean interactive Claude run
// where the user hasn't already decided and no context makes a prompt wrong
// (piped/automated/workspace-pane/adopted). This keeps entire-tail's read-only
// charter: it never silently writes global config, and never nags.
func shouldOfferHookInstall(g hookOfferInputs) bool {
	if !g.isTTY || !g.isClaude {
		return false
	}
	if g.adopted || g.followSession || g.noPick {
		return false // workspace pane / automated latch — never interrupt
	}
	if g.alreadyInstalled || g.choiceRecorded || g.noHookInstall {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestShouldOfferHookInstall ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add hookinstall.go hookinstall_test.go
git commit -m "feat: pure gate predicate for the first-run hook offer"
```

---

## Task 5: Renderer — early card + suppression (`render.go`)

**Files:**
- Modify: `render.go` (Renderer struct ~line 96, `newRendererWith` ~line 162, `reset` ~line 187, `question` ~line 244)
- Test: `render_test.go` (or the existing render test file) + golden

**Interfaces:**
- Consumes: `contentKey` needs a `[]QuestionItem` variant — add `func questionsContentKey(qs []QuestionItem) string` in `render.go`, and have `contentKey` (Task 1) call it for questions so the marker path and JSONL path agree.
- Produces:
  - `func (r *Renderer) pendingQuestion(qs []QuestionItem)`
  - `func (r *Renderer) pendingPermission(summary string)`
  - `pendingShown map[string]bool` field.

> **Note for implementer:** Task 1's `contentKey(m)` hashes `m.Payload` (raw JSON bytes). The JSONL card path has `[]QuestionItem`, not raw bytes. To guarantee the marker key and the JSONL key MATCH, both must derive from the same normalized content. Change Task 1's `contentKey` for the `"question"` kind to parse the payload via `claudeParseQuestions` and delegate to `questionsContentKey`, so key equality is by rendered questions, not by byte-identical JSON. Update this in `pending.go` as the first step here.

- [ ] **Step 1: Adjust `contentKey` to normalize question payloads**

In `pending.go`, replace the body of `contentKey`:

```go
func contentKey(m *pendingMarker) string {
	if m.Kind == "question" {
		return "question:" + questionsContentKey(claudeParseQuestions(m.Payload))
	}
	sum := sha256.Sum256(m.Payload)
	return m.Kind + ":" + hex.EncodeToString(sum[:8])
}
```

- [ ] **Step 2: Write the failing test**

```go
func TestPendingQuestionSuppressesLaterJSONLCard(t *testing.T) {
	var buf bytes.Buffer
	r := newRendererWith(&buf, testTheme(), "dots", 5, plainRender)
	r.live = true

	qs := []QuestionItem{{Header: "Drink", Question: "Tea or coffee?", Options: []string{"Tea", "Coffee"}}}

	// Marker fires first (pre-answer).
	r.pendingQuestion(qs)
	afterMarker := buf.String()
	if !strings.Contains(afterMarker, "Tea or coffee?") {
		t.Fatal("marker must render the card")
	}
	if !strings.Contains(afterMarker, "\a") {
		t.Fatal("marker must ring the bell")
	}

	// The eventual JSONL record for the SAME question must NOT render a 2nd card.
	buf.Reset()
	r.emit(Record{Kind: KindQuestion, QID: "toolu_x", Questions: qs})
	if strings.Contains(buf.String(), "Tea or coffee?") {
		t.Fatal("JSONL card must be suppressed after the marker showed it")
	}
}

func TestQuestionKeyMatchesMarkerAndJSONL(t *testing.T) {
	qs := []QuestionItem{{Header: "H", Question: "Q", Options: []string{"A — d"}}}
	m, _ := parsePendingMarker([]byte(`{"kind":"question","payload":{"questions":[{"question":"Q","header":"H","options":[{"label":"A","description":"d"}]}]}}`))
	if contentKey(m) != "question:"+questionsContentKey(qs) {
		t.Fatal("marker key must equal the JSONL questions key")
	}
}
```

(Use whatever the existing render tests use for `testTheme()`/`plainRender`; if named differently, match them — grep the existing `render_test.go`.)

- [ ] **Step 3: Run test to verify it fails**

Run: `go test -run 'TestPendingQuestion|TestQuestionKey' ./...`
Expected: FAIL — `undefined: r.pendingQuestion` / `questionsContentKey`.

- [ ] **Step 4: Write minimal implementation**

In `render.go`, add the field to the `Renderer` struct (after `seenQuestions`):

```go
	// pendingShown holds content keys of questions already rendered from a live
	// marker (the pre-answer alert). The eventual JSONL card for the same
	// question is suppressed so the user sees exactly one card. Cleared by
	// reset() so a full re-render (r / rollover) shows JSONL cards normally.
	pendingShown map[string]bool
```

Init it in `newRendererWith` (alongside `seenQuestions`):

```go
		seenQuestions:   map[string]bool{},
		pendingShown:    map[string]bool{},
```

Clear it in `reset()`:

```go
func (r *Renderer) reset() {
	r.lastKind = ""
	r.inDotStreak = false
	r.lineOpen = false
	clear(r.pendingShown)
}
```

Add `questionsContentKey` and the two render methods (near `question`):

```go
// questionsContentKey is a stable hash of a question set's rendered content, so
// a pre-answer marker card and the eventual JSONL card dedup against each other
// regardless of tool_use ids.
func questionsContentKey(qs []QuestionItem) string {
	h := sha256.New()
	for _, q := range qs {
		io.WriteString(h, q.Header+"\x00"+q.Question+"\x00")
		for _, o := range q.Options {
			io.WriteString(h, o+"\x1f")
		}
		io.WriteString(h, "\x1e")
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// pendingQuestion renders a question card from a live marker (before the JSONL
// flush), always ringing the bell, and records its content key so the eventual
// JSONL card is suppressed. Runs on the render goroutine like every other emit.
func (r *Renderer) pendingQuestion(qs []QuestionItem) {
	r.endLine()
	io.WriteString(r.w, "\a")
	io.WriteString(r.w, questionCard(qs))
	r.pendingShown[questionsContentKey(qs)] = true
}

// pendingPermission renders a one-line "waiting on a permission prompt" notice
// from a live marker, with the bell. There's no JSONL counterpart to dedup — a
// granted permission just becomes a normal tool render later.
func (r *Renderer) pendingPermission(summary string) {
	r.endLine()
	io.WriteString(r.w, "\a")
	io.WriteString(r.w, r.theme.DimANSI+"⏳ waiting: "+summary+reset+"\n")
}
```

Modify `question` to suppress a card the marker already showed (add at the very top, before `endLine`):

```go
func (r *Renderer) question(rec Record) {
	key := questionsContentKey(rec.Questions)
	if r.pendingShown[key] {
		delete(r.pendingShown, key) // one-shot: consume so a later reload re-renders
		return
	}
	r.endLine()
	if r.live && rec.QID != "" && !r.seenQuestions[rec.QID] {
		io.WriteString(r.w, "\a")
	}
	if rec.QID != "" {
		r.seenQuestions[rec.QID] = true
	}
	io.WriteString(r.w, questionCard(rec.Questions))
}
```

Add imports to `render.go` if not present: `crypto/sha256`, `encoding/hex`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -run 'TestPendingQuestion|TestQuestionKey|TestParsePendingMarker|TestContentKey' ./...`
Expected: PASS

- [ ] **Step 6: Add goldens for the pending renders**

Create a golden-producing test (follow the existing `TestGolden` pattern — grep `render_test.go` / `golden` for the exact harness). Add two fixtures: a pending question and a pending permission, rendered via `pendingQuestion`/`pendingPermission`, captured to `testdata/pending_question.golden` and `testdata/pending_permission.golden`.

```bash
UPDATE_GOLDEN=1 go test -run TestGolden ./...
```

Eyeball the two new `.golden` files (the card must match the existing question card; the permission line must be `⏳ waiting: Bash(…)` dim).

- [ ] **Step 7: Full suite + race**

Run: `go vet ./... && go test -race ./...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add render.go pending.go render_test.go testdata/pending_question.golden testdata/pending_permission.golden
git commit -m "feat: render pending question/permission early, dedup vs JSONL card"
```

---

## Task 6: Live-loop marker watch (`main.go`)

**Files:**
- Modify: `main.go` — `tailSession` live loop (the `<-ticker.C` case, ~line 405) and the loop's local state setup (before the `for`, ~line 369).

**Interfaces:**
- Consumes: `readPendingMarker`, `pendingAction`, `pendingDir` (Task 1); `r.pendingQuestion`, `r.pendingPermission` (Task 5); existing `sessionIDFromPath`, `claudeParseQuestions`, `home`, `agent`, `cur`.
- Produces: no new exported symbols; a `permissionSummary(*pendingMarker) string` helper in `pending.go`.

- [ ] **Step 1: Add `permissionSummary` + test (`pending.go` / `pending_test.go`)**

```go
// permissionSummary renders a permission marker's payload as "Tool(preview)" for
// the one-line pending notice, reusing Bash-style flattening.
func permissionSummary(m *pendingMarker) string {
	var p struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	_ = json.Unmarshal(m.Payload, &p)
	preview := toolSummary(p.ToolName, p.ToolInput) // reuse existing tool-input summarizer
	if preview == "" {
		return p.ToolName
	}
	return preview
}
```

> Implementer: grep for the existing tool-input→one-line summarizer used to build `Record.Summary` (adapter_claude.go). Use it here so the permission preview matches how Bash commands render elsewhere (flattened, truncated to 120 runes). If its signature differs, adapt this call; the goal is `Bash(git push …)`-style text.

Test:

```go
func TestPermissionSummary(t *testing.T) {
	m, _ := parsePendingMarker([]byte(`{"kind":"permission","payload":{"tool_name":"Bash","tool_input":{"command":"git push"}}}`))
	got := permissionSummary(m)
	if !strings.Contains(got, "git push") {
		t.Fatalf("want the command in the summary, got %q", got)
	}
}
```

Run: `go test -run TestPermissionSummary ./...` → FAIL, then implement → PASS.

- [ ] **Step 2: Wire the watch into the live loop**

Before the `for {` (near line 369, alongside `idle := 0`), add:

```go
	// Live pending-prompt watch (Claude only, and only when the hook is
	// installed — i.e. the markers dir exists). lastMarkerKey dedups so a marker
	// lingering across ticks renders exactly once.
	pendingWatch := agent == AgentClaude && isDir(pendingDir(home))
	lastMarkerKey := ""
```

In the `<-ticker.C` case, AFTER `poll()` and the rollover block, before `out.Flush()`, add:

```go
			if pendingWatch {
				m, ok := readPendingMarker(home, sessionIDFromPath(cur))
				if render, key := pendingAction(lastMarkerKey, m, ok); render {
					switch m.Kind {
					case "question":
						r.pendingQuestion(claudeParseQuestions(m.Payload))
					case "permission":
						r.pendingPermission(permissionSummary(m))
					}
					lastMarkerKey = key
				} else {
					lastMarkerKey = key
				}
			}
```

> Implementer: if `isDir` doesn't already exist, add a trivial helper next to `isFile` (grep — `isFile` is defined in the codebase). `sessionIDFromPath` is used in `rollover` already; reuse it.

- [ ] **Step 3: Build + manual smoke**

Run: `go build -o entire-tail .`
Then a manual render check (won't fire markers, just proves no regression):
`timeout 2 ./entire-tail --agent claude --no-pick --tool-style dots testdata/claude_session.jsonl`
Expected: renders as before, no panic.

- [ ] **Step 4: Full suite + race**

Run: `go vet ./... && go test -race ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go pending.go pending_test.go
git commit -m "feat: watch pending markers in the live loop, render early"
```

---

## Task 7: Subcommands + first-run offer + flag (`config.go`, `main.go`, `hookinstall.go`)

**Files:**
- Modify: `config.go` (add `--no-hook-install`, `NoHookInstall bool`; add `ActionInstallHooks`, `ActionUninstallHooks`; dispatch `install-hooks`/`uninstall-hooks` like `handover`).
- Modify: `hookinstall.go` (add IO wrappers `installHooks(home) error`, `uninstallHooks(home) error`, `offerHookInstallIfNeeded(...)`, `recordHookChoice`, `hookChoiceRecorded`).
- Modify: `main.go` (dispatch the two actions; call the offer near the top of the normal run, after adoption is known).

**Interfaces:**
- Consumes: `mergeHooks`, `unmergeHooks`, `hasHookInstalled`, `shouldOfferHookInstall`, `pendingDir` (Tasks 3-4-1); the embedded hook script bytes.
- Produces: `ActionInstallHooks`, `ActionUninstallHooks` (config.go); `installHooks`, `uninstallHooks` (hookinstall.go).

- [ ] **Step 1: config — flag + actions + dispatch**

In `config.go`: add to `Config`:

```go
	NoHookInstall bool // --no-hook-install: suppress the first-run pending-hook offer
```

Add actions to the `const` block:

```go
	ActionInstallHooks   // `entire-tail install-hooks`
	ActionUninstallHooks // `entire-tail uninstall-hooks`
```

At the top of `parseCLI` (next to the `handover` check):

```go
	if len(args) > 0 && args[0] == "install-hooks" {
		return c, ActionInstallHooks, nil
	}
	if len(args) > 0 && args[0] == "uninstall-hooks" {
		return c, ActionUninstallHooks, nil
	}
```

Add the flag case in the arg loop (near `--no-mark-continuation`):

```go
		case a == "--no-hook-install":
			c.NoHookInstall = true
```

- [ ] **Step 2: hookinstall — embed script + IO wrappers**

Add to `hookinstall.go`:

```go
import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed hooks/entire-tail-pending.sh
var pendingHookScript []byte

func hookScriptPath(home string) string {
	return filepath.Join(home, ".claude", "entire-tail", "entire-tail-pending.sh")
}

func hookChoicePath(home string) string {
	return filepath.Join(home, ".claude", "entire-tail", "hook-choice")
}

func hookChoiceRecorded(home string) bool {
	_, err := os.Stat(hookChoicePath(home))
	return err == nil
}

func recordHookChoice(home, choice string) {
	_ = os.MkdirAll(filepath.Dir(hookChoicePath(home)), 0o755)
	_ = os.WriteFile(hookChoicePath(home), []byte(choice+"\n"), 0o644)
}

// installHooks writes the vendored script, creates the markers dir, and merges
// the hook entries into settings.json (backing the old file up first).
func installHooks(home string) error {
	base := filepath.Join(home, ".claude", "entire-tail")
	if err := os.MkdirAll(filepath.Join(base, "pending"), 0o755); err != nil {
		return err
	}
	sp := hookScriptPath(home)
	if err := os.WriteFile(sp, pendingHookScript, 0o755); err != nil {
		return err
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	cur, _ := os.ReadFile(settingsPath)
	if len(cur) > 0 {
		_ = os.WriteFile(settingsPath+".entire-tail.bak", cur, 0o644)
	}
	merged, err := mergeHooks(cur, sp)
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, merged, 0o644)
}

// uninstallHooks removes our entries from settings.json (leaving the script +
// markers dir in place is harmless, but remove the script too).
func uninstallHooks(home string) error {
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	cur, err := os.ReadFile(settingsPath)
	if err != nil {
		return err
	}
	out, err := unmergeHooks(cur, hookScriptPath(home))
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		return err
	}
	_ = os.Remove(hookScriptPath(home))
	return nil
}
```

- [ ] **Step 3: hookinstall — the offer (IO)**

```go
import (
	"bufio"
	"fmt"
	"strings"
)

// offerHookInstall prints the one-time prompt and, on yes, installs. Either way
// it records the choice so it never asks again. Reads a single line from in.
func offerHookInstall(home string, in *bufio.Reader) {
	fmt.Fprint(os.Stderr, "entire-tail: add the live pending-question hook to ~/.claude/settings.json? "+
		"It surfaces AskUserQuestion/permission prompts the instant they appear. [y/N] ")
	line, _ := in.ReadString('\n')
	if strings.EqualFold(strings.TrimSpace(line), "y") {
		if err := installHooks(home); err != nil {
			fmt.Fprintln(os.Stderr, "entire-tail: hook install failed: "+err.Error())
			return // don't record — let them retry next run
		}
		recordHookChoice(home, "yes")
		fmt.Fprintln(os.Stderr, "entire-tail: installed. Restart Claude Code (or open /hooks once) so it loads the hook.")
		return
	}
	recordHookChoice(home, "no")
	fmt.Fprintln(os.Stderr, "entire-tail: skipped. Run `entire-tail install-hooks` anytime to enable it.")
}
```

- [ ] **Step 4: main — dispatch actions + call the offer**

In `main.go` where actions are dispatched (grep `ActionHandover`), add:

```go
	case ActionInstallHooks:
		if err := installHooks(home); err != nil {
			die("install-hooks: " + err.Error())
		}
		fmt.Println("entire-tail: hooks installed. Restart Claude Code (or open /hooks once) to load them.")
		return
	case ActionUninstallHooks:
		if err := uninstallHooks(home); err != nil {
			die("uninstall-hooks: " + err.Error())
		}
		fmt.Println("entire-tail: hooks removed.")
		return
```

In `run`, AFTER the adopt block (so `adopted` is known — track whether adoption happened in a local `adopted bool` set inside the adopt `if`), and only on the normal interactive path, add the offer. Place it just before the picker/discovery dispatch:

```go
	if shouldOfferHookInstall(hookOfferInputs{
		isTTY:            ttyUsable(),
		adopted:          adopted,
		alreadyInstalled: hookInstalledFor(home),
		choiceRecorded:   hookChoiceRecorded(home),
		noHookInstall:    cfg.NoHookInstall,
		followSession:    cfg.FollowSession != "",
		noPick:           cfg.Pick == "never",
		isClaude:         agentStr == "auto" || agentStr == "claude",
	}) {
		offerHookInstall(home, bufio.NewReader(os.Stdin))
	}
```

Add a tiny helper in `hookinstall.go`:

```go
func hookInstalledFor(home string) bool {
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	return hasHookInstalled(b, hookScriptPath(home))
}
```

> Implementer: set `adopted := false` before the adopt block in `run`, and `adopted = true` inside the `if p, cwd := adoptPaneSession(...); p != ""` branch. The offer must read from `os.Stdin`; it runs before the picker's alt-screen, so a plain line read is safe.

- [ ] **Step 5: Build + suite + race**

Run: `go build -o entire-tail . && go vet ./... && go test -race ./...`
Expected: PASS

- [ ] **Step 6: Manual end-to-end (documented, not automated)**

```bash
./entire-tail install-hooks
# restart a claude session, provoke: "Use your AskUserQuestion tool to ask me tea vs coffee"
# in a second pane: ./entire-tail --follow-session <that-id>   (card should appear BEFORE you answer)
./entire-tail uninstall-hooks
```

- [ ] **Step 7: Commit**

```bash
git add config.go hookinstall.go main.go
git commit -m "feat: install-hooks/uninstall-hooks subcommands + first-run offer"
```

---

## Task 8: Docs (`README.md`, `CLAUDE.md`)

**Files:**
- Modify: `README.md` (new section: live pending-question alert — what it does, `install-hooks`, `--no-hook-install`, the opt-in/footprint note).
- Modify: `CLAUDE.md` (Architecture: add `pending.go` + `hookinstall.go` bullets and the vendored hook script; Things-that-are-load-bearing: the content-key dedup and the "first global-config write" footprint).

- [ ] **Step 1: README**

Add a section documenting: the problem (deferred flush), what the alert does, `entire-tail install-hooks` / `uninstall-hooks`, `--no-hook-install`, that it edits `~/.claude/settings.json` (opt-in, reversible), and that it's Claude-only.

- [ ] **Step 2: CLAUDE.md**

Add architecture bullets for `pending.go` (marker model + live watch) and `hookinstall.go` (settings merge + offer gate) and `hooks/entire-tail-pending.sh`. Note in the load-bearing section: content-key dedup keeps the marker card and JSONL card from doubling; this is the first feature that writes global config, gated by the offer predicate.

- [ ] **Step 3: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: live pending-question alert + hook install"
```

---

## Self-Review (author checklist — completed)

**Spec coverage:** signals/hooks → Tasks 2,7; marker transport → Task 1,2; watch+render+dedup → Tasks 5,6; install UX (auto-offer + gates + subcommands + flag) → Tasks 4,7; testing matrix → Tasks 1,3,4,5,6; footprint/docs → Task 8. All spec sections mapped.

**Placeholder scan:** no TBD/TODO; every code step shows real code. Two implementer notes (existing tool-summary helper name; `isDir`/`sessionIDFromPath` reuse) point at real, greppable symbols rather than leaving gaps.

**Type consistency:** `pendingMarker`, `contentKey`, `pendingAction`, `pendingQuestion`, `pendingPermission`, `permissionSummary`, `mergeHooks`/`unmergeHooks`/`hasHookInstalled`, `shouldOfferHookInstall`/`hookOfferInputs`, `installHooks`/`uninstallHooks`, `ActionInstallHooks`/`ActionUninstallHooks` are named identically across tasks. `contentKey` is reconciled with `questionsContentKey` in Task 5 Step 1 (the one cross-task dependency, called out explicitly).
