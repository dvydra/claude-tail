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
