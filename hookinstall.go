package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if root == nil {
		root = map[string]any{}
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
	cur, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		// Any read failure other than "no such file" means the file exists but we
		// couldn't read it — bail rather than overwrite it with a merge into empty
		// (which would clobber it with no backup taken).
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}
	if len(cur) > 0 {
		if err := os.WriteFile(settingsPath+".entire-tail.bak", cur, 0o644); err != nil {
			return fmt.Errorf("back up %s: %w", settingsPath, err)
		}
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

// hookInstalledFor reports whether our hook entries are already present in
// home's settings.json (a missing/unreadable file is "not installed").
func hookInstalledFor(home string) bool {
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	return hasHookInstalled(b, hookScriptPath(home))
}

// offerHookInstall prints the one-time prompt and, on yes, installs. Either way
// it records the choice so it never asks again. Reads a single line from in.
func offerHookInstall(home string, in *bufio.Reader) {
	fmt.Fprint(os.Stderr, "entire-tail: add the live pending-question hook to ~/.claude/settings.json? "+
		"It surfaces AskUserQuestion/permission prompts the instant they appear. [y/N] ")
	line, _ := in.ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans == "y" || ans == "yes" {
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
