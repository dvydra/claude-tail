package main

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// trailtrack.go — which trails a tail watches, and when it polls them.
//
// Two sources: the change on the session's current branch ("pushing"), and
// trails the transcript names ("reviewing", via trailMentionsInLine). The
// tracker runs on its own goroutine; it never touches the screen. Updates go
// out on a channel only when a fetch produced events, so the render goroutine
// prints a line exactly when something changed. Errors are kept for the modal
// and never sent: a tail must stay quiet when `entire` is logged out or offline.

const (
	trailMentionMaxAge = 24 * time.Hour
	trailMaxTracked    = 10
)

type trailUpdate struct {
	State   changeState
	Events  []trailEvent
	Dropped bool // merged or closed: this was its last line
	// Baseline is the first look at this change in this process: its state as
	// found, not a change. Shown in the tail, never sent as a trail FYI.
	Baseline bool
}

type trailTrackerDeps struct {
	Run  wtfCommandRunner
	Now  func() time.Time
	Home string
}

type trailExpansion struct {
	refs []trailRef
	at   time.Time
}

type trailTracker struct {
	deps trailTrackerDeps
	out  chan<- trailUpdate

	mu       sync.Mutex
	mentions map[string]trailMention // by ref key, or "?"+partial+"@"+cwd
	path     string
	refresh  bool
	errs     map[string]string
	branch   string          // the session's branch, for the modal's "nothing found" line
	ignored  map[string]bool // removed in the g panel: never tracked again this run

	wake  chan struct{} // Refresh: step now rather than at the next tick
	steps atomic.Int64  // completed steps (tests)

	// Below: touched only by step, which runs on one goroutine.
	prev       map[string]*changeState
	due        map[string]time.Time
	dropped    map[string]bool
	pushing    trailRef
	pushKey    string    // cwd|branch the pushing lookup was made for
	pushAt     time.Time // when that lookup ran
	head       string
	remotes    map[string]string // cwd → forge/owner/repo
	expansions map[string]trailExpansion
}

// trailExecRun is the real command runner. Tests pass a fake instead.
func trailExecRun(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.Output()
}

func newTrailTracker(deps trailTrackerDeps, out chan<- trailUpdate) *trailTracker {
	return &trailTracker{
		deps: deps, out: out,
		mentions: map[string]trailMention{}, errs: map[string]string{}, ignored: map[string]bool{},
		prev: map[string]*changeState{}, due: map[string]time.Time{}, dropped: map[string]bool{},
		remotes: map[string]string{}, expansions: map[string]trailExpansion{},
		wake: make(chan struct{}, 1),
	}
}

// Untrack stops watching a change for the rest of this run, whatever mentions
// it again (the `x` key in the g panel). Not persisted: a restart brings it back.
func (t *trailTracker) Untrack(key string) {
	t.mu.Lock()
	t.ignored[key] = true
	delete(t.errs, key)
	t.mu.Unlock()
}

// Branch is the session's current branch, as last read.
func (t *trailTracker) Branch() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.branch
}

func (t *trailTracker) Steps() int64 { return t.steps.Load() }

// Observe records mentions. Cheap and non-blocking: safe on the render goroutine.
func (t *trailTracker) Observe(ms []trailMention) {
	if len(ms) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range ms {
		k := "?" + m.Partial + "@" + m.Cwd
		if !m.Ref.zero() {
			k = m.Ref.Key()
		}
		if old, ok := t.mentions[k]; !ok || m.At.After(old.At) {
			t.mentions[k] = m
		}
	}
}

// Follow names the transcript being tailed; its cwd decides the pushing branch.
func (t *trailTracker) Follow(path string) {
	t.mu.Lock()
	t.path = path
	t.mu.Unlock()
}

// Refresh makes the next step refetch everything, skipping the shared cache.
func (t *trailTracker) Refresh() {
	t.mu.Lock()
	t.refresh = true
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *trailTracker) Errors() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]string, len(t.errs))
	for k, v := range t.errs {
		out[k] = v
	}
	return out
}

func (t *trailTracker) setErr(key, msg string) {
	t.mu.Lock()
	if msg == "" {
		delete(t.errs, key)
	} else {
		t.errs[key] = msg
	}
	t.mu.Unlock()
}

// Run steps immediately, then every fast-poll interval, until ctx ends.
func (t *trailTracker) Run(ctx context.Context) {
	tick := time.NewTicker(trailFastPoll)
	defer tick.Stop()
	for {
		t.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-t.wake:
		}
	}
}

func (t *trailTracker) git(ctx context.Context, dir string, args ...string) string {
	b, err := trailCall(ctx, t.deps.Run, dir, "git", args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// remote is the forge/owner/repo of cwd's origin, cached per cwd.
func (t *trailTracker) remote(ctx context.Context, cwd string) string {
	if r, ok := t.remotes[cwd]; ok {
		return r
	}
	r := trailsRepoRemote(t.git(ctx, cwd, "remote", "get-url", "origin"))
	t.remotes[cwd] = r
	return r
}

// updatePushing re-reads the session's branch and its change. It returns the
// pushing change's key when a new HEAD means it should be refetched now.
func (t *trailTracker) updatePushing(ctx context.Context, path string, now time.Time) (forced string) {
	if path == "" {
		return ""
	}
	cwd := sessionCwdNow(path)
	if cwd == "" {
		return ""
	}
	head := t.git(ctx, cwd, "rev-parse", "HEAD")
	branch := t.git(ctx, cwd, "branch", "--show-current")
	key := cwd + "|" + branch
	if branch != "" && (key != t.pushKey || (t.pushing.zero() && now.Sub(t.pushAt) >= trailSlowPoll)) {
		t.pushKey, t.pushAt, t.pushing = key, now, trailRef{}
		if repo := t.remote(ctx, cwd); repo != "" {
			if ref, ok, err := fetchBranchChange(ctx, t.deps.Run, repo, branch); err == nil && ok {
				t.pushing = ref
			}
		}
	}
	if branch == "" {
		t.pushing, t.pushKey = trailRef{}, ""
	}
	t.mu.Lock()
	t.branch = branch
	t.mu.Unlock()
	if head != "" && t.head != "" && head != t.head && !t.pushing.zero() {
		forced = t.pushing.Key()
	}
	if head != "" {
		t.head = head
	}
	return forced
}

// resolve turns a mention into a ref, using cwd's origin for a bare selector.
func (t *trailTracker) resolve(ctx context.Context, m trailMention) (trailRef, bool) {
	if !m.Ref.zero() {
		return m.Ref, true
	}
	if m.Cwd == "" {
		return trailRef{}, false
	}
	parts := strings.Split(t.remote(ctx, m.Cwd), "/")
	if len(parts) != 3 {
		return trailRef{}, false
	}
	if i := strings.LastIndexByte(m.Partial, '/'); i >= 0 {
		n, err := strconv.Atoi(m.Partial[i+1:])
		if err != nil || n <= 0 {
			return trailRef{}, false
		}
		return trailRef{trailRefChange, parts[0], parts[1], strings.ToLower(m.Partial[:i]), n}, true
	}
	n, err := strconv.Atoi(m.Partial)
	if err != nil || n <= 0 {
		return trailRef{}, false
	}
	return trailRef{Kind: trailRefProject, Forge: parts[0], Project: parts[1], Number: n}, true
}

func (t *trailTracker) expand(ctx context.Context, p trailRef, now time.Time) []trailRef {
	k := p.Key()
	if e, ok := t.expansions[k]; ok && now.Sub(e.at) < trailSlowPoll {
		return e.refs
	}
	refs, err := fetchProjectChanges(ctx, t.deps.Run, p)
	if err != nil {
		t.setErr(k, err.Error())
		refs = t.expansions[k].refs
	} else {
		t.setErr(k, "")
	}
	t.expansions[k] = trailExpansion{refs, now}
	return refs
}

// step is one tick: work out what to track, and fetch whatever is due.
func (t *trailTracker) step(ctx context.Context) {
	defer t.steps.Add(1)
	now := t.deps.Now()
	t.mu.Lock()
	path, refresh := t.path, t.refresh
	t.refresh = false
	mentions := make([]trailMention, 0, len(t.mentions))
	for _, m := range t.mentions {
		mentions = append(mentions, m)
	}
	ignored := make(map[string]bool, len(t.ignored))
	for k := range t.ignored {
		ignored[k] = true
	}
	t.mu.Unlock()
	skip := func(k string) bool { return t.dropped[k] || ignored[k] }

	forced := map[string]bool{}
	if k := t.updatePushing(ctx, path, now); k != "" {
		forced[k] = true
	}

	type cand struct {
		ref trailRef
		at  time.Time
	}
	cands := map[string]cand{}
	consider := func(r trailRef, at time.Time) {
		k := r.Key()
		if skip(k) {
			return
		}
		if c, ok := cands[k]; !ok || at.After(c.at) {
			cands[k] = cand{r, at}
		}
	}
	for _, m := range mentions {
		if m.At.IsZero() || now.Sub(m.At) > trailMentionMaxAge {
			continue
		}
		r, ok := t.resolve(ctx, m)
		if !ok {
			continue
		}
		if r.Kind == trailRefProject {
			for _, c := range t.expand(ctx, r, now) {
				consider(c, m.At)
			}
			continue
		}
		consider(r, m.At)
	}
	list := make([]cand, 0, len(cands))
	for _, c := range cands {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.After(list[j].at) })
	if !t.pushing.zero() && !skip(t.pushing.Key()) {
		pk := t.pushing.Key()
		out := []cand{{t.pushing, now}}
		for _, c := range list {
			if c.ref.Key() != pk {
				out = append(out, c)
			}
		}
		list = out
	}
	if len(list) > trailMaxTracked {
		list = list[:trailMaxTracked]
	}

	send := func(u trailUpdate) bool {
		select {
		case t.out <- u:
			return true
		case <-ctx.Done():
			return false
		}
	}
	tracked := map[string]bool{}
	for _, c := range list {
		k := c.ref.Key()
		tracked[k] = true
		if d, ok := t.due[k]; ok && now.Before(d) && !forced[k] && !refresh {
			continue
		}
		u, ok := t.fetch(ctx, c.ref, now, forced[k] || refresh)
		if ok && !send(u) {
			return
		}
	}
	// A change that left the list (its project trail no longer lists it once
	// merged, its mention aged out, the cap or a branch switch pushed it off)
	// gets one last fetch, so a merge still prints. Otherwise it leaves the
	// modal quietly: Dropped with no events.
	for k, p := range t.prev {
		if tracked[k] || t.dropped[k] {
			continue
		}
		// Removed by hand: the panel already forgot it, so no last fetch.
		if ignored[k] {
			delete(t.prev, k)
			delete(t.due, k)
			continue
		}
		u, ok := t.fetch(ctx, p.Ref, now, true)
		if !ok || !u.Dropped {
			u = trailUpdate{State: *p, Dropped: true}
		}
		delete(t.prev, k)
		delete(t.due, k)
		t.setErr(k, "")
		if !send(u) {
			return
		}
	}
}

// fetch reads one change and diffs it against the last fetch. Every success
// is sent, events or not, so the modal always shows the latest state; the
// render goroutine prints a line only when there are events.
func (t *trailTracker) fetch(ctx context.Context, ref trailRef, now time.Time, force bool) (trailUpdate, bool) {
	k := ref.Key()
	if force {
		_ = os.Remove(trailCachePath(t.deps.Home, ref))
	}
	s, err := fetchChangeState(ctx, t.deps.Run, ref, t.deps.Home, now)
	if err != nil {
		t.setErr(k, err.Error())
		t.due[k] = now.Add(trailSlowPoll)
		return trailUpdate{}, false
	}
	t.setErr(k, "")
	prev := t.prev[k]
	evs := trailEvents(prev, s)
	settled := settleGates(prev, s)
	t.prev[k] = &settled
	if s.Running() {
		t.due[k] = now.Add(trailFastPoll)
	} else {
		t.due[k] = now.Add(trailSlowPoll)
	}
	done := s.Status == "merged" || s.Status == "closed"
	if done {
		t.dropped[k] = true
		delete(t.prev, k)
		// Already finished the first time we look: old news, not an event.
		if prev == nil {
			evs = nil
		}
	}
	return trailUpdate{State: s, Events: evs, Dropped: done, Baseline: prev == nil}, true
}
