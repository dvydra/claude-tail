package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const wtfStateVersion = 1

type wtfState struct {
	Version      int                        `json:"version"`
	UpdatedAt    int64                      `json:"updatedAt"`
	Sessions     map[string]wtfSession      `json:"sessions"`
	Trails       map[string]wtfTrail        `json:"trails"`
	Worktrees    map[string]wtfWorktree     `json:"worktrees"`
	Findings     map[string]wtfFinding      `json:"findings"`
	SummaryCache map[string]wtfSummaryCache `json:"summaryCache,omitempty"`
}

type wtfTrail struct {
	Key               string           `json:"key"`
	Owner             string           `json:"owner"`
	Repo              string           `json:"repo"`
	Number            int              `json:"number"`
	URL               string           `json:"url"`
	Title             string           `json:"title,omitempty"`
	Status            string           `json:"status,omitempty"`
	SourceBranch      string           `json:"sourceBranch,omitempty"`
	TargetBranch      string           `json:"targetBranch,omitempty"`
	MetadataUpdatedAt int64            `json:"metadataUpdatedAt,omitempty"`
	MetadataError     string           `json:"metadataError,omitempty"`
	MetadataAttempts  int              `json:"metadataAttempts,omitempty"`
	MetadataNextRetry int64            `json:"metadataNextRetry,omitempty"`
	CanonicalWorktree string           `json:"canonicalWorktree,omitempty"`
	OwnerSession      string           `json:"ownerSession,omitempty"`
	FirstClaim        *wtfClaim        `json:"firstClaim,omitempty"`
	Associations      []wtfAssociation `json:"associations,omitempty"`
	FirstSeen         int64            `json:"firstSeen"`
	LastSeen          int64            `json:"lastSeen"`
	LastWIPAt         int64            `json:"lastWipAt,omitempty"`
}

type wtfClaim struct {
	SessionKey string `json:"sessionKey"`
	Worktree   string `json:"worktree"`
	At         int64  `json:"at"`
	Evidence   string `json:"evidence"`
}

type wtfAssociation struct {
	SessionKey string `json:"sessionKey,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	At         int64  `json:"at"`
	Evidence   string `json:"evidence"`
	Source     string `json:"source"`
}

type wtfGitEvidence struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

type wtfWorktree struct {
	Repo            string           `json:"repo"`
	Path            string           `json:"path"`
	Branch          string           `json:"branch,omitempty"`
	Head            string           `json:"head,omitempty"`
	Exists          bool             `json:"exists"`
	DirtyFiles      int              `json:"dirtyFiles"`
	DirtySummary    []string         `json:"dirtySummary,omitempty"`
	DefaultBranch   string           `json:"defaultBranch,omitempty"`
	UnmergedCommits int              `json:"unmergedCommits"`
	GitError        string           `json:"gitError,omitempty"`
	SessionKeys     []string         `json:"sessionKeys,omitempty"`
	TrailKeys       []string         `json:"trailKeys,omitempty"`
	GitEvidence     []wtfGitEvidence `json:"gitEvidence,omitempty"`
	FirstSeen       int64            `json:"firstSeen"`
	LastSeen        int64            `json:"lastSeen"`
	LastWIPAt       int64            `json:"lastWipAt,omitempty"`
}

type wtfFinding struct {
	ID          string                       `json:"id"`
	Kind        string                       `json:"kind"`
	Severity    int                          `json:"severity"`
	TrailKey    string                       `json:"trailKey"`
	Owner       string                       `json:"owner,omitempty"`
	Challenger  string                       `json:"challenger,omitempty"`
	Worktrees   []string                     `json:"worktrees,omitempty"`
	Explanation string                       `json:"explanation"`
	Evidence    []string                     `json:"evidence"`
	FirstSeen   int64                        `json:"firstSeen"`
	LastSeen    int64                        `json:"lastSeen"`
	Active      bool                         `json:"active"`
	Occurrence  int                          `json:"occurrence"`
	Delivery    map[string]wtfDeliveryStatus `json:"delivery,omitempty"`
}

type wtfDeliveryStatus struct {
	State       string `json:"state"`
	Attempts    int    `json:"attempts"`
	LastAttempt int64  `json:"lastAttempt,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}

func wtfDir(home string) string {
	return filepath.Join(home, "Library", "Application Support", "entire-tail", "wtf")
}

func wtfStatePath(home string) string { return filepath.Join(wtfDir(home), "state.json") }

func newWTFState(now int64) wtfState {
	return wtfState{
		Version: wtfStateVersion, UpdatedAt: now,
		Sessions: make(map[string]wtfSession), Trails: make(map[string]wtfTrail),
		Worktrees: make(map[string]wtfWorktree), Findings: make(map[string]wtfFinding),
		SummaryCache: make(map[string]wtfSummaryCache),
	}
}

func initializeWTFStateMaps(state *wtfState) {
	if state.Sessions == nil {
		state.Sessions = make(map[string]wtfSession)
	}
	if state.Trails == nil {
		state.Trails = make(map[string]wtfTrail)
	}
	if state.Worktrees == nil {
		state.Worktrees = make(map[string]wtfWorktree)
	}
	if state.Findings == nil {
		state.Findings = make(map[string]wtfFinding)
	}
	if state.SummaryCache == nil {
		state.SummaryCache = make(map[string]wtfSummaryCache)
	}
}

func loadWTFState(home string, now int64) (wtfState, error) {
	state := newWTFState(now)
	data, err := os.ReadFile(wtfStatePath(home))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		corrupt := filepath.Join(wtfDir(home), fmt.Sprintf("state.corrupt-%d.json", now))
		if renameErr := os.Rename(wtfStatePath(home), corrupt); renameErr != nil {
			return newWTFState(now), fmt.Errorf("recover corrupt wtf state: decode: %v; preserve: %w", err, renameErr)
		}
		return newWTFState(now), fmt.Errorf("recovered corrupt wtf state as %s: %w", corrupt, err)
	}
	initializeWTFStateMaps(&state)
	return state, nil
}

func saveWTFState(home string, state wtfState) (err error) {
	dir := wtfDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	initializeWTFStateMaps(&state)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, wtfStatePath(home)); err != nil {
		return err
	}
	dirFile, openErr := os.Open(dir)
	if openErr != nil {
		return openErr
	}
	err = dirFile.Sync()
	closeErr := dirFile.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func expireWTFSessions(state *wtfState, midnight int64) {
	for key, session := range state.Sessions {
		if !session.Active && session.State == "ended" && session.LastActivity < midnight {
			delete(state.Sessions, key)
		}
	}
}

type wtfSession struct {
	Agent        Agent
	ID           string
	Name         string
	Repo         string
	Cwd          string
	Branch       string
	Transcript   string
	State        string
	Active       bool
	StartedAt    int64
	LastActivity int64
	SocketPath   string
	Summary      string
	NeedsUser    string
}

type wtfSnapshot struct {
	GeneratedAt int64
	Home        string
	Sessions    []wtfSession
}

type wtfInventoryDeps struct {
	Today func(home string, now int64, loc *time.Location) []handoverItem
	Live  func(home string) []liveSession
}

type wtfSummarizer func(wtfSession, string, wtfSummaryCache) (wtfSummary, wtfSummaryCache, error)

func collectWTFSessions(home string, now int64, loc *time.Location, deps wtfInventoryDeps) []wtfSession {
	byID := map[string]wtfSession{}
	for _, item := range deps.Today(home, now, loc) {
		s := wtfSession{
			Agent: item.Agent, ID: item.SessionID, Name: item.Title, Repo: item.Repo,
			Cwd: item.Cwd, Branch: item.Branch, Transcript: item.Path,
			State: "ended", LastActivity: item.LastActivity,
		}
		byID[wtfSessionKey(s.Agent, s.ID)] = s
	}

	repoCache := map[string]string{}
	for _, live := range deps.Live(home) {
		key := wtfSessionKey(live.Agent, live.SessionID)
		s := byID[key]
		s.Agent = live.Agent
		s.ID = live.SessionID
		s.Name = live.Name
		s.Cwd = live.Cwd
		s.Branch = live.Branch
		s.Transcript = live.Path
		s.State = firstNonEmpty(live.Status, "active")
		s.Active = true
		s.StartedAt = live.StartedAt / 1000
		s.LastActivity = live.UpdatedAt / 1000
		s.SocketPath = live.SocketPath
		if s.Repo == "" && s.Cwd != "" {
			s.Repo = repoForCwd(s.Cwd, home, repoCache)
		}
		byID[key] = s
	}

	out := make([]wtfSession, 0, len(byID))
	for _, session := range byID {
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		if (out[i].State == "busy") != (out[j].State == "busy") {
			return out[i].State == "busy"
		}
		if out[i].LastActivity != out[j].LastActivity {
			return out[i].LastActivity > out[j].LastActivity
		}
		return wtfSessionKey(out[i].Agent, out[i].ID) < wtfSessionKey(out[j].Agent, out[j].ID)
	})
	return out
}

func wtfSessionKey(agent Agent, id string) string {
	return string(agent) + ":" + id
}

func summarizeWTFSnapshot(snapshot wtfSnapshot, home string, cache map[string]wtfSummaryCache, summarize wtfSummarizer) (wtfSnapshot, map[string]wtfSummaryCache) {
	if cache == nil {
		cache = make(map[string]wtfSummaryCache)
	}
	for i := range snapshot.Sessions {
		session := &snapshot.Sessions[i]
		if session.Transcript == "" {
			session.Summary = fallbackWTFSummary(*session)
			session.NeedsUser = deterministicNeed(home, *session)
			continue
		}
		key := wtfSessionKey(session.Agent, session.ID)
		summary, updated, _ := summarize(*session, home, cache[key])
		cache[key] = updated
		session.Summary = summary.Summary
		session.NeedsUser = summary.NeedsUser
	}
	return snapshot, cache
}

func runWTF(cfg Config) error {
	if len(cfg.WTFArgs) > 0 {
		return fmt.Errorf("wtf: unsupported arguments: %s", strings.Join(cfg.WTFArgs, " "))
	}

	home := firstNonEmpty(os.Getenv("HOME"), mustHome())
	chosen, err := runWTFDashboard(home, cfg)
	if err != nil || chosen == nil {
		return err
	}
	claudeBin := resolveClaudeBin(cfg, exec.LookPath, os.Stderr)
	resolved, ok := resolveTreeChoice(home, claudeBin, wtfTreeChoice(*chosen))
	if !ok {
		return nil
	}
	cfg.WTFArgs = nil
	cfg.Agent = string(firstNonEmptyAgent(chosen.Agent, AgentClaude))
	cfg.Pick = "never"
	if chosen.Agent == AgentAmp {
		cfg.FollowSession = chosen.ID
	} else {
		cfg.Positional = []string{resolved}
	}
	run(cfg)
	return nil
}
