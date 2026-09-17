package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// panelinkd.go — the IO half of the pane link: the watcher daemon, its Python
// child, and the `entire-tail link` subcommand.
//
// The daemon exists only while tails do. It is started on demand by the first
// entire-tail that registers a pane and exits once no live pane has been
// registered for a minute, so there is no LaunchAgent to install and nothing
// left running on a machine that has stopped using it. That is the difference
// from the tap, which has to outlive its sessions because they bake its URL in
// at launch; a focus watcher is worthless without a tail on screen.

//go:embed panelink.py
var paneLinkPy string

const (
	paneLinkIdleExit      = 60 * time.Second // no live panes for this long → exit
	paneLinkClaudeRefresh = 5 * time.Second  // re-resolve running claudes
	paneLinkTick          = 2 * time.Second
	paneLinkMaxRestarts   = 3
)

// paneLinkState is the daemon's advertised presence. There is no port to probe,
// so liveness is the pid plus a name check — a recycled pid belonging to
// something else must not read as a running watcher.
type paneLinkState struct {
	Pid     int    `json:"pid"`
	Python  string `json:"python"`
	Started string `json:"started"`
	// Connected is the watcher's own report that it reached iTerm's API. The
	// daemon being alive says nothing: with the API switched off the child
	// starts, fails to connect and dies, on a loop. Only the child can tell us
	// the difference, so it says so (see panelink.py's "!ready").
	Connected bool `json:"connected"`
}

// paneLinkReadyLine is the child's "I am connected" marker. A session id is a
// UUID, so the "!" prefix cannot collide with one.
const paneLinkReadyLine = "!ready"

func paneLinkStatePath(home string) string { return filepath.Join(paneLinkDir(home), "daemon.json") }
func paneLinkLockPath(home string) string  { return filepath.Join(paneLinkDir(home), "daemon.lock") }
func paneLinkLogPath(home string) string   { return filepath.Join(paneLinkDir(home), "daemon.log") }
func paneLinkScriptPath(home string) string {
	return filepath.Join(paneLinkDir(home), "panelink.py")
}
func paneLinkVenvDir(home string) string { return filepath.Join(paneLinkDir(home), "venv") }
func paneLinkVenvPython(home string) string {
	return filepath.Join(paneLinkVenvDir(home), "bin", "python3")
}

// Package vars so `go test` never spawns a python, drives iTerm, or reaches the
// network. Same rule as the tap's launchctl stubs: a test must not touch the
// machine's real state.
var (
	// paneLinkRun returns what the script decided ("stale", "no-window", or the
	// number of tabs switched) rather than just an error: the script is where
	// both guards live, so its verdict is the only honest thing to log.
	paneLinkRun    = func(script string) (string, error) { return osaOut(script) }
	paneLinkPipRun = func(python, dir string) error { return runPaneLinkPip(python, dir) }
)

func readPaneLinkState(home string) (paneLinkState, bool) {
	b, err := os.ReadFile(paneLinkStatePath(home))
	if err != nil {
		return paneLinkState{}, false
	}
	var st paneLinkState
	if json.Unmarshal(b, &st) != nil || st.Pid == 0 {
		return paneLinkState{}, false
	}
	return st, true
}

// paneLinkRunning reports whether the daemon named in the state file is really
// there. The pid alone is not enough — pids are recycled — so the process is
// confirmed to be an entire-tail before it is believed.
func paneLinkRunning(st paneLinkState) bool {
	if st.Pid == 0 || !pidAlive(st.Pid) {
		return false
	}
	return strings.Contains(psCommand(st.Pid), "entire-tail")
}

// acquirePaneLinkLock takes the daemon slot, or reports that someone else holds
// it. Two tails starting together both see "no daemon" and both spawn one; the
// loser exits here instead of running a second watcher that would double every
// tab switch. A lock whose pid is dead is stale and gets broken.
func acquirePaneLinkLock(home string) bool {
	p := paneLinkLockPath(home)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return false
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return true
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return false
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && pidAlive(pid) {
			return false
		}
		_ = os.Remove(p) // stale lock from a killed daemon
	}
	return false
}

func releasePaneLinkLock(home string) { _ = os.Remove(paneLinkLockPath(home)) }

// runningClaudePanes maps a running claude's iTerm session id → the transcript
// it is writing. This is the half nobody registers: claude knows nothing about
// entire-tail, so the daemon works it out with the same join nearby.go performs,
// minus the "same tab" filter (a claude in any window is a candidate here).
//
// Re-run every few seconds rather than cached forever, which is what makes the
// pairing self-healing: restart claude in a different tab and the link re-forms
// with no action from the tail, whose own registration never changed.
func runningClaudePanes(home string) map[string]string {
	out := map[string]string{}
	if !pickerToolsAvailable() {
		return out
	}
	for _, p := range claudeProcs() {
		uuid := paneUUIDFromSessionID(p.itermID)
		if uuid == "" {
			continue
		}
		path := resolveClaudeSession(home, lsofCwd(p.pid), psCommand(p.pid))
		if path == "" {
			continue
		}
		if id := sessionIDFromPath(path); id != "" {
			out[uuid] = id
		}
	}
	return out
}

// osaOut runs an AppleScript and returns its output (osaRun discards it).
func osaOut(script string) (string, error) {
	cmd := exec.Command("osascript", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	return string(out), err
}

// paneWindows maps each of the given iTerm session ids to the id of the window
// holding it. Ids not on screen are simply absent.
var paneWindows = func(uuids []string) map[string]string {
	quoted := make([]string, 0, len(uuids))
	for _, u := range uuids {
		quoted = append(quoted, `"`+asEscape(u)+`"`)
	}
	script := "tell application \"iTerm2\"\n" +
		"\tset wantIDs to {" + strings.Join(quoted, ", ") + "}\n" +
		"\tset out to \"\"\n" +
		"\trepeat with w in windows\n" +
		"\t\trepeat with t in tabs of w\n" +
		"\t\t\trepeat with s in sessions of t\n" +
		"\t\t\t\tif wantIDs contains (id of s) then set out to out & (id of s) & \" \" & (id of w as text) & linefeed\n" +
		"\t\t\tend repeat\n" +
		"\t\tend repeat\n" +
		"\tend repeat\n" +
		"\treturn out\n" +
		"end tell\n"
	res := map[string]string{}
	out, err := osaOut(script)
	if err != nil {
		return res
	}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(strings.TrimSpace(line)); len(f) == 2 {
			res[f[0]] = f[1]
		}
	}
	return res
}

// paneNamesScript reads the tab names of the given iTerm sessions, one
// "<uuid> <name>" line each.
//
// The separator is a SPACE, and that is not laziness. A session id is a UUID
// and contains none, so the first space splits the line exactly — while the
// obvious choice of a tab character cannot be written here at all: inside
// `tell application "iTerm2"`, `tab` is iTerm's own tab CLASS, and the script
// silently emits the literal word instead of a separator. Verified the hard way:
//
//	5A50D36E-…-50EFED17C27Dtab✳ ROADMAP 1300 push_ci permission (python3)
func paneNamesScript(uuids []string) string {
	quoted := make([]string, 0, len(uuids))
	for _, u := range uuids {
		quoted = append(quoted, `"`+asEscape(u)+`"`)
	}
	return "tell application \"iTerm2\"\n" +
		"\tset wantIDs to {" + strings.Join(quoted, ", ") + "}\n" +
		"\tset out to \"\"\n" +
		"\trepeat with w in windows\n" +
		"\t\trepeat with t in tabs of w\n" +
		"\t\t\trepeat with s in sessions of t\n" +
		"\t\t\t\tif wantIDs contains (id of s) then set out to out & (id of s) & \" \" & (name of s) & linefeed\n" +
		"\t\t\tend repeat\n" +
		"\t\tend repeat\n" +
		"\tend repeat\n" +
		"\treturn out\n" +
		"end tell\n"
}

// parsePaneNames turns that output into a map. A name keeps its own spaces and
// glyphs — only the first space is a separator.
func parsePaneNames(out string) map[string]string {
	res := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		uuid, name, ok := strings.Cut(line, " ")
		if !ok || uuid == "" || strings.TrimSpace(name) == "" {
			continue
		}
		res[uuid] = strings.TrimSpace(name)
	}
	return res
}

// paneNames maps iTerm session ids to their current tab names.
var paneNames = func(uuids []string) map[string]string {
	if len(uuids) == 0 {
		return map[string]string{}
	}
	out, err := osaOut(paneNamesScript(uuids))
	if err != nil {
		return map[string]string{}
	}
	return parsePaneNames(out)
}

// syncPaneTitles publishes, for each live tail, the current tab name of the
// agent it is following. The tail itself does the setting — see titleOSC.
//
// Polled on the daemon tick rather than driven by focus events, because a title
// changes while you are looking somewhere else, and watching another agent go
// from ✳ to ◐ without switching to it is most of the value. A couple of seconds
// of lag on a title is invisible, which is why polling is fine here and was not
// for focus.
func syncPaneTitles(home string, panes map[string]paneEntry, claudes map[string]string) {
	if len(panes) == 0 {
		return
	}
	uuids := make([]string, 0, len(claudes))
	for uuid := range claudes {
		uuids = append(uuids, uuid)
	}
	titles := paneTitleTargets(panes, claudes, paneNames(uuids))
	for tailUUID := range panes {
		if title, ok := titles[tailUUID]; ok {
			writePaneTitle(home, tailUUID, stripJobSuffix(title))
		} else {
			removePaneTitle(home, tailUUID)
		}
	}
}

// paneLinkPairExists reports whether there is something to link RIGHT NOW: a
// running claude writing the session this tail is about to follow, sitting in a
// different iTerm window from us.
//
// This is the whole condition the one-time offer hangs on, and the different-
// window part is what keeps the question away from the 3-pane workspace, where
// both sides are already on screen and the feature would do nothing. It costs
// one osascript call, paid only on a run that is otherwise eligible to ask —
// interactive, Claude, unanswered — so at most once per machine.
func paneLinkPairExists(home, sessionID, ownUUID string) bool {
	if ownUUID == "" || sessionID == "" {
		return false
	}
	var mates []string
	for uuid, sess := range runningClaudePanes(home) {
		if sess == sessionID {
			mates = append(mates, uuid)
		}
	}
	if len(mates) == 0 {
		return false
	}
	wins := paneWindows(append([]string{ownUUID}, mates...))
	own := wins[ownUUID]
	if own == "" {
		return false
	}
	for _, m := range mates {
		if w, ok := wins[m]; ok && w != own {
			return true
		}
	}
	return false
}

// paneLinkEvent is one line from the Python child, or its death.
type paneLinkEvent struct {
	uuid string
	err  error // non-nil: the child exited, uuid is empty
}

// startPaneLinkChild runs the watcher and streams focused session ids.
func startPaneLinkChild(python, script string) (*exec.Cmd, <-chan paneLinkEvent, error) {
	cmd := exec.Command(python, script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	ch := make(chan paneLinkEvent, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				ch <- paneLinkEvent{uuid: line}
			}
		}
		err := cmd.Wait()
		if err == nil {
			err = io.EOF
		}
		ch <- paneLinkEvent{err: err}
		close(ch)
	}()
	return cmd, ch, nil
}

// runPaneLinkDaemon is `entire-tail link start`: follow focus, switch the
// partner's tab, exit when no tail is left.
func runPaneLinkDaemon(home string, out io.Writer) error {
	logf := func(f string, a ...any) {
		fmt.Fprintf(out, "%s entire-tail link: "+f+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
	}
	if st, ok := readPaneLinkState(home); ok && paneLinkRunning(st) {
		logf("already running (pid %d)", st.Pid)
		return nil
	}
	if !acquirePaneLinkLock(home) {
		logf("another watcher is starting")
		return nil
	}
	defer releasePaneLinkLock(home)

	python := paneLinkVenvPython(home)
	if !isFile(python) {
		return fmt.Errorf("no watcher venv at %s — run `entire-tail link install`", paneLinkVenvDir(home))
	}
	// Rewrite the script every start so a binary upgrade ships its own version
	// rather than leaving whatever an older install left on disk.
	if err := os.WriteFile(paneLinkScriptPath(home), []byte(paneLinkPy), 0o700); err != nil {
		return fmt.Errorf("cannot write watcher script: %w", err)
	}
	st := paneLinkState{Pid: os.Getpid(), Python: python, Started: time.Now().Format(time.RFC3339)}
	if err := writeJSONAtomic(paneLinkStatePath(home), st); err != nil {
		return fmt.Errorf("cannot publish daemon state: %w", err)
	}
	defer os.Remove(paneLinkStatePath(home))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	claudes := runningClaudePanes(home)
	claudesAt := time.Now()
	idleSince := time.Now()
	guard := newSwitchGuard()
	ticker := time.NewTicker(paneLinkTick)
	defer ticker.Stop()

	for restarts := 0; ; restarts++ {
		if restarts > 0 {
			if restarts > paneLinkMaxRestarts {
				return fmt.Errorf("watcher failed %d times, giving up", restarts-1)
			}
			// Back off rather than hammering. The usual cause is iTerm quitting,
			// and the second most likely is the user clicking Deny on the API
			// permission prompt — which must NOT become a respawn loop, because
			// every attempt re-prompts them.
			time.Sleep(time.Duration(restarts) * 2 * time.Second)
		}
		cmd, events, err := startPaneLinkChild(python, paneLinkScriptPath(home))
		if err != nil {
			return fmt.Errorf("cannot start watcher: %w", err)
		}
		logf("watching (pid %d, python %s)", os.Getpid(), python)

		alive := true
		for alive {
			select {
			case <-stop:
				_ = cmd.Process.Kill()
				logf("shutting down")
				return nil

			case ev := <-events:
				if ev.err != nil {
					logf("watcher exited: %v", ev.err)
					st.Connected = false
					_ = writeJSONAtomic(paneLinkStatePath(home), st)
					alive = false
					break
				}
				if ev.uuid == paneLinkReadyLine {
					restarts = 0 // a working connection clears the failure budget
					st.Connected = true
					_ = writeJSONAtomic(paneLinkStatePath(home), st)
					logf("connected to iTerm")
					continue
				}
				// Our own tab switch comes back as an event. Dropping it here is
				// what stops two linked pairs flipping tabs at each other forever.
				if guard.isEcho(ev.uuid, time.Now().UnixMilli()) {
					continue
				}
				if time.Since(claudesAt) > paneLinkClaudeRefresh {
					claudes, claudesAt = runningClaudePanes(home), time.Now()
				}
				panes := prunePanes(readPaneRegistry(home), pidAlive)
				partners := linkPartners(ev.uuid, panes, claudes)
				if len(partners) == 0 {
					// An unknown pane may simply be a claude we haven't resolved
					// yet (started since the last refresh); re-resolve once and
					// retry before concluding there is nothing to do.
					if _, known := panes[ev.uuid]; !known && time.Since(claudesAt) > time.Second {
						claudes, claudesAt = runningClaudePanes(home), time.Now()
						partners = linkPartners(ev.uuid, panes, claudes)
					}
				}
				if len(partners) == 0 {
					continue
				}
				// Recorded BEFORE the switch: the event it provokes can arrive
				// while osascript is still returning.
				guard.remember(partners, time.Now().UnixMilli())
				// Logged unconditionally, and with the script's own verdict. This
				// is the only window onto what the daemon does to someone's
				// screen; without it a report of "it keeps switching" cannot be
				// told apart from anything else moving tabs, which cost a whole
				// debugging round. "stale" means the guards declined — common and
				// healthy, not a failure.
				res, err := paneLinkRun(linkScript(ev.uuid, partners))
				switch {
				case err != nil:
					logf("%s → %s: failed: %v", ev.uuid, strings.Join(partners, ", "), err)
				default:
					logf("%s → %s: %s", ev.uuid, strings.Join(partners, ", "), strings.TrimSpace(res))
				}

			case <-ticker.C:
				if panes := prunePanes(readPaneRegistry(home), pidAlive); len(panes) > 0 {
					idleSince = time.Now()
					if time.Since(claudesAt) > paneLinkClaudeRefresh {
						claudes, claudesAt = runningClaudePanes(home), time.Now()
					}
					syncPaneTitles(home, panes, claudes)
					continue
				}
				if time.Since(idleSince) > paneLinkIdleExit {
					_ = cmd.Process.Kill()
					logf("no tails for %s — exiting", paneLinkIdleExit)
					return nil
				}
			}
		}
	}
}

// ensurePaneLinkDaemon starts the watcher if the user enabled the link and none
// is running. Best-effort and silent: a tail must never fail, stall, or print a
// wall of diagnostics because a convenience daemon didn't come up.
func ensurePaneLinkDaemon(home string) {
	if !paneLinkEnabled(home) {
		return
	}
	if st, ok := readPaneLinkState(home); ok && paneLinkRunning(st) {
		return
	}
	if !isFile(paneLinkVenvPython(home)) {
		return // never set up, or the venv was removed — `link install` fixes it
	}
	if !itermAPIEnabled() {
		return // the child could only fail to connect, three times, per tail start
	}
	log, err := os.OpenFile(paneLinkLogPath(home), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer log.Close()
	cmd := exec.Command(selfPath(), "link", "start")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlive this terminal
	if cmd.Start() != nil {
		return
	}
	go func() { _ = cmd.Wait() }() // reap it rather than leaving a zombie behind
}

// stopPaneLinkDaemon signals a running watcher to exit and waits for it to go.
//
// The wait is the point. SIGTERM returns immediately, but the daemon still has
// to unwind and delete its state file — and a `link stop && link start` in that
// gap has the new daemon read the dying one's state, decide one is already
// running, and exit, leaving none at all.
func stopPaneLinkDaemon(home string) bool {
	st, ok := readPaneLinkState(home)
	if !ok || !paneLinkRunning(st) {
		return false
	}
	if syscall.Kill(st.Pid, syscall.SIGTERM) != nil {
		return false
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(st.Pid) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// ── setup ──

// paneLinkPythonCandidates is the preference order for the interpreter the venv
// is built from: stable paths first, PATH last.
func paneLinkPythonCandidates() []string {
	c := []string{"/opt/homebrew/bin/python3", "/usr/local/bin/python3", "/usr/bin/python3"}
	if p, err := exec.LookPath("python3"); err == nil {
		c = append(c, p)
	}
	return c
}

// itermAPIEnabled reports whether iTerm2's Python API is switched on. It is OFF
// by default and there is no way around it: with it off nothing listens, the
// watcher's connect is refused, and the feature cannot work at all.
//
// Deliberately not flipped for the user. The preference is read by iTerm at
// launch, so writing it would need iTerm restarted — which would take every
// session they have running with it. A checkbox they tick costs them two
// seconds; a restart could cost them an afternoon.
func itermAPIEnabled() bool {
	out, err := exec.Command("defaults", "read", "com.googlecode.iterm2", "EnableAPIServer").Output()
	if err != nil {
		return false // key absent = the default, which is off
	}
	return strings.TrimSpace(string(out)) == "1"
}

func runPaneLinkPip(python, dir string) error {
	if out, err := exec.Command(python, "-m", "venv", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("venv: %v: %s", err, strings.TrimSpace(string(out)))
	}
	pip := filepath.Join(dir, "bin", "pip")
	if out, err := exec.Command(pip, "install", "--quiet", "iterm2").CombinedOutput(); err != nil {
		return fmt.Errorf("pip install iterm2: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// installPaneLink builds the venv and starts the watcher. It reports what it
// did, because this is the one part of the feature that reaches the network and
// creates something outside our own directory tree.
func installPaneLink(home string, out io.Writer) error {
	// Checked before anything is built: without the API there is nothing to
	// connect to, and a venv the user then has to be told is useless is worse
	// than a clear instruction and no venv.
	if !itermAPIEnabled() {
		return fmt.Errorf("iTerm's Python API is off, so the watcher cannot connect.\n" +
			"  Turn it on: iTerm2 → Settings → General → Magic → \"Enable Python API\"\n" +
			"  then re-run `entire-tail link install`. (No restart needed; leave your sessions alone.)")
	}
	python := pickPython(paneLinkPythonCandidates(), isFile)
	if python == "" {
		return fmt.Errorf("no python3 found — install one, then re-run `entire-tail link install`")
	}
	fmt.Fprintf(out, "entire-tail link: building watcher venv with %s\n", python)
	if err := paneLinkPipRun(python, paneLinkVenvDir(home)); err != nil {
		return err
	}
	if err := os.WriteFile(paneLinkScriptPath(home), []byte(paneLinkPy), 0o700); err != nil {
		return err
	}
	recordPaneLinkChoice(home, "yes")
	ensurePaneLinkDaemon(home)
	// Confirm rather than assume. Reporting a watcher that isn't running is
	// worse than reporting a failure — the lesson tap install was built on.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := readPaneLinkState(home); ok && paneLinkRunning(st) && st.Connected {
			fmt.Fprintf(out, "entire-tail link: watching (pid %d), connected to iTerm.\n", st.Pid)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !itermAPIEnabled() {
		return fmt.Errorf("iTerm's Python API is off, so the watcher cannot connect.\n" +
			"  Turn it on: iTerm2 → Settings → General → Magic → \"Enable Python API\"\n" +
			"  then re-run `entire-tail link install`. (No restart needed; leave your sessions alone.)")
	}
	return fmt.Errorf("watcher did not connect — see %s", paneLinkLogPath(home))
}

func uninstallPaneLink(home string, out io.Writer) error {
	stopPaneLinkDaemon(home)
	recordPaneLinkChoice(home, "no")
	if err := os.RemoveAll(paneLinkVenvDir(home)); err != nil {
		return err
	}
	_ = os.RemoveAll(paneRegistryDir(home))
	fmt.Fprintln(out, "entire-tail link: removed the watcher venv and stopped the daemon.")
	return nil
}

// offerPaneLink is the one-time prompt. Either answer is recorded so it is never
// asked again; a failed setup is NOT recorded, so a network blip doesn't cost
// the user the feature permanently.
func offerPaneLink(home string, in *bufio.Reader, out io.Writer) {
	fmt.Fprint(out, "entire-tail: link this tail to its claude, so selecting one switches "+
		"the other window to its tab? (one-time setup, ~10s) [y/N] ")
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		if err := installPaneLink(home, out); err != nil {
			fmt.Fprintln(out, "entire-tail link: setup failed: "+err.Error())
			return
		}
	default:
		recordPaneLinkChoice(home, "no")
		fmt.Fprintln(out, "entire-tail: skipped. `entire-tail link install` enables it later.")
	}
}

// runPaneLink dispatches `entire-tail link <sub>`.
func runPaneLink(args []string, home string, out io.Writer) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "start":
		return runPaneLinkDaemon(home, out)
	case "install":
		return installPaneLink(home, out)
	case "uninstall":
		return uninstallPaneLink(home, out)
	case "stop":
		if stopPaneLinkDaemon(home) {
			fmt.Fprintln(out, "entire-tail link: stopped.")
		} else {
			fmt.Fprintln(out, "entire-tail link: not running.")
		}
		return nil
	case "status":
		fmt.Fprintln(out, paneLinkStatus(home))
		return nil
	default:
		return fmt.Errorf("link: unknown subcommand %q (start|install|uninstall|stop|status)", sub)
	}
}

// paneLinkLabel is the state in one phrase. Pure, because the distinction it
// draws is the one this feature got wrong first time round: a daemon that is
// RUNNING is not necessarily CONNECTED — with iTerm's API off it spends its life
// restarting a child that cannot reach anything, and calling that "watching"
// reports success for something doing nothing.
func paneLinkLabel(enabled, running, connected bool) string {
	switch {
	case !enabled:
		return "off"
	case running && connected:
		return "watching"
	case running:
		return "not connected — is iTerm's Python API on?"
	default:
		return "enabled, not running"
	}
}

// paneLinkRowValue is the ? panel's state for the pane-link row.
func paneLinkRowValue(home string) string {
	st, ok := readPaneLinkState(home)
	return paneLinkLabel(paneLinkEnabled(home), ok && paneLinkRunning(st), st.Connected)
}

// togglePaneLink is the ? panel's action: set it up (building the venv on first
// use, which is why the row asks for a second ⏎) or tear it down.
func togglePaneLink(home string) string {
	if paneLinkEnabled(home) {
		if err := uninstallPaneLink(home, io.Discard); err != nil {
			return "cannot remove pane link: " + err.Error()
		}
		return "pane link off"
	}
	if err := installPaneLink(home, io.Discard); err != nil {
		return "cannot set up pane link: " + err.Error()
	}
	return "pane link watching"
}

// paneLinkStatus is the human-readable state, also used by the ? panel.
func paneLinkStatus(home string) string {
	if !paneLinkEnabled(home) {
		if paneLinkChoiceRecorded(home) {
			return "entire-tail link: off (declined) — `entire-tail link install` enables it"
		}
		return "entire-tail link: not set up — `entire-tail link install` enables it"
	}
	panes := prunePanes(readPaneRegistry(home), pidAlive)
	st, ok := readPaneLinkState(home)
	running := ok && paneLinkRunning(st)
	if !running || !st.Connected {
		return fmt.Sprintf("entire-tail link: %s (%d live tails)",
			paneLinkLabel(true, running, st.Connected), len(panes))
	}
	return fmt.Sprintf("entire-tail link: watching (pid %d, %d live tails, %d claudes placed)",
		st.Pid, len(panes), len(runningClaudePanes(home)))
}
