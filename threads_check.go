package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// threadFacts is what reality says about a thread's written claims.
type threadFacts struct {
	Stale   []string // "<claim> disagrees with <source>"
	Risks   []string
	Running *liveSession
	Details []string // for the d view
}

// threadCheckDeps are the seams for every outside look: git and entire
// through Run, running sessions through Live, the filesystem through Exists.
type threadCheckDeps struct {
	Run    wtfCommandRunner
	Live   func() []liveSession
	Exists func(string) bool
	Now    func() int64
	Home   string // for tildify in risk text
}

const threadCheckTimeout = 12 * time.Second

// defaultThreadCheckDeps wires the real runner, live sessions (Claude registry
// plus local Amp processes), the filesystem and the clock.
func defaultThreadCheckDeps(home string) threadCheckDeps {
	return threadCheckDeps{
		Run: defaultWTFScanDeps().Run,
		Live: func() []liveSession {
			out := collectLiveSessions(liveRoots(home), pidAlive)
			return append(out, lightLiveAmpSessions()...)
		},
		Home:   home,
		Exists: isDir,
		Now:    func() int64 { return time.Now().Unix() },
	}
}

// lightLiveAmpSessions maps running amp pids to thread ids through pgrep, ps
// and lsof only. Unlike collectLiveAmpSessions it never exports a thread or
// writes a snapshot, which a read-only check must not do.
func lightLiveAmpSessions() []liveSession {
	out, err := exec.Command("pgrep", "-x", "amp").Output()
	if err != nil {
		return nil
	}
	var sessions []liveSession
	for _, pid := range parsePIDs(out) {
		id := strconv.Itoa(pid)
		tty, err := exec.Command("ps", "-o", "tty=", "-p", id).Output()
		if err != nil || strings.TrimSpace(string(tty)) == "" || strings.TrimSpace(string(tty)) == "??" {
			continue
		}
		files, err := exec.Command("lsof", "-a", "-p", id, "-Fn").Output()
		if err != nil {
			continue
		}
		tid := ampThreadIDFromLsof(files)
		if tid == "" {
			continue
		}
		cwdOut, _ := exec.Command("lsof", "-a", "-p", id, "-d", "cwd", "-Fn").Output()
		sessions = append(sessions, liveSession{Agent: AgentAmp, PID: pid, SessionID: tid, Cwd: parseLsofCwd(cwdOut)})
	}
	return sessions
}

// checkThreads reads live sessions once, then checks every thread
// concurrently, keyed by its directory name.
func checkThreads(ctx context.Context, ts []thread, deps threadCheckDeps) map[string]threadFacts {
	out := make(map[string]threadFacts, len(ts))
	var live []liveSession
	if deps.Live != nil {
		live = deps.Live()
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go func(t thread) {
			defer wg.Done()
			f := checkThreadLive(ctx, t, deps, live)
			mu.Lock()
			out[filepath.Base(t.Dir)] = f
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return out
}

// checkThread compares one thread's claims with git, entire and the live
// session registry.
func checkThread(ctx context.Context, t thread, deps threadCheckDeps) threadFacts {
	var live []liveSession
	if deps.Live != nil {
		live = deps.Live()
	}
	return checkThreadLive(ctx, t, deps, live)
}

// checkThreadLive is checkThread with the running sessions already read.
func checkThreadLive(ctx context.Context, t thread, deps threadCheckDeps, live []liveSession) threadFacts {
	ctx, cancel := context.WithTimeout(ctx, threadCheckTimeout)
	defer cancel()
	var f threadFacts
	for _, c := range t.Meta.Claims {
		switch c.Kind {
		case "worktree":
			checkWorktreeClaim(ctx, c, deps, &f)
		case "trail":
			checkTrailClaim(ctx, c, deps, &f)
		case "linear":
			f.Details = append(f.Details, "linear "+c.Linear+" not checked (no Linear access from here)")
		}
	}
	if len(t.Meta.Sessions) > 0 {
		ids := map[string]bool{}
		for _, s := range t.Meta.Sessions {
			ids[s.ID] = true
		}
		for _, l := range live {
			if ids[l.SessionID] {
				l := l
				f.Running = &l
				f.Risks = append(f.Risks, "running now in "+tildify(l.Cwd, deps.Home))
				break
			}
		}
	}
	if f.Running == nil && deps.Exists != nil {
		if l, wt := agentInWorktree(t, live, deps.Exists); l != nil {
			f.Risks = append(f.Risks, fmt.Sprintf("an agent is already running in %s (pid %d)", tildify(wt, deps.Home), l.PID))
		}
	}
	return f
}

// agentInWorktree finds a running session, of any thread or none, whose cwd
// is inside one of t's claimed worktrees that still exists. Opening the
// thread there would put a second agent in that worktree.
func agentInWorktree(t thread, live []liveSession, exists func(string) bool) (*liveSession, string) {
	for _, c := range t.Meta.Claims {
		if c.Kind != "worktree" || c.Path == "" || !exists(c.Path) {
			continue
		}
		wt := filepath.Clean(c.Path)
		for _, l := range live {
			if l.Cwd == "" {
				continue
			}
			cwd := filepath.Clean(l.Cwd)
			if cwd == wt || strings.HasPrefix(cwd, wt+string(filepath.Separator)) {
				l := l
				return &l, wt
			}
		}
	}
	return nil, ""
}

func checkWorktreeClaim(ctx context.Context, c threadClaim, deps threadCheckDeps, f *threadFacts) {
	base := filepath.Base(c.Path)
	if !deps.Exists(c.Path) {
		f.Stale = append(f.Stale, "worktree "+base+" gone")
		return
	}
	git := func(args ...string) (string, error) {
		out, err := deps.Run(ctx, c.Path, "git", append([]string{"-C", c.Path}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		f.Details = append(f.Details, "worktree "+base+": git unavailable")
		return
	}
	head, _ := git("rev-parse", "HEAD")
	if c.Branch != "" && branch != c.Branch {
		f.Stale = append(f.Stale, fmt.Sprintf("branch is %s, thread says %s", branch, c.Branch))
	}
	w := inspectWorktree(ctx, c.Path, gitWorktreeEntry{Path: c.Path, Head: head, Branch: branch}, deps.Now(), deps.Run)
	if w.UnmergedCommits > 0 {
		f.Details = append(f.Details, fmt.Sprintf("%s: %d commits not on %s", base, w.UnmergedCommits, w.DefaultBranch))
	}
	if w.DirtyFiles > 0 {
		f.Details = append(f.Details, fmt.Sprintf("%s: %d dirty files", base, w.DirtyFiles))
	}
	// Not pushed: commits ahead of the upstream, or no upstream at all.
	ahead, aerr := git("rev-list", "--count", "@{u}..HEAD")
	noUpstream := aerr != nil
	n, _ := strconv.Atoi(ahead)
	unpushed := noUpstream || n > 0
	if c.Head != "" && unpushed {
		f.Stale = append(f.Stale, branch+" not pushed")
	}
	if c.Head != "" && head != "" && !strings.HasPrefix(head, c.Head) && !strings.HasPrefix(c.Head, head) {
		f.Details = append(f.Details, fmt.Sprintf("%s: head moved (%s, thread says %s)", base, shortSHA(head), shortSHA(c.Head)))
	}
	if noUpstream && w.DirtyFiles > 0 {
		f.Risks = append(f.Risks, "at risk: work exists only on this laptop")
	}
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// checkTrailClaim asks entire for the trail's status. A claim is
// `<forge>/<project>/<repo>/<n>`, and the repo slot decides which number space
// <n> is in:
//
//   - `-`: a project trail (entire.com/<forge>/<project>/-/trails/<n>), read
//     with `entire trail show <n> --project <forge>/<project>`.
//   - a repo name: a repo-scoped trail (entire.io/<forge>/<project>/<repo>/trails/<n>),
//     read through the repo's API path.
//
// `entire trail show <n> --repo <r>` is NOT a repo-scoped read: it ignores the
// repo and returns the PROJECT trail numbered <n> (measured: entiredb 4103 came
// back as an unrelated fleet promote trail), so it can never be used here.
func checkTrailClaim(ctx context.Context, c threadClaim, deps threadCheckDeps, f *threadFacts) {
	parts := strings.Split(c.Trail, "/")
	if len(parts) != 4 {
		f.Details = append(f.Details, "trail "+c.Trail+": unrecognised form")
		return
	}
	num, err := strconv.Atoi(parts[3])
	if err != nil {
		f.Details = append(f.Details, "trail "+c.Trail+": unrecognised form")
		return
	}
	var data []byte
	if parts[2] == "-" {
		data, err = deps.Run(ctx, "", "entire", "trail", "show", parts[3], "--project", parts[0]+"/"+parts[1], "--json")
	} else {
		// The active login's jurisdiction may not hold the repo (an au login
		// 404s entirehq's us repos), so retry in us before calling it unknown.
		path := "/api/v1/trails/" + c.Trail
		data, err = deps.Run(ctx, "", "entire", "api", path)
		if err != nil {
			data, err = deps.Run(ctx, "", "entire", "api", "-j", "us", path)
		}
	}
	var md entireTrailJSON
	if err == nil {
		err = json.Unmarshal(data, &md)
	}
	if err != nil {
		f.Details = append(f.Details, fmt.Sprintf("trail %d: status unavailable", num))
		return
	}
	f.Details = append(f.Details, fmt.Sprintf("trail %d is %s", num, md.Status))
	if c.Expect != "" && md.Status != c.Expect {
		f.Stale = append(f.Stale, fmt.Sprintf("trail %d is %s, thread says %s", num, md.Status, c.Expect))
	}
}

// threadListStatus picks the one line the triage list shows for a thread.
func threadListStatus(t thread, f threadFacts) (string, string) {
	switch {
	case t.ParseErr != nil:
		return "unreadable: " + t.ParseErr.Error(), "stale"
	case len(f.Stale) > 0:
		return "stale: " + f.Stale[0], "stale"
	case t.ClosedInRoot:
		return "marked closed but not moved", "risk"
	case len(f.Risks) > 0:
		return f.Risks[0], "risk"
	case t.Meta.WaitingOn == "":
		return "no status written", "plain"
	case t.Meta.NeedsYou:
		return "waiting on you: " + t.Meta.WaitingOn, "you"
	}
	return t.Meta.WaitingOn, "plain"
}
