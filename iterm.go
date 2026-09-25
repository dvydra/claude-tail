package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// newSessionID mints a random v4 UUID for the launcher's `--session-id`. crypto/rand
// makes a collision with an existing session file effectively impossible.
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// iterm.go drives iTerm2 via AppleScript (osascript, always present on macOS) to
// lay out the workspace: pick a session in the tree, and the current window
// becomes three panes — the agent (`<bin> --resume`, see resolveClaudeBin),
// entire-tail following it, and a shell — all in the picked session's folder.
// macOS + iTerm2 only; callers gate with itermAvailable.

func itermAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if os.Getenv("TERM_PROGRAM") == "iTerm.app" {
		return true
	}
	_, err := os.Stat("/Applications/iTerm.app")
	return err == nil
}

// itermSinglePane reports whether the current iTerm tab has exactly one pane, so
// we can lay out the workspace here without disturbing an existing split. On any
// error (not iTerm, no window) it returns false — the caller then just tails.
func itermSinglePane() bool {
	out, err := exec.Command("osascript", "-e",
		`tell application "iTerm2" to count of sessions of current tab of current window`).Output()
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return err == nil && n == 1
}

// shQuote single-quotes a string for safe inclusion in a POSIX shell command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// asEscape escapes a string for inclusion inside an AppleScript "..." literal.
func asEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// selfPath is the absolute path to this binary, so the panes run the exact same
// entire-tail rather than relying on it being on PATH.
func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "entire-tail"
	}
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		return rp
	}
	return p
}

func osaRun(script string) error {
	cmd := exec.Command("osascript", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// launchWorkspace opens the three-pane dev layout in the current window:
//
//	A │ B    A = <bin> (the agent), B = entire-tail (full-height right column),
//	C │ B    C = a plain shell.
//
// All three cd into cwd. B follows the resumed session by id (--follow-session),
// so a later worktree fork is followed too.
//
// prof is the account that owns the session: resuming a personal session with
// the work account's credentials would not be a resume at all, so A carries that
// account's env (see accountEnvPrefix). B needs none — it globs every account's
// projects root.
//
// It returns only on failure: on success this process BECOMES pane A's agent.
func launchWorkspace(cwd, resumeID, bin string, prof claudeProfile) error {
	return startWorkspace(workspaceScript(cwd, resumeID, selfPath(), paneBin(bin), accountEnvPrefix(prof), tapEnvPrefix(tapBaseURL(homeDir()))))
}

// workspaceLaunch is a workspace split by how each pane gets its command.
// Script is the AppleScript that creates the other panes, each STARTED with its
// command (`split … command`), so no pane is ever typed into. Agent is pane A's
// shell command line; when InPlace, pane A is the pane we're running in, and
// startWorkspace execs Agent here once Script has run.
//
// The panes used to be driven by `write text`, which types a command as if at
// the keyboard. That put every command on screen (A's twice: echoed while we
// still held the tty, then again at the prompt that ran it) and queued A's line
// in the tty's input buffer, where anything typed before the agent took over
// landed behind it.
type workspaceLaunch struct {
	Script      string
	Agent       string
	Tail, Shell string // panes B and C's command lines, as started inside Script
	InPlace     bool
}

// startWorkspace runs a workspaceLaunch. It returns only on failure.
func startWorkspace(w workspaceLaunch) error {
	if err := osaRun(w.Script); err != nil {
		return err
	}
	if !w.InPlace {
		os.Exit(0)
	}
	if err := execAgent(w.Agent); err != nil {
		// The other panes are already up, so name what pane A should have run.
		fmt.Fprintln(os.Stderr, "entire-tail: start the agent with: "+w.Agent)
		return err
	}
	return nil
}

// execAgent replaces this process with pane A's agent, through /bin/sh because
// the line carries shell syntax: the `cd`, and accountEnvPrefix's Keychain
// command substitution, which has to stay the shell's (see accountEnvPrefix for
// why the token never passes through us). When the agent exits, the pane is back
// at the shell that started entire-tail — in the directory it started in, since
// the `cd` happened in the child. syscall.Exec returns only on error.
func execAgent(cmdline string) error {
	return syscall.Exec("/bin/sh", []string{"sh", "-c", cmdline}, os.Environ())
}

// paneShell is the shell a started pane runs its command under and becomes
// afterwards.
func paneShell() string {
	if sh := os.Getenv("SHELL"); filepath.IsAbs(sh) {
		return sh
	}
	return "/bin/zsh"
}

// paneCommand wraps a shell command line for iTerm's `command`: run it under a
// login shell, then replace that with an interactive one, so the pane is still a
// shell after the tail quits or the agent exits (a started command's pane
// otherwise closes with it). iTerm splits `command` with shell quoting rules,
// `'\”` included — verified live on iTerm 3.6.11.
func paneCommand(shell, cmd string) string {
	return shQuote(shell) + " -lc " + shQuote(cmd+"; exec "+shQuote(shell)+" -l")
}

// paneBin resolves the agent binary to an absolute path while we still have the
// PATH of the interactive shell that started us. A started pane's login shell
// skips the rc files, so a PATH entry added there would be missing. Unresolvable
// → as given, so the pane reports "not found" rather than us guessing.
func paneBin(bin string) string {
	if strings.ContainsRune(bin, '/') {
		return bin
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	return bin
}

// tapEnvPrefix returns the shell assignment that routes a launched agent's API
// traffic through the tap daemon, or "" when no daemon answers.
//
// This is the fail-open half of the tap: the decision is made once, at launch,
// from a live health check (tapBaseURL). A session is only ever pointed at the
// daemon if the daemon is actually there — so a machine that never ran
// `entire-tail tap start`, or ran it and stopped it, launches agents exactly as
// before. The remaining exposure is deliberate and documented: a daemon that
// dies MID-session takes that session's API endpoint with it, which is why the
// LaunchAgent (tap install) sets KeepAlive.
// ENABLE_TOOL_SEARCH is NOT optional here, and this cost a broken session before
// it was understood. Claude Code defers MCP tool schemas behind tool_reference
// blocks ("tool search"), but it disables that the moment ANTHROPIC_BASE_URL
// points anywhere that isn't a first-party Anthropic host — it can't know a
// gateway forwards those blocks. Its own debug log spells out both the behaviour
// and the remedy:
//
//	[ToolSearch:optimistic] disabled: ANTHROPIC_BASE_URL=… is not a first-party
//	Anthropic host. Set ENABLE_TOOL_SEARCH=true (…) if your proxy forwards
//	tool_reference blocks.
//
// With tool search off, every tool schema ships inline. On a machine with a large
// MCP fleet that is the difference between a normal prompt and "Prompt is too
// long" on the second turn of a fresh session. Our proxy is a byte-transparent
// pass-through, so it does forward tool_reference blocks and the opt-in is
// correct — it restores the first-party default (verified: the decision line
// reads `mode=tst, ENABLE_TOOL_SEARCH=true, result=true`, same as direct).
const tapToolSearchEnv = "ENABLE_TOOL_SEARCH=true"

func tapEnvPrefix(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	return "ANTHROPIC_BASE_URL=" + shQuote(baseURL) + " " + tapToolSearchEnv + " "
}

func homeDir() string { return firstNonEmpty(os.Getenv("HOME"), mustHome()) }

// launchNewWorkspace opens the 3-pane workspace for a FRESH Claude session in
// cwd (the tree's `n` key): A = a new agent with a pinned session id, B =
// entire-tail following exactly that id, C = a shell.
// prof picks the account the new agent runs as — the tree's `n` uses the default
// one, `@` the personal one. Returns only on failure (see startWorkspace).
func launchNewWorkspace(cwd, bin string, prof claudeProfile) error {
	return startWorkspace(newWorkspaceScript(cwd, selfPath(), newSessionID(), paneBin(bin), accountEnvPrefix(prof), tapEnvPrefix(tapBaseURL(homeDir())), itermSinglePane()))
}

func launchAmpWorkspace(cwd, threadID string) error {
	bin, err := exec.LookPath("amp")
	if err != nil {
		return errors.New("amp is not installed or not on PATH")
	}
	return startWorkspace(ampWorkspaceScript(cwd, threadID, selfPath(), bin, true))
}

func launchNewAmpWorkspace(cwd string) error {
	bin, err := exec.LookPath("amp")
	if err != nil {
		return errors.New("amp is not installed or not on PATH")
	}
	return startWorkspace(ampWorkspaceScript(cwd, "", selfPath(), bin, itermSinglePane()))
}

func ampWorkspaceScript(cwd, threadID, self, bin string, inPlace bool) workspaceLaunch {
	cd := "cd " + shQuote(cwd)
	a := cd + " && " + shQuote(bin)
	b := cd + " && " + shQuote(self) + " --agent amp"
	if threadID != "" {
		a += " threads continue " + shQuote(threadID)
		b += " --follow-session " + shQuote(threadID)
	} else {
		b += " --wait-new"
	}
	return workspaceLaunch{Script: splitScript(a, b, cd, inPlace), Agent: a, Tail: b, Shell: cd, InPlace: inPlace}
}

// pinsSessionID reports whether bin forwards Claude's `--session-id` to the
// process that actually writes the transcript, which is what lets the fresh
// workspace pin a shared id across both panes.
//
// Only plain `claude` does. happy *extracts* `--session-id` for its own
// bookkeeping and then, in the local hook mode it runs interactive sessions
// under, spawns claude without it (`dist/index-*.mjs`: the `hookSettingsPath`
// branch pushes `--resume` only) — so Claude mints its own id, the pinned file
// never appears, and a `--follow-session` tail waits forever. Verified live:
// `happy --session-id X` produced a transcript under a different id entirely.
// `--resume` IS forwarded on both branches, so the resume workspace still pins.
//
// Anything that isn't `claude` is treated as not pinning. A wrapper that does
// pass the flag through only loses the pin and falls back to --wait-new, whereas
// wrongly assuming support strands the tail — so the conservative default is the
// safe one.
func pinsSessionID(bin string) bool {
	return filepath.Base(bin) == fallbackClaudeBin
}

// newWorkspaceScript lays out a fresh-session workspace. Unlike the resume
// workspace (whose caller only fires it in a single-pane window, else tails in
// place), a fresh session has nothing to tail in place — so this splits the
// current window when it's a single pane (inPlace: pane A is this one), else
// opens a NEW window, started with A's command, rather than carving up an
// existing split.
//
//	A = <bin> --session-id <id>    B = entire-tail --follow-session <id>
//	C = shell                          (waits for A's file, then follows it +forks)
//
// Pinning a shared id (rather than --wait-new racing the newest file) means B
// latches onto exactly A's session even when other Claude sessions are live in
// the same repo. That only works when bin forwards the flag — see pinsSessionID;
// a launcher that doesn't gets no id and B falls back to --wait-new.
// acctEnv (accountEnvPrefix) leads the assignments so the account decision reads
// first in the agent's command line; it is "" for the default account, leaving
// that launch byte-identical to the pre-profiles one.
func newWorkspaceScript(cwd, self, sessionID, bin, acctEnv, tapEnv string, inPlace bool) workspaceLaunch {
	cd := "cd " + shQuote(cwd)
	a := cd + " && " + acctEnv + tapEnv + shQuote(bin)
	b := cd + " && " + shQuote(self)
	if pinsSessionID(bin) {
		a += " --session-id " + shQuote(sessionID)
		b += " --follow-session " + shQuote(sessionID)
	} else {
		b += " --wait-new"
	}
	return workspaceLaunch{Script: splitScript(a, b, cd, inPlace), Agent: a, Tail: b, Shell: cd, InPlace: inPlace}
}

// workspaceScript builds the 3-pane workspace:
//
//	A │ B    A = <bin> --resume <id>
//	--+ B    B = entire-tail --follow-session <id>
//	C │ B    C = shell
//
// It reuses the CURRENT window (the caller only invokes this when the window is a
// single pane — see itermSinglePane): the pane running the picker becomes A,
// which entire-tail execs into once B and C are up. All three panes cd into the
// picked session's folder. B follows by id (not the file path) so a worktree
// fork of the resumed session is followed too.
func workspaceScript(cwd, resumeID, self, bin, acctEnv, tapEnv string) workspaceLaunch {
	cd := "cd " + shQuote(cwd)
	a := cd + " && " + acctEnv + tapEnv + shQuote(bin) + " --resume " + shQuote(resumeID)
	b := cd + " && " + shQuote(self) + " --follow-session " + shQuote(resumeID)
	return workspaceLaunch{Script: splitScript(a, b, cd, true), Agent: a, Tail: b, Shell: cd, InPlace: true}
}

// splitScript is the AppleScript for the layout. B and C are started with their
// command lines. A is either this pane (inPlace — the caller execs a once the
// script has run) or a new window started with a.
func splitScript(a, b, c string, inPlace bool) string {
	sh := paneShell()
	cmd := func(s string) string { return asEscape(paneCommand(sh, s)) }
	open := "\ttell current window"
	if !inPlace {
		open = "\tcreate window with default profile command \"" + cmd(a) + "\"\n" + open
	}
	return fmt.Sprintf(`tell application "iTerm2"
%s
		set a to current session
		tell a
			set b to (split vertically with default profile command "%s")
			set c to (split horizontally with default profile command "%s")
		end tell
		select a
	end tell
end tell`, open, cmd(b), cmd(c))
}
