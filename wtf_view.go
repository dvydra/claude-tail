package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const wtfRefresh = time.Second

type wtfUI struct {
	Snapshot wtfSnapshot
	Cursor   int
	Top      int
	Width    int
	Height   int
	Refresh  bool
	Quit     bool
	Chosen   *wtfSession
}

type wtfRenderOpts struct {
	width        int
	theme        Theme
	selected     string
	clear        bool
	top          int
	height       int
	snapshotHome string
}

type wtfComposedRow struct {
	text       string
	sessionKey string
}

type wtfKeyEvent struct {
	key           treeKey
	r             rune
	width, height int
	err           error
}

func orderedWTFSessions(snapshot wtfSnapshot) []wtfSession {
	sessions := append([]wtfSession(nil), snapshot.Sessions...)
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].Active != sessions[j].Active {
			return sessions[i].Active
		}
		if sessions[i].Active && (sessions[i].State == "busy") != (sessions[j].State == "busy") {
			return sessions[i].State == "busy"
		}
		if sessions[i].LastActivity != sessions[j].LastActivity {
			return sessions[i].LastActivity > sessions[j].LastActivity
		}
		return wtfSessionKey(sessions[i].Agent, sessions[i].ID) < wtfSessionKey(sessions[j].Agent, sessions[j].ID)
	})

	// Keep each active repo contiguous. The first session encountered chooses the
	// repo's position, so repos doing busy work still lead; the sort above keeps
	// busy before idle within each repo.
	groups := map[string][]wtfSession{}
	var repos []string
	var ended []wtfSession
	for _, session := range sessions {
		if !session.Active {
			ended = append(ended, session)
			continue
		}
		repo := firstNonEmpty(session.Repo, session.Cwd, "Other")
		if _, ok := groups[repo]; !ok {
			repos = append(repos, repo)
		}
		groups[repo] = append(groups[repo], session)
	}
	ordered := make([]wtfSession, 0, len(sessions))
	for _, repo := range repos {
		ordered = append(ordered, groups[repo]...)
	}
	return append(ordered, ended...)
}

func renderWTFSnapshot(snapshot wtfSnapshot, width int, color bool) string {
	theme := Theme{}
	if color {
		theme = Theme{
			UserANSI:   "\x1b[1;38;2;187;154;247m",
			ClaudeANSI: "\x1b[1;38;2;122;162;247m",
			DimANSI:    "\x1b[2;38;2;86;95;137m",
		}
	}
	return composeWTF(snapshot, wtfRenderOpts{width: width, theme: theme})
}

func renderWTF(ui wtfUI, theme Theme) string {
	sessions := orderedWTFSessions(ui.Snapshot)
	selected := ""
	if len(sessions) > 0 {
		cursor := max(0, min(ui.Cursor, len(sessions)-1))
		selected = wtfSessionKey(sessions[cursor].Agent, sessions[cursor].ID)
	}
	return composeWTF(ui.Snapshot, wtfRenderOpts{width: ui.Width, theme: theme, selected: selected, clear: true, top: ui.Top, height: ui.Height})
}

func composeWTF(snapshot wtfSnapshot, opts wtfRenderOpts) string {
	if opts.width <= 0 {
		if opts.clear {
			return "\x1b[H\x1b[2J"
		}
		return ""
	}
	opts.snapshotHome = snapshot.Home
	width := opts.width
	reset := ""
	if opts.theme.DimANSI != "" || opts.theme.ClaudeANSI != "" || opts.theme.UserANSI != "" {
		reset = "\x1b[0m"
	}
	clip := func(line string) string {
		clipped := truncVisible(line, width)
		if reset == "" {
			return stripANSI(clipped)
		}
		return clipped + reset
	}
	var header []wtfComposedRow
	var rows []wtfComposedRow
	var footer []wtfComposedRow
	line := func(text string) { rows = append(rows, wtfComposedRow{text: clip(text)}) }
	sessionLine := func(text, key string) {
		rows = append(rows, wtfComposedRow{text: clip(text), sessionKey: key})
	}

	sessions := orderedWTFSessions(snapshot)
	active, ended := 0, 0
	for _, session := range sessions {
		if session.Active {
			active++
		} else {
			ended++
		}
	}

	findings := activeWTFFindings(snapshot.Findings)
	wipTrails := selectWIPTrails(snapshot)
	stamp := ""
	if snapshot.GeneratedAt > 0 {
		stamp = "       " + time.Unix(snapshot.GeneratedAt, 0).Format("15:04:05")
	}
	header = append(header, wtfComposedRow{text: clip(fmt.Sprintf("%sWTF%s  %d active · %d ended today · %d WIP trails · %d findings%s", opts.theme.ClaudeANSI, reset, active, ended, len(wipTrails), len(findings), stamp))})

	line("")
	line(opts.theme.ClaudeANSI + "Badness" + reset)
	trailCanonical := make(map[string]string, len(snapshot.Trails))
	for _, trail := range snapshot.Trails {
		trailCanonical[trail.Key] = trail.CanonicalWorktree
	}
	sessionCwds := make(map[string]string, len(snapshot.Sessions))
	sessionTrails := make(map[string][]string)
	for _, session := range snapshot.Sessions {
		sessionCwds[wtfSessionKey(session.Agent, session.ID)] = session.Cwd
	}
	for _, trail := range snapshot.Trails {
		for _, association := range trail.Associations {
			if association.SessionKey != "" {
				sessionTrails[association.SessionKey] = append(sessionTrails[association.SessionKey], trail.Key)
			}
		}
	}
	for key := range sessionTrails {
		sessionTrails[key] = sortedUnique(sessionTrails[key])
	}
	if len(findings) == 0 {
		line(opts.theme.DimANSI + "  None" + reset)
	}
	for _, finding := range findings {
		canonical := trailCanonical[finding.TrailKey]
		if canonical == "" && len(finding.Worktrees) > 0 {
			canonical = finding.Worktrees[0]
		}
		var actual []string
		for _, path := range finding.Worktrees {
			if path != canonical {
				actual = append(actual, path)
			}
		}
		if len(actual) == 0 {
			actual = append(actual, finding.Worktrees...)
		}
		if len(actual) == 0 && finding.Challenger != "" && sessionCwds[finding.Challenger] != "" {
			actual = append(actual, sessionCwds[finding.Challenger])
		}
		line(fmt.Sprintf("  S%d %s  %s", finding.Severity, finding.Kind, finding.TrailKey))
		line(fmt.Sprintf("    owner %s · challenger %s · delivery %s", firstNonEmpty(finding.Owner, "unknown"), firstNonEmpty(finding.Challenger, "none"), wtfDeliveryLabel(finding)))
		line(fmt.Sprintf("    canonical %s · actual %s", firstNonEmpty(canonical, "unknown"), firstNonEmpty(strings.Join(actual, ", "), "none")))
		if finding.Explanation != "" {
			line("    " + finding.Explanation)
		}
		for _, evidence := range finding.Evidence {
			line("    evidence " + evidence)
		}
	}

	line("")
	line(opts.theme.ClaudeANSI + "Now" + reset)
	if len(sessions) == 0 {
		line("  No sessions active or seen today.")
	} else {
		renderSection := func(wantActive bool) {
			lastRepo := "\x00"
			for i, session := range sessions {
				if session.Active != wantActive {
					continue
				}
				if i < opts.top {
					continue
				}
				repo := firstNonEmpty(session.Repo, tildify(session.Cwd, snapshot.Home), "Other")
				if repo != lastRepo {
					line(opts.theme.DimANSI + "  " + repo + reset)
					lastRepo = repo
				}
				key := wtfSessionKey(session.Agent, session.ID)
				for rowIndex, row := range wtfSessionLines(session, sessionTrails[key], opts, reset) {
					if rowIndex == 0 {
						sessionLine(row, key)
					} else {
						line(row)
					}
				}
			}
		}
		if active > 0 {
			renderSection(true)
		} else {
			line("  No active sessions.")
		}
	}
	line("")
	line(opts.theme.ClaudeANSI + "WIP trails" + reset)
	if len(wipTrails) == 0 {
		line(opts.theme.DimANSI + "  None" + reset)
	}
	for _, trail := range wipTrails {
		line(fmt.Sprintf("  %s  canonical %s", trail.trail.Key, firstNonEmpty(trail.trail.CanonicalWorktree, "unknown")))
		line(fmt.Sprintf("    owner %s · active %d · dirty %s · unmerged %s · %s", firstNonEmpty(trail.trail.OwnerSession, "unknown"), trail.active, wtfCountLabel(trail.dirty, trail.dirtyKnown), wtfCountLabel(trail.unmerged, trail.unmergedKnown), strings.Join(trail.reasons, ", ")))
		for _, worktree := range trail.worktrees {
			line(fmt.Sprintf("    %s · dirty %s · unmerged %s · %s", worktree.path, wtfCountLabel(worktree.dirty, worktree.dirtyKnown), wtfCountLabel(worktree.unmerged, worktree.unmergedKnown), strings.Join(worktree.evidence, ", ")))
		}
	}
	line("")
	line(opts.theme.ClaudeANSI + "Recently stopped" + reset)
	if ended > 0 {
		// renderSection supplies its own heading, so render ended rows directly here.
		lastRepo := "\x00"
		for i, session := range sessions {
			if session.Active || i < opts.top {
				continue
			}
			repo := firstNonEmpty(session.Repo, tildify(session.Cwd, snapshot.Home), "Other")
			if repo != lastRepo {
				line(opts.theme.DimANSI + "  " + repo + reset)
				lastRepo = repo
			}
			key := wtfSessionKey(session.Agent, session.ID)
			for rowIndex, row := range wtfSessionLines(session, sessionTrails[key], opts, reset) {
				if rowIndex == 0 {
					sessionLine(row, key)
				} else {
					line(row)
				}
			}
		}
	} else {
		line(opts.theme.DimANSI + "  None" + reset)
	}
	for _, sourceErr := range snapshot.Errors {
		line("  degraded: " + sourceErr)
	}
	if opts.clear && (opts.height == 0 || opts.height > 2) {
		status := wtfHealthFooter(snapshot)
		if snapshot.HealthError != "" {
			status += " · health: " + snapshot.HealthError
		}
		if len(snapshot.Errors) > 0 {
			status += " · degraded: " + snapshot.Errors[0]
		}
		footer = append(footer, wtfComposedRow{text: clip(opts.theme.DimANSI + status + reset)})
		if opts.height == 0 || opts.height >= 4 {
			footer = append(footer, wtfComposedRow{text: clip(opts.theme.DimANSI + "↑↓ move · ⏎ tail · r refresh · q quit" + reset)})
		}
	}
	bodyHeight := opts.height
	if bodyHeight > 0 {
		bodyHeight-- // fixed header
		bodyHeight -= len(footer)
	}
	if bodyHeight >= 0 && opts.height > 0 && len(rows) > bodyHeight {
		start := 0
		if opts.selected != "" {
			for i, row := range rows {
				if row.sessionKey == opts.selected && i >= bodyHeight {
					start = i - bodyHeight + 1
					break
				}
			}
		}
		rows = rows[start:min(start+bodyHeight, len(rows))]
	}
	var b strings.Builder
	for _, row := range append(header, rows...) {
		b.WriteString(row.text + "\n")
	}
	for _, row := range footer {
		b.WriteString(row.text + "\n")
	}
	result := b.String()
	if opts.clear {
		result = "\x1b[H\x1b[2J" + result
	}
	return result
}

func wtfHealthFooter(snapshot wtfSnapshot) string {
	if !snapshot.Monitoring {
		return "monitoring off · run entire wtf install"
	}
	last := snapshot.Health.LastSuccessfulScan
	age := "never"
	if last > 0 {
		now := snapshot.Now
		if now == 0 {
			now = time.Now().Unix()
		}
		age = formatAge(max(int64(0), now-last)) + " ago"
	}
	footer := "monitoring on · last successful scan " + age
	if snapshot.RefreshPending {
		footer += " · refresh pending"
	}
	if snapshot.Health.LastError != "" {
		footer += " · error: " + snapshot.Health.LastError
	}
	return footer
}

func formatAge(seconds int64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%dh", seconds/3600)
	}
}

func activeWTFFindings(findings []wtfFinding) []wtfFinding {
	out := make([]wtfFinding, 0, len(findings))
	for _, finding := range findings {
		if finding.Active {
			out = append(out, finding)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity > out[j].Severity
		}
		if out[i].FirstSeen != out[j].FirstSeen {
			return out[i].FirstSeen < out[j].FirstSeen
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func wtfDeliveryLabel(finding wtfFinding) string {
	if len(finding.Delivery) == 0 {
		return "not attempted"
	}
	states := make([]string, 0, len(finding.Delivery))
	for _, delivery := range finding.Delivery {
		states = append(states, delivery.State)
	}
	sort.Strings(states)
	return strings.Join(states, ", ")
}

type wtfWIPTrail struct {
	trail                   wtfTrail
	active, dirty, unmerged int
	dirtyKnown              bool
	unmergedKnown           bool
	reasons                 []string
	worktrees               []wtfWIPWorktree
}

type wtfWIPWorktree struct {
	path                      string
	dirty, unmerged           int
	dirtyKnown, unmergedKnown bool
	evidence                  []string
}

func wtfCountLabel(count int, known bool) string {
	if !known {
		return "unknown"
	}
	return fmt.Sprint(count)
}

func wtfDirtyCountKnown(worktree wtfWorktree) bool {
	if worktree.DirtyFiles < 0 || !worktree.Exists {
		return false
	}
	return worktree.GitError != "git status failed" && worktree.GitError != "worktree not listed by git" && worktree.GitError != "worktree record missing"
}

func selectWIPTrails(snapshot wtfSnapshot) []wtfWIPTrail {
	sessions := make(map[string]wtfSession, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		sessions[wtfSessionKey(session.Agent, session.ID)] = session
	}
	worktrees := make(map[string]wtfWorktree, len(snapshot.Worktrees))
	for _, worktree := range snapshot.Worktrees {
		worktrees[worktree.Path] = worktree
	}
	var out []wtfWIPTrail
	for _, trail := range snapshot.Trails {
		item := wtfWIPTrail{trail: trail, dirtyKnown: true, unmergedKnown: true}
		seenPaths := map[string]bool{}
		seenSessions := map[string]bool{}
		hasWorktreeWIP := false
		for _, association := range trail.Associations {
			if !seenSessions[association.SessionKey] && sessions[association.SessionKey].Active {
				item.active++
				seenSessions[association.SessionKey] = true
			}
			if association.Worktree == "" || seenPaths[association.Worktree] {
				continue
			}
			seenPaths[association.Worktree] = true
			worktree, ok := worktrees[association.Worktree]
			if !ok {
				worktree = wtfWorktree{Path: association.Worktree, DirtyFiles: -1, UnmergedCommits: -1, GitError: "worktree record missing"}
			}
			if worktreeHasWIP(worktree) {
				hasWorktreeWIP = true
			}
			if worktreeHasWIP(worktree) {
				var evidence []string
				for _, candidate := range trail.Associations {
					if candidate.Worktree == association.Worktree {
						evidence = append(evidence, candidate.Source+": "+candidate.Evidence)
					}
				}
				item.worktrees = append(item.worktrees, wtfWIPWorktree{path: association.Worktree, dirty: worktree.DirtyFiles, unmerged: worktree.UnmergedCommits, dirtyKnown: wtfDirtyCountKnown(worktree), unmergedKnown: worktree.UnmergedCommits >= 0, evidence: sortedUnique(evidence)})
			}
			if !wtfDirtyCountKnown(worktree) {
				item.dirtyKnown = false
			} else {
				item.dirty += worktree.DirtyFiles
			}
			if worktree.UnmergedCommits < 0 {
				item.unmergedKnown = false
			} else {
				item.unmerged += worktree.UnmergedCommits
			}
			if worktree.GitError != "" {
				item.reasons = append(item.reasons, worktree.GitError)
			}
		}
		if item.active > 0 {
			item.reasons = append(item.reasons, "active session")
		}
		if item.dirty > 0 {
			item.reasons = append(item.reasons, "dirty worktree")
		}
		if item.unmerged > 0 {
			item.reasons = append(item.reasons, "unmerged commits")
		}
		if item.active > 0 || hasWorktreeWIP {
			item.reasons = sortedUnique(item.reasons)
			sort.Slice(item.worktrees, func(i, j int) bool { return item.worktrees[i].path < item.worktrees[j].path })
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].trail.Key < out[j].trail.Key })
	return out
}

// wtfComposedRows returns the exact row count for a rendered session range,
// including section and repository headers introduced at viewport boundaries.
func wtfComposedRows(snapshot wtfSnapshot, top, end int) int {
	sessions := orderedWTFSessions(snapshot)
	if top < 0 || top >= len(sessions) || end < top {
		return 0
	}
	end = min(end, len(sessions)-1)
	rows := 0
	lastRepo := "\x00"
	lastActive := false
	for i := top; i <= end; i++ {
		session := sessions[i]
		count := 1
		if strings.TrimSpace(session.Summary) != "" {
			count++
		}
		if strings.TrimSpace(session.NeedsUser) != "" {
			count++
		}
		if i == top || session.Active != lastActive {
			count += 2 // blank row and section heading
			lastRepo = "\x00"
		}
		repo := firstNonEmpty(session.Repo, tildify(session.Cwd, snapshot.Home), "Other")
		if repo != lastRepo {
			count++
			lastRepo = repo
		}
		rows += count
		lastActive = session.Active
	}
	return rows
}

func wtfSessionLines(session wtfSession, trails []string, opts wtfRenderOpts, reset string) []string {
	mark := "  "
	if opts.selected == wtfSessionKey(session.Agent, session.ID) {
		mark = "▸ "
	}
	agent, color := "C", opts.theme.ClaudeANSI
	if session.Agent == AgentAmp {
		agent, color = "A", opts.theme.UserANSI
	}
	name := firstNonEmpty(session.Name, shortID(session.ID))
	state := firstNonEmpty(session.State, "ended")
	meta := strings.TrimSpace(strings.Join([]string{session.Branch, tildify(session.Cwd, opts.snapshotHome)}, "   "))
	head := fmt.Sprintf("%s%s%s%s  %-13s %-6s %s", mark, color, agent, reset, name, state, meta)
	lines := []string{head}
	if len(trails) > 0 {
		lines = append(lines, "   trails "+strings.Join(sortedUnique(trails), ", "))
	}
	if summary := strings.TrimSpace(session.Summary); summary != "" {
		lines = append(lines, "   "+summary)
	}
	if need := strings.TrimSpace(session.NeedsUser); need != "" {
		lines = append(lines, "   "+color+"NEEDS DANIEL"+reset+"  "+need)
	}
	return lines
}

func updateWTF(ui wtfUI, key treeKey, r rune) wtfUI {
	ui.Refresh = false
	ui.Chosen = nil
	sessions := orderedWTFSessions(ui.Snapshot)
	switch key {
	case kUp:
		ui.Cursor--
	case kDown:
		ui.Cursor++
	case kHome:
		ui.Cursor = 0
	case kEnd:
		ui.Cursor = len(sessions) - 1
	case kEsc, kCtrlC:
		ui.Quit = true
	case kEnter:
		if len(sessions) > 0 {
			chosen := sessions[max(0, min(ui.Cursor, len(sessions)-1))]
			ui.Chosen = &chosen
		}
	case kRune:
		switch r {
		case 'q':
			ui.Quit = true
		case 'r':
			ui.Refresh = true
		}
	}
	ui.Cursor = max(0, min(ui.Cursor, len(sessions)-1))
	ui.Top = max(0, min(ui.Top, ui.Cursor))
	if ui.Cursor < ui.Top {
		ui.Top = ui.Cursor
	}
	rowBudget := max(1, ui.Height-1) // the Today row remains fixed above the viewport
	for wtfComposedRows(ui.Snapshot, ui.Top, ui.Cursor) > rowBudget && ui.Top < ui.Cursor {
		ui.Top++
	}
	return ui
}

func normalizeWTFViewport(ui wtfUI, width, height int) wtfUI {
	ui.Width = width
	ui.Height = height
	return updateWTF(ui, treeKey(-1), 0)
}

func applyWTFSnapshot(ui wtfUI, snapshot wtfSnapshot) wtfUI {
	selected := ""
	old := orderedWTFSessions(ui.Snapshot)
	if len(old) > 0 {
		index := max(0, min(ui.Cursor, len(old)-1))
		selected = wtfSessionKey(old[index].Agent, old[index].ID)
	}
	ui.Snapshot = snapshot
	next := orderedWTFSessions(snapshot)
	if selected != "" {
		for i, session := range next {
			if wtfSessionKey(session.Agent, session.ID) == selected {
				ui.Cursor = i
				return updateWTF(ui, treeKey(-1), 0)
			}
		}
	}
	ui.Cursor = max(0, min(ui.Cursor, len(next)-1))
	return updateWTF(ui, treeKey(-1), 0)
}

func collectWTFSnapshot(home string, cache map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache) {
	now := time.Now().Unix()
	snapshot := wtfSnapshot{GeneratedAt: now, Home: home, Sessions: collectWTFSessions(home, now, time.Local, wtfInventoryDeps{Today: todaysSessions, Live: currentLiveSessions})}
	return summarizeWTFSnapshot(snapshot, home, cache, summarizeWTFSession)
}

type wtfCollectResult struct {
	snapshot wtfSnapshot
	cache    map[string]wtfSummaryCache
}

type wtfDashboardDeps struct {
	ReadState  func(string, int64) (wtfState, error)
	ReadHealth func(string) (wtfHealth, error)
	Running    func(wtfHealth) bool
	Scan       func(context.Context, string, wtfState, wtfScanDeps) (wtfState, error)
	Request    func(string) error
	Now        func() time.Time
	ScanDeps   wtfScanDeps
}

type wtfDashboardCollector struct {
	home            string
	deps            wtfDashboardDeps
	state           wtfState
	stateErr        error
	health          wtfHealth
	healthErr       error
	monitoring      bool
	fallbackCurrent bool
	refreshPending  bool
	refreshBaseline int64
	durableUpdated  int64
	refreshErr      error
	refreshQueued   atomic.Bool
}

func newWTFDashboardCollector(home string, deps wtfDashboardDeps) *wtfDashboardCollector {
	now := deps.Now()
	state, stateErr := deps.ReadState(home, now.Unix())
	health, healthErr := deps.ReadHealth(home)
	durableUpdated := int64(0)
	if stateErr == nil {
		durableUpdated = state.UpdatedAt
	}
	return &wtfDashboardCollector{home: home, deps: deps, state: state, stateErr: stateErr, health: health, healthErr: healthErr, durableUpdated: durableUpdated}
}

func (c *wtfDashboardCollector) readHealth() bool {
	health, err := c.deps.ReadHealth(c.home)
	if err == nil {
		c.health, c.healthErr = health, nil
	} else {
		c.healthErr = err
	}
	return c.deps.Running(c.health)
}

func (c *wtfDashboardCollector) Collect(_ map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache) {
	now := c.deps.Now()
	refreshRequested := c.refreshQueued.Swap(false)
	running := c.readHealth()
	if running {
		next, err := c.deps.ReadState(c.home, now.Unix())
		durableReadOK := err == nil
		if err == nil {
			c.state, c.stateErr = next, nil
			c.durableUpdated = next.UpdatedAt
		} else {
			c.stateErr = err
		}
		c.fallbackCurrent = false
		if refreshRequested {
			c.refreshPending = false
			if !durableReadOK {
				c.refreshErr = fmt.Errorf("establish refresh baseline: %w", err)
			} else {
				c.refreshBaseline = c.durableUpdated
				c.refreshPending = true
				c.refreshErr = nil
				if c.deps.Request != nil {
					if err := c.deps.Request(c.home); err != nil {
						c.refreshPending = false
						c.refreshErr = err
					}
				}
			}
		}
	} else if refreshRequested || c.monitoring || !c.fallbackCurrent {
		next, scanErr := c.deps.Scan(context.Background(), c.home, c.state, c.deps.ScanDeps)
		c.state = next
		if scanErr != nil {
			c.stateErr = errors.Join(c.stateErr, scanErr)
		}
		c.fallbackCurrent = true
	}
	if !running {
		c.refreshPending = false
	} else if !refreshRequested && c.refreshPending && c.durableUpdated > c.refreshBaseline {
		c.refreshPending = false
	}
	c.monitoring = running
	snapshot := snapshotFromWTFState(c.home, c.state, c.stateErr)
	if c.refreshErr != nil {
		snapshot.Errors = append(snapshot.Errors, "request refresh: "+c.refreshErr.Error())
	}
	snapshot.Monitoring = running
	snapshot.Health = c.health
	if c.healthErr != nil {
		snapshot.HealthError = c.healthErr.Error()
	}
	snapshot.RefreshPending = c.refreshPending
	snapshot.Now = now.Unix()
	return snapshot, c.state.SummaryCache
}

func (c *wtfDashboardCollector) RequestRefresh() error {
	c.refreshQueued.Store(true)
	return nil
}

func runWTFDashboardLoop(ui wtfUI, initialCache map[string]wtfSummaryCache, collect func(map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache), keys <-chan wtfKeyEvent, render func(wtfUI) error) (*wtfSession, error) {
	return runWTFDashboardLoopWithRefresh(ui, initialCache, collect, keys, render, nil, nil)
}

func runWTFDashboardLoopWithWorkerExit(ui wtfUI, initialCache map[string]wtfSummaryCache, collect func(map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache), keys <-chan wtfKeyEvent, render func(wtfUI) error, workerExited chan<- struct{}) (*wtfSession, error) {
	return runWTFDashboardLoopWithRefresh(ui, initialCache, collect, keys, render, nil, workerExited)
}

func runWTFDashboardLoopWithRefresh(ui wtfUI, initialCache map[string]wtfSummaryCache, collect func(map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache), keys <-chan wtfKeyEvent, render func(wtfUI) error, requestScan func() error, workerExited chan<- struct{}) (*wtfSession, error) {
	requests := make(chan map[string]wtfSummaryCache)
	results := make(chan wtfCollectResult)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer func() {
			if workerExited != nil {
				close(workerExited)
			}
		}()
		for {
			var cache map[string]wtfSummaryCache
			select {
			case cache = <-requests:
			case <-done:
				return
			}
			snapshot, nextCache := collect(cache)
			select {
			case results <- wtfCollectResult{snapshot: snapshot, cache: nextCache}:
			case <-done:
				return
			}
		}
	}()

	cache := initialCache
	if cache == nil {
		cache = map[string]wtfSummaryCache{}
	}
	refreshing := false
	refreshPending := false
	requestRefresh := func() {
		if refreshing {
			refreshPending = true
			return
		}
		refreshing = true
		requests <- cache
	}
	requestRefresh()
	if err := render(ui); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(wtfRefresh)
	defer ticker.Stop()
	for {
		select {
		case result := <-results:
			refreshing = false
			cache = result.cache
			ui = applyWTFSnapshot(ui, result.snapshot)
			if err := render(ui); err != nil {
				return nil, err
			}
			if refreshPending {
				refreshPending = false
				requestRefresh()
			}
		case event, ok := <-keys:
			if !ok {
				return nil, nil
			}
			if event.err != nil {
				return nil, event.err
			}
			if event.width > 0 && event.height > 0 {
				ui = normalizeWTFViewport(ui, event.width, event.height)
			}
			ui = updateWTF(ui, event.key, event.r)
			if ui.Refresh {
				ui.Refresh = false
				if requestScan != nil {
					if err := requestScan(); err != nil {
						ui.Snapshot.Errors = append(ui.Snapshot.Errors, "request scan: "+err.Error())
					}
				}
				requestRefresh()
			}
			if ui.Quit {
				return nil, nil
			}
			if ui.Chosen != nil {
				return ui.Chosen, nil
			}
			if err := render(ui); err != nil {
				return nil, err
			}
		case <-ticker.C:
			requestRefresh()
		}
	}
}

type wtfReader interface{ Read([]byte) (int, error) }

func readWTFKeyEvents(reader wtfReader, size func() (int, int), width, height int, events chan<- wtfKeyEvent, done <-chan struct{}) {
	buf := make([]byte, 16)
	for {
		n, readErr := reader.Read(buf)
		w, h := size()
		event := wtfKeyEvent{width: w, height: h}
		publish := w != width || h != height
		if n > 0 {
			event.key, event.r = decodeKey(buf[:n])
			publish = true
		}
		if publish {
			select {
			case events <- event:
				width, height = w, h
			case <-done:
				return
			}
		}
		if readErr != nil && readErr != io.EOF {
			select {
			case events <- wtfKeyEvent{err: readErr}:
			case <-done:
			}
			return
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func runWTFDashboard(home string, cfg Config) (*wtfSession, error) {
	collector := newWTFDashboardCollector(home, wtfDashboardDeps{
		ReadState: readWTFState, ReadHealth: readWTFHealthFile, Running: wtfDaemonRunning,
		Scan: scanWTF, Request: requestWTFScan, Now: time.Now, ScanDeps: defaultWTFScanDeps(),
	})
	cache := collector.state.SummaryCache
	collect := collector.Collect
	if !isCharDevice(os.Stdout) {
		snapshot, _ := collect(cache)
		_, err := io.WriteString(os.Stdout, renderWTFSnapshot(snapshot, 120, false))
		return nil, err
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer tty.Close()
	saved, ok := setRawTimed(tty)
	if !ok {
		return nil, fmt.Errorf("wtf: could not set terminal raw mode")
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return nil, err
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	theme := mustLoadTheme(cfg)
	width, height := termSize(tty)
	keys := make(chan wtfKeyEvent)
	readDone := make(chan struct{})
	defer close(readDone)
	go func() {
		readWTFKeyEvents(tty, func() (int, int) { return termSize(tty) }, width, height, keys, readDone)
	}()
	render := func(ui wtfUI) error {
		_, err := io.WriteString(tty, renderWTF(ui, theme))
		return err
	}
	return runWTFDashboardLoopWithRefresh(wtfUI{Width: width, Height: height}, cache, collect, keys, render, collector.RequestRefresh, nil)
}

func loadWTFDashboardCache(home string, now int64) (map[string]wtfSummaryCache, error) {
	state, err := loadWTFState(home, now)
	if err != nil {
		return nil, err
	}
	return state.SummaryCache, nil
}

func wtfTreeChoice(session wtfSession) treeChoice {
	return treeChoice{Result: treeChosen, Agent: session.Agent, Path: session.Transcript, Cwd: session.Cwd, ID: session.ID, Repo: session.Repo}
}
