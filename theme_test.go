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

// TestAllThemesRenderValidly loads every bundled theme and renders a document
// exercising headings, bold/emph, a blockquote, a list, and a code block — so a
// malformed theme JSON or a bad/missing hex color (which makes chroma panic on
// the first code block) fails the build.
func TestAllThemesRenderValidly(t *testing.T) {
	sample := "# H1\n## H2\n### H3\n#### H4\n##### H5\n###### H6\n\n" +
		"**bold** and *emph* text\n\n> a quoted line\n\n- one\n- two\n\n```go\nfunc main() {}\n```\n"
	infos := listThemeInfos()
	if len(infos) == 0 {
		t.Fatal("no bundled themes found")
	}
	for _, info := range infos {
		t.Run(info.Name, func(t *testing.T) {
			th, err := loadTheme(info.Name, "")
			if err != nil {
				t.Fatalf("loadTheme(%s): %v", info.Name, err)
			}
			if th.UserANSI == "" || th.ClaudeANSI == "" || th.DimANSI == "" {
				t.Errorf("theme %s is missing header ANSI colors (USER=%q CLAUDE=%q DIM=%q)",
					info.Name, th.UserANSI, th.ClaudeANSI, th.DimANSI)
			}
			var b strings.Builder
			r, err := newRenderer(&b, th, "dots", 0)
			if err != nil {
				t.Fatalf("newRenderer(%s): %v", info.Name, err)
			}
			r.emit(Record{Kind: KindAssistant, Ts: "2026-01-02 15:04:05", Body: sample})
			if b.Len() == 0 {
				t.Errorf("theme %s rendered empty output", info.Name)
			}
		})
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
