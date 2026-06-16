package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const version = "0.6.0"

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

	scanner := newCodexScanner(home)
	session := cfg.Session
	resolved := false

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
		if path, ag, ok := runPicker(agents, home, pwd, cfg.Pick, scanner, os.Stderr); ok {
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

	// ── theme ──
	if !themeExists(cfg.Theme) {
		fmt.Fprintf(os.Stderr, "entire-tail: unknown theme %q.\n\n", cfg.Theme)
		fmt.Fprint(os.Stderr, listThemesText(cfg.Theme))
		os.Exit(2)
	}
	theme, err := loadTheme(cfg.Theme, cfg.GlowStyle)
	if err != nil {
		die(err.Error())
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

	// ── quit handling ──
	installSignalHandlers(out)

	// ── phase 2: live follow ──
	r.live = true
	emit := func(line []byte) {
		for _, rec := range normalize(agent, line, loc) {
			r.emit(rec)
		}
		out.Flush()
	}
	stop := make(chan struct{})
	if agent == AgentAgy {
		keep := newAgyDedup(maxStepIndex(lines))
		dedup := func(line []byte) {
			if keep(line) {
				emit(line)
			}
		}
		followRewrite(session, dedup, stop)
	} else {
		followAppend(session, liveOffset(data), emit, stop)
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

// installSignalHandlers wires Ctrl-C (exit 130) and Ctrl-D (exit 0) to match the
// bash traps, flushing buffered output first.
func installSignalHandlers(out *bufio.Writer) {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT)
	go func() {
		<-sigc
		out.Flush()
		fmt.Fprintln(os.Stderr)
		os.Exit(130)
	}()

	// Ctrl-D on an empty line closes stdin; nothing else reads the tty once the
	// picker is done, so a reader goroutine can own it and quit on EOF.
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		go func() {
			tty, err := os.Open("/dev/tty")
			if err != nil {
				return
			}
			defer tty.Close()
			buf := make([]byte, 256)
			for {
				if _, err := tty.Read(buf); err != nil {
					out.Flush()
					fmt.Fprintln(os.Stderr)
					os.Exit(0)
				}
			}
		}()
	}
}

func printBanner(cfg Config, agent Agent, session string, from, total, collapse int) {
	w := os.Stderr
	fmt.Fprintln(w, "entire-tail "+version)
	fmt.Fprintln(w, "  agent:    "+string(agent))
	fmt.Fprintln(w, "  session:  "+session)
	fmt.Fprintln(w, "  theme:    "+cfg.Theme)
	fmt.Fprintf(w, "  backfill: %s (%d..%d of %d)\n", cfg.Backfill, from, total, total)
	fmt.Fprintln(w, "  tools:    "+cfg.ToolStyle)
	if collapse > 0 {
		fmt.Fprintf(w, "  collapse: user pastes > %d lines\n", collapse)
	} else {
		fmt.Fprintln(w, "  collapse: off")
	}
	if cfg.ToolStyle == "dots" {
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
  SESSION_FILE              Path to a session jsonl. If omitted, picks the
                            most recently modified session for $PWD across
                            all supported agents (or just the agent forced
                            via --agent).

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
                              none   drop tool events entirely; show only
                                     user + assistant text.
                              lines  verbose '⚙ Tool  input-preview' line
                                     per call (the original style).
      --no-compact-tools    Alias for --tool-style lines.
  -c, --collapse N          Collapse user pastes longer than N lines down to
                            the first N lines plus a "… M more lines" marker,
                            so a big pasted blob (command output, logs) doesn't
                            overwhelm the view. Integer >= 1 (default: 5).
                            Re-run with --no-collapse to see the full text.
      --no-collapse         Never collapse — show every user message in full.
  -p, --pick                Pick which live session to tail from a menu.
                            Finds every cwd with a running agent process
                            (Claude or Codex), lists each one's most recent
                            sessions — one row per running pane, with a preview
                            of its last message — and tails your choice. By
                            default (auto) the one live session in $PWD is
                            tailed without asking; the menu only appears when
                            $PWD is ambiguous (2+ live here) or has none but 2+
                            are live elsewhere. --pick forces it even for one.
                            Scoped to --agent ('auto' scans claude/codex/agy).
                            claude and codex split per pane; agy is one per cwd.
      --no-pick             Never show the picker — always auto-discover.
  -l, --list-themes         List available themes (with descriptions) and exit.
  -h, --help                Show this help and exit.
  -V, --version             Show version and exit.

ENVIRONMENT (lower priority than flags):
  ENTIRE_TAIL_AGENT         Same as --agent.
  ENTIRE_TAIL_THEME         Same as --theme.
  ENTIRE_TAIL_BACKFILL      Same as --backfill.
  ENTIRE_TAIL_TOOL_STYLE    Same as --tool-style.
  ENTIRE_TAIL_COLLAPSE      Same as --collapse (or 'off' to disable).
  ENTIRE_TAIL_PICK          'always'/'never'/'auto' — same as --pick/--no-pick.
  GLOW_STYLE                Same as --style.

  CLAUDE_TAIL_* variants of the above are honored for back-compat.

EXAMPLES:
  entire-tail                                 # auto-detect agent for $PWD
  entire-tail --agent codex                   # follow the latest Codex session
  entire-tail --agent agy                     # follow the latest Antigravity session
  entire-tail --theme dracula
  entire-tail -t nord -b 50
  entire-tail --no-backfill
  entire-tail --pick                          # choose among live Claude sessions
  entire-tail --list-themes
  entire-tail ~/.codex/sessions/2026/05/.../rollout-...jsonl
`, version)
}
