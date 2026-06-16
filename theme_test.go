package main

import (
	"os"
	"strings"
	"testing"
)

func TestUnescapeANSI(t *testing.T) {
	cases := map[string]string{
		`\033[1;38;2;187;154;247m`: "\x1b[1;38;2;187;154;247m",
		`\e[0m`:                    "\x1b[0m",
		`\x1b[2m`:                  "\x1b[2m",
		`plain`:                    "plain",
	}
	for in, want := range cases {
		if got := unescapeANSI(in); got != want {
			t.Errorf("unescapeANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadThemeTokyoNight(t *testing.T) {
	th, err := loadTheme("tokyo-night", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(th.StyleJSON) == 0 {
		t.Error("expected style JSON")
	}
	// Tokyo Night: magenta USER, blue CLAUDE, dim comment — all truecolor escapes.
	if !strings.HasPrefix(th.UserANSI, "\x1b[") || !strings.Contains(th.UserANSI, "187;154;247") {
		t.Errorf("UserANSI = %q", th.UserANSI)
	}
	if !strings.Contains(th.ClaudeANSI, "122;162;247") {
		t.Errorf("ClaudeANSI = %q", th.ClaudeANSI)
	}
	if !strings.Contains(th.DimANSI, "86;95;137") {
		t.Errorf("DimANSI = %q", th.DimANSI)
	}
}

func TestLoadThemeUnknown(t *testing.T) {
	if _, err := loadTheme("does-not-exist", ""); err == nil {
		t.Error("expected error for unknown theme")
	}
}

func TestThemeExists(t *testing.T) {
	if !themeExists("tokyo-night") {
		t.Error("tokyo-night should exist")
	}
	if themeExists("nope") {
		t.Error("nope should not exist")
	}
}

func TestThemeDesc(t *testing.T) {
	desc := themeDesc("tokyo-night")
	if !strings.HasPrefix(desc, "Tokyo Night") {
		t.Errorf("desc = %q", desc)
	}
	if strings.HasPrefix(desc, "#") || strings.HasSuffix(desc, ".") {
		t.Errorf("desc should be stripped of leading # and trailing period: %q", desc)
	}
}

func TestListThemeInfos(t *testing.T) {
	infos := listThemeInfos()
	if len(infos) < 5 {
		t.Fatalf("expected several themes, got %d", len(infos))
	}
	// Sorted and includes tokyo-night.
	found := false
	for i, info := range infos {
		if i > 0 && infos[i-1].Name > info.Name {
			t.Error("themes not sorted")
		}
		if info.Name == "tokyo-night" {
			found = true
		}
	}
	if !found {
		t.Error("tokyo-night missing from list")
	}
}

func TestLoadThemeStyleOverride(t *testing.T) {
	// Override supplies style bytes but keeps the named theme's ANSI colors.
	dir := t.TempDir()
	custom := dir + "/custom.json"
	if err := os.WriteFile(custom, []byte(`{"document":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	th, err := loadTheme("tokyo-night", custom)
	if err != nil {
		t.Fatal(err)
	}
	if string(th.StyleJSON) != `{"document":{}}` {
		t.Errorf("style override not applied: %q", th.StyleJSON)
	}
	if !strings.Contains(th.UserANSI, "187;154;247") {
		t.Error("ANSI colors should still come from the named theme")
	}
}
