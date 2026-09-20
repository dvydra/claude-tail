package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// hunk.go is the `h` key: hand the whole pane to hunk (hunk.dev) for a review
// of what the agent has changed, then take it back when you quit.
//
// It is the same hand-off the `?` and `→` overlays use — the keyboard goroutine
// signals overlayCh and PARKS, so there is exactly one tty reader — with one
// difference that shapes everything here: the thing that owns the screen is a
// CHILD PROCESS, not our own alt-screen drawing. So the transcript's open line
// is settled, the status bar gives its row back, and hunk is handed our tty
// wholesale. We deliberately do NOT touch the terminal modes around it: hunk
// sets up its own raw mode and restores what it inherited (our cbreak), which
// is exactly the state the keyboard reader needs back; a restore-to-cooked-then-
// re-cbreak dance would add two failure paths to buy nothing.
//
// The second half is the hand-off to the agent. Hunk's own docs are explicit
// that there is no session id to pass, no env var and no handshake file: the TUI
// registers with a local loopback daemon when it starts, and the agent finds it
// itself with `hunk session get --repo <path>`. What the docs DO prescribe is a
// prompt (hunkPrompt). So once the daemon confirms the session is up, we type
// that prompt into the claude sitting in this iTerm tab, and the agent takes it
// from there. Off iTerm — or when the tab holds more than one claude and we
// cannot tell which is yours — the prompt goes to the clipboard instead, which
// is a paste rather than a failure.

// hunkPrompt is the hand-off line hunk's agent-workflow docs tell you to give
// the agent. It's a quotation; `TestHunkPromptMatchesTheDocumentedOne` pins it
// so it doesn't drift into a phrasing of our own.
const hunkPrompt = "Load the Hunk skill and use it for this review. Run `hunk skill path` to get the skill path."

// hunkReadyTimeout bounds the wait for the TUI to register with the daemon
// before the prompt is sent. Registration is near-instant in practice; this is
// only there so a hunk that fails to start never leaves us telling the agent
// about a session that doesn't exist.
const hunkReadyTimeout = 5 * time.Second

// hunkReadyPoll is how often the daemon is asked during that window.
const hunkReadyPoll = 200 * time.Millisecond

// hunkPlan is what pressing `h` is about to do, decided BEFORE the screen is
// handed over. Everything that can be known in advance is resolved here so the
// live loop's overlay case stays a switch over intent, and so the two "nothing
// will happen" cases never blank the tail: a suspend/resume for a hunk that was
// never going to run reads as a broken key rather than a missing tool.
type hunkPlan struct {
	Bin  string // the hunk binary; empty means don't hand the screen over at all
	Dir  string // the session's cwd — what gets reviewed
	Pane string // iTerm session uuid to type the prompt into; "" → clipboard
	Msg  string // the status-bar line once we're back (or why we never left)
}

// planHunk decides what the key does. dirOK is whether Dir still exists: the
// normal end state of a worktree is that its directory has been deleted, and
// `hunk diff` in a missing cwd would fail inside the alt-screen, which is the
// worst place to read an error.
func planHunk(bin, dir string, dirOK bool, pane string) hunkPlan {
	switch {
	case bin == "":
		return hunkPlan{Msg: "hunk not installed — see hunk.dev"}
	case !dirOK:
		return hunkPlan{Msg: "hunk: session folder is gone"}
	case pane == "":
		return hunkPlan{Bin: bin, Dir: dir, Msg: "hunk ended — prompt for claude copied to the clipboard"}
	}
	return hunkPlan{Bin: bin, Dir: dir, Pane: pane, Msg: "hunk ended — claude was told to load the skill"}
}

// hunkBin finds the review binary: PATH first, then ~/.hunk/bin, which is where
// hunk.dev's install.sh puts a standalone binary and is NOT added to PATH. A
// viewer that only consulted PATH would report "not installed" on a machine that
// has it — this one does. Empty when there's nothing to run.
//
// `look` is exec.LookPath, injected so the test doesn't depend on whether the
// developer happens to have hunk installed.
func hunkBin(home string, look func(string) (string, error)) string {
	if p, err := look("hunk"); err == nil {
		return p
	}
	p := filepath.Join(home, ".hunk", "bin", "hunk")
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

// hunkClaudePane picks the iTerm session to type the prompt into: a claude in
// OUR tab, and preferably the one writing the transcript we are tailing.
//
// The tab is the whole horizon, for the reason nearby.go spells out — a claude
// in another tab is someone else's work, and typing a review prompt into it is
// worse than doing nothing. Within the tab, matching the tailed transcript comes
// first because a tab can hold two agents; falling back to "the only one here"
// is adopt.go's exactly-one rule, and anything still ambiguous gives back "",
// which routes the prompt to the clipboard instead of guessing.
//
// sessionOf is resolveClaudeSession in a real run, injected so the placement
// logic is testable without processes.
func hunkClaudePane(ownTab, cur string, procs []claudeProc, sessionOf func(claudeProc) string) string {
	if ownTab == "" {
		return ""
	}
	var inTab []claudeProc
	for _, p := range procs {
		if itermTab(p.itermID) == ownTab {
			inTab = append(inTab, p)
		}
	}
	// One claude in the tab is the answer without asking which transcript it
	// writes — and that question costs an lsof and a ps per process, which is
	// the whole reason to check the cheap case first.
	if len(inTab) == 1 {
		return paneUUIDFromSessionID(inTab[0].itermID)
	}
	for _, p := range inTab {
		if cur != "" && sessionOf(p) == cur {
			return paneUUIDFromSessionID(p.itermID)
		}
	}
	return ""
}

// hunkOverlay is the live loop's entire `h` case: work out what can happen,
// hand the screen over if anything can, and give back the line for the status
// bar. Split out so main.go's overlay switch stays a switch over intent.
func hunkOverlay(tty *os.File, home, cur, dir string) string {
	p := planHunk(hunkBin(home, exec.LookPath), dir, isDir(dir), hunkPaneFor(home, cur))
	runHunk(tty, p)
	return p.Msg
}

// hunkPaneFor resolves the claude pane to prompt, or "" when there isn't one we
// can name with confidence. pgrep/lsof stay optional here as they are
// everywhere else — without them the prompt simply goes to the clipboard.
func hunkPaneFor(home, cur string) string {
	if !pickerToolsAvailable() {
		return ""
	}
	return hunkClaudePane(itermTab(os.Getenv("ITERM_SESSION_ID")), cur, claudeProcs(), func(p claudeProc) string {
		return resolveClaudeSession(home, lsofCwd(p.pid), psCommand(p.pid))
	})
}

// hunkNotifyScript types one line into a pane addressed by its session id.
//
// By id, never by index: panelink.go learned the hard way that tab and window
// indexes shift under you, and a mistyped prompt would land in someone's shell.
// The whole thing is wrapped in `try` because the pane may have closed while
// hunk was up — the review is already on screen by then, and a failed nicety
// must not surface as an error.
func hunkNotifyScript(pane, prompt string) string {
	var b strings.Builder
	b.WriteString("tell application \"iTerm2\"\n")
	b.WriteString("\trepeat with w in windows\n")
	b.WriteString("\t\trepeat with t in tabs of w\n")
	b.WriteString("\t\t\trepeat with s in sessions of t\n")
	b.WriteString("\t\t\t\tif (id of s) is \"" + asEscape(pane) + "\" then\n")
	b.WriteString("\t\t\t\t\ttry\n")
	b.WriteString("\t\t\t\t\t\ttell s to write text \"" + asEscape(prompt) + "\"\n")
	b.WriteString("\t\t\t\t\tend try\n")
	b.WriteString("\t\t\t\tend if\n")
	b.WriteString("\t\t\tend repeat\n")
	b.WriteString("\t\tend repeat\n")
	b.WriteString("\tend repeat\n")
	b.WriteString("end tell\n")
	return b.String()
}

// runHunk hands the tty to hunk and blocks until it exits. The IO half: the
// decisions were all made by planHunk.
//
// The prompt is sent from a goroutine rather than before the spawn, because the
// agent's first move is `hunk session get --repo .` and the daemon only knows
// about the session once the TUI has started. It is best-effort throughout — a
// prompt that never lands leaves a perfectly good review on screen.
func runHunk(tty *os.File, p hunkPlan) {
	if p.Bin == "" {
		return
	}
	cmd := exec.Command(p.Bin, "diff")
	cmd.Dir = p.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if err := cmd.Start(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !hunkWaitReady(p.Bin, p.Dir) {
			return
		}
		if p.Pane == "" {
			_ = clipboardWrite(hunkPrompt, tty)
			return
		}
		_ = osaRun(hunkNotifyScript(p.Pane, hunkPrompt))
	}()
	_ = cmd.Wait()
	<-done
}

// hunkWaitReady polls the loopback daemon until it reports a session for dir.
// `session get` is the documented way to ask; its exit status is the answer, so
// nothing here parses output.
func hunkWaitReady(bin, dir string) bool {
	deadline := time.Now().Add(hunkReadyTimeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command(bin, "session", "get", "--repo", dir)
		if cmd.Run() == nil {
			return true
		}
		time.Sleep(hunkReadyPoll)
	}
	return false
}
