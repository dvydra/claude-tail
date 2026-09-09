package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// prefs.go is the persisted settings layer: what the `?` panel wrote last time,
// so the tail comes back looking the way you left it.
//
// It sits BELOW flags and env in precedence and above the built-in defaults —
// a preference is what you want when you haven't said otherwise, and
// `--theme dracula` on the command line is saying otherwise. That ordering is
// the whole reason the string fields are "" and the bools are pointers when
// unset: "saved as off" and "never saved" have to be different answers, or a
// preference file would silently pin every default it didn't mean to.
//
// Everything here is best-effort. A missing, unreadable or malformed file leaves
// the built-in defaults in place without a word — this is a viewer, and it must
// start even when its own preference file is nonsense. Writes are atomic
// (temp + rename) so a crash mid-save can't leave a half-written file that the
// next run would refuse.

// savedPrefs is the on-disk shape. Only the settings the panel can change are
// here; the rest of Config is a per-run choice, not a preference.
type savedPrefs struct {
	Theme     string `json:"theme,omitempty"`
	ToolStyle string `json:"toolStyle,omitempty"`
	Collapse  string `json:"collapse,omitempty"` // "5", or "0" for off
	Wrap      *bool  `json:"wrap,omitempty"`
	StatusBar *bool  `json:"statusBar,omitempty"`
}

// prefsPath is where the file lives — beside the other entire-tail state
// (pending markers, tap sidecars, the hook-choice record) rather than in a new
// dotfile of its own.
func prefsPath(home string) string {
	return filepath.Join(home, ".claude", "entire-tail", "settings.json")
}

// loadPrefs reads the saved preferences, or the zero value if there aren't any.
func loadPrefs(home string) savedPrefs {
	var p savedPrefs
	b, err := os.ReadFile(prefsPath(home))
	if err != nil {
		return savedPrefs{}
	}
	if json.Unmarshal(b, &p) != nil {
		return savedPrefs{} // malformed: fall back to the defaults, silently
	}
	return p
}

// savePrefs writes the preferences atomically. Best-effort: the caller reports
// failure in the panel's footer and carries on, because a tail that can't save a
// preference is still a working tail.
func savePrefs(home string, p savedPrefs) error {
	path := prefsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// prefBool resolves a tri-state preference against an env var and a default: the
// env var wins when it's set at all (even to "false"), then the saved value,
// then the built-in.
func prefBool(env string, saved *bool, def bool) bool {
	if env != "" {
		return envTrue(env)
	}
	if saved != nil {
		return *saved
	}
	return def
}

// boolPtr wraps a live value for storing in a savedPrefs.
func boolPtr(b bool) *bool { return &b }

// negBool flips a tri-state, keeping "unset" unset. The preferences are stored
// positively (wrap, statusBar) while the Config fields they feed are negative
// (--no-wrap, --no-status), and inverting a nil pointer to `false` would turn
// "never saved" into "saved as on".
func negBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	return boolPtr(!*b)
}
