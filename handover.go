package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// handover.go implements `entire-tail handover`: enumerate today's Claude
// sessions, let the user group them (handover_picker.go), write a JSON manifest,
// and launch an interactive claude that enriches each group and writes one
// Obsidian handover doc per group (the handover-sessions skill). The pure parts
// (today-filter, manifest builder) are unit-tested; runHandover is the driver.

// handoverItem is one of today's sessions, distilled from the session tree.
type handoverItem struct {
	SessionID    string
	Repo         string // owner/repo, else ~path
	Cwd          string
	Branch       string
	Title        string // cleaned snippet
	Live         bool
	LastActivity int64
	Tokens       int64
	Path         string // local transcript
}

// localMidnight returns the Unix time of the most recent local midnight at or
// before now.
func localMidnight(now int64, loc *time.Location) int64 {
	t := time.Unix(now, 0).In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).Unix()
}

// flattenToday collapses a session tree to a flat list of local Claude sessions
// with activity at/after midnight and an on-disk transcript (needed to enrich),
// sorted live-first then most-recent-first so the priority sessions lead.
func flattenToday(t sessionTree, midnight int64, home string) []handoverItem {
	cache := map[string]string{}
	var out []handoverItem
	for _, f := range t.Folders {
		for _, s := range f.Sessions {
			if s.Path == "" || s.Mtime < midnight {
				continue
			}
			cwd := firstNonEmpty(s.cwd, f.Dir, f.Cwd)
			out = append(out, handoverItem{
				SessionID:    s.ID,
				Repo:         repoForCwd(cwd, home, cache),
				Cwd:          cwd,
				Branch:       s.Branch,
				Title:        cleanTitle(s.Snippet),
				Live:         s.Live,
				LastActivity: s.Mtime,
				Tokens:       s.Tokens,
				Path:         s.Path,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Live != out[j].Live {
			return out[i].Live // live sessions first
		}
		return out[i].LastActivity > out[j].LastActivity
	})
	return out
}

var (
	reCmdName  = regexp.MustCompile(`<command-name>\s*/?([^<]+?)\s*</command-name>`)
	reAngleTag = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
)

const caveatBoilerplate = "Caveat: The messages below were generated"

// cleanTitle turns a raw session snippet into a human title for the picker:
// a slash-command session ("<command-name>progress</command-name> …") becomes
// "/progress"; command-output / resumed wrappers (local-command-caveat) have no
// real prompt, so they return "" (the picker shows a placeholder, `i` to preview);
// anything else is stripped of angle-bracket wrapper tags and whitespace-collapsed.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if m := reCmdName.FindStringSubmatch(s); m != nil {
		return "/" + strings.TrimPrefix(strings.TrimSpace(m[1]), "/")
	}
	if strings.Contains(s, "local-command-caveat") || strings.Contains(s, caveatBoilerplate) {
		return ""
	}
	s = strings.Join(strings.Fields(reAngleTag.ReplaceAllString(s, " ")), " ")
	if len(s) < 4 {
		return ""
	}
	return s
}

// defaultTags selects live sessions by default (their own doc) and excludes ended
// ones — live work is the priority; opt ended sessions back in with x/1-9.
func defaultTags(items []handoverItem) []handoverTag {
	tags := make([]handoverTag, len(items))
	for i, it := range items {
		if !it.Live {
			tags[i] = tagExcluded
		}
	}
	return tags
}

// ── manifest ──────────────────────────────────────────────────────────────────

type manifestSession struct {
	SessionID        string   `json:"sessionId"`
	Agent            string   `json:"agent"`
	Cwd              string   `json:"cwd"`
	Repo             string   `json:"repo"`
	Title            string   `json:"title"`
	State            string   `json:"state"` // live|ended
	LastActivity     string   `json:"lastActivity"`
	Tokens           int64    `json:"tokens"`
	TranscriptPath   string   `json:"transcriptPath"`
	TrailUrls        []string `json:"trailUrls"`
	PrUrls           []string `json:"prUrls"`
	EntireSessionIds []string `json:"entireSessionIds"`
}

type manifestGroup struct {
	GroupID  string            `json:"groupId"`
	Sessions []manifestSession `json:"sessions"`
}

type handoverManifest struct {
	Date             string          `json:"date"`
	GeneratedAt      string          `json:"generatedAt"`
	VaultHandoverDir string          `json:"vaultHandoverDir"`
	Groups           []manifestGroup `json:"groups"`
}

const defaultHandoverVault = "/Users/dvydra/Library/Mobile Documents/iCloud~md~obsidian/Documents"

// handoverVaultDir resolves the dated output directory: <root>/Handover/YYYY-MM-DD,
// where root is $ENTIRE_TAIL_HANDOVER_VAULT or the iCloud Obsidian default.
func handoverVaultDir(getenv func(string) string, now int64, loc *time.Location) string {
	root := firstNonEmpty(getenv("ENTIRE_TAIL_HANDOVER_VAULT"), defaultHandoverVault)
	date := time.Unix(now, 0).In(loc).Format("2006-01-02")
	return filepath.Join(root, "Handover", date)
}

// Doc/example URLs use conventional placeholder atoms that the link regex can't
// tell from real ones, so a transcript quoting docs (e.g.
// https://entire.io/gh/org/repo/trails/N/) would otherwise leak into the manifest.
var (
	placeholderOwners = map[string]bool{"org": true, "owner": true, "o": true, "example": true, "acme": true, "myorg": true, "your-org": true}
	placeholderRepos  = map[string]bool{"repo": true, "r": true, "example": true, "name": true, "myrepo": true, "your-repo": true}
	placeholderIDs    = map[string]bool{"n": true, "id": true, "number": true, "x": true, "abc": true, "xxx": true}
)

// placeholderLink reports whether a scanned link is a documentation placeholder
// (org/repo/N and friends) rather than a real trail/PR reference.
func placeholderLink(ln sessionLink) bool {
	return placeholderOwners[strings.ToLower(ln.Owner)] ||
		placeholderRepos[strings.ToLower(ln.Repo)] ||
		placeholderIDs[strings.ToLower(ln.ID)]
}

// manifestSessionFrom builds a manifest session from an item + its extracted
// links (trails and PRs split by kind). Pure — links are injected.
func manifestSessionFrom(it handoverItem, links []sessionLink) manifestSession {
	state := "ended"
	if it.Live {
		state = "live"
	}
	trails, prs := []string{}, []string{}
	for _, ln := range links {
		if placeholderLink(ln) {
			continue // an example/doc URL like .../org/repo/trails/N/, not a real ref
		}
		if ln.Kind == "trail" {
			trails = append(trails, ln.URL)
		} else {
			prs = append(prs, ln.URL)
		}
	}
	return manifestSession{
		SessionID:        it.SessionID,
		Agent:            "claude",
		Cwd:              it.Cwd,
		Repo:             it.Repo,
		Title:            it.Title,
		State:            state,
		LastActivity:     time.Unix(it.LastActivity, 0).Format(time.RFC3339),
		Tokens:           it.Tokens,
		TranscriptPath:   it.Path,
		TrailUrls:        trails,
		PrUrls:           prs,
		EntireSessionIds: []string{},
	}
}

// buildManifest assembles the manifest; linksOf is injected so it's pure/testable
// (production passes extractLinks).
func buildManifest(groups []handoverGroup, vaultDir, date string, now int64, linksOf func(string) []sessionLink) handoverManifest {
	m := handoverManifest{
		Date:             date,
		GeneratedAt:      time.Unix(now, 0).Format(time.RFC3339),
		VaultHandoverDir: vaultDir,
	}
	for _, g := range groups {
		mg := manifestGroup{GroupID: g.GroupID}
		for _, it := range g.Sessions {
			mg.Sessions = append(mg.Sessions, manifestSessionFrom(it, linksOf(it.Path)))
		}
		m.Groups = append(m.Groups, mg)
	}
	return m
}

func writeManifestTemp(m handoverManifest) (string, error) {
	f, err := os.CreateTemp("", "entire-tail-handover-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// ── orchestration (driver) ────────────────────────────────────────────────────

// todaysSessions enumerates this machine's Claude sessions with activity since
// local midnight (a 2-day crawl window is cheap and safely spans midnight).
func todaysSessions(home string, now int64, loc *time.Location) []handoverItem {
	tree := buildClaudeTree(home, "", 2, now, claudeLiveCwds())
	return flattenToday(tree, localMidnight(now, loc), home)
}

func runHandover(cfg Config) {
	home := firstNonEmpty(os.Getenv("HOME"), mustHome())
	now := time.Now().Unix()
	loc := time.Local

	items := todaysSessions(home, now, loc)
	if len(items) == 0 {
		fmt.Fprintln(os.Stderr, "entire-tail: no Claude sessions with activity today.")
		return
	}

	var groups []handoverGroup
	if ttyUsable() {
		theme := mustLoadTheme(cfg)
		g, ok := runHandoverPicker(items, home, theme, now)
		if !ok {
			fmt.Fprintln(os.Stderr, "entire-tail: handover aborted.")
			return
		}
		groups = g
	} else {
		printHandoverList(items)
		if !confirmYN(fmt.Sprintf("Write handover docs for these %d sessions (live only)?", len(items))) {
			return
		}
		groups = buildGroups(items, defaultTags(items))
	}
	if len(groups) == 0 {
		fmt.Fprintln(os.Stderr, "entire-tail: nothing selected.")
		return
	}

	date := time.Unix(now, 0).In(loc).Format("2006-01-02")
	vaultDir := handoverVaultDir(os.Getenv, now, loc)
	manifest := buildManifest(groups, vaultDir, date, now, extractLinks)
	path, err := writeManifestTemp(manifest)
	if err != nil {
		die("cannot write manifest: " + err.Error())
	}

	total := 0
	for _, g := range groups {
		total += len(g.Sessions)
	}
	fmt.Fprintf(os.Stderr, "entire-tail: %d group(s), %d session(s) → %s\n", len(groups), total, vaultDir)

	// Hand off to claude in-place (works in any terminal — zellij/tmux/iTerm).
	// Exec only returns on failure, in which case fall back to printing the command.
	if err := launchClaude(path); err != nil {
		fmt.Fprintln(os.Stderr, "entire-tail: "+err.Error())
		printHandoverCmd(path)
	}
}

func handoverPrompt(manifestPath string) string {
	return "Use the handover-sessions skill to write today's handover docs. Manifest JSON: " + manifestPath
}

// launchClaude replaces this process with an interactive claude seeded with the
// handover prompt, so the docs are written in the terminal the user ran the
// picker in. syscall.Exec returns only on error.
func launchClaude(manifestPath string) error {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found on PATH")
	}
	fmt.Fprintln(os.Stderr, "entire-tail: launching claude to write the handover docs…")
	return syscall.Exec(bin, []string{"claude", handoverPrompt(manifestPath)}, os.Environ())
}

func printHandoverCmd(manifestPath string) {
	fmt.Fprintln(os.Stderr, "entire-tail: run this to generate the docs:")
	fmt.Fprintf(os.Stderr, "  claude %q\n", handoverPrompt(manifestPath))
}

func printHandoverList(items []handoverItem) {
	fmt.Fprintf(os.Stderr, "Found %d Claude sessions from today:\n", len(items))
	for _, it := range items {
		state := "ended"
		if it.Live {
			state = "live"
		}
		fmt.Fprintf(os.Stderr, "  %-8s %-24s %-6s %s\n", shortID(it.SessionID), it.Repo, state, it.Title)
	}
}

func confirmYN(prompt string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(sc.Text()))
	return a == "y" || a == "yes"
}
