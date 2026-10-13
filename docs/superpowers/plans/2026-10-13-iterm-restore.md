# iTerm Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Save the iTerm window/tab layout while tails run, and rebuild every pre-reboot workspace tab in new windows with `entire-tail restore`.

**Architecture:** Running tails save `~/.claude/entire-tail/layout/<boot-sec>/snapshot.json` every 30s (one writer via `flock`). `entire-tail restore` reads the newest snapshot from an older boot, builds one AppleScript per saved window from the existing workspace command builders, reports per tab, then offers to close the idle restored windows. The tree shows a footer hint and binds `R`.

**Tech Stack:** Go 1.24, `osascript`, `ps`, `lsof`, `golang.org/x/sys/unix` (already an indirect dependency).

**Spec:** `docs/superpowers/specs/2026-10-13-iterm-restore-design.md`

## Global Constraints

- No new runtime dependencies beyond what macOS ships (`osascript`, `ps`, `lsof`); everything degrades to a silent no-op off iTerm.
- `go test` must never touch iTerm, `ps`, `lsof` or `osascript`: every IO call goes through a package var the tests stub.
- The snapshot must never launch iTerm (`tell application "iTerm2"` starts it), never write an empty snapshot, and never disturb the tail on failure.
- No pane is typed into: every pane starts through `paneCommand` with iTerm's `command`.
- The flock file is never unlinked.
- No golden changes. `go vet ./... && go test -race ./...` passes.
- Docs and comments: no em dashes, no hard-wrapped prose lines in Markdown.

## Review Focus

- A window with a pane we can't classify (no tty, `ps` row missing): the pane is a shell, the snapshot still writes. Test in Task 1.
- A saved Claude session whose transcript is gone: the tab is skipped with `transcript missing`, the other tabs still open. Test in Task 3.
- A cwd containing a single quote or double quote: the AppleScript stays valid (`asEscape` over `paneCommand`). Test in Task 3.
- Running `restore` twice: the second run refuses without `--force`. Test in Task 4.
- The window `restore` runs in has an `entire-tail` on its tty: it still counts as idle, and is closed last. Test in Task 3.

---

## File Structure

- Create `layout.go`: snapshot types, parsing of the osascript and `ps` output, pane classification, `buildLayout`, snapshot discovery across boots. Pure.
- Create `layoutsnap.go`: IO half of the snapshot (package vars, flock, freshness check, write) and the background saver started by the tail.
- Create `restore.go`: `planTab`, `restoreWindowScript`, `idleWindows`, `closeWindowsScript`, `runRestore` (flags, report, prompt, marker).
- Modify `config.go`: `ActionRestore`, `RestoreArgs`.
- Modify `main.go`: dispatch `ActionRestore`; start the saver in the tail loop.
- Modify `tree.go` / `picker.go`: footer hint, `R` key, `treeRestore` result.
- Modify `README.md`, `CLAUDE.md`: document the feature.
- Tests: `layout_test.go`, `restore_test.go`, additions to `tree_test.go` and `config_test.go`.

---

### Task 1: Layout model and pane classification (pure)

**Files:**
- Create: `layout.go`
- Test: `layout_test.go`

**Interfaces:**
- Produces:
  - `type itermPane struct { Window, Tab int; UUID, TTY string }`
  - `type psProc struct { PID int; TTY, Comm string }`
  - `type layoutSnapshot struct { Boot int64; Taken time.Time; Windows []layoutWindow }`
  - `type layoutWindow struct { ITermWindow int; Tabs []layoutTab }`
  - `type layoutTab struct { Agent Agent; Session, Cwd string; Panes []string }`
  - `func layoutListScript() string`
  - `func parseItermPanes(out string) []itermPane`
  - `func parsePSProcs(out string) []psProc`
  - `func procsByTTY(procs []psProc) map[string][]psProc` (key is the bare tty name, `ttys003`)
  - `func paneRole(procs []psProc) string` (`"tail"`, `"agent"`, `"shell"`)
  - `func paneShellPID(procs []psProc) int` (0 when none)
  - `func buildLayout(panes []itermPane, byTTY map[string][]psProc, cwds map[string]string, agents map[string]string, registry map[string]paneEntry) []layoutWindow`
  - `func splitSessionKey(key string) (Agent, string)`
  - `func layoutRoot(home string) string`
  - `func previousBootDir(root string, currentBoot int64) (string, bool)`
  - `func readLayoutSnapshot(dir string) (layoutSnapshot, bool)`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseItermPanes(t *testing.T) {
	out := "155\t1\tAAA\t/dev/ttys003\n155\t1\tBBB\t/dev/ttys004\n155\t2\tCCC\t\nbad line\n"
	got := parseItermPanes(out)
	want := []itermPane{
		{Window: 155, Tab: 1, UUID: "AAA", TTY: "ttys003"},
		{Window: 155, Tab: 1, UUID: "BBB", TTY: "ttys004"},
		{Window: 155, Tab: 2, UUID: "CCC", TTY: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d panes, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pane %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestParsePSProcs(t *testing.T) {
	out := "  101 ttys003  login\n  102 ttys003  -zsh\n  150 ttys003  /Users/x/My Tools/entire-tail\n  200 ??       launchd\n"
	got := parsePSProcs(out)
	if len(got) != 4 {
		t.Fatalf("got %d procs: %#v", len(got), got)
	}
	if got[2] != (psProc{PID: 150, TTY: "ttys003", Comm: "/Users/x/My Tools/entire-tail"}) {
		t.Errorf("comm with a space parsed as %#v", got[2])
	}
}

func TestPaneRole(t *testing.T) {
	sh := psProc{PID: 2, Comm: "-zsh"}
	cases := []struct {
		procs []psProc
		want  string
	}{
		{nil, "shell"},
		{[]psProc{sh}, "shell"},
		{[]psProc{sh, {PID: 3, Comm: "claude"}}, "agent"},
		{[]psProc{sh, {PID: 3, Comm: "amp"}}, "agent"},
		{[]psProc{sh, {PID: 3, Comm: "/opt/bin/entire-tail"}}, "tail"},
		{[]psProc{sh, {PID: 3, Comm: "vim"}}, "shell"},
	}
	for _, c := range cases {
		if got := paneRole(c.procs); got != c.want {
			t.Errorf("paneRole(%v) = %q, want %q", c.procs, got, c.want)
		}
	}
}

func TestPaneShellPID(t *testing.T) {
	procs := []psProc{{PID: 9, Comm: "claude"}, {PID: 5, Comm: "-zsh"}, {PID: 7, Comm: "zsh"}, {PID: 4, Comm: "login"}}
	if got := paneShellPID(procs); got != 5 {
		t.Errorf("paneShellPID = %d, want 5 (lowest shell pid, login excluded)", got)
	}
	if got := paneShellPID([]psProc{{PID: 9, Comm: "claude"}}); got != 0 {
		t.Errorf("no shell: got %d", got)
	}
}

func TestBuildLayout(t *testing.T) {
	panes := []itermPane{
		{Window: 155, Tab: 1, UUID: "A1", TTY: "ttys001"},
		{Window: 155, Tab: 1, UUID: "B1", TTY: "ttys002"},
		{Window: 155, Tab: 1, UUID: "C1", TTY: "ttys003"},
		{Window: 155, Tab: 2, UUID: "A2", TTY: "ttys004"},
		{Window: 155, Tab: 3, UUID: "A3", TTY: "ttys005"},
		{Window: 155, Tab: 3, UUID: "B3", TTY: "ttys006"},
		{Window: 200, Tab: 1, UUID: "X", TTY: ""},
	}
	byTTY := map[string][]psProc{
		"ttys001": {{PID: 1, Comm: "-zsh"}, {PID: 10, Comm: "claude"}},
		"ttys002": {{PID: 2, Comm: "-zsh"}, {PID: 20, Comm: "entire-tail"}},
		"ttys003": {{PID: 3, Comm: "-zsh"}},
		"ttys004": {{PID: 4, Comm: "-zsh"}},
		"ttys005": {{PID: 5, Comm: "-zsh"}},
		"ttys006": {{PID: 6, Comm: "-zsh"}, {PID: 60, Comm: "entire-tail"}},
	}
	cwds := map[string]string{"C1": "/r/one", "A2": "/r/two", "A3": "/r/three"}
	agents := map[string]string{"A1": "claude:11111111-1111-4111-8111-111111111111"}
	registry := map[string]paneEntry{"B3": {Follow: "T-abc", Agent: AgentAmp, Pid: 60}}

	got := buildLayout(panes, byTTY, cwds, agents, registry)
	if len(got) != 2 {
		t.Fatalf("windows = %d, want 2", len(got))
	}
	w := got[0]
	if w.ITermWindow != 155 || len(w.Tabs) != 3 {
		t.Fatalf("window 0 = %#v", w)
	}
	if tab := w.Tabs[0]; tab.Agent != AgentClaude || tab.Session != "11111111-1111-4111-8111-111111111111" || tab.Cwd != "/r/one" {
		t.Errorf("tab 1 = %#v", tab)
	}
	if got, want := w.Tabs[0].Panes, []string{"agent", "tail", "shell"}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("tab 1 panes = %v", got)
	}
	if tab := w.Tabs[1]; tab.Agent != "" || tab.Session != "" || tab.Cwd != "/r/two" {
		t.Errorf("tab 2 (plain shell) = %#v", tab)
	}
	if tab := w.Tabs[2]; tab.Agent != AgentAmp || tab.Session != "T-abc" || tab.Cwd != "/r/three" {
		t.Errorf("tab 3 (tail only, from registry) = %#v", tab)
	}
	if tab := got[1].Tabs[0]; len(tab.Panes) != 1 || tab.Panes[0] != "shell" {
		t.Errorf("unclassifiable pane must be a shell: %#v", tab)
	}
}

func TestSplitSessionKey(t *testing.T) {
	if a, id := splitSessionKey("claude:abc"); a != AgentClaude || id != "abc" {
		t.Errorf("claude key = %q %q", a, id)
	}
	if a, id := splitSessionKey("amp:T-1"); a != AgentAmp || id != "T-1" {
		t.Errorf("amp key = %q %q", a, id)
	}
	if a, id := splitSessionKey(""); a != "" || id != "" {
		t.Errorf("empty key = %q %q", a, id)
	}
}

func TestPreviousBootDir(t *testing.T) {
	root := t.TempDir()
	write := func(boot string) {
		d := filepath.Join(root, boot)
		_ = os.MkdirAll(d, 0o700)
		_ = os.WriteFile(filepath.Join(d, "snapshot.json"), []byte(`{"boot":1}`), 0o600)
	}
	write("100")
	write("300")
	write("500") // the current boot
	_ = os.MkdirAll(filepath.Join(root, "400"), 0o700) // no snapshot inside
	_ = os.MkdirAll(filepath.Join(root, "junk"), 0o700)

	dir, ok := previousBootDir(root, 500)
	if !ok || filepath.Base(dir) != "300" {
		t.Errorf("previousBootDir = %q %v, want .../300", dir, ok)
	}
	if _, ok := previousBootDir(root, 100); ok {
		t.Error("nothing older than the oldest boot")
	}
}

func TestReadLayoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	snap := layoutSnapshot{Boot: 7, Taken: time.Unix(10, 0).UTC(), Windows: []layoutWindow{{ITermWindow: 1, Tabs: []layoutTab{{Cwd: "/x", Panes: []string{"shell"}}}}}}
	if err := writeJSONAtomic(filepath.Join(dir, "snapshot.json"), snap); err != nil {
		t.Fatal(err)
	}
	got, ok := readLayoutSnapshot(dir)
	if !ok || got.Boot != 7 || len(got.Windows) != 1 || got.Windows[0].Tabs[0].Cwd != "/x" {
		t.Errorf("round trip = %#v %v", got, ok)
	}
	if _, ok := readLayoutSnapshot(t.TempDir()); ok {
		t.Error("missing snapshot must read as not ok")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -run 'TestParseItermPanes|TestParsePSProcs|TestPaneRole|TestPaneShellPID|TestBuildLayout|TestSplitSessionKey|TestPreviousBootDir|TestReadLayoutSnapshot' ./...`
Expected: FAIL, `undefined: parseItermPanes` (and the rest).

- [ ] **Step 3: Implement `layout.go`**

```go
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

// previousBootDir is the newest boot directory older than currentBoot that holds
// a snapshot. The current boot's own snapshot is never a restore source: it
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
		if _, err := os.Stat(filepath.Join(root, e.Name(), "snapshot.json")); err == nil {
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
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -run 'TestParseItermPanes|TestParsePSProcs|TestPaneRole|TestPaneShellPID|TestBuildLayout|TestSplitSessionKey|TestPreviousBootDir|TestReadLayoutSnapshot' ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add layout.go layout_test.go
git commit -m "feat(restore): layout snapshot model and pane classification"
```

---

### Task 2: Snapshot writer and the background saver

**Files:**
- Create: `layoutsnap.go`
- Modify: `main.go` (tail loop, next to `ticker := time.NewTicker(pollInterval)` around line 771)
- Modify: `go.mod` (`golang.org/x/sys` moves to a direct require; run `go mod tidy`)
- Test: `layout_test.go`

**Interfaces:**
- Consumes: everything Task 1 produces; `osaOut` (panelinkd.go), `lsofCwd` (adopt.go), `runningAgentPanes`, `readPaneRegistry`, `prunePanes`, `pidAlive`, `writeJSONAtomic`, `itermAvailable`.
- Produces:
  - package vars `layoutOsa func(string) (string, error)`, `layoutPS func() (string, error)`, `layoutCwd func(int) string`, `layoutAgents func(string) map[string]string`, `layoutBoot func() int64`
  - `const layoutEvery = 30 * time.Second`
  - `func saveLayoutSnapshot(home string, now time.Time) (bool, error)` (true when a file was written)
  - `func startLayoutSaver(home string) (stop func())`

- [ ] **Step 1: Write the failing tests** (append to `layout_test.go`)

```go
func stubLayoutIO(t *testing.T, osa, ps string) {
	t.Helper()
	o1, o2, o3, o4, o5 := layoutOsa, layoutPS, layoutCwd, layoutAgents, layoutBoot
	t.Cleanup(func() { layoutOsa, layoutPS, layoutCwd, layoutAgents, layoutBoot = o1, o2, o3, o4, o5 })
	layoutOsa = func(string) (string, error) { return osa, nil }
	layoutPS = func() (string, error) { return ps, nil }
	layoutCwd = func(pid int) string { return "/r/" + strconv.Itoa(pid) }
	layoutAgents = func(string) map[string]string { return map[string]string{"A1": "claude:11111111-1111-4111-8111-111111111111"} }
	layoutBoot = func() int64 { return 900 }
}

func TestSaveLayoutSnapshotWrites(t *testing.T) {
	home := t.TempDir()
	stubLayoutIO(t, "7\t1\tA1\t/dev/ttys001\n7\t1\tB1\t/dev/ttys002\n",
		"  1 ttys001 -zsh\n 10 ttys001 claude\n  2 ttys002 -zsh\n")
	now := time.Unix(1000, 0)
	wrote, err := saveLayoutSnapshot(home, now)
	if err != nil || !wrote {
		t.Fatalf("save = %v %v", wrote, err)
	}
	snap, ok := readLayoutSnapshot(filepath.Join(layoutRoot(home), "900"))
	if !ok || snap.Boot != 900 || len(snap.Windows) != 1 {
		t.Fatalf("snapshot = %#v %v", snap, ok)
	}
	tab := snap.Windows[0].Tabs[0]
	if tab.Session != "11111111-1111-4111-8111-111111111111" || tab.Cwd != "/r/1" {
		t.Errorf("tab = %#v", tab)
	}
}

func TestSaveLayoutSnapshotSkipsFreshAndEmpty(t *testing.T) {
	home := t.TempDir()
	stubLayoutIO(t, "7\t1\tA1\t/dev/ttys001\n", "  1 ttys001 -zsh\n")
	now := time.Now()
	if wrote, _ := saveLayoutSnapshot(home, now); !wrote {
		t.Fatal("first save should write")
	}
	if wrote, _ := saveLayoutSnapshot(home, now.Add(10*time.Second)); wrote {
		t.Error("a snapshot under 30s old must not be rewritten")
	}

	home2 := t.TempDir()
	stubLayoutIO(t, "", "")
	if wrote, _ := saveLayoutSnapshot(home2, now); wrote {
		t.Error("zero windows (iTerm not running) must not write")
	}
}

func TestSaveLayoutSnapshotKeepsGoodSnapshotOnError(t *testing.T) {
	home := t.TempDir()
	stubLayoutIO(t, "7\t1\tA1\t/dev/ttys001\n", "  1 ttys001 -zsh\n")
	if wrote, _ := saveLayoutSnapshot(home, time.Unix(1000, 0)); !wrote {
		t.Fatal("first save should write")
	}
	layoutOsa = func(string) (string, error) { return "", errors.New("osascript died") }
	if wrote, err := saveLayoutSnapshot(home, time.Unix(2000, 0)); wrote || err == nil {
		t.Errorf("osascript failure: wrote=%v err=%v", wrote, err)
	}
	if snap, ok := readLayoutSnapshot(filepath.Join(layoutRoot(home), "900")); !ok || len(snap.Windows) != 1 {
		t.Error("the good snapshot must survive a failed save")
	}
}
```

Add `"errors"` and `"strconv"` to the test file imports.

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -run TestSaveLayoutSnapshot ./...`
Expected: FAIL, `undefined: layoutOsa`.

- [ ] **Step 3: Implement `layoutsnap.go`**

```go
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// layoutsnap.go is the IO half of the layout snapshot (layout.go). Every running
// tail tries a save every layoutEvery; a non-blocking flock lets one through and
// the freshness check makes the rest no-ops, so N tails cost one save per 30s.
// Every IO step is a package var so `go test` never touches iTerm or the machine.

const layoutEvery = 30 * time.Second

var (
	layoutOsa    = osaOut
	layoutPS     = func() (string, error) { b, err := exec.Command("ps", "-A", "-o", "pid=,tty=,comm=").Output(); return string(b), err }
	layoutCwd    = lsofCwd
	layoutAgents = runningAgentPanes
	layoutBoot   = bootSec
)

// bootSec identifies this boot. iTerm reuses small window ids across boots, so
// the boot is part of the snapshot's key.
func bootSec() int64 {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return 0
	}
	return tv.Sec
}

// saveLayoutSnapshot writes layout/<boot>/snapshot.json. It writes nothing when
// another process holds the lock, the snapshot is under layoutEvery old, iTerm
// lists no windows, or any IO step fails: on shutdown iTerm can quit before the
// tails, and an empty or partial snapshot must never replace a good one.
func saveLayoutSnapshot(home string, now time.Time) (bool, error) {
	boot := layoutBoot()
	if boot == 0 {
		return false, errors.New("boot time unavailable")
	}
	root := layoutRoot(home)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return false, err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false, nil
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	path := filepath.Join(root, strconv.FormatInt(boot, 10), "snapshot.json")
	if fi, err := os.Stat(path); err == nil && now.Sub(fi.ModTime()) < layoutEvery {
		return false, nil
	}
	out, err := layoutOsa(layoutListScript())
	if err != nil {
		return false, err
	}
	panes := parseItermPanes(out)
	if len(panes) == 0 {
		return false, nil
	}
	psOut, err := layoutPS()
	if err != nil {
		return false, err
	}
	byTTY := procsByTTY(parsePSProcs(psOut))
	cwds := map[string]string{}
	for _, p := range panes {
		if pid := paneShellPID(byTTY[p.TTY]); pid != 0 {
			if d := layoutCwd(pid); d != "" {
				cwds[p.UUID] = d
			}
		}
	}
	registry := prunePanes(readPaneRegistry(home), pidAlive)
	snap := layoutSnapshot{Boot: boot, Taken: now.UTC(), Windows: buildLayout(panes, byTTY, cwds, layoutAgents(home), registry)}
	if err := writeJSONAtomic(path, snap); err != nil {
		return false, err
	}
	// The freshness check reads the mtime, so stamp the time we were given.
	_ = os.Chtimes(path, now, now)
	return true, nil
}

// startLayoutSaver saves once now and then every layoutEvery until stop is
// called. Errors are dropped: a snapshot is best-effort and must never disturb
// the tail it runs inside.
func startLayoutSaver(home string) (stop func()) {
	done := make(chan struct{})
	go func() {
		_, _ = saveLayoutSnapshot(home, time.Now())
		t := time.NewTicker(layoutEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				_, _ = saveLayoutSnapshot(home, now)
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
```

Check `prunePanes`'s signature before using it: it is `func prunePanes(panes map[string]paneEntry, alive func(int) bool) map[string]paneEntry` (panelink.go:101), and `pidAlive` is `func(int) bool` (live.go:608).

- [ ] **Step 4: Start the saver from the tail**

In `main.go`, directly after `defer ticker.Stop()` in the tail loop (around line 772), add:

```go
	// Save the iTerm layout while we run, so `entire-tail restore` can rebuild
	// these tabs after a reboot (layoutsnap.go). One process writes per 30s.
	if itermAvailable() {
		defer startLayoutSaver(home)()
	}
```

- [ ] **Step 5: Tidy and run the tests**

Run: `go mod tidy && go vet ./... && go test -race -run 'TestSaveLayoutSnapshot|TestBuildLayout' ./...`
Expected: PASS, and `golang.org/x/sys` listed in the direct `require` block.

- [ ] **Step 6: Commit**

```bash
git add layoutsnap.go layout_test.go main.go go.mod go.sum
git commit -m "feat(restore): save the iTerm layout every 30s while a tail runs"
```

---

### Task 3: Restore planning, window script, idle windows (pure)

**Files:**
- Create: `restore.go`
- Test: `restore_test.go`

**Interfaces:**
- Consumes: `layoutTab`, `layoutWindow`, `itermPane`, `psProc`, `paneRole`, `workspaceScript`, `ampWorkspaceScript`, `paneCommand`, `asEscape`, `shQuote`, `accountEnvPrefix`, `profileByName`, `profileForPath`, `workspaceCwd`, `loadClaudeMeta`, `tildify`, `shortID`, `validSessionID`, `validAmpThreadID`.
- Produces:
  - `type restoreEnv struct { Home, Self, ClaudeBin, AmpBin, TapEnv string; PathForID func(string) string; DirExists func(string) bool }`
  - `type restoreTab struct { Label, Skip, Agent, Tail, Shell string }`
  - `func planTab(t layoutTab, env restoreEnv) restoreTab`
  - `func restoreWindowScript(sh string, tabs []restoreTab) string` (returns "" when every tab is skipped)
  - `func idleWindows(panes []itermPane, byTTY map[string][]psProc, ownTTY string) []int`
  - `func closeWindowsScript(ids []int) string`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRestoreEnv(t *testing.T) (restoreEnv, string) {
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "-r-one")
	_ = os.MkdirAll(proj, 0o700)
	id := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(proj, id+".jsonl")
	_ = os.WriteFile(path, []byte(`{"type":"user","cwd":"/r/one","gitBranch":"main","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	return restoreEnv{
		Home: home, Self: "/bin/entire-tail", ClaudeBin: "/bin/claude", AmpBin: "/bin/amp",
		PathForID: func(s string) string {
			if s == id {
				return path
			}
			return ""
		},
		DirExists: func(d string) bool { return d == "/r/one" || d == "/r/two" },
	}, id
}

func TestPlanTabClaude(t *testing.T) {
	env, id := testRestoreEnv(t)
	got := planTab(layoutTab{Agent: AgentClaude, Session: id, Cwd: "/r/one"}, env)
	if got.Skip != "" {
		t.Fatalf("skipped: %q", got.Skip)
	}
	if !strings.Contains(got.Agent, "--resume '"+id+"'") || !strings.Contains(got.Tail, "--follow-session '"+id+"'") {
		t.Errorf("agent=%q tail=%q", got.Agent, got.Tail)
	}
	if got.Shell == "" || !strings.Contains(got.Label, shortID(id)) {
		t.Errorf("shell=%q label=%q", got.Shell, got.Label)
	}
}

func TestPlanTabMissingTranscriptSkips(t *testing.T) {
	env, _ := testRestoreEnv(t)
	got := planTab(layoutTab{Agent: AgentClaude, Session: "22222222-2222-4222-8222-222222222222", Cwd: "/r/one"}, env)
	if got.Skip != "transcript missing" {
		t.Errorf("skip = %q", got.Skip)
	}
}

func TestPlanTabAmpAndShell(t *testing.T) {
	env, _ := testRestoreEnv(t)
	amp := planTab(layoutTab{Agent: AgentAmp, Session: "T-01a131ed-1c68-776f-8255-616e9ccc86b0", Cwd: "/r/two"}, env)
	if amp.Skip != "" || !strings.Contains(amp.Agent, "threads continue") || !strings.Contains(amp.Tail, "--agent amp") {
		t.Errorf("amp = %#v", amp)
	}
	sh := planTab(layoutTab{Cwd: "/gone"}, env)
	if sh.Skip != "" || sh.Agent != "" || sh.Tail != "" || sh.Shell != "cd "+shQuote(env.Home) {
		t.Errorf("shell tab with a deleted cwd must open in home: %#v", sh)
	}
}

func TestRestoreWindowScript(t *testing.T) {
	tabs := []restoreTab{
		{Agent: "cd '/r/it'\\''s' && claude --resume 'x'", Tail: "tail \"q\"", Shell: "cd '/r'"},
		{Skip: "transcript missing"},
		{Shell: "cd '/r/two'"},
	}
	s := restoreWindowScript("/bin/zsh", tabs)
	if strings.Count(s, "create window") != 1 || strings.Count(s, "create tab") != 1 {
		t.Errorf("want one window and one extra tab:\n%s", s)
	}
	if strings.Count(s, "split vertically") != 1 || strings.Count(s, "split horizontally") != 1 {
		t.Errorf("only the workspace tab is split:\n%s", s)
	}
	if !strings.Contains(s, asEscape(paneCommand("/bin/zsh", `tail "q"`))) {
		t.Errorf("double quotes must be AppleScript-escaped:\n%s", s)
	}
	if !strings.Contains(s, "return id of w") {
		t.Errorf("script must return the new window id:\n%s", s)
	}
	if restoreWindowScript("/bin/zsh", []restoreTab{{Skip: "x"}}) != "" {
		t.Error("all tabs skipped → no script")
	}
}

func TestIdleWindows(t *testing.T) {
	panes := []itermPane{
		{Window: 1, Tab: 1, UUID: "a", TTY: "ttys001"},
		{Window: 1, Tab: 1, UUID: "b", TTY: "ttys002"},
		{Window: 2, Tab: 1, UUID: "c", TTY: "ttys003"},
		{Window: 3, Tab: 1, UUID: "d", TTY: "ttys004"},
	}
	byTTY := map[string][]psProc{
		"ttys001": {{PID: 1, Comm: "login"}, {PID: 2, Comm: "-zsh"}},
		"ttys002": {{PID: 3, Comm: "-zsh"}, {PID: 4, Comm: "entire-tail"}}, // the restore itself
		"ttys003": {{PID: 5, Comm: "-zsh"}, {PID: 6, Comm: "vim"}},
		"ttys004": {{PID: 7, Comm: "-zsh"}},
	}
	got := idleWindows(panes, byTTY, "ttys002")
	if len(got) != 2 || got[0] != 3 || got[1] != 1 {
		t.Errorf("idleWindows = %v, want [3 1] (window 2 busy, own window last)", got)
	}
}

func TestCloseWindowsScript(t *testing.T) {
	s := closeWindowsScript([]int{3, 1})
	if !strings.Contains(s, "close (windows whose id is 3)") || strings.Index(s, "id is 3") > strings.Index(s, "id is 1") {
		t.Errorf("close script keeps order:\n%s", s)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -run 'TestPlanTab|TestRestoreWindowScript|TestIdleWindows|TestCloseWindowsScript' ./...`
Expected: FAIL, `undefined: restoreEnv`.

- [ ] **Step 3: Implement the pure half of `restore.go`**

```go
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
	PathForID                            func(string) string
	DirExists                            func(string) bool
}

// restoreTab is one tab to open. Skip non-empty means it is reported, not
// opened. Agent and Tail empty means a plain shell tab running Shell.
type restoreTab struct {
	Label, Skip         string
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
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -run 'TestPlanTab|TestRestoreWindowScript|TestIdleWindows|TestCloseWindowsScript' ./...`
Expected: PASS. If `TestPlanTabClaude` fails on the label because `workspaceCwd` reads a different cwd, check `sessionCwdNow` in hunk.go and adjust the fixture line, not the code.

- [ ] **Step 5: Commit**

```bash
git add restore.go restore_test.go
git commit -m "feat(restore): plan tabs and build the new-window script"
```

---

### Task 4: `entire-tail restore` command

**Files:**
- Modify: `restore.go`
- Modify: `config.go` (Action enum near line 79, `Config` near line 40, `parseCLI` near line 195)
- Modify: `main.go` (dispatch, next to `case ActionTrails:`)
- Test: `restore_test.go`, `config_test.go`

**Interfaces:**
- Consumes: Tasks 1-3; `osaOut`, `osaRun`, `selfPath`, `paneBin`, `paneShell`, `resolveClaudeBin`, `tapEnvPrefix`, `tapBaseURL`, `localPathForID`, `isDir`.
- Produces:
  - `ActionRestore`, `Config.RestoreArgs []string`
  - package vars `restoreOsa func(string) (string, error)`, `restorePS func() (string, error)`, `restoreOwnTTY func() string`
  - `func pendingRestore(home string) (dir string, tabs int)` (0 tabs when nothing to restore)
  - `func runRestore(args []string, cfg Config, home string, in io.Reader, out io.Writer) error`

- [ ] **Step 1: Write the failing tests** (append to `restore_test.go`; add `bufio`/`bytes`/`strconv`/`time` imports as needed)

```go
func seedPreviousBoot(t *testing.T, home string, tabs []layoutTab) string {
	t.Helper()
	dir := filepath.Join(layoutRoot(home), "100")
	snap := layoutSnapshot{Boot: 100, Windows: []layoutWindow{{ITermWindow: 5, Tabs: tabs}}}
	if err := writeJSONAtomic(filepath.Join(dir, "snapshot.json"), snap); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stubRestoreIO(t *testing.T) *[]string {
	t.Helper()
	var scripts []string
	o1, o2, o3, o4 := restoreOsa, restorePS, restoreOwnTTY, layoutBoot
	t.Cleanup(func() { restoreOsa, restorePS, restoreOwnTTY, layoutBoot = o1, o2, o3, o4 })
	restoreOsa = func(s string) (string, error) {
		scripts = append(scripts, s)
		if strings.Contains(s, "create window") {
			return "77\n", nil
		}
		if strings.Contains(s, "unique id of s") {
			return "5\t1\tR\t/dev/ttys009\n77\t1\tN\t/dev/ttys010\n", nil
		}
		return "", nil
	}
	restorePS = func() (string, error) { return "  1 ttys009 -zsh\n  2 ttys010 -zsh\n  3 ttys010 claude\n", nil }
	restoreOwnTTY = func() string { return "ttys009" }
	layoutBoot = func() int64 { return 200 }
	return &scripts
}

func TestRunRestoreOpensWindowAndMarks(t *testing.T) {
	home := t.TempDir()
	dir := seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	scripts := stubRestoreIO(t)
	var out bytes.Buffer
	if err := runRestore(nil, Config{}, home, strings.NewReader("n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "✔") {
		t.Errorf("report:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "restored")); err != nil {
		t.Error("restored marker not written")
	}
	for _, s := range *scripts {
		if strings.Contains(s, "close (windows") {
			t.Error("answered n: nothing may be closed")
		}
	}
}

func TestRunRestoreRefusesTwiceWithoutForce(t *testing.T) {
	home := t.TempDir()
	seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	stubRestoreIO(t)
	var out bytes.Buffer
	_ = runRestore(nil, Config{}, home, strings.NewReader("n\n"), &out)
	out.Reset()
	scripts := stubRestoreIO(t)
	if err := runRestore(nil, Config{}, home, strings.NewReader("n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already restored") || len(*scripts) != 0 {
		t.Errorf("second run must refuse: %q, %d scripts", out.String(), len(*scripts))
	}
	out.Reset()
	if err := runRestore([]string{"--force"}, Config{}, home, strings.NewReader("n\n"), &out); err != nil || len(*scripts) == 0 {
		t.Errorf("--force must restore again: %v", err)
	}
}

func TestRunRestoreClosesOnYes(t *testing.T) {
	home := t.TempDir()
	seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	scripts := stubRestoreIO(t)
	var out bytes.Buffer
	if err := runRestore(nil, Config{}, home, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	last := (*scripts)[len(*scripts)-1]
	if !strings.Contains(last, "close (windows whose id is 5)") || strings.Contains(last, "id is 77") {
		t.Errorf("must close only the old idle window 5:\n%s", last)
	}
}

func TestRunRestoreDryRunAndDismiss(t *testing.T) {
	home := t.TempDir()
	dir := seedPreviousBoot(t, home, []layoutTab{{Cwd: home, Panes: []string{"shell"}}})
	scripts := stubRestoreIO(t)
	var out bytes.Buffer
	_ = runRestore([]string{"--dry-run"}, Config{}, home, strings.NewReader(""), &out)
	if len(*scripts) != 0 {
		t.Error("--dry-run must not run osascript")
	}
	if _, err := os.Stat(filepath.Join(dir, "restored")); err == nil {
		t.Error("--dry-run must not mark")
	}
	_ = runRestore([]string{"--dismiss"}, Config{}, home, strings.NewReader(""), &out)
	if _, tabs := pendingRestore(home); tabs != 0 {
		t.Error("--dismiss must clear the pending restore")
	}
}

func TestRunRestoreNothingToDo(t *testing.T) {
	home := t.TempDir()
	stubRestoreIO(t)
	var out bytes.Buffer
	if err := runRestore(nil, Config{}, home, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no saved layout") {
		t.Errorf("out = %q", out.String())
	}
}
```

Append to `config_test.go`:

```go
func TestParseCLIRestore(t *testing.T) {
	c, act, err := parseCLI([]string{"restore", "--force"}, func(string) string { return "" }, savedPrefs{})
	if err != nil || act != ActionRestore || len(c.RestoreArgs) != 1 || c.RestoreArgs[0] != "--force" {
		t.Errorf("parseCLI restore = %v %v %v", c.RestoreArgs, act, err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -run 'TestRunRestore|TestParseCLIRestore' ./...`
Expected: FAIL, `undefined: runRestore`, `undefined: ActionRestore`.

- [ ] **Step 3: Add the action**

`config.go`: add `ActionRestore // \`entire-tail restore\`: rebuild pre-reboot iTerm tabs` after `ActionTrails` in the enum; add `RestoreArgs []string` next to `TrailsArgs` in `Config`; in `parseCLI`, after the `trails` block:

```go
	if len(args) > 0 && args[0] == "restore" {
		c.RestoreArgs = append([]string(nil), args[1:]...)
		return c, ActionRestore, nil
	}
```

`main.go`, after `case ActionTrails:`'s block:

```go
	case ActionRestore:
		if err := runRestore(cfg.RestoreArgs, cfg, firstNonEmpty(os.Getenv("HOME"), mustHome()), os.Stdin, os.Stdout); err != nil {
			die("restore: " + err.Error())
		}
		return
```

- [ ] **Step 4: Implement the command** (append to `restore.go`, extend imports with `bufio`, `errors`, `io`, `os`, `os/exec`, `path/filepath`, `strconv`)

```go
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

	// The windows open BEFORE we create any: only these may be offered for closing.
	before, _ := restoreOsa(layoutListScript())
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
```

Note: `stubRestoreIO` returns window 77 from the list call too, because the stub answers `layoutListScript()` with both windows; window 77 has a `claude` on its tty, so it is busy and never closed. That is what `TestRunRestoreClosesOnYes` pins.

- [ ] **Step 5: Run the tests**

Run: `go vet ./... && go test -race -run 'TestRunRestore|TestParseCLIRestore|TestPlanTab' ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add restore.go restore_test.go config.go config_test.go main.go
git commit -m "feat(restore): entire-tail restore command"
```

---

### Task 5: Tree footer hint and `R` key, docs

**Files:**
- Modify: `tree.go` (`treeUI` near line 638, `updateTree` normal-mode switch near line 750, `composeFooter` near line 1073, `treeResult` consts near line 1178, `runClaudeTree` loop near line 1255)
- Modify: `picker.go` (`resolveTreeChoice`, near line 168)
- Modify: `README.md`, `CLAUDE.md`
- Test: `tree_test.go`

**Interfaces:**
- Consumes: `pendingRestore`, `runRestore`.
- Produces: `treeUI.RestoreTabs int`, `treeUI.Restore bool`, `treeRestore treeResult`.

- [ ] **Step 1: Write the failing tests** (append to `tree_test.go`)

```go
func TestTreeRestoreKey(t *testing.T) {
	ui := treeUI{RestoreTabs: 7}
	ui = updateTree(ui, kRune, 'R')
	if !ui.Restore {
		t.Error("R with a pending restore must request it")
	}
	ui = updateTree(treeUI{}, kRune, 'R')
	if ui.Restore {
		t.Error("R with nothing to restore must do nothing")
	}
}

func TestTreeFooterRestoreHint(t *testing.T) {
	if f := composeFooter(treeUI{RestoreTabs: 7}); !strings.Contains(f, "⟲ 7 tabs from before the reboot · R to restore") {
		t.Errorf("footer = %q", f)
	}
	if f := composeFooter(treeUI{}); strings.Contains(f, "⟲") {
		t.Errorf("no hint without a pending restore: %q", f)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -run 'TestTreeRestoreKey|TestTreeFooterRestoreHint' ./...`
Expected: FAIL, `unknown field RestoreTabs`.

- [ ] **Step 3: Implement**

`tree.go`, in `treeUI` after `Workspace`:

```go
	RestoreTabs int  // tabs in the pre-reboot layout snapshot not yet restored (restore.go)
	Restore     bool // 'R' → run `entire-tail restore`; ends the loop
```

In `updateTree`'s normal-mode switch, after the `'q', 'Q'` case:

```go
		case 'R':
			ui.Restore = ui.RestoreTabs > 0
```

In `composeFooter`, before `return out`:

```go
	if ui.RestoreTabs > 0 {
		out += fmt.Sprintf(" · ⟲ %d tabs from before the reboot · R to restore", ui.RestoreTabs)
	}
```

Append `treeRestore` AFTER `treeNone` in the `treeResult` consts, so the existing values don't move:

```go
	treeNone
	treeRestore // rebuild the pre-reboot iTerm tabs (R)
```

In `runClaudeTree`, set the count where `ui` is created (`ui := treeUI{Tree: tree, Theme: theme}`):

```go
	ui := treeUI{Tree: tree, Theme: theme}
	if itermAvailable() {
		_, ui.RestoreTabs = pendingRestore(home)
	}
```

and in the loop, after the `ui.Quit` check:

```go
		if ui.Restore {
			return treeChoice{Result: treeRestore}
		}
```

`picker.go`, `resolveTreeChoice`, a new first case:

```go
	case treeRestore:
		if err := runRestore(nil, Config{ClaudeBin: claudeBin}, home, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "entire-tail: restore: "+err.Error())
		}
		os.Exit(0)
```

Check `Config` has a `ClaudeBin` field (config.go `resolveClaudeBin(c Config, …)` reads it). If the field has a different name, use that name; if `resolveClaudeBin` needs `ClaudeBinSet` too, set it to `claudeBin != ""`.

- [ ] **Step 4: Run the full suite**

Run: `go vet ./... && go test -race ./...`
Expected: PASS, no golden diffs.

- [ ] **Step 5: Docs**

`README.md`: add a "Restore after a reboot" section: running tails save the layout every 30s; `entire-tail restore` reopens every pre-reboot tab in new windows; flags `--pick`, `--force`, `--dry-run`, `--dismiss`; the tree's `R`; only layouts saved after this feature shipped can be restored.

`CLAUDE.md`, Architecture list: add a `layout.go` / `layoutsnap.go` / `restore.go` entry covering: restored panes get new UUIDs (measured 2026-10-13, 298 vs 0), so the snapshot is keyed by boot and tab order; the running check before `tell application "iTerm2"` (it launches iTerm); `character id 9` because `tab` is iTerm's class inside the tell; never write an empty snapshot; one writer per 30s via flock; restore never types into a pane and closes only all-idle windows, its own last, after `y`.

- [ ] **Step 6: Commit**

```bash
git add tree.go tree_test.go picker.go README.md CLAUDE.md
git commit -m "feat(restore): tree footer hint and R key, docs"
```

---

### Task 6: Live check

**Files:** none.

- [ ] **Step 1: Build and install:** `./install.sh`
- [ ] **Step 2: Start any tail in iTerm, wait 35s, then:** `cat ~/.claude/entire-tail/layout/$(sysctl -n kern.boottime | sed 's/.*sec = \([0-9]*\).*/\1/')/snapshot.json`
  Expected: every open window, tabs in order, workspace tabs carrying their session id.
- [ ] **Step 3: Simulate a previous boot:** `cp -R ~/.claude/entire-tail/layout/<current-boot> ~/.claude/entire-tail/layout/1`
- [ ] **Step 4: Dry run:** `entire-tail restore --dry-run` lists every tab as `would open`.
- [ ] **Step 5: Real run:** `entire-tail restore`, answer `n`. A new window opens with every tab: agents resumed, tails following, shells cd'd. Then `rm -r ~/.claude/entire-tail/layout/1`.
