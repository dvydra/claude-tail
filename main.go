package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const version = "0.14.0"

func main() {
	cfg, action, err := parseCLI(os.Args[1:], os.Getenv)
	if err != nil {
		die(err.Error())
	}

	switch action {
	case ActionHelp:
		fmt.Print(helpText())
		return
	case ActionVersion:
		fmt.Println("entire-tail " + version)
		return
	case ActionListThemes:
		fmt.Print(listThemesText(cfg.Theme))
		return
	case ActionList:
		runList(cfg)
		return
	}

	run(cfg)
}

func run(cfg Config) {
	home := firstNonEmpty(os.Getenv("HOME"), mustHome())
	pwd := firstNonEmpty(os.Getenv("PWD"), mustGetwd())

	agentStr, err := validateAgent(cfg.Agent)
	if err != nil {
		die(err.Error())
	}

	// Theme is loaded up front because the interactive picker (session tree) needs
	// it to color rows, and the picker runs before the session is resolved.
	theme := mustLoadTheme(cfg)

	scanner := newCodexScanner(home)
	session := cfg.Session
	resolved := false

	// --search: rank sessions by relevance to a content query, then let the user
	// pick one to tail/resume (or dump the ranking when non-interactive).
	if cfg.Search != "" {
		tree := buildSearchTree(home, pwd, cfg.Search, cfg.Local, time.Now().Unix())
		if len(tree.Folders) == 0 {
			die(fmt.Sprintf("no sessions matched %q", cfg.Search))
		}
		if !ttyUsable() {
			out := bufio.NewWriter(os.Stdout)
			renderList(out, tree, isCharDevice(os.Stdout))
			out.Flush()
			return
		}
		p, ok := resolveTreeChoice(runTreeTUI(tree, theme))
		if !ok {
			return
		}
		session = p // fall through to tail it via the explicit-session path below
	}

	switch {
	case session != "":
		if !isFile(session) {
			die("session file not found: " + session)
		}
		if agentStr == "auto" {
			agentStr = string(detectAgentForFile(home, session))
		}
		resolved = true
	case cfg.Pick != "never":
		var agents []Agent
		if agentStr == "auto" {
			agents = []Agent{AgentClaude, AgentCodex, AgentAgy}
		} else {
			agents = []Agent{Agent(agentStr)}
		}
		days, derr := resolveDays(cfg.Days, 7)
		if derr != nil {
			die(derr.Error())
		}
		if path, ag, ok := runPicker(agents, home, pwd, days, cfg.Local, cfg.Cloud, theme); ok {
			session, agentStr, resolved = path, string(ag), true
		}
	}

	if !resolved {
		session, agentStr = discoverSession(agentStr, home, pwd, scanner)
	}

	if session == "" || !isFile(session) {
		die("no session jsonl found for agent=" + agentStr + " (tried $PWD and agent default dirs).")
	}
	agent := Agent(agentStr)

	if note := cwdMismatchNote(agent, session, home, pwd, scanner); note != "" {
		fmt.Fprintln(os.Stderr, "entire-tail: "+note)
	}

	// ── snapshot + ranges ──
	data, err := os.ReadFile(session)
	if err != nil {
		die("cannot read session: " + err.Error())
	}
	lines := splitLines(data)
	total := bytes.Count(data, []byte("\n"))

	backfillFrom, err := resolveBackfill(cfg.Backfill, total)
	if err != nil {
		die(err.Error())
	}
	if err := validateToolStyle(cfg.ToolStyle); err != nil {
		die(err.Error())
	}
	collapse, err := resolveCollapse(cfg.Collapse)
	if err != nil {
		die(err.Error())
	}

	loc := time.Local
	printBanner(cfg, agent, session, backfillFrom, total, collapse)

	out := bufio.NewWriter(os.Stdout)
	r, err := newRenderer(out, theme, cfg.ToolStyle, collapse)
	if err != nil {
		die("cannot init renderer: " + err.Error())
	}

	// ── phase 1: backfill ──
	if backfillFrom > 0 {
		for i := backfillFrom - 1; i < total && i < len(lines); i++ {
			for _, rec := range normalize(agent, lines[i], loc) {
				r.emit(rec)
			}
		}
	}
	out.Flush()

	// ── phase 2: live follow until Ctrl-C / Ctrl-D ──
	//
	// One select loop on the render goroutine owns ALL stdout writes — polling
	// for new lines, reloading, and the final flush. The signal/keyboard
	// goroutines only report on channels (a quit code, or a reload request);
	// they never touch the writer, so there's no data race and no torn output.
	r.live = true
	codeCh := make(chan int, 3)        // signal + keyboard quit; never block a sender
	reloadCh := make(chan struct{}, 1) // keyboard 'r'; coalesced (buffered 1)
	installSignals(codeCh)             // SIGINT → 130, SIGTERM → 0
	restoreTTY := startKeyboard(r, codeCh, reloadCh)
	defer restoreTTY() // panic safety; the normal path restores explicitly below

	emit := func(line []byte) {
		for _, rec := range normalize(agent, line, loc) {
			r.emit(rec)
		}
	}
	offset := liveOffset(data)
	agyKeep := newAgyDedup(maxStepIndex(lines))
	var agyLastSize, agyLastMtime int64 = -1, -1
	poll := func() {
		if agent == AgentAgy {
			// agy rewrites the whole file each step; only re-read (and re-scan
			// for new step_index) when it actually changes.
			fi, err := os.Stat(session)
			if err != nil {
				return
			}
			sz, mt := fi.Size(), fi.ModTime().UnixNano()
			if sz == agyLastSize && mt == agyLastMtime {
				return
			}
			agyLastSize, agyLastMtime = sz, mt
			if d, err := os.ReadFile(session); err == nil {
				for _, l := range splitLines(d) {
					if agyKeep(l) {
						emit(l)
					}
				}
			}
		} else {
			offset = appendStep(session, offset, emit)
		}
	}
	// reload re-renders the entire current transcript with the live settings
	// (the `r` key) — the streaming-native way to apply t/c retrospectively.
	reload := func() {
		d, err := os.ReadFile(session)
		if err != nil {
			return
		}
		all := splitLines(d)
		r.reset()
		io.WriteString(out, "\n"+r.theme.DimANSI+"⟳ reloaded"+reset+"\n\n")
		for _, l := range all {
			emit(l)
		}
		offset = liveOffset(d)
		agyKeep = newAgyDedup(maxStepIndex(all))
		agyLastSize, agyLastMtime = -1, -1 // force a re-stat next tick
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case code := <-codeCh:
			// Quit: restore the terminal and do the final flush here, on the
			// sole writer goroutine (130 for Ctrl-C/SIGINT, 0 for q/Ctrl-D/SIGTERM).
			restoreTTY()
			out.Flush()
			fmt.Fprintln(os.Stderr)
			os.Exit(code)
		case <-reloadCh:
			reload()
			out.Flush()
		case <-ticker.C:
			poll()
			out.Flush()
		}
	}
}

// discoverSession resolves a session path + concrete agent via per-agent
// discovery. For "auto" it picks the newest session across all agents.
func discoverSession(agentStr, home, pwd string, scanner *codexScanner) (string, string) {
	switch agentStr {
	case "claude":
		return findSessionClaude(home, pwd), "claude"
	case "codex":
		return scanner.findForCwd(pwd), "codex"
	case "agy":
		return findSessionAgy(home, pwd), "agy"
	default: // auto
		type cand struct {
			path  string
			agent string
		}
		var cands []cand
		if c := findSessionClaude(home, pwd); c != "" {
			cands = append(cands, cand{c, "claude"})
		}
		if x := scanner.findForCwd(pwd); x != "" {
			cands = append(cands, cand{x, "codex"})
		}
		if g := findSessionAgy(home, pwd); g != "" {
			cands = append(cands, cand{g, "agy"})
		}
		best, bestAgent, bestMtime := "", "", int64(-1)
		for _, c := range cands {
			if m := fileMtimeNano(c.path); best == "" || m > bestMtime {
				best, bestAgent, bestMtime = c.path, c.agent, m
			}
		}
		return best, bestAgent
	}
}

func fileMtimeNano(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.ModTime().UnixNano()
}

// installSignals reports an exit code on codeCh for Ctrl-C (SIGINT → 130) and
// SIGTERM (→ 0). It never touches stdout, so the render goroutine stays the sole
// writer of the buffered output.
func installSignals(codeCh chan<- int) {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		if <-sigc == syscall.SIGINT {
			codeCh <- 130
		} else {
			codeCh <- 0
		}
	}()
}

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func printBanner(cfg Config, agent Agent, session string, from, total, collapse int) {
	w := os.Stderr
	fmt.Fprintln(w, "entire-tail "+version)
	fmt.Fprintln(w, "  agent:    "+string(agent))
	fmt.Fprintln(w, "  session:  "+session)
	fmt.Fprintln(w, "  theme:    "+cfg.Theme)
	fmt.Fprintf(w, "  backfill: %s (%d..%d of %d)\n", cfg.Backfill, from, total, total)
	toolStyle := parseToolStyle(cfg.ToolStyle)
	fmt.Fprintln(w, "  tools:    "+toolStyle.label())
	if collapse > 0 {
		fmt.Fprintf(w, "  collapse: user pastes > %d lines\n", collapse)
	} else {
		fmt.Fprintln(w, "  collapse: off")
	}
	if isCharDevice(os.Stdin) {
		fmt.Fprintln(w, "  keys:     t=cycle tools (full/dots/hidden)  c=toggle collapse  r=reload  q/Ctrl-D=quit")
	}
	if toolStyle == toolDots {
		fmt.Fprint(w, bannerLegend())
	}
	fmt.Fprintln(w, "---")
}

// bannerLegend mirrors the dot color key printed at startup; the colors match
// dotColor().
func bannerLegend() string {
	dot := func(sgr, label string) string {
		return sgr + "●" + reset + " " + label + "  "
	}
	return "  legend:   " +
		dot("\x1b[38;2;125;185;235m", "read") +
		dot("\x1b[38;2;125;215;145m", "edit") +
		dot("\x1b[38;2;240;200;110m", "bash/exec") +
		dot("\x1b[38;2;220;140;230m", "grep") +
		dot("\x1b[38;2;110;220;220m", "web") +
		dot("\x1b[38;2;205;155;255m", "task") +
		"\x1b[38;2;255;180;130m●" + reset + " mcp\n"
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "entire-tail: "+msg)
	os.Exit(2)
}

func mustHome() string {
	h, _ := os.UserHomeDir()
	return h
}

func mustGetwd() string {
	d, _ := os.Getwd()
	return d
}

// mustLoadTheme resolves the configured theme, printing the theme list and
// exiting on an unknown name (matching the prior inline handling).
func mustLoadTheme(cfg Config) Theme {
	if !themeExists(cfg.Theme) {
		fmt.Fprintf(os.Stderr, "entire-tail: unknown theme %q.\n\n", cfg.Theme)
		fmt.Fprint(os.Stderr, listThemesText(cfg.Theme))
		os.Exit(2)
	}
	theme, err := loadTheme(cfg.Theme, cfg.GlowStyle)
	if err != nil {
		die(err.Error())
	}
	return theme
}

// runList prints the static ls-style dump of Claude sessions (--list). It's
// uncapped by default (the full inventory); --days narrows the window. Color is
// used only when stdout is a terminal.
func runList(cfg Config) {
	home := firstNonEmpty(os.Getenv("HOME"), mustHome())
	pwd := firstNonEmpty(os.Getenv("PWD"), mustGetwd())
	now := time.Now().Unix()
	var tree sessionTree
	if cfg.Search != "" {
		tree = buildSearchTree(home, pwd, cfg.Search, cfg.Local, now)
	} else {
		days, err := resolveDays(cfg.Days, 0)
		if err != nil {
			die(err.Error())
		}
		tree = buildSessionTree(home, pwd, days, now, cfg.Local, cfg.Cloud)
	}
	if len(tree.Folders) == 0 {
		fmt.Fprintln(os.Stderr, "entire-tail: no sessions found.")
		return
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	renderList(out, tree, isCharDevice(os.Stdout))
}

// listThemesText renders the --list-themes output, marking the default.
func listThemesText(defaultTheme string) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Available themes:\n\n")
	for _, t := range listThemeInfos() {
		marker := ""
		if t.Name == defaultTheme {
			marker = " (default)"
		}
		fmt.Fprintf(&b, "  %-18s %s%s\n", t.Name, t.Desc, marker)
	}
	fmt.Fprintf(&b, "\nPick with --theme NAME or ENTIRE_TAIL_THEME=NAME.\n")
	return b.String()
}

func helpText() string {
	return fmt.Sprintf(`entire-tail %s — live-view of your current AI coding agent session.

Tails the agent's session jsonl (Claude Code, Codex CLI, or Antigravity CLI)
for the current working directory and renders each turn in-process. Quit with
Ctrl-D or Ctrl-C.

USAGE:
  entire-tail [OPTIONS] [SESSION_FILE]
  entire tail [OPTIONS] [SESSION_FILE]    # when installed as an entire plugin

ARGUMENTS:
  SESSION_FILE              Path to a session jsonl. If omitted on an
                            interactive terminal, opens the session tree picker
                            (see --pick). Non-interactively (piped) or with
                            --no-pick, auto-discovers the most recently modified
                            session for $PWD across all agents (or the one
                            forced via --agent).

OPTIONS:
  -a, --agent NAME          Which agent's session to tail:
                              auto    (default) pick the agent with the most
                                      recently modified session for $PWD,
                                      falling back to the global newest.
                              claude  Claude Code (~/.claude/projects/...).
                              codex   Codex CLI (~/.codex/sessions/...).
                              agy     Antigravity CLI
                                      (~/.gemini/antigravity-cli/brain/...).
  -t, --theme NAME          Bundled theme (default: tokyo-night).
                            See --list-themes for choices.
  -b, --backfill N          Trailing events to replay at startup. Integer,
                            'all', or '0' (default: all).
      --no-backfill         Alias for --backfill 0 — only follow new events.
  -s, --style PATH          Override the glamour style path entirely. Bypasses
                            the theme's style.json but still uses the theme's
                            ANSI box header colors.
      --tool-style STYLE    How to render tool-use events:
                              dots   (default) one colored dot per tool
                                     call — blue=read, green=edit,
                                     yellow=bash/exec, magenta=grep,
                                     cyan=web, lavender=task, orange=mcp.
                                     Tool results are dropped (1:1 with
                                     tool_use, so a dot would just
                                     double-count).
                              hidden drop tool events entirely; show only
                                     user + assistant text. (alias: none)
                              full   verbose '⚙ Tool  input-preview' line
                                     per call. (alias: lines)
      --no-compact-tools    Alias for --tool-style full.
  -c, --collapse N          Collapse user pastes longer than N lines down to
                            the first N lines plus a "… M more lines" marker,
                            so a big pasted blob (command output, logs) doesn't
                            overwhelm the view. Integer >= 1 (default: 5).
                            Re-run with --no-collapse to see the full text.
      --no-collapse         Never collapse — show every user message in full.
  -p, --pick                Force the session tree (it's the DEFAULT already;
                            use this to override ENTIRE_TAIL_PICK=never). The
                            tree lists every local session grouped by repo (from
                            each session's git remote), so you can find "which
                            one was that?" without remembering. Fast + offline by
                            default; --cloud adds 'entire' titles + sessions from
                            other machines. Arrow keys / hjkl
                            move, → expands a folder, / filters by path or
                            snippet, q/Esc quits. Rows are colored by recency:
                            bright green = live now, muted green = active in the
                            last 15m, white = today, grey = older. Scoped to the
                            last --days days (default 7). On a session:
                              Enter   open the iTerm workspace — split the
                                      current window into claude --resume, a
                                      live tail, and a shell, all in the
                                      session's folder (macOS + iTerm2; falls
                                      back to tailing in place otherwise).
                              t       just tail the session in the current pane.
                            Claude only (codex/agy tail directly via --agent).
      --no-pick             Skip the picker — auto-discover and tail $PWD's most
                            recent session in place (the pre-tree behavior).
      --days N              Window for the session tree, in days (default 7).
                            'all' or 0 = every session, no cap.
  -L, --list                Print the session tree as a static, greppable
                            ls-style dump instead of the TUI, then exit.
                            Uncapped by default; narrow with --days.
  -S, --search QUERY        Find sessions by what was *said* in them, not just
                            titles. Searches local transcripts (ripgrep) and
                            'entire' checkpoint search (semantic + keyword,
                            all repos), merges by session, and shows the tree
                            ranked by relevance — an exact local phrase match
                            first, then entire's semantic hits — with the
                            matching snippet on each row. Enter/t resume or tail
                            the hit. Add --local to search only local
                            transcripts (no network).
      --cloud               Enrich the tree from the 'entire' cloud: generated
                            titles and sessions tracked on other machines. The
                            fetch takes a few seconds; the result is cached for
                            ~10 min, so subsequent runs (without --cloud) stay
                            instant and still show the cached titles.
      --local               Build the tree by crawling ~/.claude directly,
                            grouped by folder — no git remote lookups, no cloud.
                            The fastest / fully-offline view.
  -w, --workspace           Alias for the default: force the session tree. Its
                            Enter opens the iTerm workspace (macOS + iTerm2).
  -l, --list-themes         List available themes (with descriptions) and exit.
  -h, --help                Show this help and exit.
  -V, --version             Show version and exit.

LIVE KEYS (while following, on an interactive terminal):
  t                         Cycle tool-call rendering for new events:
                            full → dots → hidden → full.
  c                         Toggle collapsing of long user pastes for new
                            events.
  r                         Reload: re-render the whole current transcript with
                            the current settings — applies t/c retrospectively
                            by appending a fresh copy to the scrollback.
  q, Ctrl-D, Ctrl-C         Quit.

  t/c affect events rendered from now on; press r to re-render the history with
  the new settings (this is a streaming view, not an alt-screen TUI, so it
  appends rather than repainting in place).

ENVIRONMENT (lower priority than flags):
  ENTIRE_TAIL_AGENT         Same as --agent.
  ENTIRE_TAIL_THEME         Same as --theme.
  ENTIRE_TAIL_BACKFILL      Same as --backfill.
  ENTIRE_TAIL_TOOL_STYLE    Same as --tool-style.
  ENTIRE_TAIL_COLLAPSE      Same as --collapse (or 'off' to disable).
  ENTIRE_TAIL_PICK          'always'/'never'/'auto' — same as --pick/--no-pick.
  ENTIRE_TAIL_DAYS          Same as --days (session-tree window).
  GLOW_STYLE                Same as --style.

  CLAUDE_TAIL_* variants of the above are honored for back-compat.

EXAMPLES:
  entire-tail                                 # open the session tree picker
  entire-tail --no-pick                       # skip it: auto-detect + tail $PWD
  entire-tail --agent codex                   # follow the latest Codex session
  entire-tail --theme dracula
  entire-tail -t nord -b 50
  entire-tail --no-backfill
  entire-tail --search "fire socks"           # find the session where that was said
  entire-tail --list                          # static ls-style dump of all sessions
  entire-tail --list --days 3                 # ...only the last 3 days
  entire-tail ~/.codex/sessions/2026/05/.../rollout-...jsonl
`, version)
}
