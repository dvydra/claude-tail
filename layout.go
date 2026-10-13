package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// layout.go is the pure half of the iTerm layout snapshot that `entire-tail
// restore` rebuilds after a reboot. iTerm restores the tabs but gives every
// restored pane a NEW session UUID (measured 2026-10-13: 298 pre-reboot
// pane-link files, 0 matching a live pane), so nothing keyed on the UUID
// survives. The snapshot records what does matter, per window and tab in order:
// which agent session the tab was running, and where.

type itermPane struct {
	Window, Tab int
	UUID, TTY   string // TTY is the bare name ("ttys003"), "" when iTerm reports none
}

type psProc struct {
	PID       int
	TTY, Comm string
}

type layoutSnapshot struct {
	Boot    int64          `json:"boot"`
	Taken   time.Time      `json:"taken"`
	Windows []layoutWindow `json:"windows"`
}

type layoutWindow struct {
	ITermWindow int         `json:"iterm_window"`
	Tabs        []layoutTab `json:"tabs"`
}

type layoutTab struct {
	Agent   Agent    `json:"agent,omitempty"`
	Session string   `json:"session,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Panes   []string `json:"panes"`
}

// layoutListScript lists every pane as "window<TAB>tab<TAB>uuid<TAB>tty". The
// running check comes first because `tell application "iTerm2"` LAUNCHES iTerm,
// and a snapshot must never do that. `character id 9` rather than `tab`: inside
// the iTerm tell block `tab` is iTerm's tab class, not the tab character.
func layoutListScript() string {
	return `if application "iTerm2" is not running then return ""
tell application "iTerm2"
	set sep to character id 9
	set out to ""
	repeat with w in windows
		set ti to 0
		repeat with t in tabs of w
			set ti to ti + 1
			repeat with s in sessions of t
				set out to out & (id of w) & sep & ti & sep & (unique id of s) & sep & (tty of s) & linefeed
			end repeat
		end repeat
	end repeat
	return out
end tell`
}

func parseItermPanes(out string) []itermPane {
	var panes []itermPane
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		w, err1 := strconv.Atoi(strings.TrimSpace(f[0]))
		t, err2 := strconv.Atoi(strings.TrimSpace(f[1]))
		if err1 != nil || err2 != nil || f[2] == "" {
			continue
		}
		panes = append(panes, itermPane{Window: w, Tab: t, UUID: f[2], TTY: strings.TrimPrefix(strings.TrimSpace(f[3]), "/dev/")})
	}
	return panes
}

// parsePSProcs reads `ps -A -o pid=,tty=,comm=`. comm is the rest of the line,
// since a binary path can contain spaces.
func parsePSProcs(out string) []psProc {
	var procs []psProc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		rest := strings.TrimSpace(line)
		rest = strings.TrimSpace(strings.TrimPrefix(rest, f[0]))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, f[1]))
		procs = append(procs, psProc{PID: pid, TTY: f[1], Comm: rest})
	}
	return procs
}

func procsByTTY(procs []psProc) map[string][]psProc {
	out := map[string][]psProc{}
	for _, p := range procs {
		if p.TTY == "" || p.TTY == "??" {
			continue
		}
		out[p.TTY] = append(out[p.TTY], p)
	}
	return out
}

func procName(comm string) string {
	return strings.TrimPrefix(filepath.Base(comm), "-")
}

var layoutShells = map[string]bool{"zsh": true, "bash": true, "fish": true, "sh": true}

// paneRole names what a pane was for. A tail wins over an agent: a tab can hold
// both only on different ttys, and on one tty a tail is the more specific fact.
func paneRole(procs []psProc) string {
	role := "shell"
	for _, p := range procs {
		switch procName(p.Comm) {
		case "entire-tail":
			return "tail"
		case "claude", "amp":
			role = "agent"
		}
	}
	return role
}

// paneShellPID is the pane's login shell: the lowest-pid shell on the tty, which
// is the one iTerm started and so holds the pane's cwd.
func paneShellPID(procs []psProc) int {
	best := 0
	for _, p := range procs {
		if layoutShells[procName(p.Comm)] && (best == 0 || p.PID < best) {
			best = p.PID
		}
	}
	return best
}

func splitSessionKey(key string) (Agent, string) {
	if key == "" {
		return "", ""
	}
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return Agent(key[:i]), key[i+1:]
	}
	return "", key
}

// buildLayout groups panes into windows and tabs, keeping iTerm's order. A tab's
// session comes from its agent pane (agents: uuid → paneSessionKey), else from a
// tail pane's pane-link registration. A tab's cwd is the first pane cwd known.
func buildLayout(panes []itermPane, byTTY map[string][]psProc, cwds map[string]string, agents map[string]string, registry map[string]paneEntry) []layoutWindow {
	var windows []layoutWindow
	widx := map[int]int{}
	type tabKey struct{ w, t int }
	tidx := map[tabKey]int{}
	for _, p := range panes {
		wi, ok := widx[p.Window]
		if !ok {
			wi = len(windows)
			widx[p.Window] = wi
			windows = append(windows, layoutWindow{ITermWindow: p.Window})
		}
		k := tabKey{p.Window, p.Tab}
		ti, ok := tidx[k]
		if !ok {
			ti = len(windows[wi].Tabs)
			tidx[k] = ti
			windows[wi].Tabs = append(windows[wi].Tabs, layoutTab{})
		}
		tab := &windows[wi].Tabs[ti]
		role := paneRole(byTTY[p.TTY])
		tab.Panes = append(tab.Panes, role)
		if tab.Cwd == "" {
			tab.Cwd = cwds[p.UUID]
		}
		if role == "agent" {
			if a, id := splitSessionKey(agents[p.UUID]); id != "" {
				tab.Agent, tab.Session = a, id
			}
		}
		if role == "tail" && tab.Session == "" {
			if e, ok := registry[p.UUID]; ok && e.Follow != "" {
				tab.Agent, tab.Session = e.Agent, e.Follow
				if tab.Agent == "" {
					tab.Agent = AgentClaude
				}
			}
		}
	}
	return windows
}

func layoutRoot(home string) string {
	return filepath.Join(home, ".claude", "entire-tail", "layout")
}

// previousBootDir is the newest boot directory older than currentBoot whose
// snapshot holds at least one session. The current boot's own snapshot is never a restore source: it
// describes the windows that are open right now.
func previousBootDir(root string, currentBoot int64) (string, bool) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	var boots []int64
	for _, e := range ents {
		b, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || !e.IsDir() || b >= currentBoot {
			continue
		}
		if s, ok := readLayoutSnapshot(filepath.Join(root, e.Name())); ok && layoutHasSession(s) {
			boots = append(boots, b)
		}
	}
	if len(boots) == 0 {
		return "", false
	}
	sort.Slice(boots, func(i, j int) bool { return boots[i] > boots[j] })
	return filepath.Join(root, strconv.FormatInt(boots[0], 10)), true
}

func readLayoutSnapshot(dir string) (layoutSnapshot, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return layoutSnapshot{}, false
	}
	var s layoutSnapshot
	if json.Unmarshal(b, &s) != nil {
		return layoutSnapshot{}, false
	}
	return s, true
}

// followFromArgv reads what a tail was started to follow from its command line:
// the fallback for a tab whose tail has no pane-link registration (pane link is
// opt-in) and whose agent pane couldn't be resolved. It can name an ancestor
// after a lineage fork, which still resumes into the same conversation.
func followFromArgv(argv string) (Agent, string) {
	toks := strings.Fields(argv)
	agent, id := AgentClaude, ""
	for i := 0; i+1 < len(toks); i++ {
		switch toks[i] {
		case "--follow-session":
			id = toks[i+1]
		case "--agent":
			agent = Agent(toks[i+1])
		}
	}
	if id == "" {
		return "", ""
	}
	return agent, id
}

// layoutHasSession reports whether any tab carries a session. A boot that only
// ever saw idle restored shells has nothing to restore, and must not hide the
// boot before it.
func layoutHasSession(s layoutSnapshot) bool {
	for _, w := range s.Windows {
		for _, t := range w.Tabs {
			if t.Session != "" {
				return true
			}
		}
	}
	return false
}
