package main

import (
	"fmt"
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
