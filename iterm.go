package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// iterm.go drives iTerm2 via AppleScript (osascript, always present on macOS) to
// launch multi-pane windows. Two layouts:
//
//	workspace  — a fresh dev window: claude | entire-tail | shell, all in $PWD.
//	resumePair — pick a session in the tree, resume it beside its live tail.
//
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
//	A │ B    A = claude, B = entire-tail (full-height right column),
//	C │ B    C = a plain shell.
//
// All three cd into cwd. B sleeps briefly so the fresh Claude session file
// exists before entire-tail auto-discovers it.
func launchWorkspace(cwd string) error {
	return osaRun(workspaceScript(cwd, selfPath()))
}

// workspaceScript builds the AppleScript for the 3-pane dev window.
func workspaceScript(cwd, self string) string {
	cd := "cd " + shQuote(cwd)
	a := cd + " && claude"
	b := cd + " && sleep 1 && " + shQuote(self) + " --no-pick"
	c := cd
	return fmt.Sprintf(`tell application "iTerm2"
	activate
	create window with default profile
	tell current window
		set a to current session
		tell a
			write text "%s"
			set b to (split vertically with default profile)
			set c to (split horizontally with default profile)
		end tell
		tell b to write text "%s"
		tell c to write text "%s"
		select a
	end tell
end tell`, asEscape(a), asEscape(b), asEscape(c))
}

// launchResumePair opens a new iTerm window split in two: claude --resume <id> on
// the left, entire-tail following that exact session on the right — both in cwd.
func launchResumePair(cwd, sessionFile, sessionID string) error {
	return osaRun(resumePairScript(cwd, sessionFile, sessionID, selfPath()))
}

// resumePairScript builds the AppleScript for the 2-pane resume window.
func resumePairScript(cwd, sessionFile, sessionID, self string) string {
	cd := "cd " + shQuote(cwd)
	a := cd + " && claude --resume " + shQuote(sessionID)
	b := cd + " && " + shQuote(self) + " " + shQuote(sessionFile)
	return fmt.Sprintf(`tell application "iTerm2"
	activate
	create window with default profile
	tell current window
		set a to current session
		tell a
			write text "%s"
			set b to (split vertically with default profile)
		end tell
		tell b to write text "%s"
		select a
	end tell
end tell`, asEscape(a), asEscape(b))
}
