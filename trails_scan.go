package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type trailsCursor struct {
	Path         string          `json:"path,omitempty"`
	Inode        uint64          `json:"inode,omitempty"`
	Offset       int64           `json:"offset,omitempty"`
	TailHash     string          `json:"tailHash,omitempty"`
	LastActivity int64           `json:"lastActivity,omitempty"`
	Fallback     int64           `json:"fallback,omitempty"`
	ExportAt     int64           `json:"exportAt,omitempty"`
	Seen         map[string]bool `json:"seen,omitempty"`
}
type trailsBranchCache struct {
	URL     string `json:"url"`
	RetryAt int64  `json:"retryAt"`
	Error   string `json:"error,omitempty"`
}
type trailsObservation struct {
	URL, Source, Evidence string
	At                    int64
}
type trailsScanDeps struct {
	Now       func() time.Time
	Inventory func(context.Context) ([]wtfSession, error)
	Observe   func(context.Context, wtfSession, trailsCursor) ([]trailsObservation, trailsCursor, error)
	Lookup    func(context.Context, string, string, bool) (trailsEntry, error)
}

var errTrailsDeferred = errors.New("metadata queued")

func scanTrails(ctx context.Context, prior trailsCatalog, d trailsScanDeps) (trailsCatalog, error) {
	c := prior
	c.Trails, c.Sessions = maps.Clone(prior.Trails), maps.Clone(prior.Sessions)
	c.Cursors, c.Branches = maps.Clone(prior.Cursors), maps.Clone(prior.Branches)
	if c.Cursors == nil {
		c.Cursors = map[string]trailsCursor{}
	}
	if c.Branches == nil {
		c.Branches = map[string]trailsBranchCache{}
	}
	c.Errors = nil
	for key, tr := range c.Trails {
		tr.Associations = maps.Clone(tr.Associations)
		c.Trails[key] = tr
	}
	now := d.Now().Unix()
	sessions, err := d.Inventory(ctx)
	if err != nil {
		c.Errors = append(c.Errors, "inventory: "+err.Error())
	} else {
		for id, s := range c.Sessions {
			s.Active = false
			c.Sessions[id] = s
		}
	}
	for _, s := range sessions {
		if err := ctx.Err(); err != nil {
			return prior, err
		}
		id := wtfSessionKey(s.Agent, s.ID)
		cursor := c.Cursors[id]
		cursor.Seen = maps.Clone(cursor.Seen)
		observations, next, readErr := d.Observe(ctx, s, cursor)
		if readErr != nil {
			c.Errors = append(c.Errors, id+": "+readErr.Error())
		}
		c.Cursors[id] = next
		if next.LastActivity > 0 {
			s.LastActivity = next.LastActivity
		}
		c.Sessions[id] = s
		for _, o := range observations {
			canonical, repo, _, ok := parseTrailsURL(o.URL)
			if !ok {
				continue
			}
			tr := c.Trails[canonical]
			tr.URL, tr.Repo = canonical, repo
			if tr.Associations == nil {
				tr.Associations = map[string]trailsAssociation{}
			}
			a := tr.Associations[id]
			if a.FirstAt == 0 || o.At < a.FirstAt {
				a.FirstAt = o.At
			}
			a.LastAt = max(a.LastAt, o.At)
			a.Source, a.Evidence = o.Source, o.Evidence
			tr.Associations[id] = a
			c.Trails[canonical] = tr
		}
		if s.Repo != "" && s.Branch != "" && s.Branch != "main" && s.Branch != "master" && s.Branch != "HEAD" {
			key := s.Repo + "\n" + s.Branch
			cached := c.Branches[key]
			if cached.RetryAt <= now {
				tr, e := d.Lookup(ctx, s.Repo, s.Branch, true)
				if errors.Is(e, errTrailsDeferred) {
					continue
				}
				cached = trailsBranchCache{RetryAt: now + 60}
				if e != nil {
					cached.Error = e.Error()
				} else {
					cached.URL = tr.URL
					old := c.Trails[tr.URL]
					tr.Associations = old.Associations
					tr.MetadataAt, tr.RetryAt = now, now+600
					c.Trails[tr.URL] = tr
					cached.RetryAt = now + 600
				}
				c.Branches[key] = cached
			}
			if cached.Error != "" {
				c.Errors = append(c.Errors, s.Repo+" "+s.Branch+": "+cached.Error)
			}
		}
	}
	for _, key := range sortedMapKeys(c.Trails) {
		tr := c.Trails[key]
		if tr.RetryAt <= now {
			_, repo, number, ok := parseTrailsURL(key)
			if !ok {
				continue
			}
			fresh, e := d.Lookup(ctx, repo, number, false)
			if errors.Is(e, errTrailsDeferred) {
				// Keep the entry due for the next scan.
			} else if e != nil {
				tr.Attempts++
				tr.MetadataError = e.Error()
				tr.RetryAt = now + min(int64(3600), int64(60)<<min(tr.Attempts-1, 6))
			} else {
				fresh.Associations = tr.Associations
				fresh.MetadataAt, fresh.RetryAt = now, now+600
				tr = fresh
			}
		}
		if tr.Associations == nil {
			tr.Associations = map[string]trailsAssociation{}
		}
		for _, s := range sessions {
			id := wtfSessionKey(s.Agent, s.ID)
			s = c.Sessions[id]
			if tr.Branch == "" || s.Repo != tr.Repo || s.Branch != tr.Branch {
				continue
			}
			a := tr.Associations[id]
			if a.FirstAt == 0 {
				a.FirstAt = s.LastActivity
			}
			a.Branch, a.BranchMatched = s.Branch, true
			a.LastAt = max(a.LastAt, s.LastActivity)
			if a.Source == "" {
				a.Source, a.Evidence = "branch", s.Branch
			}
			tr.Associations[id] = a
		}
		c.Trails[key] = tr
	}
	c.UpdatedAt = now
	return c, nil
}

func parseTrailsURL(raw string) (canonical, repo, number string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "entire.io") || u.User != nil {
		return
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 5 || (parts[0] != "gh" && parts[0] != "et") || parts[3] != "trails" {
		return
	}
	n, err := strconv.Atoi(parts[4])
	if err != nil || n <= 0 {
		return
	}
	repo = strings.ToLower(strings.Join(parts[:3], "/"))
	canonical = "https://entire.io/" + repo + "/trails/" + strconv.Itoa(n)
	if len(trailMatches(canonical)) != 1 {
		return "", "", "", false
	}
	return canonical, repo, strconv.Itoa(n), true
}

func observeTrailsFile(s wtfSession, path string, c trailsCursor) ([]trailsObservation, trailsCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, c, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, c, err
	}
	inode := st.Sys().(*syscall.Stat_t).Ino
	if c.Path != path || c.Inode != inode || st.Size() < c.Offset || (c.TailHash != "" && c.TailHash != trailsFileHash(f, c.Offset)) {
		c.Path, c.Inode, c.Offset = path, inode, 0
	}
	if c.Fallback == 0 {
		c.Fallback = max(s.LastActivity, st.ModTime().Unix())
	}
	if c.Seen == nil {
		c.Seen = map[string]bool{}
	}
	if _, err = f.Seek(c.Offset, io.SeekStart); err != nil {
		return nil, c, err
	}
	r := bufio.NewReader(f)
	var out []trailsObservation
	for {
		line, e := r.ReadBytes('\n')
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, c, e
		}
		c.Offset += int64(len(line))
		out = append(out, trailsLine(s.Agent, line, &c)...)
	}
	c.TailHash = trailsFileHash(f, c.Offset)
	return out, c, nil
}

func trailsFileHash(f *os.File, offset int64) string {
	if offset == 0 {
		return ""
	}
	b := make([]byte, min(offset, 256))
	n, err := f.ReadAt(b, offset-int64(len(b)))
	if err != nil || n != len(b) {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func trailsLine(agent Agent, line []byte, c *trailsCursor) []trailsObservation {
	var texts []trailTextEvent
	if agent == AgentAmp {
		id := ampCompleteMessageID(line)
		if id == "" || c.Seen[id] {
			return nil
		}
		var env ampEnvelope
		if json.Unmarshal(line, &env) != nil {
			return nil
		}
		c.Seen[id] = true
		texts = ampTrailEvents(ampExport{Messages: []ampMessage{env.Message}}, c.Fallback)
	} else {
		var ev claudeEvent
		if json.Unmarshal(line, &ev) != nil || ev.Message == nil || ev.IsSidechain || (ev.Type != "user" && ev.Type != "assistant") {
			return nil
		}
		if ev.Type == "user" && isSyntheticUser(ev.Origin.Kind, ev.PromptSource, ev.IsMeta) {
			return nil
		}
		at := parsedTrailTime(ev.Timestamp, c.Fallback)
		c.LastActivity = max(c.LastActivity, at)
		var plain string
		if json.Unmarshal(ev.Message.Content, &plain) == nil {
			if !isTaskNote(ev.Origin.Kind, ev.PromptSource, plain) {
				texts = append(texts, trailTextEvent{At: at, Source: ev.Type, Text: plain})
			}
		} else {
			var blocks []claudeBlock
			_ = json.Unmarshal(ev.Message.Content, &blocks)
			for _, b := range blocks {
				if b.Type == "text" && !isTaskNote(ev.Origin.Kind, ev.PromptSource, b.Text) {
					texts = append(texts, trailTextEvent{At: at, Source: ev.Type, Text: b.Text})
				}
			}
		}
	}
	var out []trailsObservation
	for _, text := range texts {
		c.LastActivity = max(c.LastActivity, text.At)
		if !trailClaimSource(text.Source) {
			continue
		}
		for _, m := range trailMatches(text.Text) {
			canonical, _, _, ok := parseTrailsURL(m.matched)
			if ok {
				out = append(out, trailsObservation{URL: canonical, Source: text.Source, Evidence: m.matched, At: text.At})
			}
		}
	}
	return out
}

func trailsLookup(ctx context.Context, repo, selector string, byBranch bool) (trailsEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	args := []string{"trail", "show"}
	if byBranch {
		args = append(args, "--branch", selector)
	} else {
		args = append(args, selector)
	}
	args = append(args, "--repo", repo, "--json")
	b, err := exec.CommandContext(ctx, "entire", args...).Output()
	if err != nil {
		return trailsEntry{}, fmt.Errorf("trail lookup unavailable: %w", err)
	}
	var m entireTrailJSON
	if err = json.Unmarshal(b, &m); err != nil {
		return trailsEntry{}, err
	}
	u, r, n, ok := parseTrailsURL(m.URL)
	branch := firstNonEmpty(m.Branch, m.OriginalBranch)
	if !ok || r != repo || (byBranch && branch != selector) || (!byBranch && n != selector) {
		return trailsEntry{}, fmt.Errorf("trail lookup returned a different identity")
	}
	return trailsEntry{URL: u, Repo: r, Title: m.Title, Status: m.Status, Branch: branch}, nil
}

func newTrailsScanDeps(home string) (trailsScanDeps, func()) {
	watcher := startAmpTop()
	var today []handoverItem
	var inventoryAt time.Time
	var inventoryErr error
	lookups := 0
	type repoFact struct {
		repo, defaultBranch string
		at                  time.Time
	}
	repos := map[string]repoFact{}
	d := trailsScanDeps{Now: time.Now, Lookup: func(ctx context.Context, repo, selector string, branch bool) (trailsEntry, error) {
		if lookups >= 4 {
			return trailsEntry{}, errTrailsDeferred
		}
		lookups++
		return trailsLookup(ctx, repo, selector, branch)
	}}
	d.Inventory = func(ctx context.Context) ([]wtfSession, error) {
		lookups = 0
		now := time.Now()
		if inventoryAt.IsZero() || now.Sub(inventoryAt) >= 30*time.Second {
			tree := buildClaudeTree(home, "", 2, now.Unix(), nil)
			amp, ampErr := buildAmpTree(home, "", 2, now.Unix(), false)
			inventoryErr = ampErr
			today = flattenToday(mergeAgentTrees(tree, amp), localMidnight(now.Unix(), now.Location()), home)
			inventoryAt = now
		}
		active, available := watcher.snapshot()
		live, _ := buildLiveSessionsWithAmp(home, active, available)
		sessions := collectWTFSessions(home, now.Unix(), now.Location(), wtfInventoryDeps{
			Today: func(string, int64, *time.Location) []handoverItem { return today },
			Live:  func(string) []liveSession { return live },
		})
		for i := range sessions {
			s := &sessions[i]
			fact := repos[s.Cwd]
			if fact.at.IsZero() || now.Sub(fact.at) >= time.Minute {
				fact = repoFact{at: now}
				if s.Cwd != "" && isDir(s.Cwd) {
					gitctx, cancel := context.WithTimeout(ctx, 2*time.Second)
					b, _ := exec.CommandContext(gitctx, "git", "-C", s.Cwd, "remote", "get-url", "origin").Output()
					fact.repo = trailsRepoRemote(strings.TrimSpace(string(b)))
					b, _ = exec.CommandContext(gitctx, "git", "-C", s.Cwd, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
					fact.defaultBranch = strings.TrimPrefix(strings.TrimSpace(string(b)), "origin/")
					cancel()
				}
				repos[s.Cwd] = fact
			}
			s.Repo = fact.repo
			if s.Branch == fact.defaultBranch {
				s.Branch = ""
			}
		}
		return sessions, errors.Join(inventoryErr, ctx.Err())
	}
	d.Observe = func(ctx context.Context, s wtfSession, c trailsCursor) ([]trailsObservation, trailsCursor, error) {
		if s.Agent != AgentAmp {
			return observeTrailsFile(s, s.Transcript, c)
		}
		if c.Seen == nil {
			c.Seen = map[string]bool{}
		}
		if c.Fallback == 0 {
			c.Fallback = s.LastActivity
			if c.Fallback == 0 {
				c.Fallback = time.Now().Unix()
			}
		}
		feed := ampLivePath(home, s.ID)
		hasFeed := isFile(feed)
		var out []trailsObservation
		var exportErr error
		if c.ExportAt == 0 || (!hasFeed && (s.Active || s.LastActivity > c.LastActivity) && time.Now().Unix()-c.ExportAt >= 10) {
			ex, e := ampExportThreadWithin(home, s.ID, hasFeed, 5*time.Second)
			exportErr = e
			if ex.ID != "" {
				for _, line := range strings.Split(string(ampExportLines(ex)), "\n") {
					out = append(out, trailsLine(AgentAmp, []byte(line), &c)...)
				}
			}
			c.ExportAt = time.Now().Unix()
		}
		if ctx.Err() != nil {
			return out, c, ctx.Err()
		}
		if hasFeed {
			more, next, e := observeTrailsFile(s, feed, c)
			return append(out, more...), next, errors.Join(exportErr, e)
		}
		return out, c, exportErr
	}
	return d, watcher.close
}

func trailsRepoRemote(remote string) string {
	remote = strings.TrimSuffix(remote, ".git")
	if strings.HasPrefix(remote, "git@github.com:") {
		return "gh/" + strings.ToLower(strings.TrimPrefix(remote, "git@github.com:"))
	}
	u, err := url.Parse(remote)
	if err != nil {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	if u.Hostname() == "github.com" && len(strings.Split(path, "/")) == 2 {
		return "gh/" + strings.ToLower(path)
	}
	if strings.HasSuffix(u.Hostname(), ".entire.io") || u.Hostname() == "entire.io" {
		parts := strings.Split(path, "/")
		if len(parts) == 3 && (parts[0] == "et" || parts[0] == "gh") {
			return strings.ToLower(path)
		}
	}
	return ""
}
