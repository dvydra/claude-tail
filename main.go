package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "0.26.0"

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
	case ActionHandover:
		runHandover(cfg)
		return
	case ActionInstallHooks:
		if err := installHooks(firstNonEmpty(os.Getenv("HOME"), mustHome())); err != nil {
			die("install-hooks: " + err.Error())
		}
		fmt.Println("entire-tail: hooks installed. Restart Claude Code (or open /hooks once) to load them.")
		return
	case ActionTap:
		if err := runTap(cfg.TapArgs, firstNonEmpty(os.Getenv("HOME"), mustHome()), os.Getenv, os.Stdout); err != nil {
			die(err.Error())
		}
		return
	case ActionUninstallHooks:
		if err := uninstallHooks(firstNonEmpty(os.Getenv("HOME"), mustHome())); err != nil {
			die("uninstall-hooks: " + err.Error())
		}
		fmt.Println("entire-tail: hooks removed.")
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

	// Which binary the workspace panes launch is a preference (plain `claude` by
	// default); resolved once here so an explicit-but-missing choice is reported at
	// startup rather than at the moment a pane silently runs the wrong thing.
	claudeBin := resolveClaudeBin(cfg, exec.LookPath, os.Stderr)

	scanner := newCodexScanner(home)
	resolved := false
	session := ""

	if cfg.FollowSession != "" {
		// The workspace pins a session id (claude --session-id) and hands it to us,
		// so we follow exactly that file — waiting for it to appear, immune to any
		// other Claude running in the same repo. Forks are then followed by lineage.
		if !validSessionID(cfg.FollowSession) {
			die("invalid --follow-session id (want a UUID): " + cfg.FollowSession)
		}
		session = waitForSessionFile(home, pwd, cfg.FollowSession)
		if agentStr == "auto" {
			agentStr = string(AgentClaude)
		}
	} else if cfg.WaitNew {
		// The `n` workspace launches this alongside a fresh `claude`: block until a
		// session that didn't exist at launch appears in $PWD, then tail exactly it
		// (no racing the newest-file heuristic).
		session = waitForNewSession(home, pwd)
		if agentStr == "auto" {
			agentStr = string(AgentClaude)
		}
	} else {
		// Positional args are sugar: a single existing file is a session to tail;
		// anything else is a search query (so `entire-tail fire socks` just
		// searches). An explicit --search always wins.
		query := cfg.Search
		if len(cfg.Positional) == 1 && isFile(cfg.Positional[0]) {
			session = cfg.Positional[0]
		} else if query == "" && len(cfg.Positional) > 0 {
			query = strings.Join(cfg.Positional, " ")
		}
		// search: rank sessions by relevance to a content query, then let the user
		// pick one to tail/resume (or dump the ranking when non-interactive).
		if query != "" {
			tree := buildSearchTree(home, pwd, query, cfg.Local, time.Now().Unix())
			if len(tree.Folders) == 0 {
				die(fmt.Sprintf("no sessions matched %q", query))
			}
			if !ttyUsable() {
				out := bufio.NewWriter(os.Stdout)
				renderList(out, tree, isCharDevice(os.Stdout))
				out.Flush()
				return
			}
			p, ok := resolveTreeChoice(home, claudeBin, runTreeTUI(home, tree, theme))
			if !ok {
				return
			}
			session = p // fall through to tail it via the explicit-session path below
		}
	}

	// Auto-adopt: launched bare in a pane beside a single `claude` in this iTerm
	// tab? Tail exactly its session, skipping the tree. Matched by iTerm tab so a
	// claude elsewhere is never grabbed; self-disables off iTerm or when the tab
	// holds zero/many claudes. Explicit -p/--pick (always) opts out.
	adopted := false
	if session == "" && cfg.Pick != "always" && (agentStr == "auto" || agentStr == "claude") {
		if p, cwd := adoptPaneSession(home, os.Getenv); p != "" {
			fmt.Fprintln(os.Stderr, "entire-tail: adopted the claude session in this iTerm tab")
			session = p
			adopted = true
			if cwd != "" {
				pwd = cwd // watch the adopted agent's repo, not ours
			}
			if agentStr == "auto" {
				agentStr = string(AgentClaude)
			}
		}
	}

	if session == "" && shouldOfferHookInstall(hookOfferInputs{
		isTTY:            ttyUsable(),
		adopted:          adopted,
		alreadyInstalled: hookInstalledFor(home),
		choiceRecorded:   hookChoiceRecorded(home),
		noHookInstall:    cfg.NoHookInstall,
		followSession:    cfg.FollowSession != "",
		noPick:           cfg.Pick == "never",
		isClaude:         agentStr == "auto" || agentStr == "claude",
	}) {
		offerHookInstall(home, bufio.NewReader(os.Stdin))
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
		if path, ag, ok := runPicker(agents, home, pwd, days, cfg.Local, cfg.Cloud, theme, claudeBin); ok {
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

	loc := time.Local
	// One signal handler + code channel are shared across the whole picker↔tail
	// loop. tailSession follows one session and, on Ctrl-X, RETURNS so we re-enter
	// the tree picker to choose another (tailed in this same pane). Quit
	// (q/Ctrl-D/Ctrl-C) exits from inside tailSession. The tree is Claude-only, so
	// Ctrl-X is a no-op on codex/agy sessions and runPicker below just exits if no
	// Claude tree is in scope.
	codeCh := make(chan int, 3) // signal + keyboard quit; never block a sender
	installSignals(codeCh)      // SIGINT → 130, SIGTERM → 0
	for {
		tailSession(cfg, agent, session, home, pwd, scanner, theme, loc, codeCh)
		// tailSession only returns on Ctrl-X — pop back to the tree picker.
		days, derr := resolveDays(cfg.Days, 7)
		if derr != nil {
			die(derr.Error())
		}
		path, ag, ok := runPicker([]Agent{AgentClaude}, home, pwd, days, cfg.Local, cfg.Cloud, theme, claudeBin)
		if !ok {
			os.Exit(0) // no tty / no Claude tree in scope — nothing to go back to
		}
		session, agent = path, ag
		// The picker's alt-screen has closed, restoring the primary cursor to the
		// end of the previous tail's deferred (unterminated) line. Terminate it now
		// so the next session's banner starts on a fresh row — the newline abortLine
		// withheld on Ctrl-X, moved here where the flip already happened.
		fmt.Fprintln(os.Stdout)
	}
}

// tailSession renders one session: a backfill of trailing history, then a live
// follow loop. One select loop on this goroutine owns ALL stdout writes — polling
// for new lines, reloading, and the final flush. The signal/keyboard goroutines
// only report on channels (a quit code, a reload/back-to-tree/focus request);
// they never touch the writer, so there's no data race and no torn output.
//
// It os.Exit()s on quit (q/Ctrl-D/Ctrl-C, via the shared codeCh) and RETURNS on
// Ctrl-X ("back to tree"), leaving run() to re-enter the picker.
func tailSession(cfg Config, agent Agent, session, home, pwd string, scanner *codexScanner, theme Theme, loc *time.Location, codeCh chan int) {
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

	printBanner(cfg, agent, session, backfillFrom, total, collapse)

	out := bufio.NewWriter(os.Stdout)
	r, err := newRenderer(out, theme, cfg.ToolStyle, collapse, wrapWidth(os.Stdout, cfg.NoWrap))
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

	// ── phase 2: live follow until Ctrl-C / Ctrl-D / Ctrl-X ──
	r.live = true
	// Display toggles (t/T/c/m/r/y): applied on the render goroutine, never
	// coalesced — two `t` presses are two steps through the cycle and a second
	// `y` means one more message, so a dropped press lands on the wrong state.
	actionCh := make(chan keyAction, 8)
	overlayCh := make(chan keyAction, 1) // '→' focus / '?' help; both park the keyboard
	treeCh := make(chan struct{}, 1)     // keyboard Ctrl-X; back to the tree picker
	resumeCh := make(chan struct{})      // handed back to unpark the keyboard after the overlay
	winchCh := installWinch()            // terminal resized → the status bar re-claims its row
	treeEnabled := agent == AgentClaude

	// The status bar has to run its DSR query on the tty after cbreak is on but
	// before the reader goroutine could swallow the reply, so the tty is opened
	// here rather than inside startKeyboardOn.
	kbTTY, ttySaved, haveTTY := openControlTTY()
	var status *statusBar
	restoreTTY := func() {}
	if haveTTY {
		if !cfg.NoStatus && isCharDevice(os.Stdout) {
			// The bar claims a row, which means knowing the cursor isn't already
			// on it — so settle the backfill's deferred trailing newline first and
			// start the live phase on a line of its own. (Without a bar the line
			// is deliberately left open, so a dot streak can ride it.)
			r.endLine()
			out.Flush()
			status = newStatusBar(kbTTY)
		}
		restoreTTY = startKeyboardOn(kbTTY, ttySaved, treeEnabled, codeCh, actionCh, overlayCh, treeCh, resumeCh)
	}
	// restoreTerm always runs the status bar's teardown FIRST: it has to hand the
	// full scrolling region back before the terminal modes are restored, or the
	// shell inherits a terminal that can only scroll h-1 rows.
	restoreTerm := func() { status.close(); restoreTTY() }
	defer restoreTerm() // panic safety; the normal paths restore explicitly below

	emit := func(line []byte) {
		for _, rec := range normalize(agent, line, loc) {
			r.emit(rec)
		}
	}
	// cur is the file currently being followed — it starts at `session` but rolls
	// over to a Claude worktree-fork child (see rollover below), so poll/reload
	// read cur, not the immutable `session` param. lineage is the set of session
	// ids we own; only a sibling forked from one of them is ever adopted.
	cur := session
	projectDir := filepath.Dir(cur)
	lineage := map[string]bool{sessionIDFromPath(cur): true}
	offset := liveOffset(data)
	agyKeep := newAgyDedup(maxStepIndex(lines))
	var agyLastSize, agyLastMtime int64 = -1, -1
	poll := func() {
		if agent == AgentAgy {
			// agy rewrites the whole file each step; only re-read (and re-scan
			// for new step_index) when it actually changes.
			fi, err := os.Stat(cur)
			if err != nil {
				return
			}
			sz, mt := fi.Size(), fi.ModTime().UnixNano()
			if sz == agyLastSize && mt == agyLastMtime {
				return
			}
			agyLastSize, agyLastMtime = sz, mt
			if d, err := os.ReadFile(cur); err == nil {
				for _, l := range splitLines(d) {
					if agyKeep(l) {
						emit(l)
					}
				}
			}
		} else {
			offset = appendStep(cur, offset, emit)
		}
	}
	// rerender re-renders the current transcript with the live settings — the
	// streaming-native way to apply t/T/c/m retrospectively, since printed lines
	// can't be repainted in place. banner is the dim divider printed before the
	// fresh copy (it reads r.theme, so a theme swap must land BEFORE this runs to
	// colour the banner in the new theme).
	//
	// keep caps how many rendered LINES are actually printed, newest last (0 =
	// all of them). A toggle only needs to refresh what you can see: dumping a
	// whole session on every keypress buries the screen in scrollback and — since
	// the tail of it looks much like the tail of the old copy — reads as if
	// nothing happened. The transcript is still rendered in full so the renderer's
	// state (turn boundaries, the yank buffer, dot streaks) stays exact; only the
	// output is trimmed. `r` passes 0 and re-renders everything.
	rerender := func(banner string, keep int) {
		d, err := os.ReadFile(cur)
		if err != nil {
			return
		}
		all := splitLines(d)
		r.endLine() // close any open dot-streak bracket before wiping state
		r.reset()
		// Render into a buffer so the tail can be taken; r.w is only ever touched
		// on this goroutine.
		var buf bytes.Buffer
		prevW := r.w
		r.w = &buf
		for _, l := range all {
			emit(l)
		}
		r.w = prevW

		body, trimmed := tailLinesOf(buf.String(), keep)
		if trimmed {
			banner += " · showing the last " + fmt.Sprint(keep) + " lines (r for all)"
		}
		io.WriteString(out, "\n"+r.theme.DimANSI+banner+reset+"\n\n")
		io.WriteString(out, body)
		offset = liveOffset(d)
		agyKeep = newAgyDedup(maxStepIndex(all))
		agyLastSize, agyLastMtime = -1, -1 // force a re-stat next tick
	}
	// reload is the `r` key: re-render the WHOLE transcript, settings unchanged.
	reload := func() { rerender("⟳ reloaded", 0) }
	// screenful is how much a toggle re-renders: enough to refresh what's on
	// screen, and no more. Without a status bar there's no measured height, so
	// fall back to a conservative screen.
	screenful := func() int {
		if status == nil {
			return 40
		}
		return max(status.h-2, 8)
	}
	// cycleTheme is the `T` key: swap to the next bundled theme, then re-render so
	// the whole visible transcript recolours at once (glamour body colours already
	// in scrollback can't be recoloured in place). A rebuild failure is a no-op.
	cycleTheme := func() string {
		next, err := nextTheme(theme.Name)
		if err != nil {
			return "no other theme to switch to"
		}
		if err := r.applyTheme(next); err != nil {
			return "cannot load theme " + next.Name
		}
		theme = next // so a later focus overlay (runFocus) uses the new theme too
		rerender("⟳ theme: "+next.Name, screenful())
		return "theme: " + next.Name
	}
	// rollover follows the session across a Claude worktree fork. When the
	// current file has gone quiet and a sibling in the same project dir forked
	// from our lineage, switch to it and stream from its start (fork children are
	// short at the moment they appear). Claude-only: worktree-state pointers are a
	// Claude construct, and agy/codex don't rotate session ids mid-run.
	rollover := func() bool {
		if agent != AgentClaude {
			return false
		}
		np, nid := lineageChild(projectDir, cur, lineage)
		if np == "" {
			// No NEW-id fork in this dir. Maybe a worktree cwd switch moved this
			// session's whole transcript to a different project dir (same id, new
			// dir). Follow it, KEEPING our byte offset — the moved file shares our
			// content prefix, so appendStep resumes from there (and self-heals if it
			// ever shrank). No r.reset(): it's the same session, stream it seamlessly.
			// The root is taken from the file we're following, not from home: a
			// personal-account session lives under ~/.claude-personal/projects, and
			// a worktree cwd switch relocates it within THAT root. Searching the
			// work root for it would silently never find the move.
			rp := relocatedSession(projectsRootOf(cur), sessionIDFromPath(cur), cur)
			if rp == "" {
				return false
			}
			// Only adopt once the relocated file has caught up to at least our byte
			// offset. A candidate still mid-write (size < offset) would trip
			// appendStep's truncation reset and re-render the whole prefix; skip it
			// and re-check on the next idle tick instead.
			if fi, err := os.Stat(rp); err != nil || fi.Size() < offset {
				return false
			}
			r.endLine() // close any open dot-streak bracket before the note
			io.WriteString(out, "\n"+r.theme.DimANSI+"⟳ following session into "+filepath.Base(filepath.Dir(rp))+reset+"\n\n")
			cur = rp
			projectDir = filepath.Dir(rp)
			agyLastSize, agyLastMtime = -1, -1
			return true
		}
		// Name both ends of the flip. On disk the old file just stops with no
		// forward pointer, so without the ids printed here the continuation is
		// impossible to locate. "continued in" is the tail of the old session;
		// "…continuing from" heads the new one. Both are <id>.jsonl basenames in
		// the same project dir.
		oldID := sessionIDFromPath(cur)
		oldPath := cur
		lineage[nid] = true
		cur = np
		// Opt-in: leave a forward pointer in the now-stopped file too, so the
		// continuation is findable when that session is reopened in Claude Code
		// (not just in this live window). cur has already moved on, so the tail
		// never re-reads the appended line.
		if cfg.MarkContinuation {
			markContinuation(oldPath, nid)
		}
		r.endLine() // close any open dot-streak bracket before wiping state
		io.WriteString(out, "\n"+r.theme.DimANSI+"⟳ continued in "+nid+reset+"\n")
		r.reset()
		io.WriteString(out, "\n"+r.theme.DimANSI+"⟳ …continuing from "+oldID+reset+"\n\n")
		offset = 0
		agyLastSize, agyLastMtime = -1, -1
		return true
	}

	// Status-bar state. lastEventAt answers the question a tail is actually for
	// — "when did anything last happen" — so it moves on transcript growth, not
	// on wall-clock ticks. pendingNow mirrors the pending-prompt marker.
	lastEventAt := time.Now()
	lastTurns := r.turnsRendered
	pendingNow := false
	// wrapOff is the `w` toggle: wrapping suspended so a mouse drag-select copies
	// whole paragraphs. Kept separate from cfg.NoWrap so a resize while it's on
	// doesn't re-wrap behind the user's back — every wrapWidth call ORs the two.
	wrapOff := false
	statusNow := func() statusInfo {
		return statusInfo{
			Agent:    agent,
			Repo:     statusRepo(pwd),
			Session:  sessionIDFromPath(cur),
			Turns:    r.turnsRendered,
			LastAt:   lastEventAt,
			Pending:  pendingNow,
			Tools:    toolStyleKind(r.toolStyle.Load()),
			Theme:    theme.Name,
			Collapse: int(r.collapse.Load()),
			Mrkdwn:   r.mrkdwn.Load(),
			NoWrap:   wrapOff,
		}
	}
	// note shows a keypress result. With a status bar it's the yellow transient
	// line; without one (piped output, --no-status) it falls back to stderr,
	// which is where these messages used to go.
	note := func(msg string) {
		if status == nil {
			fmt.Fprintln(os.Stderr, "entire-tail: "+msg)
			return
		}
		status.setMessage(msg, time.Now())
		status.update(statusNow(), time.Now())
	}
	status.update(statusNow(), time.Now())

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	idle := 0 // consecutive ticks the current Claude file hasn't grown
	// winchAt is the last SIGWINCH; zero when there's no re-wrap owed.
	var winchAt time.Time
	// Live pending-prompt watch (Claude only, and only when the hook is
	// installed — i.e. the markers dir exists). lastMarkerKey dedups so a marker
	// lingering across ticks renders exactly once.
	pendingWatch := agent == AgentClaude && isDir(pendingDir(home))
	lastMarkerKey := ""
	// Live API-tap watch (Claude only) — what surfaces a blocked question's
	// preamble, which the transcript withholds until the user answers.
	//
	// Created unconditionally rather than gated on the sidecar existing: on a
	// fresh session the transcript file appears BEFORE the first tap event (~200ms
	// after the request starts vs. the first completed content block), so a
	// start-up existence check is always false for exactly the flow the tap is for
	// — and it would also miss a daemon started mid-session. A watcher whose
	// sidecar never appears polls a missing path and does nothing, the same cheap
	// per-tick stat as the pending-marker watch.
	var tap *tapWatcher
	if agent == AgentClaude && !cfg.NoTap {
		tap = newTapWatcher(home, sessionIDFromPath(cur))
	}
	for {
		select {
		case code := <-codeCh:
			// Quit: restore the terminal and do the final flush here, on the
			// sole writer goroutine (130 for Ctrl-C/SIGINT, 0 for q/Ctrl-D/SIGTERM).
			restoreTerm()
			r.endLine() // terminate a deferred body/dots line so exit lands on a fresh row
			out.Flush()
			fmt.Fprintln(os.Stderr)
			os.Exit(code)
		case <-treeCh:
			// Ctrl-X: restore the tty so the tree picker (which opens its own
			// /dev/tty reader) is the sole reader, flush, and return to run() to
			// re-enter the picker. The keyboard goroutine has already stopped.
			// abortLine (not endLine) leaves the trailing newline unwritten so the
			// flip to the picker's alt-screen doesn't leave a blank line behind;
			// run() writes it before the next session's banner.
			restoreTerm()
			r.abortLine()
			out.Flush()
			return
		case act := <-actionCh:
			// Every display toggle lands here, on the one goroutine allowed to
			// render. Each applies its change, re-renders the history so the whole
			// transcript reflects it (this is a streaming view — already-printed
			// lines can't be repainted in place, so "apply" means "append a fresh
			// copy"), and reports through the status bar.
			msg := ""
			switch act {
			case keyCycleTools:
				msg = r.cycleTools()
				rerender("⟳ "+msg, screenful())
			case keyCycleTheme:
				msg = cycleTheme()
			case keyToggleCollapse:
				msg = r.toggleCollapse()
				rerender("⟳ "+msg, screenful())
			case keyToggleMrkdwn:
				msg = r.toggleMrkdwn()
				rerender("⟳ "+msg, screenful())
			case keyToggleWrap:
				// The copy escape hatch. Wrapped prose reads better but a mouse
				// drag-select copies the breaks with it; unwrapped, each paragraph
				// is one logical line the terminal soft-wraps and rejoins on copy.
				// So: press w, drag out the paragraph you want, press w again.
				wrapOff = !wrapOff
				r.setWrap(wrapWidth(os.Stdout, cfg.NoWrap || wrapOff))
				msg = "wrap: on"
				if wrapOff {
					msg = "wrap: off — drag-select now copies whole paragraphs"
				}
				rerender("⟳ "+msg, screenful())
			case keyReload:
				reload()
				msg = "re-rendered"
			case keyYank:
				// No re-render: a yank copies, it doesn't change the view. It runs
				// here rather than on the keyboard goroutine because it reads the
				// renderer's message buffer and may write OSC 52 to the tty.
				msg = r.yank(kbTTY, time.Now())
			}
			// Flush the transcript before the bar: the bar goes straight to the
			// tty, so a buffered re-render still sitting in `out` would land after
			// it and the two would interleave.
			out.Flush()
			note(msg)
		case act := <-overlayCh:
			// The keyboard goroutine is parked on resumeCh; we're the sole tty
			// owner. The overlays draw full-height, so the status bar hands the
			// scrolling region back for the duration.
			out.Flush()
			status.suspend()
			switch act {
			case keyFocus:
				// Only Claude sessions have subagents; runFocus no-ops (with a
				// hint) otherwise.
				runFocus(kbTTY, cur, home, theme)
			case keyHelp:
				// The state shown is sampled HERE, not at startup: t/T/c/m may have
				// moved since the banner was printed.
				runHelp(kbTTY, helpInfo{
					Agent:       agent,
					Session:     tildify(cur, home),
					Theme:       theme.Name,
					Backfill:    cfg.Backfill,
					From:        backfillFrom,
					Total:       total,
					Tools:       toolStyleKind(r.toolStyle.Load()),
					Collapse:    int(r.collapse.Load()),
					Mrkdwn:      r.mrkdwn.Load(),
					Wrap:        r.wrap,
					TreeEnabled: treeEnabled,
				}, theme)
			}
			status.resume()
			status.update(statusNow(), time.Now())
			resumeCh <- struct{}{}
		case <-winchCh:
			// The bar reclaims its row immediately (cheap), but the re-wrap is
			// deferred to the ticker: SIGWINCH fires continuously while a window
			// edge is dragged and re-rendering the transcript per signal would
			// bury the screen.
			status.resize()
			status.update(statusNow(), time.Now())
			winchAt = time.Now()
		case <-ticker.C:
			// A resize changes the wrap width, and printed lines can't be
			// rewrapped in place — so re-render once the resize has settled.
			// setWrap reports whether the WIDTH moved, so dragging the bottom
			// edge (height only) costs nothing.
			if winchSettled(winchAt, time.Now()) {
				winchAt = time.Time{}
				if r.setWrap(wrapWidth(os.Stdout, cfg.NoWrap || wrapOff)) {
					rerender("⟳ resized", screenful())
				}
			}
			before := offset
			poll()
			// A quiet Claude file may mean it forked. After rolloverIdleTicks with
			// no growth, look for a lineage child and, on adoption, immediately
			// stream its content. offset only advances for Claude (agy gates out).
			if agent == AgentClaude {
				if offset != before {
					idle = 0
				} else if idle++; idle >= rolloverIdleTicks {
					if rollover() {
						poll()
					}
					idle = 0
				}
			}
			if tap != nil {
				// Follow the tap across a lineage flip/relocation, same as the
				// transcript follower does.
				tap.rebind(home, sessionIDFromPath(cur))
				for _, p := range tap.poll() {
					r.tapPreamble(p, formatTapTS(p.Ts, loc))
				}
			}
			if pendingWatch {
				m, ok := readPendingMarker(home, sessionIDFromPath(cur))
				render, key := pendingAction(lastMarkerKey, m, ok)
				if render {
					switch m.Kind {
					case "question":
						r.pendingQuestion(claudeParseQuestions(m.Payload))
					case "permission":
						r.pendingPermission(permissionSummary(m))
					}
				}
				lastMarkerKey = key
				pendingNow = ok
			}
			out.Flush()
			// The bar is written straight to the tty, so the transcript writer
			// has to be flushed first or the two interleave.
			if offset != before || r.turnsRendered != lastTurns {
				lastEventAt, lastTurns = time.Now(), r.turnsRendered
			}
			status.update(statusNow(), time.Now())
		}
	}
}

// winchSettle is how long the SIGWINCH stream must be quiet before the re-wrap
// re-renders. Longer than pollInterval, so a drag that's still in flight always
// pushes the re-render out by another tick instead of firing mid-drag.
const winchSettle = 300 * time.Millisecond

// winchSettled reports whether a resize is owed and its SIGWINCH stream has gone
// quiet. A zero winchAt means nothing is owed.
func winchSettled(winchAt, now time.Time) bool {
	return !winchAt.IsZero() && now.Sub(winchAt) >= winchSettle
}

// wrapWidth is the column limit for the live renderer: the terminal's width when
// stdout is one, and 0 (no wrap) otherwise — piped output has no width to wrap
// to, which is also how the goldens are generated. `--no-wrap` forces 0 as well:
// unwrapped, each paragraph is a single logical line the terminal soft-wraps, so
// the terminal rejoins it on copy and a mouse drag-select yields one unbroken
// paragraph. Wrapping trades that for breaks that land between words.
func wrapWidth(f *os.File, noWrap bool) int {
	if noWrap || !isCharDevice(f) {
		return 0
	}
	w, _ := termSize(f)
	return wrapWidthFor(w, true)
}

// installWinch reports terminal resizes. SIGWINCH is delivered to the process,
// but the scrolling region and the bar's row both have to be recomputed on the
// render goroutine, so it arrives as a channel the live loop selects on.
func installWinch() <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch
}

// waitForNewSession blocks until a Claude session file appears in pwd's project
// dir that wasn't there at launch (and has content), then returns its path. Used
// by the `n` workspace so entire-tail latches onto the session the freshly
// launched `claude` creates, not whatever was newest before.
// It watches every account's projects root, so the pane latches on whether the
// agent beside it was launched as the work or the personal account.
func waitForNewSession(home, pwd string) string {
	var patterns []string
	for _, root := range claudeProjectsRoots(home) {
		patterns = append(patterns, filepath.Join(root, claudeSlug(pwd), "*.jsonl"))
	}
	before := map[string]bool{}
	for _, p := range patterns {
		for _, f := range globList(p) {
			before[f] = true
		}
	}
	fmt.Fprintf(os.Stderr, "entire-tail: waiting for a new Claude session in %s … (Ctrl-C to cancel)\n", pwd)
	for {
		for _, p := range patterns {
			for _, f := range globList(p) {
				if before[f] {
					continue
				}
				if fi, err := os.Stat(f); err == nil && fi.Size() > 0 {
					return f // the session the new `claude` just created
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// waitForSessionFile blocks until pwd's project dir holds <id>.jsonl with
// content, then returns its path. Used by the pinned workspace: `claude
// --session-id <id>` and `entire-tail --follow-session <id>` share the id, so
// entire-tail latches onto exactly that file no matter how many other Claude
// sessions are live in the same repo.
// The id is watched under every account's projects root, so a pinned personal
// workspace needs no extra flag to say which account it launched — the file
// simply appears under whichever root its agent is writing to.
func waitForSessionFile(home, pwd, id string) string {
	var paths []string
	for _, root := range claudeProjectsRoots(home) {
		paths = append(paths, filepath.Join(root, claudeSlug(pwd), id+".jsonl"))
	}
	ready := func() string {
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
				return p
			}
		}
		return ""
	}
	if p := ready(); p != "" {
		return p
	}
	fmt.Fprintf(os.Stderr, "entire-tail: waiting for Claude session %s in %s … (Ctrl-C to cancel)\n", id, pwd)
	for {
		if p := ready(); p != "" {
			return p
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func globList(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
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
		back := ""
		if agent == AgentClaude {
			back = "Ctrl-X=back to tree  "
		}
		fmt.Fprintln(w, "  keys:     ?=help  y=copy as slack mrkdwn  m=mrkdwn view  w=wrap  t=tools  T=theme  c=collapse  →=subagents  r=re-render  "+back+"q/Ctrl-D=quit")
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
	query := cfg.Search
	if query == "" && len(cfg.Positional) > 0 {
		query = strings.Join(cfg.Positional, " ")
	}
	var tree sessionTree
	if query != "" {
		tree = buildSearchTree(home, pwd, query, cfg.Local, now)
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
  entire-tail [OPTIONS] [SESSION_FILE | SEARCH WORDS...]
  entire tail [OPTIONS] [SESSION_FILE | SEARCH WORDS...]   # as an entire plugin
  entire-tail handover                                     # write today's handover docs

SUBCOMMANDS:
  handover                  Enumerate today's Claude sessions, group them in a
                            picker (1-9 group · x separate · - skip · ⏎ write),
                            then launch an interactive claude that enriches each
                            group with live Linear/GitHub/Entire state and writes
                            one Obsidian handover doc per group (via the
                            handover-sessions skill). Docs go to
                            $ENTIRE_TAIL_HANDOVER_VAULT/Entire/Handover/YYYY-MM-DD/
                            (default: the iCloud Obsidian vault).
  tap start|status|stop|install|uninstall
                            The API tap: a local reverse proxy (127.0.0.1:%d)
                            that agents launched from the tree are pointed at
                            with ANTHROPIC_BASE_URL, so entire-tail can see the
                            assistant stream as it happens. It exists for one
                            case the transcript can't cover: Claude Code
                            withholds the whole message containing an
                            AskUserQuestion — the preamble text included —
                            until you answer, so a blocked question otherwise
                            shows up with no sign of the reasoning behind it.
                            It also yields exact per-session activity (which
                            session is generating right now) for the picker.
                            Opt-in and fail-open: sessions are only routed when
                            the daemon answers a health check at launch, so with
                            no daemon everything behaves exactly as before.
                            'install' writes a KeepAlive LaunchAgent (a daemon
                            that dies mid-session takes that session's API
                            endpoint with it, so it should come back by itself).
                            Traffic other than POST /v1/messages is proxied
                            untouched, and headers are never logged or stored.

ARGUMENTS:
  [ARGS...]                 With no args, if exactly one 'claude' is running in
                            this iTerm tab (i.e. you're in a pane beside it),
                            entire-tail adopts and tails THAT session — no flags,
                            no picking. It's matched by iTerm tab, so a claude in
                            another tab/window is never grabbed; off iTerm, or
                            with zero/many claudes in the tab, it self-disables.
                            Failing that, an interactive terminal opens the
                            session tree picker (see --pick); non-interactively
                            or with --no-pick it auto-discovers + tails $PWD's
                            newest session. Force the tree over adoption with -p.
                            A single arg that's an existing file tails that file.
                            Otherwise the args are a search query (same as
                            --search) — so 'entire-tail fire socks' finds the
                            session where that was said.

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
                                      current window into '<agent> --resume', a
                                      live tail, and a shell, all in the
                                      session's folder (macOS + iTerm2; falls
                                      back to tailing in place otherwise). The
                                      agent is --claude-bin (default 'claude').
                              p       preview the session's recent transcript.
                              i       summary card: an on-device Apple
                                      Intelligence summary (headline, summary,
                                      key points, outcome — macOS 26+ with
                                      Apple Intelligence) plus entire's metadata
                                      (repo, model, tokens, checkpoints, prompt).
                              t       just tail the session in the current pane.
                              n       open a workspace for a NEW Claude session
                                      in the highlighted folder's directory
                                      (or $PWD) — fresh agent + tail + shell.
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
      --wait-new            Block until a NEW Claude session appears in $PWD,
                            then tail exactly it. The picker's 'n' workspace uses
                            this so the tail latches onto the session the fresh
                            'claude' creates, instead of racing an older one.
      --follow-session ID   Follow exactly $PWD's <ID>.jsonl (waiting for it to
                            appear), then follow worktree forks by lineage. The
                            workspace pairs it with '<agent> --session-id ID' so
                            the tail can't latch onto the wrong concurrent session.
      --mark-continuation   At a Claude lineage flip (worktree fork or /clear),
                            also write a forward-pointer note into the now-stopped
                            session file, so the continuation is findable when
                            that session is reopened in Claude Code — not just in
                            this live window. Off by default (entire-tail is
                            otherwise read-only). Uses Claude Code's own
                            transcript-only 'informational' record, so it shows on
                            resume without steering Claude.
      --no-tap              Ignore the API tap even for a session routed
                            through it (see the 'tap' subcommand) — a blocked
                            question then shows the card alone, with its
                            preamble arriving after you answer.
      --no-status           Don't reserve the bottom row for the status bar.
                            The bar shrinks the terminal's scrolling region by
                            one row; use this if that upsets your terminal.
      --no-wrap             Don't wrap prose to the terminal width; emit each
                            paragraph as one long line and let the terminal
                            soft-wrap it. Breaks land mid-word, but the terminal
                            rejoins its own soft wraps on copy, so a mouse
                            drag-select yields unbroken paragraphs. (Piped
                            output is never wrapped either way.)
      --claude-bin BIN      Which binary the workspace panes and 'handover'
                            launch. Default 'claude'. Pass any claude-compatible
                            wrapper instead ('happy' for mobile control, a shim
                            script, an absolute path) — a wrapper writes the same
                            ~/.claude transcripts, so the tail is unaffected, but
                            one that drops --session-id costs the fresh
                            workspace its pinned id (and happy resumes rather
                            than starting clean, which is why it isn't the
                            default). Falls back to 'claude' when the named
                            binary isn't on PATH.
  -w, --workspace           Alias for the default: force the session tree. Its
                            Enter opens the iTerm workspace (macOS + iTerm2).
  -l, --list-themes         List available themes (with descriptions) and exit.
  -h, --help                Show this help and exit.
  -V, --version             Show version and exit.

LIVE KEYS (while following, on an interactive terminal):
  ?                         Help: a modal with this session's settings, the key
                            map and the dot legend. Any key closes it.
  t                         Cycle tool-call rendering: full → dots → hidden.
  c                         Toggle collapsing of long user pastes.
  T                         Cycle the colour theme.
  m                         Toggle agent text between rendered markdown and
                            Slack mrkdwn source (so a mouse-select copies
                            mrkdwn).
  w                         Toggle word wrap. Off, each paragraph is one long
                            logical line the terminal soft-wraps and rejoins on
                            copy — so press w, drag out the paragraph you want,
                            press w again. The bar shows 'nowrap' meanwhile.
  y                         Copy the last agent message to the clipboard as
                            Slack mrkdwn. Press again within 3s to add the
                            message before it.
  →                         Focus subagents: open an alt-screen view of the
                            session's subagent transcripts. ←/→ cycles between
                            them, ↑↓ scrolls, r reloads, q/Esc returns to the
                            tail. (Claude sessions only; no-op when none.)
  r                         Reload: re-render the whole current transcript with
                            the current settings.
  Ctrl-X                    Back to the tree: pop out of the live tail and
                            re-open the session tree picker (Claude sessions
                            only). Pick another session with t to tail it in
                            this same pane, or Enter/n for a workspace. q in the
                            tree quits entire-tail.
  q, Ctrl-D, Ctrl-C         Quit.

  t/T/c/m/w re-render the history themselves, so the whole transcript reflects
  the change. This is a streaming view, not an alt-screen TUI: re-rendering appends
  a fresh copy rather than repainting in place, and your terminal's own
  scrollback keeps working. r does the same on demand.

  The bottom row is a status bar: which session is being followed, how long it
  has been quiet (or whether the agent is blocked on you), and the current
  render settings. A keypress reports there in yellow for three seconds.
  --no-status turns it off.

ENVIRONMENT (lower priority than flags):
  ENTIRE_TAIL_AGENT         Same as --agent.
  ENTIRE_TAIL_THEME         Same as --theme.
  ENTIRE_TAIL_BACKFILL      Same as --backfill.
  ENTIRE_TAIL_TOOL_STYLE    Same as --tool-style.
  ENTIRE_TAIL_COLLAPSE      Same as --collapse (or 'off' to disable).
  ENTIRE_TAIL_PICK          'always'/'never'/'auto' — same as --pick/--no-pick.
  ENTIRE_TAIL_DAYS          Same as --days (session-tree window).
  ENTIRE_TAIL_CLAUDE_BIN    Same as --claude-bin (default 'happy').
  ENTIRE_TAIL_MARK_CONTINUATION  Truthy (1/true/yes/on) = --mark-continuation.
  ENTIRE_TAIL_HANDOVER_VAULT  Obsidian vault root for handover docs (default:
                            the iCloud Obsidian Documents folder).
  GLOW_STYLE                Same as --style.

  CLAUDE_TAIL_* variants of the above are honored for back-compat.

EXAMPLES:
  entire-tail                                 # open the session tree picker
  entire-tail --no-pick                       # skip it: auto-detect + tail $PWD
  entire-tail --agent codex                   # follow the latest Codex session
  entire-tail --theme dracula
  entire-tail -t nord -b 50
  entire-tail --no-backfill
  entire-tail fire socks                      # bare words = search (no --search needed)
  entire-tail --search "fire socks"           # explicit flag, same thing
  entire-tail --list                          # static ls-style dump of all sessions
  entire-tail --list --days 3                 # ...only the last 3 days
  entire-tail ~/.codex/sessions/2026/05/.../rollout-...jsonl
`, version, tapDefaultPort)
}
