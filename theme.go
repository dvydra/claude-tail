package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
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
func nextTheme(cur string) (Theme, error) { return stepTheme(cur, +1) }

// stepTheme is nextTheme with a direction, so the settings panel's ← walks back
// through the list instead of going all the way round.
func stepTheme(cur string, dir int) (Theme, error) {
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
	// idx is -1 when the current theme isn't bundled (a `-s` style override):
	// forward lands on the first, back on the last, which is what nextTheme has
	// always done at the wrap-around.
	n := len(infos)
	if dir < 0 {
		return loadTheme(infos[((idx-1)%n+n)%n].Name, "")
	}
	return loadTheme(infos[(idx+1)%n].Name, "")
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

// swatchKeys are the glamour style elements whose colours join the swatch after
// the three header colours, most visible on screen first: body text, headings,
// inline code, bold, links. `emph` is left out because in every bundled theme
// it repeats the heading, link or code colour.
var swatchKeys = []string{"document", "heading", "code", "strong", "link"}

// themeSwatch is a strip of colour blocks summarising a theme for the settings
// panel: the USER, AGENT and dim header colours first (the box chrome is what
// you see most of), then swatchKeys from the glamour style. Two cells per
// colour — one is too thin to read. A colour the theme doesn't set is skipped
// rather than drawn in the terminal default, so the strip never shows a colour
// the theme doesn't own.
func themeSwatch(t Theme) string {
	var b strings.Builder
	block := func(ansi string) {
		if ansi != "" {
			b.WriteString(ansi + "██" + reset)
		}
	}
	block(t.UserANSI)
	block(t.ClaudeANSI)
	block(t.DimANSI)
	colors := styleColors(t.StyleJSON)
	for _, k := range swatchKeys {
		block(styleANSI(colors[k]))
	}
	return b.String()
}

// styleColors pulls each top-level element's foreground colour out of a glamour
// style JSON. Best-effort: a style that doesn't parse yields nothing, and the
// swatch falls back to the header colours alone.
func styleColors(styleJSON []byte) map[string]string {
	var raw map[string]struct {
		Color string `json:"color"`
	}
	if json.Unmarshal(styleJSON, &raw) != nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = v.Color
	}
	return out
}

// styleANSI turns a glamour colour — "#rrggbb" or a 0–255 index, the two forms
// the bundled themes use — into the SGR that selects it as a foreground.
// Anything else is "" (no block).
func styleANSI(c string) string {
	if hex, ok := strings.CutPrefix(c, "#"); ok {
		if len(hex) != 6 {
			return ""
		}
		n, err := strconv.ParseUint(hex, 16, 24)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", n>>16, (n>>8)&0xff, n&0xff)
	}
	if n, err := strconv.Atoi(c); err == nil && n >= 0 && n <= 255 {
		return fmt.Sprintf("\x1b[38;5;%dm", n)
	}
	return ""
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
