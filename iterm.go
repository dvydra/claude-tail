package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// launchWorkspace opens a new iTerm window with the three-pane dev layout:
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
func launchWorkspace(cwd, resumeID, bin string, prof claudeProfile) error {
	return osaRun(workspaceScript(cwd, resumeID, selfPath(), bin, accountEnvPrefix(prof), tapEnvPrefix(tapBaseURL(homeDir()))))
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
// one, `@` the personal one.
func launchNewWorkspace(cwd, bin string, prof claudeProfile) error {
	return osaRun(newWorkspaceScript(cwd, selfPath(), newSessionID(), bin, accountEnvPrefix(prof), tapEnvPrefix(tapBaseURL(homeDir()))))
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
// current window when it's a single pane, else opens a NEW window rather than
// carving up an existing split.
//
//	A = <bin> --session-id <id>    B = entire-tail --follow-session <id>
//	C = shell                          (waits for A's file, then follows it +forks)
//
// Pinning a shared id (rather than --wait-new racing the newest file) means B
// latches onto exactly A's session even when other Claude sessions are live in
// the same repo. That only works when bin forwards the flag — see pinsSessionID;
// a launcher that doesn't gets no id and B falls back to --wait-new.
// acctEnv (accountEnvPrefix) leads the assignments so the account decision reads
// first in the queued command line; it is "" for the default account, leaving
// that launch byte-identical to the pre-profiles one.
func newWorkspaceScript(cwd, self, sessionID, bin, acctEnv, tapEnv string) string {
	cd := "cd " + shQuote(cwd)
	a := cd + " && " + acctEnv + tapEnv + shQuote(bin)
	b := cd + " && " + shQuote(self)
	if pinsSessionID(bin) {
		a += " --session-id " + shQuote(sessionID)
		b += " --follow-session " + shQuote(sessionID)
	} else {
		b += " --wait-new"
	}
	c := cd
	return fmt.Sprintf(`tell application "iTerm2"
	if (count of sessions of current tab of current window) > 1 then
		create window with default profile
	end if
	tell current window
		set a to current session
		tell a
			set b to (split vertically with default profile)
			set c to (split horizontally with default profile)
		end tell
		tell a to write text "%s"
		tell b to write text "%s"
		tell c to write text "%s"
		select a
	end tell
end tell`, asEscape(a), asEscape(b), asEscape(c))
}

// workspaceScript builds the AppleScript for the 3-pane workspace:
//
//	A │ B    A = <bin> --resume <id>
//	--+ B    B = entire-tail --follow-session <id>
//	C │ B    C = shell
//
// It reuses the CURRENT window (the caller only invokes this when the window is a
// single pane — see itermSinglePane): the pane running the picker becomes A, and
// A's command is queued to its tty and runs the moment entire-tail exits. All
// three panes cd into the picked session's folder. B follows by id (not the file
// path) so a worktree fork of the resumed session is followed too.
func workspaceScript(cwd, resumeID, self, bin, acctEnv, tapEnv string) string {
	cd := "cd " + shQuote(cwd)
	a := cd + " && " + acctEnv + tapEnv + shQuote(bin) + " --resume " + shQuote(resumeID)
	b := cd + " && " + shQuote(self) + " --follow-session " + shQuote(resumeID)
	c := cd
	return fmt.Sprintf(`tell application "iTerm2"
	tell current window
		set a to current session
		tell a
			set b to (split vertically with default profile)
			set c to (split horizontally with default profile)
		end tell
		tell a to write text "%s"
		tell b to write text "%s"
		tell c to write text "%s"
		select a
	end tell
end tell`, asEscape(a), asEscape(b), asEscape(c))
}
