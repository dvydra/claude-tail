package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type wtfCommandRunner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

type gitWorktreeEntry struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
}

const (
	wtfDirtySummaryLimit = 10
	wtfSubjectLimit      = 50
	wtfDiffLimit         = 128 * 1024
)

type entireTrailJSON struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	Branch         string `json:"branch"`
	OriginalBranch string `json:"original_branch"`
	Base           string `json:"base"`
	Title          string `json:"title"`
	Status         string `json:"status"`
}

func fetchTrailMetadata(ctx context.Context, trail wtfTrail, now int64, run wtfCommandRunner) (wtfTrail, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	data, err := run(ctx, "", "entire", "trail", "show", strconv.Itoa(trail.Number), "--repo", "gh/"+trail.Owner+"/"+trail.Repo, "--json")
	if err == nil {
		var metadata entireTrailJSON
		err = json.Unmarshal(data, &metadata)
		if err == nil {
			trail.URL = metadata.URL
			trail.Title = metadata.Title
			trail.Status = metadata.Status
			trail.SourceBranch = metadata.Branch
			if trail.SourceBranch == "" {
				trail.SourceBranch = metadata.OriginalBranch
			}
			trail.TargetBranch = metadata.Base
			trail.MetadataUpdatedAt = now
			trail.MetadataError = ""
			trail.MetadataAttempts = 0
			trail.MetadataNextRetry = 0
			return trail, nil
		}
	}
	trail.MetadataAttempts++
	delay := int64(60)
	for attempt := 1; attempt < trail.MetadataAttempts && delay < 3600; attempt++ {
		delay *= 2
		if delay > 3600 {
			delay = 3600
		}
	}
	trail.MetadataError = err.Error()
	trail.MetadataNextRetry = now + delay
	return trail, err
}

func trailMetadataDue(trail wtfTrail, activeWIP bool, now int64) bool {
	if trail.MetadataNextRetry > now {
		return false
	}
	if trail.MetadataAttempts > 0 {
		return true
	}
	if trail.MetadataUpdatedAt == 0 {
		return true
	}
	interval := int64((24 * time.Hour) / time.Second)
	if activeWIP {
		interval = int64((10 * time.Minute) / time.Second)
	}
	return now-trail.MetadataUpdatedAt >= interval
}

func parseGitWorktreePorcelain(data []byte) []gitWorktreeEntry {
	var entries []gitWorktreeEntry
	var current *gitWorktreeEntry
	flush := func() {
		if current != nil && current.Path != "" {
			entries = append(entries, *current)
		}
		current = nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			current = &gitWorktreeEntry{Path: value}
		case "HEAD":
			if current != nil {
				current.Head = value
			}
		case "branch":
			if current != nil {
				current.Branch = strings.TrimPrefix(value, "refs/heads/")
			}
		case "detached":
			if current != nil {
				current.Detached = true
			}
		}
	}
	flush()
	return entries
}

func inspectRepoWorktrees(ctx context.Context, repo, cwd string, now int64, prior []wtfWorktree, run wtfCommandRunner) ([]wtfWorktree, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := run(ctx, cwd, "git", "-C", cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return append([]wtfWorktree(nil), prior...), err
	}
	entries := parseGitWorktreePorcelain(data)
	priorByPath := make(map[string]wtfWorktree, len(prior))
	for _, worktree := range prior {
		if worktree.Repo == repo {
			priorByPath[worktree.Path] = worktree
		}
	}
	worktrees := make([]wtfWorktree, 0, len(entries)+len(priorByPath))
	var inspectionErrors []error
	for _, entry := range entries {
		worktree := inspectWorktree(ctx, repo, entry, now, run)
		if worktree.Exists && worktree.GitError != "" {
			inspectionErrors = append(inspectionErrors, fmt.Errorf("%s: %s", entry.Path, worktree.GitError))
		}
		if old, ok := priorByPath[entry.Path]; ok {
			worktree.FirstSeen = old.FirstSeen
			worktree.SessionKeys = old.SessionKeys
			worktree.TrailKeys = old.TrailKeys
			if worktree.LastWIPAt == 0 {
				worktree.LastWIPAt = old.LastWIPAt
			}
			delete(priorByPath, entry.Path)
		}
		worktrees = append(worktrees, worktree)
	}
	for _, old := range prior {
		worktree, ok := priorByPath[old.Path]
		if !ok || old.Repo != repo {
			continue
		}
		worktree.DirtyFiles = -1
		worktree.UnmergedCommits = -1
		worktree.LastSeen = now
		worktree.LastWIPAt = now
		if info, err := os.Stat(worktree.Path); err != nil || !info.IsDir() {
			worktree.Exists = false
			worktree.GitError = "worktree path missing"
		} else {
			worktree.Exists = true
			worktree.GitError = "worktree not listed by git"
		}
		worktrees = append(worktrees, worktree)
		delete(priorByPath, old.Path)
	}
	return worktrees, errors.Join(inspectionErrors...)
}

func resolveRemoteDefault(ctx context.Context, cwd string, run wtfCommandRunner) string {
	if data, err := run(ctx, cwd, "git", "-C", cwd, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(string(data))
		if strings.HasPrefix(ref, "refs/remotes/origin/") {
			return strings.TrimPrefix(ref, "refs/remotes/")
		}
	}
	for _, branch := range []string{"main", "master"} {
		ref := "refs/remotes/origin/" + branch
		if _, err := run(ctx, cwd, "git", "-C", cwd, "show-ref", "--verify", "--quiet", ref); err == nil {
			return "origin/" + branch
		}
	}
	return ""
}

func inspectWorktree(ctx context.Context, repo string, entry gitWorktreeEntry, now int64, run wtfCommandRunner) (w wtfWorktree) {
	w = wtfWorktree{
		Repo:            repo,
		Path:            entry.Path,
		Branch:          entry.Branch,
		Head:            entry.Head,
		UnmergedCommits: -1,
		FirstSeen:       now,
		LastSeen:        now,
	}
	defer func() {
		if worktreeHasWIP(w) {
			w.LastWIPAt = now
		}
	}()
	if entry.Branch != "" {
		w.GitEvidence = append(w.GitEvidence, wtfGitEvidence{Source: "branch", Text: entry.Branch})
	}
	if info, err := os.Stat(entry.Path); err != nil || !info.IsDir() {
		w.GitError = "worktree path missing"
		return w
	}
	w.Exists = true
	status, err := run(ctx, entry.Path, "git", "-C", entry.Path, "status", "--porcelain")
	if err != nil {
		w.GitError = "git status failed"
	} else {
		lines := nonemptyLines(status)
		w.DirtyFiles = len(lines)
		w.DirtySummary = boundedLines(lines, wtfDirtySummaryLimit)
	}
	w.DefaultBranch = resolveRemoteDefault(ctx, entry.Path, run)
	if w.DefaultBranch == "" {
		w.GitError = firstNonEmpty(w.GitError, "remote default unknown")
		return w
	}
	rangeArg := w.DefaultBranch + "..HEAD"
	count, err := run(ctx, entry.Path, "git", "-C", entry.Path, "rev-list", "--count", rangeArg)
	if err != nil {
		w.GitError = "git rev-list failed"
		return w
	}
	w.UnmergedCommits, err = strconv.Atoi(strings.TrimSpace(string(count)))
	if err != nil {
		w.UnmergedCommits = -1
		w.GitError = "invalid rev-list count"
		return w
	}
	if subjects, err := run(ctx, entry.Path, "git", "-C", entry.Path, "log", "--format=%s", rangeArg); err == nil {
		if lines := boundedLines(nonemptyLines(subjects), wtfSubjectLimit); len(lines) > 0 {
			w.GitEvidence = append(w.GitEvidence, wtfGitEvidence{Source: "unmerged subjects", Text: strings.Join(lines, "\n")})
		}
	} else {
		w.GitError = firstNonEmpty(w.GitError, "git log failed")
	}
	if diff, err := run(ctx, entry.Path, "git", "-C", entry.Path, "diff", "--no-ext-diff", "--unified=0", "HEAD", "--"); err == nil && len(diff) > 0 {
		if len(diff) > wtfDiffLimit {
			diff = diff[:wtfDiffLimit]
		}
		w.GitEvidence = append(w.GitEvidence, wtfGitEvidence{Source: "diff", Text: string(diff)})
	} else if err != nil {
		w.GitError = firstNonEmpty(w.GitError, "git diff failed")
	}
	return w
}

func worktreeHasWIP(w wtfWorktree) bool {
	return !w.Exists || w.GitError != "" || w.UnmergedCommits < 0 || w.DirtyFiles > 0 || w.UnmergedCommits > 0
}

func detectWTFFindings(state wtfState, now int64) map[string]wtfFinding {
	findings := make(map[string]wtfFinding)
	trailKeys := sortedMapKeys(state.Trails)
	for _, trailKey := range trailKeys {
		trail := state.Trails[trailKey]
		active := activeTrailAssociations(state, trail)
		activeSessions, activeWorktrees := associationIDs(active)

		if len(activeSessions) > 1 {
			addWTFFinding(findings, now, "duplicate-active-claim", 3, trail, activeSessions, activeWorktrees,
				fmt.Sprintf("Active sessions %s claim %s from worktrees %s.", strings.Join(activeSessions, ", "), trail.Key, strings.Join(activeWorktrees, ", ")),
				append(append([]string(nil), activeSessions...), activeWorktrees...))
		}

		var elsewhere []string
		for _, path := range associatedWorktrees(trail) {
			if containsString(activeWorktrees, path) {
				continue
			}
			worktree, ok := state.Worktrees[path]
			if ok && worktree.Exists && worktree.GitError == "" && (worktree.DirtyFiles > 0 || worktree.UnmergedCommits > 0) {
				elsewhere = append(elsewhere, path)
			}
		}
		if len(activeSessions) > 0 && len(elsewhere) > 0 {
			evidence := append([]string(nil), activeSessions...)
			for _, path := range elsewhere {
				worktree := state.Worktrees[path]
				evidence = append(evidence, fmt.Sprintf("%s dirty=%d unmerged=%d", path, worktree.DirtyFiles, worktree.UnmergedCommits))
			}
			addWTFFinding(findings, now, "existing-wip-elsewhere", 2, trail, activeSessions, append(activeWorktrees, elsewhere...),
				fmt.Sprintf("Active sessions %s claim %s while associated worktrees have WIP: %s.", strings.Join(activeSessions, ", "), trail.Key, strings.Join(elsewhere, ", ")), evidence)
		}

		var outsideSessions, outsideWorktrees []string
		for _, association := range active {
			if association.Worktree != "" && trail.CanonicalWorktree != "" && association.Worktree != trail.CanonicalWorktree {
				outsideSessions = append(outsideSessions, association.SessionKey)
				outsideWorktrees = append(outsideWorktrees, association.Worktree)
			}
		}
		outsideSessions = sortedUnique(outsideSessions)
		outsideWorktrees = sortedUnique(outsideWorktrees)
		if len(outsideSessions) > 0 {
			addWTFFinding(findings, now, "outside-canonical", 2, trail, outsideSessions, append(outsideWorktrees, trail.CanonicalWorktree),
				fmt.Sprintf("Active sessions %s work outside canonical worktree %s from %s.", strings.Join(outsideSessions, ", "), trail.CanonicalWorktree, strings.Join(outsideWorktrees, ", ")),
				append(append([]string(nil), outsideSessions...), append([]string{trail.CanonicalWorktree}, outsideWorktrees...)...))
		}

		var defaultSessions, defaultWorktrees []string
		for _, association := range active {
			worktree, ok := state.Worktrees[association.Worktree]
			if ok && worktree.Branch != "" && worktree.Branch == strings.TrimPrefix(worktree.DefaultBranch, "origin/") {
				defaultSessions = append(defaultSessions, association.SessionKey)
				defaultWorktrees = append(defaultWorktrees, association.Worktree)
			}
		}
		defaultSessions = sortedUnique(defaultSessions)
		defaultWorktrees = sortedUnique(defaultWorktrees)
		if len(defaultSessions) > 0 {
			addWTFFinding(findings, now, "default-branch", 3, trail, defaultSessions, defaultWorktrees,
				fmt.Sprintf("Active sessions %s claim %s on the resolved default branch in %s.", strings.Join(defaultSessions, ", "), trail.Key, strings.Join(defaultWorktrees, ", ")),
				append(append([]string(nil), defaultSessions...), defaultWorktrees...))
		}

		canonical, canonicalKnown := state.Worktrees[trail.CanonicalWorktree]
		canonicalMissing := trail.CanonicalWorktree != "" && (!canonicalKnown || !canonical.Exists)
		var supportingWorktrees []string
		for _, path := range activeWorktrees {
			if path != trail.CanonicalWorktree {
				supportingWorktrees = append(supportingWorktrees, path)
			}
		}
		for _, path := range associatedWorktrees(trail) {
			if path == trail.CanonicalWorktree {
				continue
			}
			worktree := state.Worktrees[path]
			if worktree.Exists && worktree.GitError == "" && (worktree.DirtyFiles > 0 || worktree.UnmergedCommits > 0) {
				supportingWorktrees = append(supportingWorktrees, path)
			}
		}
		supportingWorktrees = sortedUnique(supportingWorktrees)
		if canonicalMissing && len(supportingWorktrees) > 0 {
			addWTFFinding(findings, now, "missing-canonical", 1, trail, activeSessions, append(supportingWorktrees, trail.CanonicalWorktree),
				fmt.Sprintf("Canonical worktree %s is missing while associated worktrees remain active or have WIP: %s.", trail.CanonicalWorktree, strings.Join(supportingWorktrees, ", ")),
				append([]string{trail.CanonicalWorktree}, supportingWorktrees...))
		}
	}
	return findings
}

func mergeWTFFindings(prior, current map[string]wtfFinding, now int64) map[string]wtfFinding {
	merged := make(map[string]wtfFinding, len(prior)+len(current))
	for id, finding := range prior {
		finding.Active = false
		merged[id] = finding
	}
	for id, finding := range current {
		finding.Active = true
		finding.LastSeen = now
		if old, ok := prior[id]; ok {
			finding.FirstSeen = old.FirstSeen
			finding.Occurrence = old.Occurrence
			if finding.Occurrence == 0 {
				finding.Occurrence = 1
			}
			if old.Active {
				finding.Delivery = old.Delivery
			} else {
				finding.Occurrence++
				finding.Delivery = nil
			}
		} else {
			finding.FirstSeen = now
			finding.Occurrence = 1
		}
		merged[id] = finding
	}
	return merged
}

func wtfFindingID(kind, trail string, sessions, worktrees []string) string {
	parts := []string{kind, trail, strings.Join(sortedUnique(sessions), "\x00"), strings.Join(sortedUnique(worktrees), "\x00")}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return fmt.Sprintf("%s:%x", kind, sum[:12])
}

func addWTFFinding(findings map[string]wtfFinding, now int64, kind string, severity int, trail wtfTrail, sessions, worktrees []string, explanation string, evidence []string) {
	sessions = sortedUnique(sessions)
	worktrees = sortedUnique(worktrees)
	id := wtfFindingID(kind, trail.Key, sessions, worktrees)
	challenger := ""
	if len(sessions) > 0 {
		challenger = sessions[0]
		if challenger == trail.OwnerSession && len(sessions) > 1 {
			challenger = sessions[1]
		}
	}
	findings[id] = wtfFinding{ID: id, Kind: kind, Severity: severity, TrailKey: trail.Key, Owner: trail.OwnerSession, Challenger: challenger,
		Worktrees: worktrees, Explanation: explanation, Evidence: sortedUnique(evidence), FirstSeen: now, LastSeen: now, Active: true, Occurrence: 1}
}

func activeTrailAssociations(state wtfState, trail wtfTrail) []wtfAssociation {
	bySession := make(map[string]wtfAssociation)
	for _, association := range trail.Associations {
		session, ok := state.Sessions[association.SessionKey]
		if !ok || !session.Active {
			continue
		}
		path := session.Cwd
		if path == "" {
			path = association.Worktree
		}
		worktree, local := state.Worktrees[path]
		if !local || !worktree.Exists || worktree.GitError != "" {
			continue
		}
		bySession[association.SessionKey] = wtfAssociation{SessionKey: association.SessionKey, Worktree: path}
	}
	keys := sortedMapKeys(bySession)
	out := make([]wtfAssociation, 0, len(keys))
	for _, key := range keys {
		out = append(out, bySession[key])
	}
	return out
}

func associationIDs(associations []wtfAssociation) ([]string, []string) {
	var sessions, worktrees []string
	for _, association := range associations {
		sessions = append(sessions, association.SessionKey)
		worktrees = append(worktrees, association.Worktree)
	}
	return sortedUnique(sessions), sortedUnique(worktrees)
}

func associatedWorktrees(trail wtfTrail) []string {
	paths := make([]string, 0, len(trail.Associations))
	for _, association := range trail.Associations {
		paths = append(paths, association.Worktree)
	}
	return sortedUnique(paths)
}

func sortedUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func nonemptyLines(data []byte) []string {
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, line := range lines {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func boundedLines(lines []string, limit int) []string {
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return append([]string(nil), lines...)
}

type trailTextEvent struct {
	At     int64
	Source string
	Text   string
}

type trailContext struct {
	CurrentRepo string
	KnownRepos  []string
}

type trailEvidence struct {
	Key        string
	Owner      string
	Repo       string
	Number     int
	URL        string
	Matched    string
	Source     string
	At         int64
	Resolved   bool
	Resolution string
}

type wtfObservation struct {
	SessionKey string
	Active     bool
	Repo       string
	Worktree   string
	Branch     string
	Evidence   trailEvidence
}

func scanWTF(ctx context.Context, home string, prior wtfState, deps wtfScanDeps) (wtfState, error) {
	now := deps.Now()
	nowUnix := now.Unix()
	initializeWTFStateMaps(&prior)
	sessions := collectWTFSessions(home, nowUnix, now.Location(), deps.Inventory)
	state := prior
	state.Sessions = make(map[string]wtfSession, len(sessions))
	for _, session := range sessions {
		state.Sessions[wtfSessionKey(session.Agent, session.ID)] = session
	}
	expireWTFSessions(&state, localMidnight(nowUnix, now.Location()))

	knownRepos := make([]string, 0, len(prior.Trails)+len(sessions))
	for _, trail := range prior.Trails {
		knownRepos = append(knownRepos, trail.Owner+"/"+trail.Repo)
	}
	for _, session := range sessions {
		knownRepos = append(knownRepos, session.Repo)
	}
	knownRepos = sortedUnique(knownRepos)
	evidence := make(map[string][]trailEvidence)
	var degraded []error
	transcriptFailures := make(map[string]bool)
	for _, session := range sessions {
		if !session.Active {
			continue
		}
		key := wtfSessionKey(session.Agent, session.ID)
		events, err := transcriptTrailEvents(session, home, session.LastActivity)
		if err != nil {
			degraded = append(degraded, err)
			transcriptFailures[key] = true
			continue
		}
		evidence[key] = extractTrailEvidence(events, trailContext{CurrentRepo: session.Repo, KnownRepos: knownRepos})
	}

	repoCandidates := make(map[string][]string)
	for _, session := range sessions {
		if session.Repo != "" && session.Cwd != "" {
			repoCandidates[session.Repo] = append(repoCandidates[session.Repo], session.Cwd)
		}
	}
	for _, worktree := range prior.Worktrees {
		if worktree.Repo != "" && worktree.Path != "" {
			repoCandidates[worktree.Repo] = append(repoCandidates[worktree.Repo], worktree.Path)
		}
	}
	worktrees := make(map[string]wtfWorktree)
	localFailed := false
	for _, repo := range sortedMapKeys(repoCandidates) {
		var old []wtfWorktree
		for _, worktree := range prior.Worktrees {
			if worktree.Repo == repo {
				old = append(old, worktree)
			}
		}
		cwd := deterministicRepoSeed(repoCandidates[repo])
		inspected, err := inspectRepoWorktrees(ctx, repo, cwd, nowUnix, old, deps.Run)
		if err != nil {
			degraded = append(degraded, fmt.Errorf("git worktrees for %s: %w", repo, err))
			localFailed = true
		}
		for _, worktree := range inspected {
			worktrees[worktree.Path] = worktree
		}
	}

	state = reconcileTrailsInternal(state, sessions, evidence, worktrees, nowUnix, false)
	state.Findings = mergeWTFFindings(prior.Findings, detectWTFFindings(state, nowUnix), nowUnix)
	for _, key := range sortedMapKeys(state.Trails) {
		trail := state.Trails[key]
		if !trailMetadataDue(trail, len(activeTrailAssociations(state, trail)) > 0, nowUnix) {
			continue
		}
		updated, err := fetchTrailMetadata(ctx, trail, nowUnix, deps.Run)
		state.Trails[key] = updated
		if err != nil {
			degraded = append(degraded, fmt.Errorf("trail metadata %s: %w", key, err))
		}
	}
	state = reconcileTrails(state, sessions, evidence, state.Worktrees, nowUnix)
	state.Findings = mergeWTFFindings(prior.Findings, detectWTFFindings(state, nowUnix), nowUnix)

	for _, key := range sortedMapKeys(state.Sessions) {
		session := state.Sessions[key]
		old, existed := prior.Sessions[key]
		if transcriptFailures[key] {
			if existed {
				session.Summary = old.Summary
				session.NeedsUser = old.NeedsUser
			}
			state.Sessions[key] = session
			continue
		}
		if existed && !wtfSessionChanged(old, session) {
			session.Summary = old.Summary
			session.NeedsUser = old.NeedsUser
			state.Sessions[key] = session
			continue
		}
		if session.Transcript == "" {
			session.Summary = fallbackWTFSummary(session)
			session.NeedsUser = deterministicNeed(home, session)
			state.Sessions[key] = session
			continue
		}
		summary, cache, err := deps.Summarize(session, home, state.SummaryCache[key])
		state.SummaryCache[key] = cache
		session.Summary = summary.Summary
		session.NeedsUser = summary.NeedsUser
		state.Sessions[key] = session
		if err != nil {
			degraded = append(degraded, fmt.Errorf("summary %s: %w", key, err))
		}
	}
	state.UpdatedAt = nowUnix
	if len(degraded) > 0 {
		return state, &wtfScanError{errors: degraded, localFailed: localFailed}
	}
	return state, nil
}

type wtfScanError struct {
	errors      []error
	localFailed bool
}

func (e *wtfScanError) Error() string   { return errors.Join(e.errors...).Error() }
func (e *wtfScanError) Unwrap() []error { return e.errors }

func deterministicRepoSeed(candidates []string) string {
	paths := sortedUnique(candidates)
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
	}
	if len(paths) > 0 {
		return paths[0]
	}
	return ""
}

func wtfSessionChanged(old, current wtfSession) bool {
	return old.Agent != current.Agent || old.ID != current.ID || old.Name != current.Name || old.Repo != current.Repo ||
		old.Cwd != current.Cwd || old.Branch != current.Branch || old.Transcript != current.Transcript || old.State != current.State ||
		old.Active != current.Active || old.StartedAt != current.StartedAt || old.LastActivity != current.LastActivity || old.SocketPath != current.SocketPath
}

func reconcileTrails(prior wtfState, sessions []wtfSession, evidence map[string][]trailEvidence, worktrees map[string]wtfWorktree, now int64) wtfState {
	return reconcileTrailsInternal(prior, sessions, evidence, worktrees, now, true)
}

func reconcileTrailsInternal(prior wtfState, sessions []wtfSession, evidence map[string][]trailEvidence, worktrees map[string]wtfWorktree, now int64, chooseCanonical bool) wtfState {
	state := prior
	if state.Trails == nil {
		state.Trails = make(map[string]wtfTrail)
	} else {
		trails := make(map[string]wtfTrail, len(state.Trails))
		for key, trail := range state.Trails {
			trails[key] = trail
		}
		state.Trails = trails
	}
	state.Worktrees = make(map[string]wtfWorktree, len(worktrees))
	for path, worktree := range worktrees {
		state.Worktrees[path] = worktree
	}

	var observations []wtfObservation
	for _, session := range sessions {
		key := wtfSessionKey(session.Agent, session.ID)
		for _, found := range evidence[key] {
			if found.Resolved {
				observations = append(observations, wtfObservation{SessionKey: key, Active: session.Active, Repo: session.Repo, Worktree: session.Cwd, Branch: session.Branch, Evidence: found})
			}
		}
	}
	sort.SliceStable(observations, func(i, j int) bool {
		if observations[i].Evidence.At != observations[j].Evidence.At {
			return observations[i].Evidence.At < observations[j].Evidence.At
		}
		if observations[i].SessionKey != observations[j].SessionKey {
			return observations[i].SessionKey < observations[j].SessionKey
		}
		return observations[i].Worktree < observations[j].Worktree
	})

	claims := make(map[string][]wtfClaim)
	for _, observation := range observations {
		if !observation.Active {
			continue
		}
		found := observation.Evidence
		trail, exists := state.Trails[found.Key]
		if !exists {
			trail = wtfTrail{Key: found.Key, Owner: found.Owner, Repo: found.Repo, Number: found.Number, URL: found.URL, FirstSeen: found.At}
		}
		trail.LastSeen = now
		association := wtfAssociation{SessionKey: observation.SessionKey, Worktree: observation.Worktree, At: found.At, Evidence: found.Matched, Source: found.Source}
		trail.Associations = addAssociation(trail.Associations, association)
		if worktree, ok := worktrees[observation.Worktree]; ok && worktree.Exists && worktree.GitError == "" && worktree.Repo == observation.Repo {
			claim := wtfClaim{SessionKey: observation.SessionKey, Worktree: observation.Worktree, At: found.At, Evidence: found.Matched}
			claims[trail.Key] = append(claims[trail.Key], claim)
			if trail.FirstClaim == nil {
				copy := claim
				trail.FirstClaim = &copy
			}
			if trail.OwnerSession == "" {
				trail.OwnerSession = claim.SessionKey
			}
			associateWorktree(&state, observation.Worktree, observation.SessionKey, trail.Key)
		}
		state.Trails[trail.Key] = trail
	}

	trailKeys := make([]string, 0, len(state.Trails))
	for key := range state.Trails {
		trailKeys = append(trailKeys, key)
	}
	sort.Strings(trailKeys)
	for _, key := range trailKeys {
		trail := state.Trails[key]
		for _, path := range sortedMapKeys(worktrees) {
			worktree := worktrees[path]
			if !worktree.Exists || worktree.GitError != "" || worktree.Repo != trail.Owner+"/"+trail.Repo {
				continue
			}
			if trail.SourceBranch != "" && worktree.Branch == trail.SourceBranch {
				trail.Associations = addAssociation(trail.Associations, wtfAssociation{Worktree: path, At: worktree.FirstSeen, Evidence: worktree.Branch, Source: "source branch"})
				associateWorktree(&state, path, "", trail.Key)
			}
			for _, gitEvidence := range worktree.GitEvidence {
				event := trailTextEvent{At: worktree.FirstSeen, Source: gitEvidence.Source, Text: gitEvidence.Text}
				context := trailContext{CurrentRepo: worktree.Repo, KnownRepos: []string{worktree.Repo}}
				for _, candidate := range trailMatches(gitEvidence.Text) {
					if candidate.kind == "bare" {
						continue
					}
					match := resolveTrailMatch(candidate, event, context)
					if match.Resolved && match.Key == key {
						trail.Associations = addAssociation(trail.Associations, wtfAssociation{Worktree: path, At: match.At, Evidence: match.Matched, Source: gitEvidence.Source})
						associateWorktree(&state, path, "", trail.Key)
					}
				}
			}
		}
		if chooseCanonical && trail.CanonicalWorktree == "" {
			var canonicalClaims []wtfClaim
			if trail.FirstClaim != nil {
				canonicalClaims = append(canonicalClaims, *trail.FirstClaim)
			} else {
				canonicalClaims = append(canonicalClaims, claims[key]...)
			}
			for _, association := range trail.Associations {
				if association.Worktree != "" && trail.SourceBranch != "" && worktrees[association.Worktree].Branch == trail.SourceBranch {
					canonicalClaims = append(canonicalClaims, wtfClaim{SessionKey: association.SessionKey, Worktree: association.Worktree, At: association.At, Evidence: association.Evidence})
				}
			}
			trail.CanonicalWorktree = chooseInitialCanonical(trail, canonicalClaims, worktrees)
		}
		trail.Associations = sortedAssociations(trail.Associations)
		state.Trails[key] = trail
	}
	state.Version = wtfStateVersion
	state.UpdatedAt = now
	return state
}

func chooseInitialCanonical(trail wtfTrail, claims []wtfClaim, worktrees map[string]wtfWorktree) string {
	sorted := append([]wtfClaim(nil), claims...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].At != sorted[j].At {
			return sorted[i].At < sorted[j].At
		}
		if sorted[i].SessionKey != sorted[j].SessionKey {
			return sorted[i].SessionKey < sorted[j].SessionKey
		}
		return sorted[i].Worktree < sorted[j].Worktree
	})
	if trail.SourceBranch != "" {
		for _, claim := range sorted {
			if worktrees[claim.Worktree].Branch == trail.SourceBranch {
				return claim.Worktree
			}
		}
	}
	if len(sorted) > 0 {
		return sorted[0].Worktree
	}
	return ""
}

func addAssociation(existing []wtfAssociation, next wtfAssociation) []wtfAssociation {
	for _, association := range existing {
		if association.SessionKey == next.SessionKey && association.Worktree == next.Worktree && association.Evidence == next.Evidence && association.Source == next.Source {
			return existing
		}
	}
	return sortedAssociations(append(existing, next))
}

func sortedAssociations(existing []wtfAssociation) []wtfAssociation {
	out := append([]wtfAssociation(nil), existing...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.At != b.At {
			return a.At < b.At
		}
		if a.SessionKey != b.SessionKey {
			return a.SessionKey < b.SessionKey
		}
		if a.Worktree != b.Worktree {
			return a.Worktree < b.Worktree
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Evidence < b.Evidence
	})
	return out
}

func associateWorktree(state *wtfState, path, sessionKey, trailKey string) {
	worktree, ok := state.Worktrees[path]
	if !ok {
		return
	}
	worktree.SessionKeys = addString(worktree.SessionKeys, sessionKey)
	worktree.TrailKeys = addString(worktree.TrailKeys, trailKey)
	state.Worktrees[path] = worktree
}

func addString(existing []string, next string) []string {
	if next == "" {
		return existing
	}
	for _, value := range existing {
		if value == next {
			return existing
		}
	}
	return append(existing, next)
}

type trailPattern struct {
	kind string
	re   *regexp.Regexp
}

var trailPatterns = []trailPattern{
	{"url", regexp.MustCompile(`(?i)https://entire\.io/(?:gh|et)/([a-z0-9_.-]+)/([a-z0-9_.-]+)/trails/([0-9]+)`)},
	{"qualified", regexp.MustCompile(`(?i)([a-z0-9_.-]+)/([a-z0-9_.-]+)#([0-9]+)`)},
	{"repo", regexp.MustCompile(`(?i)([a-z0-9_.-]+)#([0-9]+)`)},
	{"bare", regexp.MustCompile(`(?i)trail[ ]+#?([0-9]+)`)},
}

type trailMatch struct {
	start, end int
	kind       string
	matched    string
	parts      []string
	// prefix is the word before a bare "trail N", which may name its repo.
	prefix string
}

// trailClaimSource reports whether text from this source is the session's
// own words. Tool text is what the session read or searched for (a memory
// index, a spec, a grep hit), and naming a trail there is not working on it.
func trailClaimSource(source string) bool {
	return source != "tool input" && source != "tool result"
}

func extractTrailEvidence(events []trailTextEvent, ctx trailContext) []trailEvidence {
	byIdentity := make(map[string]trailEvidence)
	for _, event := range events {
		if !trailClaimSource(event.Source) {
			continue
		}
		for _, match := range trailMatches(event.Text) {
			evidence := resolveTrailMatch(match, event, ctx)
			identity := evidence.Key
			if identity == "" {
				identity = "unresolved:" + strings.ToLower(evidence.Matched)
			}
			if old, ok := byIdentity[identity]; !ok || evidence.At < old.At {
				byIdentity[identity] = evidence
			}
		}
	}
	out := make([]trailEvidence, 0, len(byIdentity))
	for _, evidence := range byIdentity {
		out = append(out, evidence)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At < out[j].At
		}
		return out[i].Matched < out[j].Matched
	})
	return out
}

func trailMatches(text string) []trailMatch {
	var matches []trailMatch
	for _, pattern := range trailPatterns {
		for _, idx := range pattern.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := idx[0], idx[1]
			if !trailBoundaryBefore(text, start) || !trailBoundaryAfter(text, end) ||
				(pattern.kind == "repo" && start > 0 && text[start-1] == '/') {
				continue
			}
			parts := make([]string, 0, len(idx)/2-1)
			for i := 2; i < len(idx); i += 2 {
				if idx[i] < 0 {
					parts = append(parts, "")
				} else {
					parts = append(parts, text[idx[i]:idx[i+1]])
				}
			}
			candidate := trailMatch{start: start, end: end, kind: pattern.kind, matched: text[start:end], parts: parts}
			if pattern.kind == "bare" {
				candidate.prefix = wordBefore(text, start)
			}
			overlaps := false
			for _, accepted := range matches {
				if start < accepted.end && end > accepted.start {
					overlaps = true
					break
				}
			}
			if !overlaps {
				matches = append(matches, candidate)
			}
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	return matches
}

// wordBefore returns the token ending just before at, across spaces, without
// trailing punctuation: "company-knowledge trail 11" gives "company-knowledge".
func wordBefore(text string, at int) string {
	end := at
	for end > 0 && text[end-1] == ' ' {
		end--
	}
	start := end
	for start > 0 && isTrailTokenByte(text[start-1]) {
		start--
	}
	return strings.TrimRight(text[start:end], ".-")
}

func trailBoundaryBefore(text string, at int) bool {
	return at == 0 || !isTrailTokenByte(text[at-1])
}

func trailBoundaryAfter(text string, at int) bool {
	return at == len(text) || !isTrailTokenByte(text[at])
}

func isTrailTokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("_.@-", rune(b))
}

func resolveTrailMatch(match trailMatch, event trailTextEvent, ctx trailContext) trailEvidence {
	e := trailEvidence{Matched: match.matched, Source: event.Source, At: event.At}
	var owner, repo, numberText string
	switch match.kind {
	case "url":
		owner, repo, numberText = match.parts[0], match.parts[1], match.parts[2]
		e.Resolution = "full URL"
	case "qualified":
		owner, repo, numberText = match.parts[0], match.parts[1], match.parts[2]
		e.Resolution = "qualified owner/repo"
	case "repo":
		repo, numberText = match.parts[0], match.parts[1]
		currentOwner, currentRepo, ok := splitRepo(ctx.CurrentRepo)
		if ok && strings.EqualFold(repo, currentRepo) {
			owner, repo = currentOwner, currentRepo
			e.Resolution = "current repo basename"
		} else {
			var candidates [][2]string
			for _, known := range ctx.KnownRepos {
				o, r, valid := splitRepo(known)
				if valid && strings.EqualFold(repo, r) {
					candidates = append(candidates, [2]string{o, r})
				}
			}
			if len(candidates) == 1 {
				owner, repo = candidates[0][0], candidates[0][1]
				e.Resolution = "unique known repo basename"
			} else if len(candidates) == 0 {
				e.Resolution = "no repo matches shorthand"
			} else {
				e.Resolution = "ambiguous repo shorthand"
			}
		}
	case "bare":
		numberText = match.parts[0]
		owner, repo, e.Resolution = resolveBareTrailRepo(match.prefix, ctx)
	}
	number, err := strconv.Atoi(numberText)
	if err != nil || number <= 0 {
		e.Resolution = "invalid trail number"
		return e
	}
	e.Number = number
	if owner == "" || repo == "" {
		return e
	}
	e.Owner, e.Repo = strings.ToLower(owner), strings.ToLower(repo)
	e.Key = e.Owner + "/" + e.Repo + "#" + strconv.Itoa(e.Number)
	e.URL = "https://entire.io/gh/" + e.Owner + "/" + e.Repo + "/trails/" + strconv.Itoa(e.Number)
	e.Resolved = true
	return e
}

// resolveBareTrailRepo picks the repo for a bare "trail N". The word before it
// can name the repo ("company-knowledge trail 11"); binding that to the
// current repo filed a mention of another repo's trail against this one.
func resolveBareTrailRepo(prefix string, ctx trailContext) (owner, repo, resolution string) {
	currentOwner, currentRepo, currentOK := splitRepo(ctx.CurrentRepo)
	if prefix != "" && !(currentOK && strings.EqualFold(prefix, currentRepo)) {
		var candidates [][2]string
		for _, known := range ctx.KnownRepos {
			o, r, valid := splitRepo(known)
			if valid && strings.EqualFold(prefix, r) {
				candidates = append(candidates, [2]string{o, r})
			}
		}
		switch {
		case len(candidates) == 1:
			return candidates[0][0], candidates[0][1], "named known repo"
		case len(candidates) > 1:
			return "", "", "ambiguous named repo"
		case strings.ContainsAny(prefix, "-_."):
			// Shaped like a repo name, not a word of prose.
			return "", "", "names an unknown repo"
		}
	}
	if !currentOK {
		return "", "", "bare trail without current repo"
	}
	return currentOwner, currentRepo, "current repo"
}

func splitRepo(value string) (string, string, bool) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func claudeTrailEvents(path string, observedAt int64) ([]trailTextEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []trailTextEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event claudeEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Message == nil || (event.Type != "user" && event.Type != "assistant") {
			continue
		}
		if event.Type == "user" && isSyntheticUser(event.Origin.Kind, event.PromptSource, event.IsMeta) {
			continue
		}
		at := parsedTrailTime(event.Timestamp, observedAt)
		var plain string
		if json.Unmarshal(event.Message.Content, &plain) == nil {
			if event.Type == "user" && !isTaskNote(event.Origin.Kind, event.PromptSource, plain) {
				events = append(events, trailTextEvent{At: at, Source: "user", Text: plain})
			}
			continue
		}
		var blocks []claudeBlock
		if json.Unmarshal(event.Message.Content, &blocks) != nil {
			continue
		}
		if event.Type == "user" {
			var text strings.Builder
			for _, block := range blocks {
				if block.Type == "text" {
					text.WriteString(block.Text)
				}
			}
			if isTaskNote(event.Origin.Kind, event.PromptSource, text.String()) {
				continue
			}
		}
		for _, block := range blocks {
			switch block.Type {
			case "text":
				events = append(events, trailTextEvent{At: at, Source: event.Type, Text: block.Text})
			case "tool_use":
				events = appendJSONText(events, at, "tool input", block.Input)
			case "tool_result":
				events = appendJSONText(events, at, "tool result", block.Content)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func ampTrailEvents(export ampExport, observedAt int64) []trailTextEvent {
	var events []trailTextEvent
	for _, message := range export.Messages {
		at := parsedTrailTime(message.CreatedAt, observedAt)
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				if message.Role == "user" || message.Role == "assistant" {
					events = append(events, trailTextEvent{At: at, Source: message.Role, Text: block.Text})
				}
			case "tool_use":
				events = appendJSONText(events, at, "tool input", block.Input)
			case "tool_result":
				events = appendJSONText(events, at, "tool result", block.Run)
			}
		}
	}
	return events
}

func transcriptTrailEvents(session wtfSession, home string, observedAt int64) ([]trailTextEvent, error) {
	switch session.Agent {
	case AgentClaude:
		events, err := claudeTrailEvents(session.Transcript, observedAt)
		if err != nil {
			return nil, fmt.Errorf("claude transcript %s: %w", session.ID, err)
		}
		return events, nil
	case AgentAmp:
		path := session.Transcript
		if path == "" {
			path = filepath.Join(ampCacheDir(home), "exports", session.ID+".json")
		}
		export, err := readAmpExport(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("amp transcript %s: %w", session.ID, err)
		}
		return ampTrailEvents(export, observedAt), nil
	default:
		return nil, nil
	}
}

func parsedTrailTime(value string, fallback int64) int64 {
	if value == "" {
		return fallback
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fallback
	}
	return parsed.Unix()
}

func appendJSONText(events []trailTextEvent, at int64, source string, raw json.RawMessage) []trailTextEvent {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return events
	}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			events = append(events, trailTextEvent{At: at, Source: source, Text: typed})
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(typed[key])
			}
		}
	}
	walk(value)
	return events
}
