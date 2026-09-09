package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A round trip has to preserve the tri-state: "saved as off" and "never saved"
// are different answers, and collapsing them would let a preference file pin
// every default it never meant to.
func TestPrefsRoundTrip(t *testing.T) {
	home := t.TempDir()
	in := savedPrefs{Theme: "nord", ToolStyle: "full", Collapse: "0", Wrap: boolPtr(false)}
	if err := savePrefs(home, in); err != nil {
		t.Fatalf("savePrefs: %v", err)
	}
	got := loadPrefs(home)
	if got.Theme != "nord" || got.ToolStyle != "full" || got.Collapse != "0" {
		t.Errorf("strings did not round-trip: %+v", got)
	}
	if got.Wrap == nil || *got.Wrap {
		t.Errorf("Wrap = %v, want a saved false", got.Wrap)
	}
	if got.StatusBar != nil {
		t.Errorf("StatusBar = %v, want unset", *got.StatusBar)
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
	prefs := savedPrefs{Theme: "nord", ToolStyle: "full", Collapse: "0", Wrap: boolPtr(false), StatusBar: boolPtr(false)}

	c, _, err := parseCLI(nil, envFunc(nil), prefs)
	if err != nil {
		t.Fatalf("parseCLI: %v", err)
	}
	if c.Theme != "nord" || c.ToolStyle != "full" || c.Collapse != "0" {
		t.Errorf("saved preferences ignored: theme=%q tools=%q collapse=%q", c.Theme, c.ToolStyle, c.Collapse)
	}
	if !c.NoWrap || !c.NoStatus {
		t.Errorf("saved wrap/status off not applied: NoWrap=%v NoStatus=%v", c.NoWrap, c.NoStatus)
	}

	env := map[string]string{"ENTIRE_TAIL_THEME": "dracula", "ENTIRE_TAIL_NO_WRAP": "0"}
	c, _, _ = parseCLI(nil, envFunc(env), prefs)
	if c.Theme != "dracula" {
		t.Errorf("env did not beat the saved theme: %q", c.Theme)
	}
	if c.NoWrap {
		t.Error("an explicitly false env var did not beat the saved wrap=off")
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
	if c.NoWrap || c.NoStatus {
		t.Errorf("wrap/status defaulted off: NoWrap=%v NoStatus=%v", c.NoWrap, c.NoStatus)
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
	if negBool(nil) != nil {
		t.Error("negBool turned unset into a value")
	}
	if got := negBool(boolPtr(true)); got == nil || *got {
		t.Errorf("negBool(true) = %v, want a false", got)
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
