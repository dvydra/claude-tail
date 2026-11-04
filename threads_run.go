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
)

// threadLaunchEnv supplies what needs IO to build a launch: the account and
// tap env prefixes for a Claude session and the cwd that session last worked in.
type threadLaunchEnv func(agent, id string) (acctEnv, tapEnv, cwd string)

// threadLaunch decides how to open a thread: resume its newest session, or
// start a fresh one primed with the thread's files. Pure, no IO beyond
// directory checks on worktree claims and the session's cwd; refuse is
// non-empty when it must not open.
func threadLaunch(t thread, f threadFacts, self, claudeBin string, newID func() string) (workspaceLaunch, threadSessionRef, string) {
	return threadLaunchWith(t, f, self, claudeBin, newID, nil)
}

func threadLaunchWith(t thread, f threadFacts, self, claudeBin string, newID func() string, env threadLaunchEnv) (workspaceLaunch, threadSessionRef, string) {
	if f.Running != nil {
		return workspaceLaunch{}, threadSessionRef{}, fmt.Sprintf("already running in %s (pid %d)", f.Running.Cwd, f.Running.PID)
	}
	var newest *threadSessionRef
	if len(t.Meta.Sessions) > 0 {
		newest = &t.Meta.Sessions[0]
	}
	acct, tap, sessCwd := "", "", ""
	if newest != nil && env != nil {
		acct, tap, sessCwd = env(newest.Agent, newest.ID)
	}
	claimCwd := ""
	for _, c := range t.Meta.Claims {
		if c.Kind == "worktree" && c.Path != "" && isDir(c.Path) {
			claimCwd = c.Path
			break
		}
	}
	if sessCwd != "" && !isDir(sessCwd) {
		sessCwd = ""
	}
	// A resume goes back where that session last worked; the first claim may be
	// a different worktree of the thread. A fresh start goes to the claim.
	cwd := firstNonEmpty(claimCwd, sessCwd)
	if newest != nil {
		cwd = firstNonEmpty(sessCwd, claimCwd)
	}
	if cwd == "" {
		return workspaceLaunch{}, threadSessionRef{}, "no working directory recorded"
	}
	switch {
	case newest != nil && newest.Agent == "amp":
		bin, err := exec.LookPath("amp")
		if err != nil {
			bin = "amp"
		}
		return ampWorkspaceScript(cwd, newest.ID, self, bin, true), *newest, ""
	case newest != nil:
		return workspaceScript(cwd, newest.ID, self, paneBin(claudeBin), acct, tap), *newest, ""
	}
	id := newID()
	if env != nil {
		acct, tap, _ = env("claude", id)
	}
	prompt := "Read " + filepath.Join(t.Dir, "thread.md") + " and " + filepath.Join(t.Dir, "knowledge.md") + ", then continue this thread."
	w := newWorkspaceScript(cwd, self, id, paneBin(claudeBin), acct, tap, true, prompt)
	if !pinsSessionID(paneBin(claudeBin)) {
		// A wrapper drops --session-id, so the id we minted is not the real one.
		return w, threadSessionRef{}, ""
	}
	return w, threadSessionRef{Agent: "claude", ID: id}, ""
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
		err = setThreadStatus(root, a.Slug, threadParked, a.Reason, now)
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
			case "open":
				ui.Note = openThread(cfg, home, root, ui, a.Slug, tty, saved, restore)
				io.WriteString(tty, "\x1b[?1049h\x1b[?25l")
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

// openThreadWith launches t's workspace. The running-session guard uses the
// live list read now, whether or not the background facts have arrived. A
// session is registered only after the panes exist. It returns a note on
// refusal or failure and does not return at all on success.
func openThreadWith(root string, t thread, f threadFacts, live []liveSession, self, bin string, env threadLaunchEnv, newID func() string) string {
	if f.Running == nil {
		f.Running = runningFor(t, live)
	}
	w, ref, refuse := threadLaunchWith(t, f, self, bin, newID, env)
	if refuse != "" {
		return refuse
	}
	slug := threadSlugOf(t)
	err := threadsStart(w, func() error {
		if ref.ID == "" {
			return nil
		}
		return registerSession(root, slug, ref.Agent, ref.ID, time.Now())
	})
	if err != nil {
		return err.Error()
	}
	return ""
}

// openThread finds the thread and opens it. It leaves the alt screen first and
// puts the terminal back when the launch fails.
func openThread(cfg Config, home, root string, ui threadsUI, slug string, tty *os.File, saved string, restore func()) string {
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
	note := openThreadWith(root, t, ui.Facts[slug], threadsLiveNow(home), selfPath(), bin, env, newSessionID)
	_, _ = setRawTimed(tty)
	return strings.TrimSpace(warn.String() + " " + note)
}
