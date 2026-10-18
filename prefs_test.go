package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestPrefsRoundTrip(t *testing.T) {
	home := t.TempDir()
	in := savedPrefs{Theme: "nord", ToolStyle: "full", Collapse: "0"}
	if err := savePrefs(home, in); err != nil {
		t.Fatalf("savePrefs: %v", err)
	}
	got := loadPrefs(home)
	if got.Theme != "nord" || got.ToolStyle != "full" || got.Collapse != "0" {
		t.Errorf("strings did not round-trip: %+v", got)
	}
}

// This is a viewer: a missing or corrupt preference file must never stop it
// starting, and must never be reported as an error the user has to deal with.
func TestLoadPrefsToleratesMissingAndCorrupt(t *testing.T) {
	home := t.TempDir()
	if got := loadPrefs(home); got != (savedPrefs{}) {
		t.Errorf("no file gave %+v, want the zero value", got)
	}
	path := prefsPath(home)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("{not json"), 0o644)
	if got := loadPrefs(home); got != (savedPrefs{}) {
		t.Errorf("corrupt file gave %+v, want the zero value", got)
	}
}

// The write is atomic, so a crash mid-save can't leave a half-written file the
// next run would refuse — and the temp file must not be left behind.
func TestSavePrefsLeavesNoTempFile(t *testing.T) {
	home := t.TempDir()
	if err := savePrefs(home, savedPrefs{Theme: "nord"}); err != nil {
		t.Fatalf("savePrefs: %v", err)
	}
	if _, err := os.Stat(prefsPath(home) + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file survived the save")
	}
}

// Precedence: a flag beats an env var beats a saved preference beats the
// built-in default. A preference is what you want when you haven't said
// otherwise, and `--theme dracula` is saying otherwise.
func TestPrefsSitBelowEnvAndFlags(t *testing.T) {
	prefs := savedPrefs{Theme: "nord", ToolStyle: "full", Collapse: "0"}

	c, _, err := parseCLI(nil, envFunc(nil), prefs)
	if err != nil {
		t.Fatalf("parseCLI: %v", err)
	}
	if c.Theme != "nord" || c.ToolStyle != "full" || c.Collapse != "0" {
		t.Errorf("saved preferences ignored: theme=%q tools=%q collapse=%q", c.Theme, c.ToolStyle, c.Collapse)
	}

	env := map[string]string{"ENTIRE_TAIL_THEME": "dracula", "ENTIRE_TAIL_NO_WRAP": "0"}
	c, _, _ = parseCLI(nil, envFunc(env), prefs)
	if c.Theme != "dracula" {
		t.Errorf("env did not beat the saved theme: %q", c.Theme)
	}
	if c.NoWrap {
		t.Error("an explicitly false env var did not beat the wrap-off default")
	}

	c, _, _ = parseCLI([]string{"--theme", "gruvbox"}, envFunc(env), prefs)
	if c.Theme != "gruvbox" {
		t.Errorf("flag did not beat env: %q", c.Theme)
	}
}

// With nothing saved, every default is exactly what it was before preferences
// existed — the file is opt-in, and its absence must change nothing.
func TestNoPrefsKeepsTheOldDefaults(t *testing.T) {
	c, _, err := parseCLI(nil, envFunc(nil), savedPrefs{})
	if err != nil {
		t.Fatalf("parseCLI: %v", err)
	}
	if c.Theme != "tokyo-night" || c.ToolStyle != "dots" || c.Collapse != "5" {
		t.Errorf("defaults moved: %+v", c)
	}
	// Wrap is the one default that moved: off, so a drag-select copies whole
	// paragraphs.
	if !c.NoWrap {
		t.Errorf("want wrap off: NoWrap=%v", c.NoWrap)
	}
}

func TestWrapFlagTurnsWrapOn(t *testing.T) {
	c, _, err := parseCLI([]string{"--wrap"}, envFunc(nil), savedPrefs{})
	if err != nil {
		t.Fatalf("parseCLI: %v", err)
	}
	if c.NoWrap {
		t.Error("--wrap did not beat the wrap-off default")
	}
	c, _, _ = parseCLI([]string{"--wrap", "--no-wrap"}, envFunc(nil), savedPrefs{})
	if !c.NoWrap {
		t.Error("a later --no-wrap did not win")
	}
}

func TestPrefBoolAndNegBool(t *testing.T) {
	if !prefBool("1", boolPtr(false), false) {
		t.Error("env did not win over the saved value")
	}
	if prefBool("", boolPtr(false), true) {
		t.Error("saved false did not win over the default")
	}
	if !prefBool("", nil, true) {
		t.Error("the default was not used when nothing is set")
	}
}

// rememberPref is read-modify-write, so a second entire-tail changing a
// different setting isn't clobbered by this one.
func TestRememberPrefKeepsOtherFields(t *testing.T) {
	home := t.TempDir()
	if err := savePrefs(home, savedPrefs{Theme: "nord", ToolStyle: "full"}); err != nil {
		t.Fatalf("savePrefs: %v", err)
	}
	if err := rememberPref(home, func(p *savedPrefs) { p.Theme = "dracula" }); err != nil {
		t.Fatalf("rememberPref: %v", err)
	}
	got := loadPrefs(home)
	if got.Theme != "dracula" {
		t.Errorf("theme = %q, want dracula", got.Theme)
	}
	if got.ToolStyle != "full" {
		t.Errorf("rememberPref clobbered toolStyle: %q", got.ToolStyle)
	}
}

// Wrap is not a preference: it's a mode you turn on to read a paragraph and off
// again to copy one, and a wrap that came back pinned on the next morning read
// as "wrap won't turn off". So the file never carries it. An old file that does
// (the panel used to write `"wrap": true`) is ignored on the way in and dropped
// on the next write, so the stale key can't keep flipping the default.
func TestWrapIsNeverASavedPreference(t *testing.T) {
	home := t.TempDir()
	path := prefsPath(home)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"theme":"nord","wrap":true}`+"\n"), 0o644)

	c, _, err := parseCLI(nil, envFunc(nil), loadPrefs(home))
	if err != nil {
		t.Fatalf("parseCLI: %v", err)
	}
	if !c.NoWrap {
		t.Error("a saved wrap=true turned wrap on; the file must not be able to")
	}
	if c.Theme != "nord" {
		t.Errorf("the rest of the file was dropped with it: theme=%q", c.Theme)
	}

	if err := rememberPref(home, func(p *savedPrefs) { p.Theme = "dracula" }); err != nil {
		t.Fatalf("rememberPref: %v", err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "wrap") {
		t.Errorf("the stale wrap key survived a rewrite:\n%s", b)
	}
}

// The status bar was removed. A settings file written while it existed still
// carries a statusBar key; loading must ignore it and the next write drop it.
func TestStaleStatusBarPrefIsDropped(t *testing.T) {
	home := t.TempDir()
	path := prefsPath(home)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"theme":"nord","statusBar":false}`+"\n"), 0o644)

	if got := loadPrefs(home); got.Theme != "nord" {
		t.Errorf("the rest of the file was dropped with it: theme=%q", got.Theme)
	}
	if err := rememberPref(home, func(p *savedPrefs) { p.Theme = "dracula" }); err != nil {
		t.Fatalf("rememberPref: %v", err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "statusBar") {
		t.Errorf("the stale statusBar key survived a rewrite:\n%s", b)
	}
}

// --no-status is still accepted, so launch lines that pass it keep working.
func TestNoStatusFlagStillParses(t *testing.T) {
	if _, _, err := parseCLI([]string{"--no-status"}, envFunc(nil), savedPrefs{}); err != nil {
		t.Errorf("--no-status rejected: %v", err)
	}
}
