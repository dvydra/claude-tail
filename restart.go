package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// restartToggle is Ctrl-R, the first key of the restart chord. A `y` straight
// after it re-execs the tail; any other key cancels and is swallowed, so a
// stray Ctrl-R followed by a display key does neither.
const restartToggle = 0x12

// restartState is what the tail carries across a restart. Only what the user
// can see and change is kept: the session being followed NOW (after a lineage
// rollover that is no longer what argv named) and the display settings the keys
// have moved since startup.
type restartState struct {
	Agent    Agent
	Cur      string // the file being followed now
	Theme    string
	Tools    toolStyleKind
	WrapOff  bool
	Collapse int // 0 = off
	Cfg      Config
}

// restartArgs is the argv (minus argv[0]) for the re-exec'd tail. It never
// reproduces the original argv: a bare launch went through the tree or --live,
// and re-running that would drop the user back into a picker instead of the
// session they were reading. So the restart pins the session directly, and
// --no-pick keeps it from ever reaching a picker.
func restartArgs(s restartState) []string {
	args := []string{"--agent", string(s.Agent), "--no-pick", "--no-hook-install"}
	if s.Theme != "" {
		args = append(args, "--theme", s.Theme)
	}
	if s.Cfg.Backfill != "" {
		args = append(args, "--backfill", s.Cfg.Backfill)
	}
	args = append(args, "--tool-style", restartToolStyle(s.Tools))
	if s.WrapOff {
		args = append(args, "--no-wrap")
	} else {
		args = append(args, "--wrap")
	}
	if s.Collapse > 0 {
		args = append(args, "--collapse", strconv.Itoa(s.Collapse))
	} else {
		args = append(args, "--no-collapse")
	}
	if s.Cfg.ClaudeBinSet {
		args = append(args, "--claude-bin", s.Cfg.ClaudeBin)
	}
	if s.Cfg.NoTap {
		args = append(args, "--no-tap")
	}
	if s.Cfg.NoPaneLink {
		args = append(args, "--no-pane-link")
	}
	if s.Cfg.MarkContinuation {
		args = append(args, "--mark-continuation")
	}
	// Amp follows a thread id, not a file: the render file under the cache is
	// ours, and tailing it as a plain file would stop at its last export.
	if id := sessionIDFromPath(s.Cur); s.Agent == AgentAmp && validAmpThreadID(id) {
		return append(args, "--follow-session", id)
	}
	return append(args, "--", s.Cur)
}

func restartToolStyle(k toolStyleKind) string {
	switch k {
	case toolNone:
		return "none"
	case toolLines:
		return "lines"
	}
	return "dots"
}

// restartBinary picks the binary to exec: whatever this process was started
// as, looked up again NOW. That is the point of the restart — install.sh has
// replaced the file behind that name, and os.Executable may still name the old
// inode's resolved path. lookPath is a seam for tests.
func restartBinary(argv0 string, lookPath func(string) (string, error)) string {
	if strings.ContainsRune(argv0, filepath.Separator) {
		if abs, err := filepath.Abs(argv0); err == nil && isFile(abs) {
			return abs
		}
	} else if p, err := lookPath(argv0); err == nil {
		return p
	}
	return selfPath()
}

// execRestart replaces this process with a fresh tail. It returns only on error.
var execRestart = func(args []string) error {
	bin := restartBinary(os.Args[0], exec.LookPath)
	return syscall.Exec(bin, append([]string{filepath.Base(bin)}, args...), os.Environ())
}
