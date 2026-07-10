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
// lay out the workspace: pick a session in the tree, and the current window
// becomes three panes — claude --resume, entire-tail following it, and a shell —
// all in the picked session's folder. macOS + iTerm2 only; callers gate with
// itermAvailable.

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
func launchWorkspace(cwd, resumeID, sessionFile string) error {
	return osaRun(workspaceScript(cwd, resumeID, sessionFile, selfPath()))
}

// workspaceScript builds the AppleScript for the 3-pane workspace, reusing the
// CURRENT window/session: the pane running the picker becomes A. A's command
// (claude --resume <id>) is queued to its tty and runs the moment entire-tail
// exits; A is split into B (entire-tail, full-height right) and C (a shell,
// bottom-left). All three cd into the picked session's folder.
//
//	A │ B    A = claude --resume <id>
//	--+ B    B = entire-tail <sessionFile>
//	C │ B    C = shell
func workspaceScript(cwd, resumeID, sessionFile, self string) string {
	cd := "cd " + shQuote(cwd)
	a := cd + " && claude --resume " + shQuote(resumeID)
	b := cd + " && " + shQuote(self) + " " + shQuote(sessionFile)
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
