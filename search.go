package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// search.go implements content search ("which session did we say 'fire socks'
// in?") across both sources: a fast local ripgrep over ~/.claude transcripts and
// entire's hybrid (semantic + keyword) checkpoint search. Hits are merged by
// session id and ranked — an exact local phrase match weighs heaviest, entire's
// semantic score adds to it, recency breaks ties — then presented as the usual
// tree so Enter/t resume or tail the match.

type searchHit struct {
	id          string
	path        string // local jsonl, "" if not on this machine
	snippet     string // match context (why it hit)
	repo        string
	branch      string
	displayName string
	mtime       int64
	localCount  int     // literal matches in the local transcript (rg)
	entireScore float64 // entire's relevance score
	entireHit   bool
}

// score ranks a hit. A literal local match dominates (the user typed the exact
// words), entire's semantic score adds on top, and matching both sources ranks
// highest of all.
func (h *searchHit) score() float64 {
	s := 0.0
	if h.entireHit {
		s += h.entireScore // typically ~5–8
	}
	if h.localCount > 0 {
		s += 10 + math.Min(float64(h.localCount), 5) // exact phrase present → strong
	}
	return s
}

// buildSearchTree runs both searches, merges + ranks, and returns a single-group
// tree ordered by relevance (not recency). localOnly skips the entire query.
func buildSearchTree(home, pwd, query string, localOnly bool, now int64) sessionTree {
	hits := map[string]*searchHit{}

	for path, count := range localSearchClaude(home, query) {
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		snippet, repo, branch := localMatchSnippet(path, query)
		hits[id] = &searchHit{
			id: id, path: path, localCount: count, mtime: statMtime(path),
			snippet: snippet, repo: repo, branch: branch,
		}
	}

	if !localOnly {
		for _, r := range entireSearchSessions(query) {
			h := hits[r.SessionID]
			if h == nil {
				h = &searchHit{id: r.SessionID, path: localPathForID(home, r.SessionID)}
				hits[r.SessionID] = h
			}
			h.entireHit = true
			h.entireScore = r.Score
			h.displayName = r.DisplayName
			if h.repo == "" {
				h.repo = r.Repo
			}
			if h.snippet == "" {
				h.snippet = collapsePreview(cleanMatch(firstNonEmpty(r.Snippet, r.DisplayName)))
			}
			if h.mtime == 0 {
				h.mtime = parseEntireTime(r.CreatedAt)
			}
		}
	}

	list := make([]*searchHit, 0, len(hits))
	for _, h := range hits {
		list = append(list, h)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if si, sj := list[i].score(), list[j].score(); si != sj {
			return si > sj
		}
		return list[i].mtime > list[j].mtime
	})

	const cap = 50 // keep the ranked view useful; ubiquitous terms match everything
	label := fmt.Sprintf("🔎 %q — %d result(s), best match first", query, len(list))
	if len(list) > cap {
		label = fmt.Sprintf("🔎 %q — top %d of %d, best match first", query, cap, len(list))
		list = list[:cap]
	}
	folder := treeFolder{Cwd: label, Slug: "search", Expanded: true}
	for _, h := range list {
		folder.Sessions = append(folder.Sessions, treeSession{
			ID:      h.id,
			Path:    h.path,
			Snippet: collapsePreview(firstNonEmpty(h.snippet, h.displayName)),
			Branch:  h.repo, // reuse the branch column to show the repo
			Mtime:   h.mtime,
			Live:    false,
		})
		if h.mtime > folder.Mtime {
			folder.Mtime = h.mtime
		}
	}
	tree := sessionTree{Now: now, Home: home, Pwd: pwd}
	if len(list) > 0 {
		tree.Folders = []treeFolder{folder}
	}
	return tree
}

func statMtime(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().Unix()
	}
	return 0
}

// localSearchClaude returns each local session jsonl containing query (literal,
// case-insensitive) mapped to its match count, via ripgrep when available, else a
// bounded Go scan.
func localSearchClaude(home, query string) map[string]int {
	root := claudeProjectsDir(home)
	out := map[string]int{}
	if _, err := exec.LookPath("rg"); err == nil {
		// rg -c: "path:count" per matching file; exit 1 (no matches) → empty output.
		b, _ := exec.Command("rg", "-c", "-i", "-F", "-g", "*.jsonl", "--", query, root).Output()
		for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			if i := strings.LastIndex(line, ":"); i >= 0 {
				if n, err := strconv.Atoi(line[i+1:]); err == nil {
					out[line[:i]] = n
				}
			}
		}
		return out
	}
	// Fallback: scan each jsonl for the (lowercased) query.
	q := strings.ToLower(query)
	matches, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	for _, p := range matches {
		if n := countInFile(p, q); n > 0 {
			out[p] = n
		}
	}
	return out
}

func countInFile(path, lowerQuery string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	n := 0
	for sc.Scan() {
		if strings.Contains(strings.ToLower(sc.Text()), lowerQuery) {
			n++
		}
	}
	return n
}

// localMatchSnippet returns a readable window around the first match in the
// transcript, plus the session's repo (from its cwd's git remote) and branch.
func localMatchSnippet(path, query string) (snippet, repo, branch string) {
	_, branch, _, cwd := loadClaudeMeta(path)
	repo = parseGitRemote(gitOriginURL(cwd))
	if repo == "" {
		repo = filepath.Base(cwd)
	}
	q := strings.ToLower(query)
	f, err := os.Open(path)
	if err != nil {
		return "", repo, branch
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if idx := strings.Index(strings.ToLower(line), q); idx >= 0 {
			return collapsePreview(cleanMatch(window(line, idx, len(query)))), repo, branch
		}
	}
	return "", repo, branch
}

// window extracts ~30 chars before and ~50 after a match offset, marking a
// mid-line cut with an ellipsis.
func window(line string, idx, qlen int) string {
	start := max(idx-30, 0)
	end := min(idx+qlen+50, len(line))
	s := line[start:end]
	if start > 0 {
		s = "…" + s
	}
	return s
}

// cleanMatch turns a raw JSONL match fragment into readable text.
func cleanMatch(s string) string {
	return strings.NewReplacer(`\n`, " ", `\t`, " ", `\"`, `"`, `\\`, `\`).Replace(s)
}

func localPathForID(home, id string) string {
	m, _ := filepath.Glob(filepath.Join(claudeProjectsDir(home), "*", id+".jsonl"))
	if len(m) > 0 {
		return m[0]
	}
	return ""
}

// ── entire search ────────────────────────────────────────────────────────────

type entireSearchHit struct {
	SessionID   string
	DisplayName string
	Repo        string
	Snippet     string
	CreatedAt   string
	Score       float64
}

// entireSearchSessions runs `entire checkpoint search` and returns its
// session-type results (which carry a sessionId), best-first. Non-fatal on
// error/offline — returns nil so search degrades to local-only.
func entireSearchSessions(query string) []entireSearchHit {
	if _, err := exec.LookPath("entire"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fmt.Fprintln(os.Stderr, "entire-tail: searching entire…")
	out, err := exec.CommandContext(ctx, "entire", "checkpoint", "search", query,
		"--all-repos", "--json", "--limit", "40").Output()
	if err != nil {
		return nil
	}
	var body struct {
		Results []struct {
			Type string `json:"type"`
			Data struct {
				SessionID   string `json:"sessionId"`
				DisplayName string `json:"displayName"`
				Repo        string `json:"repo"`
				CreatedAt   string `json:"createdAt"`
			} `json:"data"`
			SearchMeta struct {
				Score   float64 `json:"score"`
				Snippet string  `json:"snippet"`
			} `json:"searchMeta"`
		} `json:"results"`
	}
	if json.Unmarshal(out, &body) != nil {
		return nil
	}
	var hits []entireSearchHit
	for _, r := range body.Results {
		if r.Type != "session" || r.Data.SessionID == "" {
			continue
		}
		hits = append(hits, entireSearchHit{
			SessionID:   r.Data.SessionID,
			DisplayName: r.Data.DisplayName,
			Repo:        r.Data.Repo,
			Snippet:     r.SearchMeta.Snippet,
			CreatedAt:   r.Data.CreatedAt,
			Score:       r.SearchMeta.Score,
		})
	}
	return hits
}
