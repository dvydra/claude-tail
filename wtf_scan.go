package main

import (
	"bufio"
	"context"
	"encoding/json"
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

func inspectRepoWorktrees(ctx context.Context, repo, cwd string, now int64, run wtfCommandRunner) []wtfWorktree {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := run(ctx, cwd, "git", "-C", cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	entries := parseGitWorktreePorcelain(data)
	worktrees := make([]wtfWorktree, 0, len(entries))
	for _, entry := range entries {
		worktrees = append(worktrees, inspectWorktree(ctx, repo, entry, now, run))
	}
	return worktrees
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
		if w.GitError == "" {
			w.GitError = "remote default unknown"
		}
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
	}
	if diff, err := run(ctx, entry.Path, "git", "-C", entry.Path, "diff", "--no-ext-diff", "--unified=0", "HEAD", "--"); err == nil && len(diff) > 0 {
		if len(diff) > wtfDiffLimit {
			diff = diff[:wtfDiffLimit]
		}
		w.GitEvidence = append(w.GitEvidence, wtfGitEvidence{Source: "diff", Text: string(diff)})
	}
	return w
}

func worktreeHasWIP(w wtfWorktree) bool {
	return !w.Exists || w.GitError != "" || w.UnmergedCommits < 0 || w.DirtyFiles > 0 || w.UnmergedCommits > 0
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

type trailPattern struct {
	kind string
	re   *regexp.Regexp
}

var trailPatterns = []trailPattern{
	{"url", regexp.MustCompile(`(?i)https://entire\.io/gh/([a-z0-9_.-]+)/([a-z0-9_.-]+)/trails/([0-9]+)`)},
	{"qualified", regexp.MustCompile(`(?i)([a-z0-9_.-]+)/([a-z0-9_.-]+)#([0-9]+)`)},
	{"repo", regexp.MustCompile(`(?i)([a-z0-9_.-]+)#([0-9]+)`)},
	{"bare", regexp.MustCompile(`(?i)trail[ ]+#?([0-9]+)`)},
}

type trailMatch struct {
	start, end int
	kind       string
	matched    string
	parts      []string
}

func extractTrailEvidence(events []trailTextEvent, ctx trailContext) []trailEvidence {
	byIdentity := make(map[string]trailEvidence)
	for _, event := range events {
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
		var ok bool
		owner, repo, ok = splitRepo(ctx.CurrentRepo)
		if ok {
			e.Resolution = "current repo"
		} else {
			e.Resolution = "bare trail without current repo"
		}
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

func splitRepo(value string) (string, string, bool) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func claudeTrailEvents(path string, observedAt int64) []trailTextEvent {
	f, err := os.Open(path)
	if err != nil {
		return nil
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
	return events
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

func transcriptTrailEvents(session wtfSession, home string, observedAt int64) []trailTextEvent {
	switch session.Agent {
	case AgentClaude:
		return claudeTrailEvents(session.Transcript, observedAt)
	case AgentAmp:
		path := session.Transcript
		if path == "" {
			path = filepath.Join(ampCacheDir(home), "exports", session.ID+".json")
		}
		export, err := readAmpExport(filepath.Clean(path))
		if err != nil {
			return nil
		}
		return ampTrailEvents(export, observedAt)
	default:
		return nil
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
