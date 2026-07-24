package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Config holds the resolved (env + flags) settings as raw strings; typed
// validation happens in main once the session is known.
type Config struct {
	Positional       []string // bare args: one session file to tail, else (joined) a search query
	Agent            string   // auto|claude|codex|agy
	Theme            string
	Backfill         string
	GlowStyle        string
	ToolStyle        string // none|dots|lines
	Collapse         string
	Pick             string // auto|always|never
	Days             string // window for the session tree (empty = per-mode default)
	List             bool   // --list: static ls-style dump instead of the TUI
	Local            bool   // --local: pure ~/.claude crawl, folder-grouped (no git/cloud)
	Cloud            bool   // --cloud: refresh entire's cloud metadata (slow) then enrich
	Search           string // --search: content-search sessions, ranked by relevance
	WaitNew          bool   // --wait-new: block until a new Claude session appears in $PWD, then tail it
	FollowSession    string // --follow-session <id>: tail exactly $PWD's <id>.jsonl (waiting for it), following forks
	MarkContinuation bool   // --mark-continuation: at a Claude lineage flip, write a forward-pointer record into the stopped file
}

// Action is what the parsed CLI asks for beyond a normal run.
type Action int

const (
	ActionRun Action = iota
	ActionHelp
	ActionVersion
	ActionListThemes
	ActionList     // static session-tree dump (--list)
	ActionHandover // `entire-tail handover`: generate session handover docs
)

// envTrue reports whether an env var holds a truthy value (1/true/yes/on),
// case-insensitively. Empty or anything else is false.
func envTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// firstNonEmpty returns the first non-empty value (matching bash ${A:-${B:-c}},
// which treats an empty env var as unset).
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// defaultConfig applies the env-var defaults. ENTIRE_TAIL_* is canonical;
// CLAUDE_TAIL_* is honored as a back-compat fallback where the bash version did.
func defaultConfig(getenv func(string) string) Config {
	c := Config{
		Agent:            firstNonEmpty(getenv("ENTIRE_TAIL_AGENT"), "auto"),
		Theme:            firstNonEmpty(getenv("ENTIRE_TAIL_THEME"), getenv("CLAUDE_TAIL_THEME"), "tokyo-night"),
		Backfill:         firstNonEmpty(getenv("ENTIRE_TAIL_BACKFILL"), getenv("CLAUDE_TAIL_BACKFILL"), "all"),
		GlowStyle:        getenv("GLOW_STYLE"),
		ToolStyle:        firstNonEmpty(getenv("ENTIRE_TAIL_TOOL_STYLE"), getenv("CLAUDE_TAIL_TOOL_STYLE"), "dots"),
		Collapse:         firstNonEmpty(getenv("ENTIRE_TAIL_COLLAPSE"), "5"),
		Pick:             firstNonEmpty(getenv("ENTIRE_TAIL_PICK"), "auto"),
		Days:             getenv("ENTIRE_TAIL_DAYS"),
		MarkContinuation: envTrue(getenv("ENTIRE_TAIL_MARK_CONTINUATION")),
	}
	c.Collapse = normalizeCollapseWord(c.Collapse)
	c.Pick = normalizePickWord(c.Pick)
	return c
}

// normalizeCollapseWord maps the off-synonyms to "0" (the bash COLLAPSE case).
func normalizeCollapseWord(s string) string {
	switch s {
	case "off", "none", "false", "no", "OFF", "NONE":
		return "0"
	}
	return s
}

// normalizePickWord maps pick synonyms to never/always (the bash PICK case).
func normalizePickWord(s string) string {
	switch s {
	case "off", "none", "false", "no", "OFF", "NONE", "never":
		return "never"
	case "on", "yes", "true", "always":
		return "always"
	}
	return s
}

// parseCLI applies env defaults then walks args, with flags overriding env.
func parseCLI(args []string, getenv func(string) string) (Config, Action, error) {
	c := defaultConfig(getenv)

	if len(args) > 0 && args[0] == "handover" {
		return c, ActionHandover, nil
	}

	needValue := func(i int, flag string) (string, error) {
		if i+1 >= len(args) || args[i+1] == "" {
			return "", fmt.Errorf("option %s requires a value (try --help)", flag)
		}
		return args[i+1], nil
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-a" || a == "--agent":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.Agent = v
			i++
		case strings.HasPrefix(a, "--agent="):
			c.Agent = strings.TrimPrefix(a, "--agent=")
		case a == "-t" || a == "--theme":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.Theme = v
			i++
		case strings.HasPrefix(a, "--theme="):
			c.Theme = strings.TrimPrefix(a, "--theme=")
		case a == "-b" || a == "--backfill":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.Backfill = v
			i++
		case strings.HasPrefix(a, "--backfill="):
			c.Backfill = strings.TrimPrefix(a, "--backfill=")
		case a == "--no-backfill":
			c.Backfill = "0"
		case a == "-s" || a == "--style":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.GlowStyle = v
			i++
		case strings.HasPrefix(a, "--style="):
			c.GlowStyle = strings.TrimPrefix(a, "--style=")
		case a == "--tool-style":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.ToolStyle = v
			i++
		case strings.HasPrefix(a, "--tool-style="):
			c.ToolStyle = strings.TrimPrefix(a, "--tool-style=")
		case a == "--no-compact-tools":
			c.ToolStyle = "lines"
		case a == "-c" || a == "--collapse":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.Collapse = v
			i++
		case strings.HasPrefix(a, "--collapse="):
			c.Collapse = strings.TrimPrefix(a, "--collapse=")
		case a == "--no-collapse":
			c.Collapse = "0"
		case a == "-p" || a == "--pick":
			c.Pick = "always"
		case a == "--no-pick":
			c.Pick = "never"
		case a == "--days":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.Days = v
			i++
		case strings.HasPrefix(a, "--days="):
			c.Days = strings.TrimPrefix(a, "--days=")
		case a == "-L" || a == "--list":
			c.List = true
		case a == "--local":
			c.Local = true
		case a == "--cloud":
			c.Cloud = true
		case a == "--wait-new":
			c.WaitNew = true
		case a == "--follow-session":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.FollowSession = v
			i++
		case strings.HasPrefix(a, "--follow-session="):
			c.FollowSession = strings.TrimPrefix(a, "--follow-session=")
		case a == "--mark-continuation":
			c.MarkContinuation = true
		case a == "--no-mark-continuation":
			c.MarkContinuation = false
		case a == "-S" || a == "--search":
			v, err := needValue(i, a)
			if err != nil {
				return c, ActionRun, err
			}
			c.Search = v
			i++
		case strings.HasPrefix(a, "--search="):
			c.Search = strings.TrimPrefix(a, "--search=")
		case a == "-w" || a == "--workspace":
			// Workspace is the default; -w just forces the picker even when the
			// env default is 'never'.
			c.Pick = "always"
		case a == "-l" || a == "--list-themes":
			return c, ActionListThemes, nil
		case a == "-h" || a == "--help":
			return c, ActionHelp, nil
		case a == "-V" || a == "--version":
			return c, ActionVersion, nil
		case a == "--":
			// Everything after -- is positional (a session file, or search words).
			c.Positional = append(c.Positional, args[i+1:]...)
			i = len(args)
		case len(a) > 0 && a[0] == '-':
			return c, ActionRun, fmt.Errorf("unknown option: %s (try --help)", a)
		default:
			c.Positional = append(c.Positional, a)
		}
	}

	// Re-apply the off-synonym normalization for collapse provided via flag.
	c.Collapse = normalizeCollapseWord(c.Collapse)
	c.Pick = normalizePickWord(c.Pick)
	if c.List {
		return c, ActionList, nil
	}
	return c, ActionRun, nil
}

// resolveDays parses the --days window into a day count. Empty falls back to def;
// "all"/"0" mean uncapped (0). The tree defaults to 7; --list defaults to all.
func resolveDays(s string, def int) (int, error) {
	switch s {
	case "":
		return def, nil
	case "all", "ALL":
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid --days value: %s (want integer >= 0, or 'all')", s)
	}
	return n, nil
}

// validateAgent checks the --agent value and maps the antigravity alias.
func validateAgent(s string) (string, error) {
	switch s {
	case "auto", "claude", "codex", "agy":
		return s, nil
	case "antigravity":
		return "agy", nil
	}
	return "", fmt.Errorf("invalid --agent value: %s (want 'auto', 'claude', 'codex', or 'agy')", s)
}

// validateToolStyle checks the --tool-style value. full/dots/hidden are the
// canonical names; lines (=full) and none (=hidden) are accepted as aliases.
func validateToolStyle(s string) error {
	switch s {
	case "full", "dots", "hidden", "lines", "none":
		return nil
	}
	return fmt.Errorf("invalid --tool-style value: %s (want 'full', 'dots', or 'hidden')", s)
}

// resolveCollapse parses the collapse setting into a non-negative integer.
func resolveCollapse(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid --collapse value: %s (want integer >= 0, or 'off')", s)
	}
	return n, nil
}

// resolveBackfill turns the backfill setting + total line count into a 1-based
// start line (0 = no backfill).
func resolveBackfill(s string, total int) (int, error) {
	switch s {
	case "all", "ALL", "full":
		return 1, nil
	case "0":
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid --backfill value: %s (want integer, 'all', or '0')", s)
	}
	return max(total-n+1, 1), nil
}
