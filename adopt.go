package main

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// adopt.go finds the `claude`s in the other panes of our iTerm tab, and the
// transcript each one writes.
//
// entire-tail is meant to run in a second pane next to the agent. Launched bare
// (bareStart) beside one, it opens --live with the cursor on that claude's
// session (paneClaudePIDs → liveCursorFor). nearby.go, hunk.go and panelinkd.go
// reuse the same process matching and resolveClaudeSession.
//
// The session id is NOT interrogable from a bare `claude`: it is absent from the
// process argv, its environment (no CLAUDE_* vars), and its open files (the
// transcript is open-append-closed, never held). So we pin the *process* exactly
// and resolve its file. The process is matched by iTerm tab — every terminal
// carries ITERM_SESSION_ID (e.g. "w0t3p0:UUID" = window0 tab3 pane0) in its
// environment, readable via `ps eww`. We take our own tab from $ITERM_SESSION_ID
// and adopt the lone `claude` sharing it, so a claude in another tab or window is
// never grabbed. If the pane's claude *was* launched with --session-id/--resume,
// argv gives the id exactly; otherwise we fall to the actively-written .jsonl in
// its cwd's project dir. Everything self-disables off iTerm (no ITERM_SESSION_ID).

// itermSessionRe pulls the window+tab prefix out of an ITERM_SESSION_ID value
// ("w<win>t<tab>p<pane>:UUID"). Two panes share a tab iff their wNtN prefixes
// match; the pane number and the UUID are dropped.
var itermSessionRe = regexp.MustCompile(`^(w\d+t\d+)p\d+`)

// itermTab returns the "wNtN" window+tab prefix of an ITERM_SESSION_ID, or "" if
// it isn't shaped like one.
func itermTab(sessionID string) string {
	m := itermSessionRe.FindStringSubmatch(sessionID)
	if m == nil {
		return ""
	}
	return m[1]
}

// parsePsEnv extracts KEY's value from a `ps eww -o command=` line, whose env is
// appended to the argv as space-separated KEY=VALUE tokens. Values with embedded
// spaces aren't recovered (ITERM_SESSION_ID never has them); "" if absent.
func parsePsEnv(out []byte, key string) string {
	pref := key + "="
	for tok := range strings.FieldsSeq(string(out)) {
		if v, ok := strings.CutPrefix(tok, pref); ok {
			return v
		}
	}
	return ""
}

// scrapeSessionIDArg finds a session id passed to claude on the command line —
// `--session-id <id>`, `--resume <id>`, or the `=`-joined forms — returning the
// first UUID-shaped value, else "". A bare `claude` (auto-minted id) has none.
func scrapeSessionIDArg(argv string) string {
	toks := strings.Fields(argv)
	for i, t := range toks {
		switch t {
		case "--session-id", "--resume":
			if i+1 < len(toks) && validSessionID(toks[i+1]) {
				return toks[i+1]
			}
		default:
			for _, flag := range []string{"--session-id=", "--resume="} {
				if v, ok := strings.CutPrefix(t, flag); ok && validSessionID(v) {
					return v
				}
			}
		}
	}
	return ""
}

// claudeProc is a running `claude` and the ITERM_SESSION_ID it was launched
// under (empty when not in iTerm / unreadable).
type claudeProc struct {
	pid     int
	itermID string
}

// siblingPIDs returns the pids of procs sharing ownTab (same iTerm window+tab as
// us). Empty ownTab matches nothing, so a non-iTerm launch adopts nothing.
func siblingPIDs(ownTab string, procs []claudeProc) []int {
	if ownTab == "" {
		return nil
	}
	var out []int
	for _, p := range procs {
		if itermTab(p.itermID) == ownTab {
			out = append(out, p.pid)
		}
	}
	return out
}

// fileStamp is a path and its mtime in unix-nanos.
type fileStamp struct {
	path  string
	mtime int64
}

// sessionCloseWindow is how far apart two sessions' mtimes must be for the newest
// to be "clearly" the live one. Within this window we can't tell by mtime alone
// and fall to activity sampling.
const sessionCloseWindow = 2 * time.Second

// activitySampleDelay is how long we watch an ambiguous project dir to see which
// .jsonl is actually being appended right now.
const activitySampleDelay = 350 * time.Millisecond

// newestClear returns the newest stamp's path and whether it's unambiguously the
// newest — the runner-up trails it by at least windowNano. A single (or no) file
// is trivially clear. Ordering matches newestGlobAll: mtime desc, path desc.
func newestClear(stamps []fileStamp, windowNano int64) (string, bool) {
	if len(stamps) == 0 {
		return "", false
	}
	sorted := append([]fileStamp(nil), stamps...)
	sortStampsNewestFirst(sorted)
	if len(sorted) == 1 {
		return sorted[0].path, true
	}
	gap := sorted[0].mtime - sorted[1].mtime
	return sorted[0].path, gap >= windowNano
}

// sortStampsNewestFirst orders by mtime descending, ties broken by path
// descending (a stable stand-in for `ls -t`, matching newestGlobAll).
func sortStampsNewestFirst(s []fileStamp) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && stampLess(s[j-1], s[j]); j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// stampLess reports whether a should sort AFTER b in newest-first order (i.e. a
// is older, or equal-mtime with a smaller path).
func stampLess(a, b fileStamp) bool {
	if a.mtime != b.mtime {
		return a.mtime < b.mtime
	}
	return a.path < b.path
}

// ── IO layer (shells out to pgrep/ps/lsof; the pure parsers above do the work) ──

// claudeProcs enumerates running `claude` processes paired with their
// ITERM_SESSION_ID. Nil when pgrep is absent or finds none.
func claudeProcs() []claudeProc {
	out, err := exec.Command("pgrep", "-x", "claude").Output()
	if err != nil {
		return nil
	}
	var procs []claudeProc
	for _, pid := range parsePIDs(out) {
		procs = append(procs, claudeProc{pid: pid, itermID: psItermID(pid)})
	}
	return procs
}

func ampProcs() []claudeProc {
	out, err := exec.Command("pgrep", "-x", "amp").Output()
	if err != nil {
		return nil
	}
	var procs []claudeProc
	for _, pid := range parsePIDs(out) {
		procs = append(procs, claudeProc{pid: pid, itermID: psItermID(pid)})
	}
	return procs
}

// psItermID reads pid's ITERM_SESSION_ID from its environment via `ps eww`
// (works for the caller's own processes on macOS). "" if unreadable/absent.
func psItermID(pid int) string {
	out, err := exec.Command("ps", "eww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return parsePsEnv(out, "ITERM_SESSION_ID")
}

// psCommand returns pid's full argv (`ps -o command=`), trimmed.
func psCommand(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// lsofCwd returns pid's working directory (the cwd fd is always held open, so
// this is reliable where the transcript fd is not). "" if unreadable.
func lsofCwd(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	return parseLsofCwd(out)
}

// paneClaudePIDs returns the `claude`s sharing our iTerm tab, i.e. the agents
// in the other panes of this split. Nil off iTerm, or with pgrep/lsof absent.
func paneClaudePIDs(getenv func(string) string) []int {
	ownTab := itermTab(getenv("ITERM_SESSION_ID"))
	if ownTab == "" || !pickerToolsAvailable() {
		return nil
	}
	return siblingPIDs(ownTab, claudeProcs())
}

func paneAgentPIDs(getenv func(string) string) []int {
	ownTab := itermTab(getenv("ITERM_SESSION_ID"))
	if ownTab == "" || !pickerToolsAvailable() {
		return nil
	}
	return append(siblingPIDs(ownTab, claudeProcs()), siblingPIDs(ownTab, ampProcs())...)
}

// bareStart reports whether nothing on the command line or in the env already
// chose what to show, so the mode is ours to pick from the pane layout.
// --no-pick (tail $PWD directly) and -p (always the tree) are both choices.
func bareStart(cfg Config, agent string) bool {
	return cfg.FollowSession == "" && !cfg.WaitNew && !cfg.Live &&
		cfg.Search == "" && len(cfg.Positional) == 0 && cfg.Pick == "auto" &&
		(agent == "auto" || agent == "claude" || agent == "amp")
}

// resolveClaudeSession maps a pane's claude (its cwd + argv) to a transcript
// file. An id on the command line locates it exactly (by cwd, else by scanning
// every project dir for that id); otherwise the actively-written .jsonl in the
// cwd's project dir wins. "" when nothing resolves.
// Every account's projects root is searched, in profile order, so a `claude` in
// the sibling pane is adopted whether it was launched as the work or the
// personal account. Order gives the default account ties, matching what a
// single-root build used to do.
func resolveClaudeSession(home, cwd, argv string) string {
	roots := claudeProjectsRoots(home)
	if id := scrapeSessionIDArg(argv); id != "" {
		if cwd != "" {
			for _, projects := range roots {
				if p := filepath.Join(projects, claudeSlug(cwd), id+".jsonl"); isFile(p) {
					return p
				}
			}
		}
		var patterns []string
		for _, projects := range roots {
			patterns = append(patterns, filepath.Join(projects, "*", id+".jsonl"))
		}
		if p := newestAcross(patterns); p != "" {
			return p
		}
	}
	if cwd == "" {
		return ""
	}
	// No id on the command line: whichever root holds the actively-appended
	// transcript for this cwd wins, newest first.
	best, bestMtime := "", int64(-1)
	for _, projects := range roots {
		p := liveSessionInDir(filepath.Join(projects, claudeSlug(cwd)))
		if p == "" {
			continue
		}
		if mt := fileMtimeNano(p); mt > bestMtime {
			best, bestMtime = p, mt
		}
	}
	return best
}

// liveSessionInDir picks the live transcript among a project dir's .jsonl files:
// the clearly-newest by mtime, or — when two are near-simultaneous — the one
// being actively appended right now (a brief sample). "" when the dir is empty.
func liveSessionInDir(dir string) string {
	stamps := jsonlStamps(dir)
	p, clear := newestClear(stamps, int64(sessionCloseWindow))
	if p == "" || clear {
		return p
	}
	return activePick(dir, stamps)
}

// activePick re-stats an ambiguous dir after a short delay and returns the
// .jsonl whose mtime advanced most (the one being written). Falls back to the
// newest when nothing moved (both idle).
func activePick(dir string, before []fileStamp) string {
	time.Sleep(activitySampleDelay)
	prev := make(map[string]int64, len(before))
	for _, s := range before {
		prev[s.path] = s.mtime
	}
	best, bestDelta := "", int64(0)
	for _, s := range jsonlStamps(dir) {
		if d := s.mtime - prev[s.path]; d > bestDelta {
			best, bestDelta = s.path, d
		}
	}
	if best != "" {
		return best
	}
	p, _ := newestClear(before, 0) // nobody moved → newest
	return p
}

// jsonlStamps stats every *.jsonl in dir into path+mtime pairs.
func jsonlStamps(dir string) []fileStamp {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	out := make([]fileStamp, 0, len(matches))
	for _, m := range matches {
		if mt := fileMtimeNano(m); mt >= 0 {
			out = append(out, fileStamp{m, mt})
		}
	}
	return out
}
