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

// localClaudeSessionIDs maps every local Claude session uuid to its jsonl path,
// by globbing ~/.claude/projects (names only — no file contents are read). Used
// to resolve which cloud sessions are tailable on this machine.
func localClaudeSessionIDs(home string) map[string]string {
	m := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(claudeProjectsDir(home), "*", "*.jsonl"))
	for _, p := range matches {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		m[id] = p
	}
	return m
}

// buildSessionTree picks the discovery source: the entire CLI's cloud inventory
// when available (unless forceLocal), else the local ~/.claude crawl. If the
// entire fetch fails or is empty, it falls back to the crawl so offline / logged-
// out / unenabled setups still work.
func buildSessionTree(home, pwd string, days int, now int64, forceLocal bool) sessionTree {
	if !forceLocal && entireAvailable() {
		if sessions, err := fetchEntireSessions(); err == nil && len(sessions) > 0 {
			return buildEntireTree(sessions, localClaudeSessionIDs(home), days, now)
		}
	}
	return buildClaudeTree(home, pwd, days, now, claudeLiveCwds())
}

// buildEntireTree turns the cloud session inventory into the tree, grouped by
// repo (repo_full_name), newest-first. Each session's local jsonl path (if any)
// comes from localIDs — cloud-only sessions have an empty Path and can't be
// tailed on this machine. days>0 drops sessions older than the window.
func buildEntireTree(sessions []entireSession, localIDs map[string]string, days int, now int64) sessionTree {
	var cutoff int64
	if days > 0 {
		cutoff = now - int64(days)*86400
	}
	byRepo := map[string]*treeFolder{}
	var order []string
	for _, s := range sessions {
		mt := parseEntireTime(s.LastActivityAt)
		if cutoff > 0 && mt < cutoff {
			continue
		}
		repo := s.Repo
		if repo == "" {
			repo = "(unknown repo)"
		}
		f, ok := byRepo[repo]
		if !ok {
			f = &treeFolder{Cwd: repo, Slug: repo}
			byRepo[repo] = f
			order = append(order, repo)
		}
		f.Sessions = append(f.Sessions, treeSession{
			ID:      s.SessionID,
			Snippet: collapsePreview(firstNonEmpty(s.CustomName, s.DisplayName, s.Prompt)),
			Mtime:   mt,
			Msgs:    s.CheckpointCount,
			Path:    localIDs[s.SessionID], // "" when not on this machine
			Live:    now-mt < recentLiveWindow,
		})
		if mt > f.Mtime {
			f.Mtime = mt
		}
	}
	tree := sessionTree{Now: now}
	for _, repo := range order {
		f := byRepo[repo]
		sort.SliceStable(f.Sessions, func(i, j int) bool { return f.Sessions[i].Mtime > f.Sessions[j].Mtime })
		for _, s := range f.Sessions {
			if s.Live {
				f.Live++
			}
		}
		tree.Folders = append(tree.Folders, *f)
	}
	sortFolders(tree.Folders)
	if len(tree.Folders) > 0 {
		tree.Folders[0].Expanded = true // most-recent repo open by default
	}
	return tree
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
