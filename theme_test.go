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
			r, err := newRenderer(&b, th, "dots", 0, 0)
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

func TestNextThemeCyclesInOrder(t *testing.T) {
	infos := listThemeInfos()
	if len(infos) < 2 {
		t.Fatalf("need at least 2 bundled themes to test cycling, got %d", len(infos))
	}
	for i, in := range infos {
		got, err := nextTheme(in.Name)
		if err != nil {
			t.Fatalf("nextTheme(%q): %v", in.Name, err)
		}
		want := infos[(i+1)%len(infos)].Name
		if got.Name != want {
			t.Errorf("nextTheme(%q) = %q, want %q", in.Name, got.Name, want)
		}
	}
	// Wrap-around: the last theme cycles back to the first.
	last, err := nextTheme(infos[len(infos)-1].Name)
	if err != nil {
		t.Fatal(err)
	}
	if last.Name != infos[0].Name {
		t.Errorf("nextTheme(last) = %q, want first %q", last.Name, infos[0].Name)
	}
	// An unknown current name starts the cycle at the first theme.
	unknown, err := nextTheme("no-such-theme")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Name != infos[0].Name {
		t.Errorf("nextTheme(unknown) = %q, want first %q", unknown.Name, infos[0].Name)
	}
}

// The settings panel shows a theme as a strip of colour blocks beside its name,
// most visible colour first: the two box headers and the dim, then body text,
// heading, inline code, strong and link from the glamour style.
func TestThemeSwatchOrder(t *testing.T) {
	th, err := loadTheme("dracula", "")
	if err != nil {
		t.Fatal(err)
	}
	got := themeSwatch(th)
	if w := visWidth(got); w != 16 {
		t.Errorf("visWidth = %d, want 16 (8 colours × 2 cells):\n%q", w, got)
	}
	if !strings.HasSuffix(got, reset) {
		t.Errorf("swatch must end reset, got %q", got)
	}
	want := []string{
		"255;121;198",      // USER pink
		"189;147;249",      // CLAUDE purple
		"98;114;164",       // dim comment
		"38;2;248;248;242", // document
		"38;2;139;233;253", // heading
		"38;2;80;250;123",  // code
		"38;2;255;184;108", // strong
		"38;2;139;233;253", // link
	}
	pos := 0
	for _, w := range want {
		i := strings.Index(got[pos:], w)
		if i < 0 {
			t.Fatalf("colour %q missing or out of order after byte %d:\n%q", w, pos, got)
		}
		pos += i + len(w)
	}
}

// The original theme's style uses 256-colour indexes rather than hex; those
// have to render as blocks too, not vanish.
func TestThemeSwatch256Colour(t *testing.T) {
	th, err := loadTheme("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	got := themeSwatch(th)
	if !strings.Contains(got, "\x1b[38;5;252m██") {
		t.Errorf("document colour 252 missing: %q", got)
	}
	if w := visWidth(got); w != 16 {
		t.Errorf("visWidth = %d, want 16", w)
	}
}

// Every bundled theme sets all eight colours; a short strip would mean a theme
// file lost one.
func TestThemeSwatchAllThemes(t *testing.T) {
	for _, in := range listThemeInfos() {
		th, err := loadTheme(in.Name, "")
		if err != nil {
			t.Fatal(err)
		}
		if w := visWidth(themeSwatch(th)); w != 16 {
			t.Errorf("%s: swatch width %d, want 16", in.Name, w)
		}
	}
}

// A colour the theme doesn't set is skipped, never drawn in the default colour:
// the strip shows only what the theme owns. Nothing at all gives an empty string.
func TestThemeSwatchSparse(t *testing.T) {
	got := themeSwatch(Theme{UserANSI: "\x1b[36m"})
	if got != "\x1b[36m██"+reset {
		t.Errorf("header-only swatch = %q", got)
	}
	if got := themeSwatch(Theme{}); got != "" {
		t.Errorf("empty theme swatch = %q, want empty", got)
	}
	if got := themeSwatch(Theme{StyleJSON: []byte("not json")}); got != "" {
		t.Errorf("bad style swatch = %q, want empty", got)
	}
}

func TestStyleANSI(t *testing.T) {
	cases := map[string]string{
		"#ff79c6": "\x1b[38;2;255;121;198m",
		"#000000": "\x1b[38;2;0;0;0m",
		"252":     "\x1b[38;5;252m",
		"0":       "\x1b[38;5;0m",
		"":        "",
		"#zzzzzz": "",
		"#fff":    "",
		"256":     "",
		"red":     "",
	}
	for in, want := range cases {
		if got := styleANSI(in); got != want {
			t.Errorf("styleANSI(%q) = %q, want %q", in, got, want)
		}
	}
}
