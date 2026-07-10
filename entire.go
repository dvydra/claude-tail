package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// entire.go sources the session tree from the `entire` CLI's cloud inventory
// (`entire api /me/sessions`) instead of crawling ~/.claude. Entire returns rich
// per-session metadata — a generated title, repo, agent/model, checkpoint counts,
// last-activity — for every tracked session across every repo, so the listing
// needs no local file reads at all. A session's uuid still maps to its local
// jsonl (when present on this machine), which is resolved lazily for tailing and
// for `claude --resume`.

// entireSession is one row from /api/v1/me/sessions.
type entireSession struct {
	SessionID       string `json:"sessionId"`
	DisplayName     string `json:"displayName"`
	CustomName      string `json:"customName"`
	Prompt          string `json:"prompt"`
	Agent           string `json:"agent"`
	Model           string `json:"model"`
	Repo            string `json:"repo_full_name"`
	StartedAt       string `json:"startedAt"`
	LastActivityAt  string `json:"lastActivityAt"`
	CheckpointCount int    `json:"checkpointCount"`
	StepCount       int    `json:"stepCount"`
}

// entireAvailable reports whether the `entire` CLI is on PATH.
func entireAvailable() bool {
	_, err := exec.LookPath("entire")
	return err == nil
}

// localTimezone returns the IANA zone name (e.g. "Australia/Melbourne") from
// /etc/localtime, falling back to $TZ then UTC. The sessions API requires it.
func localTimezone() string {
	if p, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.LastIndex(p, "zoneinfo/"); i >= 0 {
			return p[i+len("zoneinfo/"):]
		}
	}
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	return "UTC"
}

// fetchEntireSessions pulls the user's tracked sessions from their Entire cell,
// newest-first is not guaranteed (buildEntireTree sorts). It times out so a slow
// or offline network degrades to the local crawl rather than hanging.
func fetchEntireSessions() ([]entireSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	path := "/api/v1/me/sessions?timezone=" + localTimezone()
	out, err := exec.CommandContext(ctx, "entire", "api", "--to", "cell", path).Output()
	if err != nil {
		return nil, err
	}
	var body struct {
		Sessions []entireSession `json:"sessions"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return nil, err
	}
	return body.Sessions, nil
}

// buildSessionTree builds the picker tree. The local ~/.claude crawl is the
// complete base (every session on this machine); when the entire CLI is
// available it's merged in — regrouping by repo and overlaying entire's titles,
// and appending cloud-only sessions from other machines. --local (forceLocal),
// or entire being absent/offline/empty, yields the pure folder-grouped crawl.
func buildSessionTree(home, pwd string, days int, now int64, forceLocal bool) sessionTree {
	local := buildClaudeTree(home, pwd, days, now, claudeLiveCwds())
	if forceLocal || !entireAvailable() {
		return local
	}
	sessions, err := fetchEntireSessions()
	if err != nil || len(sessions) == 0 {
		return local // offline / logged out / nothing tracked → pure local
	}
	return mergeEntire(local, sessions, home, days, now)
}

// mergeEntire regroups the complete local tree by repo and folds in entire's
// cloud metadata: a tracked session gets entire's canonical repo + generated
// title; an untracked local session is grouped by its cwd's git remote (else its
// folder path). Cloud-only sessions (tracked elsewhere, not on this machine) are
// appended with no local path so they list but can't be tailed here.
func mergeEntire(local sessionTree, sessions []entireSession, home string, days int, now int64) sessionTree {
	byID := make(map[string]entireSession, len(sessions))
	for _, es := range sessions {
		byID[es.SessionID] = es
	}

	repoCache := map[string]string{}
	groups := map[string]*treeFolder{}
	var order []string
	add := func(repo string, s treeSession) {
		g, ok := groups[repo]
		if !ok {
			g = &treeFolder{Cwd: repo, Slug: repo}
			groups[repo] = g
			order = append(order, repo)
		}
		g.Sessions = append(g.Sessions, s)
		if s.Mtime > g.Mtime {
			g.Mtime = s.Mtime
		}
		if s.Live {
			g.Live++
		}
	}

	seen := map[string]bool{}
	for fi := range local.Folders {
		f := local.Folders[fi]
		for _, s := range f.Sessions {
			seen[s.ID] = true
			repo := ""
			if es, ok := byID[s.ID]; ok {
				repo = es.Repo
				if es.DisplayName != "" {
					s.Snippet = es.DisplayName // entire's title beats the raw snippet
				}
			}
			if repo == "" {
				repo = repoForCwd(f.Cwd, home, repoCache)
			}
			add(repo, s)
		}
	}

	// Cloud-only sessions (tracked, but their jsonl isn't on this machine).
	var cutoff int64
	if days > 0 {
		cutoff = now - int64(days)*86400
	}
	for _, es := range sessions {
		if es.SessionID == "" || seen[es.SessionID] {
			continue
		}
		mt := parseEntireTime(es.LastActivityAt)
		if cutoff > 0 && mt > 0 && mt < cutoff {
			continue
		}
		repo := es.Repo
		if repo == "" {
			repo = "(unknown repo)"
		}
		add(repo, treeSession{
			ID:      es.SessionID,
			Snippet: collapsePreview(firstNonEmpty(es.CustomName, es.DisplayName, es.Prompt)),
			Mtime:   mt,
			Msgs:    es.CheckpointCount,
			Live:    now-mt < recentLiveWindow,
		})
	}

	tree := sessionTree{Now: now, Home: home, Pwd: local.Pwd}
	for _, repo := range order {
		g := groups[repo]
		sort.SliceStable(g.Sessions, func(i, j int) bool { return g.Sessions[i].Mtime > g.Sessions[j].Mtime })
		tree.Folders = append(tree.Folders, *g)
	}
	sortFolders(tree.Folders)
	if len(tree.Folders) > 0 {
		tree.Folders[0].Expanded = true
	}
	return tree
}

// repoForCwd maps a working directory to an "owner/repo" group via its git
// origin remote, caching per cwd. Falls back to the tilde-abbreviated path when
// the dir isn't a git repo (so nothing is lost, just grouped by folder).
func repoForCwd(cwd, home string, cache map[string]string) string {
	if v, ok := cache[cwd]; ok {
		return v
	}
	repo := parseGitRemote(gitOriginURL(cwd))
	if repo == "" {
		repo = tildify(cwd, home)
	}
	cache[cwd] = repo
	return repo
}

func gitOriginURL(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseGitRemote reduces a git remote URL to "owner/repo". Handles scp-style
// (git@host:owner/repo.git), https/ssh URLs, and a trailing .git; returns "" for
// an empty/unrecognizable remote.
func parseGitRemote(u string) string {
	if u == "" {
		return ""
	}
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "://"); i >= 0 { // scheme://host/owner/repo
		if _, after, ok := strings.Cut(u[i+3:], "/"); ok {
			u = after
		}
	} else if i := strings.IndexByte(u, ':'); i >= 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:] // scp-style host:owner/repo
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ""
}

func parseEntireTime(s string) int64 {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	return 0
}

// sessionCwd reads a Claude session's recorded working directory (for cd'ing the
// iTerm workspace), falling back to the file's parent dir.
func sessionCwd(path string) string {
	if _, _, _, cwd := loadClaudeMeta(path); cwd != "" {
		return cwd
	}
	return filepath.Dir(path)
}
