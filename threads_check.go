package main

import (
	"context"
	"encoding/json"
	"fmt"
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
}

const threadCheckTimeout = 12 * time.Second

// defaultThreadCheckDeps wires the real runner, live sessions (Claude registry
// plus local Amp processes), the filesystem and the clock.
func defaultThreadCheckDeps(home string) threadCheckDeps {
	return threadCheckDeps{
		Run: defaultWTFScanDeps().Run,
		Live: func() []liveSession {
			out := collectLiveSessions(liveRoots(home), pidAlive)
			return append(out, collectLiveAmpSessions(home)...)
		},
		Exists: isDir,
		Now:    func() int64 { return time.Now().Unix() },
	}
}

// checkThreads checks every thread concurrently, keyed by slug.
func checkThreads(ctx context.Context, ts []thread, deps threadCheckDeps) map[string]threadFacts {
	out := make(map[string]threadFacts, len(ts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go func(t thread) {
			defer wg.Done()
			f := checkThread(ctx, t, deps)
			mu.Lock()
			out[t.Meta.Slug] = f
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return out
}

// checkThread compares one thread's claims with git, entire and the live
// session registry.
func checkThread(ctx context.Context, t thread, deps threadCheckDeps) threadFacts {
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
	if deps.Live != nil && len(t.Meta.Sessions) > 0 {
		ids := map[string]bool{}
		for _, s := range t.Meta.Sessions {
			ids[s.ID] = true
		}
		for _, l := range deps.Live() {
			if ids[l.SessionID] {
				l := l
				f.Running = &l
				f.Risks = append(f.Risks, "running now in "+tildify(l.Cwd, mustHome()))
				break
			}
		}
	}
	return f
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

// checkTrailClaim asks entire for the trail's status. fetchTrailMetadata
// hardcodes the gh/ forge, so this runs the same command with the claim's own
// forge prefix and works for gh/ and et/ alike.
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
	data, err := deps.Run(ctx, "", "entire", "trail", "show", parts[3], "--repo", strings.Join(parts[:3], "/"), "--json")
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
	case len(f.Risks) > 0:
		return f.Risks[0], "risk"
	case t.Meta.WaitingOn == "":
		return "no status written", "plain"
	case t.Meta.NeedsYou:
		return "waiting on you: " + t.Meta.WaitingOn, "you"
	}
	return t.Meta.WaitingOn, "plain"
}
