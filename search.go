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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// search.go implements content search ("which session did we say 'fire socks'
// in?") across both sources: a local scan of ~/.claude transcripts and entire's
// hybrid (semantic + keyword) checkpoint search. Hits are merged by session id
// and ranked — an exact local phrase match weighs heaviest, entire's semantic
// score adds, recency breaks ties — then presented as the usual tree.
//
// The local scan matches only USER + ASSISTANT text with <system-reminder>
// blocks stripped, so injected boilerplate (skill lists, tool schemas, hook
// output that's identical across every session) doesn't produce false matches.
// ripgrep narrows the candidate files first (fast); each candidate is then
// parsed to confirm a real conversational match.

type searchHit struct {
	id          string
	path        string // local jsonl, "" if not on this machine
	snippet     string // match context (why it hit)
	repo        string
	cwd         string
	displayName string
	mtime       int64
	localCount  int     // conversational matches in the local transcript (any)
	wordCount   int     // matches that stand alone as a whole word (not a substring)
	entireScore float64 // entire's relevance score
	entireHit   bool
}

// score ranks a hit. A standalone-word local match is strongest (searching
// "ectl" should surface " ectl ", not "kubectl"/"directly"); a substring-only
// local match ranks well below that; entire's semantic score adds on top, so
// matching both sources still wins.
func (h *searchHit) score() float64 {
	s := 0.0
	if h.entireHit {
		s += h.entireScore // typically ~5–8
	}
	switch {
	case h.wordCount > 0:
		s += 20 + math.Min(float64(h.wordCount), 5) // whole-word hit → strongest
	case h.localCount > 0:
		s += 8 + math.Min(float64(h.localCount), 5) // substring-only → weaker
	}
	return s
}

const localSearchCap = 400 // candidates parsed for a match; newest first

// buildSearchTree runs both searches, merges + ranks, and returns a single-group
// tree ordered by relevance (not recency). localOnly skips the entire query.
func buildSearchTree(home, pwd, query string, localOnly bool, now int64) sessionTree {
	hits := localSearchClaude(home, query)

	repoCache := map[string]string{}
	for _, h := range hits {
		h.repo = repoForCwd(h.cwd, home, repoCache)
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

	const cap = 50 // keep the ranked view useful
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

// localSearchClaude returns a hit per local session whose USER/ASSISTANT text
// contains query. ripgrep narrows candidate files fast; each (newest first,
// capped) is parsed concurrently so injected boilerplate doesn't false-match.
func localSearchClaude(home, query string) map[string]*searchHit {
	cands := localCandidates(home, query)
	sort.Slice(cands, func(i, j int) bool { return statMtime(cands[i]) > statMtime(cands[j]) })
	if len(cands) > localSearchCap {
		cands = cands[:localSearchCap]
	}

	out := map[string]*searchHit{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, p := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			count, words, snippet, cwd := conversationHit(path, query)
			if count == 0 {
				return
			}
			id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
			mu.Lock()
			out[id] = &searchHit{id: id, path: path, localCount: count, wordCount: words, snippet: snippet, cwd: cwd, mtime: statMtime(path)}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return out
}

// localCandidates lists session files that contain query anywhere (fast, via
// ripgrep -l), or — without rg — every session file (conversationHit filters).
func localCandidates(home, query string) []string {
	root := claudeProjectsDir(home)
	if _, err := exec.LookPath("rg"); err == nil {
		b, _ := exec.Command("rg", "-l", "-i", "-F", "-g", "*.jsonl", "--", query, root).Output()
		var out []string
		for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
	m, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	return m
}

var sysReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// conversationHit scans one transcript, counting matches of query within the
// USER/ASSISTANT text only (system-reminders stripped). It reports the total
// match count, how many of those stand alone as a whole word, a snippet
// (preferring a whole-word match so it shows why it hit), and the session's cwd.
// Returns count 0 when the query appears only in injected/system content.
func conversationHit(path, query string) (count, words int, snippet, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	q := strings.ToLower(query)
	n := len(query)
	var wordSnip, subSnip string
	for sc.Scan() {
		var ev claudeMetaEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if cwd == "" && ev.Cwd != "" {
			cwd = ev.Cwd
		}
		text := eventConversationText(ev)
		if text == "" {
			continue
		}
		lt := strings.ToLower(text)
		for from := 0; ; {
			rel := strings.Index(lt[from:], q)
			if rel < 0 {
				break
			}
			idx := from + rel
			count++
			if standaloneAt(text, idx, n) {
				words++
				if wordSnip == "" {
					wordSnip = collapsePreview(cleanMatch(window(text, idx, n)))
				}
			} else if subSnip == "" {
				subSnip = collapsePreview(cleanMatch(window(text, idx, n)))
			}
			from = idx + n
		}
	}
	return count, words, firstNonEmpty(wordSnip, subSnip), cwd
}

// standaloneAt reports whether the match at s[idx:idx+n] is bounded by non-word
// characters on both sides (so "ectl" matches " ectl " but not "kubectl").
func standaloneAt(s string, idx, n int) bool {
	before := byte(' ')
	if idx > 0 {
		before = s[idx-1]
	}
	after := byte(' ')
	if idx+n < len(s) {
		after = s[idx+n]
	}
	return !isWordByte(before) && !isWordByte(after)
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// eventConversationText returns the human conversation text of an event — the
// user's typed message or the assistant's text blocks — with <system-reminder>
// blocks removed. Other event types (tool results, attachments, meta) yield "".
func eventConversationText(ev claudeMetaEvent) string {
	if ev.Message == nil {
		return ""
	}
	switch ev.Type {
	case "user":
		var s string
		if json.Unmarshal(ev.Message.Content, &s) == nil {
			return stripSystemReminders(s)
		}
		return stripSystemReminders(joinClaudeText(ev.Message.Content))
	case "assistant":
		return stripSystemReminders(joinClaudeText(ev.Message.Content))
	}
	return ""
}

func stripSystemReminders(s string) string {
	if !strings.Contains(s, "<system-reminder>") {
		return s
	}
	return sysReminderRe.ReplaceAllString(s, " ")
}

// window extracts ~30 chars before and ~50 after a match offset, marking a
// mid-string cut with an ellipsis.
func window(s string, idx, qlen int) string {
	start := max(idx-30, 0)
	end := min(idx+qlen+50, len(s))
	w := s[start:end]
	if start > 0 {
		w = "…" + w
	}
	return w
}

// cleanMatch turns a raw fragment into readable text.
func cleanMatch(s string) string {
	return strings.NewReplacer(`\n`, " ", `\t`, " ", `\"`, `"`, `\\`, `\`).Replace(s)
}

func statMtime(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().Unix()
	}
	return 0
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
