package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// restore.go rebuilds the tabs a reboot left idle (see layout.go for why the
// snapshot exists). It never types into a pane and never reuses a restored one:
// each saved window becomes a NEW window whose tabs are started with iTerm's
// `command`, from the same builders `⏎` uses.

type restoreEnv struct {
	Home, Self, ClaudeBin, AmpBin, TapEnv string
	PathForID                             func(string) string
	DirExists                             func(string) bool
}

// restoreTab is one tab to open. Skip non-empty means it is reported, not
// opened. Agent and Tail empty means a plain shell tab running Shell.
type restoreTab struct {
	Label, Skip        string
	Agent, Tail, Shell string
}

func planTab(t layoutTab, env restoreEnv) restoreTab {
	cwd := t.Cwd
	if cwd == "" || !env.DirExists(cwd) {
		cwd = env.Home
	}
	switch {
	case t.Agent == AgentClaude && validSessionID(t.Session):
		path := env.PathForID(t.Session)
		if path == "" {
			return restoreTab{Label: tildify(t.Cwd, env.Home) + " · " + shortID(t.Session), Skip: "transcript missing"}
		}
		dir := workspaceCwd(path)
		acct := accountEnvPrefix(profileByName(env.Home, profileForPath(env.Home, path)))
		w := workspaceScript(dir, t.Session, env.Self, env.ClaudeBin, acct, env.TapEnv)
		label := tildify(dir, env.Home)
		if b := loadClaudeMeta(path).Branch; b != "" {
			label += " · " + b
		}
		return restoreTab{Label: label + " · " + shortID(t.Session), Agent: w.Agent, Tail: w.Tail, Shell: w.Shell}
	case t.Agent == AgentAmp && validAmpThreadID(t.Session):
		w := ampWorkspaceScript(cwd, t.Session, env.Self, env.AmpBin, false)
		return restoreTab{Label: tildify(cwd, env.Home) + " · " + t.Session, Agent: w.Agent, Tail: w.Tail, Shell: w.Shell}
	}
	return restoreTab{Label: tildify(cwd, env.Home) + " · shell", Shell: "cd " + shQuote(cwd)}
}

// restoreWindowScript opens one window holding every tab that isn't skipped, in
// order, and returns the new window's id (so restore can tell it apart from the
// windows it offers to close). A workspace tab gets splitScript's layout: B to
// the right of A at full height, C under A.
func restoreWindowScript(sh string, tabs []restoreTab) string {
	cmd := func(s string) string { return asEscape(paneCommand(sh, s)) }
	var b strings.Builder
	first := true
	for _, t := range tabs {
		if t.Skip != "" {
			continue
		}
		a := t.Agent
		if a == "" {
			a = t.Shell
		}
		if first {
			b.WriteString("\tset w to (create window with default profile command \"" + cmd(a) + "\")\n")
			b.WriteString("\tset a to current session of w\n")
			first = false
		} else {
			b.WriteString("\ttell w to set nt to (create tab with default profile command \"" + cmd(a) + "\")\n")
			b.WriteString("\tset a to current session of nt\n")
		}
		if t.Agent != "" {
			b.WriteString("\ttell a\n")
			b.WriteString("\t\tsplit vertically with default profile command \"" + cmd(t.Tail) + "\"\n")
			b.WriteString("\t\tsplit horizontally with default profile command \"" + cmd(t.Shell) + "\"\n")
			b.WriteString("\tend tell\n")
		}
	}
	if first {
		return ""
	}
	return "tell application \"iTerm2\"\n" + b.String() + "\treturn id of w\nend tell"
}

// idleWindows are the windows restore may offer to close: every pane is an idle
// shell. ownTTY is the pane running restore; its entire-tail doesn't make the
// window busy, and its window comes last so closing it (and us) happens after
// the others.
func idleWindows(panes []itermPane, byTTY map[string][]psProc, ownTTY string) []int {
	busy := map[int]bool{}
	var order []int
	seen := map[int]bool{}
	own := 0
	for _, p := range panes {
		if !seen[p.Window] {
			seen[p.Window] = true
			order = append(order, p.Window)
		}
		if p.TTY != "" && p.TTY == ownTTY {
			own = p.Window
			continue
		}
		for _, pr := range byTTY[p.TTY] {
			if n := procName(pr.Comm); !layoutShells[n] && n != "login" {
				busy[p.Window] = true
			}
		}
	}
	var out []int
	for _, w := range order {
		if !busy[w] && w != own {
			out = append(out, w)
		}
	}
	if own != 0 && !busy[own] {
		out = append(out, own)
	}
	return out
}

func closeWindowsScript(ids []int) string {
	var b strings.Builder
	b.WriteString("tell application \"iTerm2\"\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "\tclose (windows whose id is %d)\n", id)
	}
	b.WriteString("end tell")
	return b.String()
}

var (
	restoreOsa    = osaOut
	restorePS     = func() (string, error) { return layoutPS() }
	restoreOwnTTY = func() string {
		b, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
)

const restoredMarker = "restored"

// pendingRestore is the previous boot's snapshot dir and its tab count, or 0
// tabs when there is none or it was already restored or dismissed.
func pendingRestore(home string) (string, int) {
	dir, ok := previousBootDir(layoutRoot(home), layoutBoot())
	if !ok {
		return "", 0
	}
	if _, err := os.Stat(filepath.Join(dir, restoredMarker)); err == nil {
		return dir, 0
	}
	snap, ok := readLayoutSnapshot(dir)
	if !ok {
		return dir, 0
	}
	n := 0
	for _, w := range snap.Windows {
		n += len(w.Tabs)
	}
	return dir, n
}

func runRestore(args []string, cfg Config, home string, in io.Reader, out io.Writer) error {
	var force, dismiss, dry, pick bool
	for _, a := range args {
		switch a {
		case "--force":
			force = true
		case "--dismiss":
			dismiss = true
		case "--dry-run":
			dry = true
		case "--pick":
			pick = true
		default:
			return fmt.Errorf("unknown option %s (try --force, --pick, --dry-run, --dismiss)", a)
		}
	}
	dir, ok := previousBootDir(layoutRoot(home), layoutBoot())
	if !ok {
		fmt.Fprintln(out, "entire-tail: no saved layout from before the last reboot.")
		return nil
	}
	marker := filepath.Join(dir, restoredMarker)
	if dismiss {
		return os.WriteFile(marker, nil, 0o600)
	}
	if _, err := os.Stat(marker); err == nil && !force {
		fmt.Fprintln(out, "entire-tail: already restored (--force to redo).")
		return nil
	}
	snap, ok := readLayoutSnapshot(dir)
	if !ok || len(snap.Windows) == 0 {
		return errors.New("the saved layout is unreadable")
	}
	r := bufio.NewReader(in)
	windows := snap.Windows
	if pick {
		w, ok := pickSavedWindow(windows, home, r, out)
		if !ok {
			return nil
		}
		windows = []layoutWindow{w}
	}

	// The windows open BEFORE we create any: only these may be offered for
	// closing. A dry run lists nothing and opens nothing.
	before := ""
	if !dry {
		before, _ = restoreOsa(layoutListScript())
	}
	env := restoreEnv{
		Home: home, Self: selfPath(),
		ClaudeBin: paneBin(resolveClaudeBin(cfg, exec.LookPath, os.Stderr)),
		AmpBin:    paneBin("amp"),
		TapEnv:    tapEnvPrefix(tapBaseURL(home)),
		PathForID: func(id string) string { return localPathForID(home, id) },
		DirExists: isDir,
	}
	sh := paneShell()
	for wi, w := range windows {
		tabs := make([]restoreTab, 0, len(w.Tabs))
		for _, t := range w.Tabs {
			tabs = append(tabs, planTab(t, env))
		}
		fmt.Fprintf(out, "window %d of %d\n", wi+1, len(windows))
		script := restoreWindowScript(sh, tabs)
		var err error
		if script != "" && !dry {
			_, err = restoreOsa(script)
		}
		for _, t := range tabs {
			switch {
			case t.Skip != "":
				fmt.Fprintf(out, "  skipped: %s  %s\n", t.Skip, t.Label)
			case err != nil:
				fmt.Fprintf(out, "  skipped: iTerm error  %s\n", t.Label)
			case dry:
				fmt.Fprintf(out, "  would open  %s\n", t.Label)
			default:
				fmt.Fprintf(out, "  ✔ %s\n", t.Label)
			}
		}
	}
	if dry {
		return nil
	}
	if !pick {
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			return err
		}
	}

	psOut, _ := restorePS()
	idle := idleWindows(parseItermPanes(before), procsByTTY(parsePSProcs(psOut)), restoreOwnTTY())
	if len(idle) == 0 {
		return nil
	}
	fmt.Fprintf(out, "close the restored windows (%d)? y/N ", len(idle))
	line, _ := r.ReadString('\n')
	if strings.EqualFold(strings.TrimSpace(line), "y") {
		_, err := restoreOsa(closeWindowsScript(idle))
		return err
	}
	return nil
}

// pickSavedWindow lists the saved windows and reads a number. Plain stdin, not
// the tree TUI: this is the rare path, and a numbered list is enough.
func pickSavedWindow(windows []layoutWindow, home string, r *bufio.Reader, out io.Writer) (layoutWindow, bool) {
	for i, w := range windows {
		fmt.Fprintf(out, "%d) %d tabs\n", i+1, len(w.Tabs))
		for _, t := range w.Tabs {
			label := tildify(t.Cwd, home)
			if t.Session != "" {
				label += " · " + string(t.Agent) + " · " + shortID(t.Session)
			}
			fmt.Fprintf(out, "     %s\n", label)
		}
	}
	fmt.Fprint(out, "restore which window? ")
	line, _ := r.ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(windows) {
		return layoutWindow{}, false
	}
	return windows[n-1], true
}
