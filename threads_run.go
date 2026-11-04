package main

import (
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
// start a fresh one primed with the thread's files. Pure, no IO beyond a
// directory check on worktree claims; refuse is non-empty when it must not open.
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
	cwd := ""
	for _, c := range t.Meta.Claims {
		if c.Kind == "worktree" && c.Path != "" && isDir(c.Path) {
			cwd = c.Path
			break
		}
	}
	if cwd == "" {
		cwd = sessCwd
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
	prompt := "Read " + filepath.Join(t.Dir, "thread.md") + " and knowledge.md, then continue this thread."
	return newWorkspaceScript(cwd, self, id, paneBin(claudeBin), acct, tap, true, prompt), threadSessionRef{Agent: "claude", ID: id}, ""
}

// threadsStart is startWorkspace through the seams. It returns only on failure.
func threadsStart(w workspaceLaunch) error {
	if err := threadsOsa(w.Script); err != nil {
		return err
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

// openThread launches the workspace for slug. It returns the note to show when
// it could not, and does not return at all on success (pane A's agent replaces
// this process).
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
	bin := resolveClaudeBin(cfg, exec.LookPath, os.Stderr)
	env := func(agent, id string) (string, string, string) {
		if agent == "amp" {
			return "", "", ""
		}
		p := localPathForID(home, id)
		if p == "" {
			prof := profileByName(home, "")
			return accountEnvPrefix(prof), tapEnvPrefix(tapBaseURL(home)), ""
		}
		prof := profileByName(home, profileForPath(home, p))
		warnMissingToken(prof)
		return accountEnvPrefix(prof), tapEnvPrefix(tapBaseURL(home)), workspaceCwd(p)
	}
	w, ref, refuse := threadLaunchWith(t, ui.Facts[slug], selfPath(), bin, newSessionID, env)
	if refuse != "" {
		return refuse
	}
	if err := registerSession(root, slug, ref.Agent, ref.ID, time.Now()); err != nil {
		return err.Error()
	}
	restore()
	if err := threadsStart(w); err != nil {
		_, _ = setRawTimed(tty)
		return err.Error()
	}
	return ""
}
