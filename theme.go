package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
)

// themesFS bundles the theme stylesheets and palette sidecars into the binary,
// so the tool is self-contained (no install-path lookup like the bash version).
// The themes/ dir stays the source of truth; glamour reads the .json directly
// and the .sh sidecars carry the ANSI box-header colors.
//
//go:embed themes
var themesFS embed.FS

// Theme is a resolved theme: the glamour style JSON plus the three ANSI escape
// strings used for USER/AGENT box headers and dim timestamps.
type Theme struct {
	Name       string
	StyleJSON  []byte // glamour ansi.StyleConfig
	UserANSI   string // box header color for USER turns
	ClaudeANSI string // box header color for AGENT turns
	DimANSI    string // dim color for timestamps / markers
}

var themeANSIRe = regexp.MustCompile(`(?m)^THEME_(USER|CLAUDE|DIM)_ANSI=\$'([^']*)'`)

// loadTheme resolves a bundled theme by name. styleOverride, when non-empty,
// supplies the glamour style JSON from an arbitrary path on disk (the -s/--style
// flag) while still using the named theme's ANSI header colors.
func loadTheme(name, styleOverride string) (Theme, error) {
	jsonBytes, err := themesFS.ReadFile("themes/" + name + ".json")
	if err != nil {
		return Theme{}, fmt.Errorf("unknown theme %q", name)
	}
	if styleOverride != "" {
		jsonBytes, err = os.ReadFile(styleOverride)
		if err != nil {
			return Theme{}, fmt.Errorf("cannot read style %q: %w", styleOverride, err)
		}
	}

	t := Theme{Name: name, StyleJSON: jsonBytes}
	if sh, err := themesFS.ReadFile("themes/" + name + ".sh"); err == nil {
		for _, m := range themeANSIRe.FindAllStringSubmatch(string(sh), -1) {
			ansi := unescapeANSI(m[2])
			switch m[1] {
			case "USER":
				t.UserANSI = ansi
			case "CLAUDE":
				t.ClaudeANSI = ansi
			case "DIM":
				t.DimANSI = ansi
			}
		}
	}
	return t, nil
}

// themeExists reports whether a bundled theme JSON is present.
func themeExists(name string) bool {
	_, err := themesFS.ReadFile("themes/" + name + ".json")
	return err == nil
}

// nextTheme resolves the bundled theme after cur in the sorted theme list,
// wrapping around at the end (the `T`-key cycle). A style override is
// deliberately NOT threaded through: cycling switches among the bundled themes'
// full looks, so a --style body override doesn't pin every theme to one style.
// An unknown current name starts the cycle at the first theme.
func nextTheme(cur string) (Theme, error) {
	infos := listThemeInfos()
	if len(infos) == 0 {
		return Theme{}, fmt.Errorf("no bundled themes")
	}
	idx := -1
	for i, in := range infos {
		if in.Name == cur {
			idx = i
			break
		}
	}
	return loadTheme(infos[(idx+1)%len(infos)].Name, "")
}

// unescapeANSI turns the backslash escapes used in the bash $'...' palette
// literals into real bytes — only the forms the theme files actually use.
func unescapeANSI(s string) string {
	r := strings.NewReplacer(
		`\033`, "\x1b",
		`\e`, "\x1b",
		`\x1b`, "\x1b",
		`\\`, `\`,
	)
	return r.Replace(s)
}

type themeInfo struct {
	Name string
	Desc string
}

// listThemeInfos returns the bundled themes (sorted) with their one-line
// descriptions taken from the first comment of each .sh sidecar.
func listThemeInfos() []themeInfo {
	entries, err := fs.ReadDir(themesFS, "themes")
	if err != nil {
		return nil
	}
	var out []themeInfo
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		out = append(out, themeInfo{Name: name, Desc: themeDesc(name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var (
	descLeadRe  = regexp.MustCompile(`^#[[:space:]]*`)
	descTrailRe = regexp.MustCompile(`\.[[:space:]]*$`)
)

// themeDesc pulls the first comment line of themes/<name>.sh as a short
// description, stripping a leading "# " and a trailing period (matching the
// bash theme_desc sed).
func themeDesc(name string) string {
	sh, err := themesFS.ReadFile("themes/" + name + ".sh")
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(sh), "\n")
	first = descLeadRe.ReplaceAllString(first, "")
	first = descTrailRe.ReplaceAllString(first, "")
	return first
}
