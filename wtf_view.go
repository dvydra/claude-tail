package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
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

	header = append(header, wtfComposedRow{text: clip(fmt.Sprintf("%sToday%s  %d active · %d ended today", opts.theme.ClaudeANSI, reset, active, ended))})
	if len(sessions) == 0 {
		line("")
		line("No sessions active or seen today.")
	} else {
		renderSection := func(title string, wantActive bool) {
			rendered := false
			for i, session := range sessions {
				if session.Active == wantActive && i >= opts.top {
					rendered = true
					break
				}
			}
			if !rendered {
				return
			}
			line("")
			line(opts.theme.ClaudeANSI + title + reset)
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
				for rowIndex, row := range wtfSessionLines(session, opts, reset) {
					if rowIndex == 0 {
						sessionLine(row, key)
					} else {
						line(row)
					}
				}
			}
		}
		if active > 0 {
			renderSection("Now", true)
		}
		if ended > 0 {
			renderSection("Recently stopped", false)
		}
		if opts.clear {
			line("")
			line(opts.theme.DimANSI + "↑↓ move · ⏎ tail · r refresh · q quit" + reset)
		}
	}
	bodyHeight := opts.height
	if bodyHeight > 0 {
		bodyHeight--
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
	result := b.String()
	if opts.clear {
		result = "\x1b[H\x1b[2J" + result
	}
	return result
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

func wtfSessionLines(session wtfSession, opts wtfRenderOpts, reset string) []string {
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

func runWTFDashboardLoop(ui wtfUI, initialCache map[string]wtfSummaryCache, collect func(map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache), keys <-chan wtfKeyEvent, render func(wtfUI) error) (*wtfSession, error) {
	requests := make(chan map[string]wtfSummaryCache, 1)
	results := make(chan wtfCollectResult)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for cache := range requests {
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

func runWTFDashboard(home string, cfg Config) (*wtfSession, error) {
	cache, err := loadWTFDashboardCache(home, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	if !isCharDevice(os.Stdout) {
		snapshot, _ := collectWTFSnapshot(home, cache)
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
		buf := make([]byte, 16)
		for {
			n, readErr := tty.Read(buf)
			if n > 0 {
				key, r := decodeKey(buf[:n])
				w, h := termSize(tty)
				select {
				case keys <- wtfKeyEvent{key: key, r: r, width: w, height: h}:
				case <-readDone:
					return
				}
			}
			if readErr != nil && readErr != io.EOF {
				select {
				case keys <- wtfKeyEvent{err: readErr}:
				case <-readDone:
				}
				return
			}
			select {
			case <-readDone:
				return
			default:
			}
		}
	}()
	collect := func(cache map[string]wtfSummaryCache) (wtfSnapshot, map[string]wtfSummaryCache) {
		return collectWTFSnapshot(home, cache)
	}
	render := func(ui wtfUI) error {
		_, err := io.WriteString(tty, renderWTF(ui, theme))
		return err
	}
	return runWTFDashboardLoop(wtfUI{Width: width, Height: height}, cache, collect, keys, render)
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
