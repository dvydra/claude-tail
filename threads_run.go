package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Seams: tests must never run osascript or replace the process.
var (
	threadsOsa  = osaRun
	threadsExec = execAgent
	// threadsItermOK reports whether an unsplit iTerm tab is there to take the workspace.
	threadsItermOK = func() bool { return itermAvailable() && itermSinglePane() }
	// threadsNote prints a line the user sees before the agent takes the pane.
	threadsNote = func(s string) { fmt.Fprintln(os.Stderr, s) }
)

// threadLaunchEnv supplies what needs IO to build a launch: the account and
// tap env prefixes for a Claude session and the cwd that session last worked in.
type threadLaunchEnv func(agent, id string) (acctEnv, tapEnv, cwd string)

// threadLaunch opens a thread as a FRESH Claude session primed with
// threadReassessPrompt, never a resume of an old one: the thread's files are
// the handover, and an old session brings back its stale context with it.
// Pure, no IO beyond directory checks; refuse is non-empty when it must not open.
func threadLaunch(t thread, f threadFacts, self, claudeBin string, newID func() string) (workspaceLaunch, threadSessionRef, string) {
	return threadLaunchWith(t, f, self, claudeBin, newID, nil)
}

func threadLaunchWith(t thread, f threadFacts, self, claudeBin string, newID func() string, env threadLaunchEnv) (workspaceLaunch, threadSessionRef, string) {
	if f.Running != nil {
		return workspaceLaunch{}, threadSessionRef{}, fmt.Sprintf("already running in %s (pid %d)", f.Running.Cwd, f.Running.PID)
	}
	id := newID()
	cwd, acct, tap := threadFreshTarget(t, env, id)
	if cwd == "" {
		return workspaceLaunch{}, threadSessionRef{}, "no working directory recorded"
	}
	bin := paneBin(claudeBin)
	w := newWorkspaceScript(cwd, self, id, bin, acct, tap, true, threadReassessPrompt(t))
	if !pinsSessionID(bin) {
		// A wrapper drops --session-id, so the id we minted is not the real one.
		return w, threadSessionRef{}, ""
	}
	return w, threadSessionRef{Agent: "claude", ID: id}, ""
}

// threadFreshTarget is where a fresh session on t starts and as which account:
// the first existing worktree claim, else the newest session's last cwd; the
// newest session's account when it was Claude, else the default for id.
func threadFreshTarget(t thread, env threadLaunchEnv, id string) (cwd, acct, tap string) {
	sessCwd := ""
	claude := len(t.Meta.Sessions) > 0 && t.Meta.Sessions[0].Agent != "amp"
	if env != nil {
		if len(t.Meta.Sessions) > 0 {
			newest := t.Meta.Sessions[0]
			var a, tp string
			a, tp, sessCwd = env(newest.Agent, newest.ID)
			if claude {
				// The fresh session runs as the account the thread last used.
				acct, tap = a, tp
			}
		}
		if !claude {
			acct, tap, _ = env("claude", id)
		}
	}
	for _, c := range t.Meta.Claims {
		if c.Kind == "worktree" && c.Path != "" && isDir(c.Path) {
			return c.Path, acct, tap
		}
	}
	if sessCwd != "" && isDir(sessCwd) {
		return sessCwd, acct, tap
	}
	return "", acct, tap
}

// threadResumeWith reopens the thread's newest session (`r`): Claude resumes
// it, Amp continues it, where it last worked, else in the first existing claim.
func threadResumeWith(t thread, f threadFacts, self, claudeBin string, env threadLaunchEnv) (workspaceLaunch, threadSessionRef, string) {
	if f.Running != nil {
		return workspaceLaunch{}, threadSessionRef{}, fmt.Sprintf("already running in %s (pid %d)", f.Running.Cwd, f.Running.PID)
	}
	if len(t.Meta.Sessions) == 0 {
		return workspaceLaunch{}, threadSessionRef{}, "no session to reopen; ⏎ starts a fresh one"
	}
	newest := t.Meta.Sessions[0]
	acct, tap, cwd := "", "", ""
	if env != nil {
		acct, tap, cwd = env(newest.Agent, newest.ID)
	}
	if cwd != "" && !isDir(cwd) {
		cwd = ""
	}
	if cwd == "" {
		for _, c := range t.Meta.Claims {
			if c.Kind == "worktree" && c.Path != "" && isDir(c.Path) {
				cwd = c.Path
				break
			}
		}
	}
	if cwd == "" {
		return workspaceLaunch{}, threadSessionRef{}, "no working directory recorded"
	}
	if newest.Agent == "amp" {
		bin, err := exec.LookPath("amp")
		if err != nil {
			bin = "amp"
		}
		return ampWorkspaceScript(cwd, newest.ID, self, bin, true), newest, ""
	}
	return workspaceScript(cwd, newest.ID, self, paneBin(claudeBin), acct, tap), newest, ""
}

// threadsStart is startWorkspace through the seams. It returns only on failure.
// before runs after the panes exist and before pane A replaces the process,
// so nothing is recorded for a launch that failed.
func threadsStart(w workspaceLaunch, before func() error) error {
	if err := threadsOsa(w.Script); err != nil {
		return err
	}
	if before != nil {
		if err := before(); err != nil {
			return err
		}
	}
	if !w.InPlace {
		os.Exit(0)
	}
	if err := threadsExec(w.Agent); err != nil {
		fmt.Fprintln(os.Stderr, "entire-tail: start the agent with: "+w.Agent)
		return err
	}
	return nil
}

// threadKnowledge reads a thread's knowledge.md, live or closed.
func threadKnowledge(root string) func(string) string {
	return func(slug string) string {
		for _, d := range []string{filepath.Join(root, slug), filepath.Join(root, threadsClosedDir, slug)} {
			if b, err := os.ReadFile(filepath.Join(d, "knowledge.md")); err == nil {
				return string(b)
			}
		}
		return ""
	}
}

// applyThreadsAction performs a non-open, non-quit action and reports whether
// the threads need reloading from disk.
func applyThreadsAction(root string, a threadsAction, now time.Time) (note string, reload bool) {
	var err error
	switch a.Kind {
	case "park":
		err = parkThread(root, a.Slug, a.ParkKind, a.Reason, now)
	case "unpark":
		err = setThreadStatus(root, a.Slug, threadOpen, "", now)
	case "close":
		err = setThreadStatus(root, a.Slug, threadClosed, "", now)
	default:
		return "", false
	}
	if err != nil {
		return err.Error(), false
	}
	return "", true
}

func runThreadsTUI(cfg Config) error {
	home := homeDir()
	root := threadsRoot(os.Getenv)
	theme, err := loadTheme(cfg.Theme, cfg.GlowStyle)
	if err != nil {
		return err
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	saved, ok := setRawTimed(tty)
	if !ok {
		return errors.New("cannot set terminal mode")
	}
	restore := func() {
		io.WriteString(tty, "\x1b[?25h\x1b[?1049l")
		restoreCbreak(tty, saved)
	}
	defer restore()
	io.WriteString(tty, "\x1b[?1049h\x1b[?25l")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type facts struct {
		gen int
		m   map[string]threadFacts
	}
	factsCh := make(chan facts, 1)
	gen := 0
	loadFacts := func(ts []thread) {
		gen++
		g := gen
		go func() {
			m := checkThreads(ctx, ts, defaultThreadCheckDeps(home))
			select {
			case factsCh <- facts{g, m}:
			case <-ctx.Done():
			}
		}()
	}

	wakeThreads(root, time.Now())
	ui := threadsUI{Threads: loadThreads(root), Mode: "list", Today: time.Now().Format("2006-01-02")}
	loadFacts(ui.Threads)
	knowledge := threadKnowledge(root)
	var render func(string) (string, error)
	renderW := -1
	frame := func() string {
		w, h := termSize(tty)
		ui.Width, ui.Height = w-1, h
		if w != renderW {
			renderW = w
			if r, e := newGlamour(theme.StyleJSON, max(20, w-2)); e == nil {
				render = r
			}
		}
		ui = clampThreadsScroll(ui, render, knowledge)
		return "\x1b[H\x1b[2J" + strings.ReplaceAll(renderThreads(ui, render, knowledge), "\n", "\r\n")
	}

	buf := make([]byte, 128)
	for {
		select {
		case f := <-factsCh:
			if f.gen == gen {
				ui.Facts = f.m
			}
		default:
		}
		ui.Today = time.Now().Format("2006-01-02")
		io.WriteString(tty, frame())
		n, e := tty.Read(buf)
		if e != nil && !(n == 0 && errors.Is(e, io.EOF)) {
			return e
		}
		if n == 0 {
			continue
		}
		var chunks [][]byte
		if buf[0] == 0x1b {
			chunks = append(chunks, append([]byte(nil), buf[:n]...))
		} else {
			for _, r := range string(buf[:n]) {
				chunks = append(chunks, []byte(string(r)))
			}
		}
		for _, chunk := range chunks {
			key, r := decodeKey(chunk)
			var a threadsAction
			ui, a = updateThreads(ui, key, r)
			ui = clampThreadsScroll(ui, render, knowledge)
			switch a.Kind {
			case "quit":
				return nil
			case "park", "unpark", "close":
				note, reload := applyThreadsAction(root, a, time.Now())
				if note != "" {
					ui.Note = note
				}
				if reload {
					ui.Threads = loadThreads(root)
					ui.Facts = nil
					loadFacts(ui.Threads)
				}
			case "open", "reopen":
				ui.Note = openThread(cfg, home, root, ui, a.Slug, a.Kind == "reopen", tty, saved, restore)
				io.WriteString(tty, "\x1b[?1049h\x1b[?25l")
			case "openall":
				ui.Note = openAllThreads(cfg, home, root, ui, a.Slugs)
				ui.Threads = loadThreads(root)
				ui.Facts = nil
				loadFacts(ui.Threads)
			}
		}
	}
}

// threadsLiveNow reads the running sessions; a seam for tests.
var threadsLiveNow = func(home string) []liveSession { return defaultThreadCheckDeps(home).Live() }

// runningFor finds a live session that belongs to the thread.
func runningFor(t thread, live []liveSession) *liveSession {
	for _, s := range t.Meta.Sessions {
		for _, l := range live {
			if l.SessionID == s.ID {
				l := l
				return &l
			}
		}
	}
	return nil
}

// openThreadWith launches t's workspace: a fresh session, or with resume its
// newest one. The running-session guards use ONLY the live list read now,
// never the background facts: those are a snapshot from when the screen last
// loaded, and a tab closed since then would still read as running (it did:
// `O`, close a tab, `⏎` refused). One of the thread's own sessions running
// anywhere, or any agent running inside one of its worktrees, refuses. A
// session is registered only after the panes exist. It returns a note on
// refusal or failure and does not return at all on success.
func openThreadWith(root, home string, t thread, resume bool, live []liveSession, self, bin string, env threadLaunchEnv, newID func() string) string {
	f := threadFacts{Running: runningFor(t, live)}
	if f.Running == nil {
		if l, wt := agentInWorktree(t, live, isDir); l != nil {
			return fmt.Sprintf("an agent is already running in %s (pid %d)", tildify(wt, home), l.PID)
		}
	}
	var w workspaceLaunch
	var ref threadSessionRef
	var refuse string
	if resume {
		w, ref, refuse = threadResumeWith(t, f, self, bin, env)
	} else {
		w, ref, refuse = threadLaunchWith(t, f, self, bin, newID, env)
	}
	if refuse != "" {
		return refuse
	}
	slug := threadSlugOf(t)
	err := threadsStart(w, func() error {
		if ref.ID == "" {
			return nil
		}
		err := registerSession(root, slug, ref.Agent, ref.ID)
		if err != nil && resume {
			// The panes exist and the session is already on the thread;
			// recording it again is bookkeeping, so say so and carry on.
			threadsNote("entire-tail: opened the thread, but could not record the session against it: " + err.Error())
			return nil
		}
		return err
	})
	if err != nil {
		return err.Error()
	}
	return ""
}

// openThread finds the thread and opens it. It leaves the alt screen first and
// puts the terminal back when the launch fails.
func openThread(cfg Config, home, root string, ui threadsUI, slug string, resume bool, tty *os.File, saved string, restore func()) string {
	var t thread
	found := false
	for _, c := range ui.Threads {
		if threadSlugOf(c) == slug {
			t, found = c, true
		}
	}
	if !found {
		return "thread not found"
	}
	if !threadsItermOK() {
		return "open threads from an unsplit iTerm tab to get a workspace"
	}
	var warn bytes.Buffer // stderr is invisible under the alt screen
	bin := resolveClaudeBin(cfg, exec.LookPath, &warn)
	env := func(agent, id string) (string, string, string) {
		if agent == "amp" {
			return "", "", ""
		}
		p := localPathForID(home, id)
		prof := profileByName(home, profileForPath(home, p))
		if prof.Name == personalProfile && !personalTokenAvailable() {
			fmt.Fprintf(&warn, "no %q token in the Keychain; the personal session may open unauthenticated. ", personalKeychainService)
		}
		cwd := ""
		if p != "" {
			cwd = workspaceCwd(p)
		}
		return accountEnvPrefix(prof), tapEnvPrefix(tapBaseURL(home)), cwd
	}
	restore()
	note := openThreadWith(root, home, t, resume, threadsLiveNow(home), selfPath(), bin, env, newSessionID)
	_, _ = setRawTimed(tty)
	return strings.TrimSpace(warn.String() + " " + note)
}

// openAllPick is one thread `O` will open: its tab and the session to record.
type openAllPick struct {
	Slug string
	Tab  restoreTab
	Ref  threadSessionRef
}

// planOpenAll decides, for each thread, a FRESH Claude session primed with
// threadReassessPrompt, never a resume: the point of opening everything at once
// is a clean look at where each thread really is. A thread with one of its
// sessions running, or any agent inside one of its worktrees, is skipped (the
// same guards as ⏎), and so is one with no directory to start in. Pure apart
// from directory checks.
func planOpenAll(ts []thread, live []liveSession, self, bin string, env threadLaunchEnv, newID func() string) (picks []openAllPick, skipped []string) {
	for _, t := range ts {
		slug := threadSlugOf(t)
		name := threadDisplayName(t)
		// Live only, never the facts snapshot; see openThreadWith.
		if runningFor(t, live) != nil {
			skipped = append(skipped, name+" (already running)")
			continue
		}
		if l, _ := agentInWorktree(t, live, isDir); l != nil {
			skipped = append(skipped, name+" (an agent is in its worktree)")
			continue
		}
		id := newID()
		cwd, acct, tap := threadFreshTarget(t, env, id)
		if cwd == "" {
			skipped = append(skipped, name+" (no working directory recorded)")
			continue
		}
		w := newWorkspaceScript(cwd, self, id, bin, acct, tap, false, threadReassessPrompt(t))
		p := openAllPick{Slug: slug, Tab: restoreTab{Label: name, Agent: w.Agent, Tail: w.Tail, Shell: w.Shell}}
		if pinsSessionID(bin) {
			p.Ref = threadSessionRef{Agent: "claude", ID: id}
		}
		picks = append(picks, p)
	}
	return picks, skipped
}

// openAllScript adds one tab per pick to the CURRENT window, each with
// splitScript's layout (B right of A at full height, C under A), then selects
// the tab the triage screen runs in again, so the note it prints is in view.
func openAllScript(sh string, tabs []restoreTab) string {
	cmd := func(s string) string { return asEscape(paneCommand(sh, s)) }
	var b strings.Builder
	b.WriteString("tell application \"iTerm2\"\n\tset w to current window\n\tset home to current tab of w\n")
	for _, t := range tabs {
		b.WriteString("\ttell w to set nt to (create tab with default profile command \"" + cmd(t.Agent) + "\")\n")
		b.WriteString("\ttell current session of nt\n")
		b.WriteString("\t\tsplit vertically with default profile command \"" + cmd(t.Tail) + "\"\n")
		b.WriteString("\t\tsplit horizontally with default profile command \"" + cmd(t.Shell) + "\"\n")
		b.WriteString("\tend tell\n")
	}
	b.WriteString("\tselect home\nend tell")
	return b.String()
}

// openAllWith opens picks as new tabs of the current window and records each
// session on its thread once the tabs exist. It returns the note for the footer.
func openAllWith(root string, picks []openAllPick, skipped []string) string {
	var note string
	if len(picks) > 0 {
		var tabs []restoreTab
		for _, p := range picks {
			tabs = append(tabs, p.Tab)
		}
		if err := threadsOsa(openAllScript(paneShell(), tabs)); err != nil {
			return "could not open the tabs: " + err.Error()
		}
		var failed []string
		for _, p := range picks {
			if p.Ref.ID == "" {
				continue
			}
			if err := registerSession(root, p.Slug, p.Ref.Agent, p.Ref.ID); err != nil {
				failed = append(failed, p.Slug)
			}
		}
		note = "opened " + plural(len(picks), "thread") + " in new tabs"
		if len(failed) > 0 {
			note += "; could not record the session on " + strings.Join(failed, ", ")
		}
	} else {
		note = "opened nothing"
	}
	if len(skipped) > 0 {
		note += " · skipped " + strings.Join(skipped, ", ")
	}
	return note
}

// openAllThreads opens every thread named in slugs. Unlike ⏎ it needs no
// unsplit tab: it makes its own window and the triage screen stays put.
func openAllThreads(cfg Config, home, root string, ui threadsUI, slugs []string) string {
	if !itermAvailable() {
		return "open all needs iTerm"
	}
	want := map[string]bool{}
	for _, s := range slugs {
		want[s] = true
	}
	var ts []thread
	for _, t := range ui.Threads {
		if want[threadSlugOf(t)] {
			ts = append(ts, t)
		}
	}
	var warn bytes.Buffer
	bin := paneBin(resolveClaudeBin(cfg, exec.LookPath, &warn))
	env := func(agent, id string) (string, string, string) {
		if agent == "amp" {
			return "", "", ""
		}
		p := localPathForID(home, id)
		prof := profileByName(home, profileForPath(home, p))
		cwd := ""
		if p != "" {
			cwd = workspaceCwd(p)
		}
		return accountEnvPrefix(prof), tapEnvPrefix(tapBaseURL(home)), cwd
	}
	picks, skipped := planOpenAll(ts, threadsLiveNow(home), selfPath(), bin, env, newSessionID)
	return strings.TrimSpace(warn.String() + " " + openAllWith(root, picks, skipped))
}
