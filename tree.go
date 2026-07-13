package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// The session tree is the interactive picker: every Claude session on disk,
// grouped by the folder it ran in, so you can find "which folder was I in?"
// without remembering. It replaces the old flat live-only menu.
//
// Scaling: nothing here is O(total sessions) at startup. A cheap dir-mtime gate
// (one stat per project folder) drops folders with no activity in the window, so
// cold folders' session files are never touched. Within the (small) kept set we
// read each session once for its snippet/branch — affordable because the window
// (default 7 days) keeps that set tiny. `--list` bypasses the window for a full
// static dump.

// ── data model ──────────────────────────────────────────────────────────────

type treeSession struct {
	Path    string
	ID      string // filename stem = session uuid
	Mtime   int64
	Branch  string
	Snippet string
	Repo    string // owner/repo (search hits) — used to reconstruct cloud-only transcripts
	Msgs    int    // rough count: user+assistant events (checkpoints for entire rows)
	Live    bool   // a running claude process holds this session's folder
	cwd     string // recovered .cwd (build-only; folder carries the display copy)

	// entire cloud metadata (0/empty for untracked local sessions) — for the token
	// column and the summary card.
	Tokens int64  // spend: input + output + cache-write tokens
	Model  string // e.g. claude-opus-4-8[1m]
	Prompt string // the session's opening prompt
}

type treeFolder struct {
	Cwd      string // display: real path (local tree) or owner/repo (merged/search)
	Dir      string // a real local directory for the group, for `n` to cd into ("" if none)
	Slug     string // project-folder base name
	Mtime    int64  // newest session mtime
	Live     int    // running claude processes in this cwd
	Sessions []treeSession
	Expanded bool
}

type sessionTree struct {
	Folders []treeFolder
	Now     int64
	Pwd     string
	Home    string
	// CurrentGroup is the group (repo) the cursor should start on — the current
	// working dir's repo in the merged tree. Empty falls back to $PWD's folder.
	CurrentGroup string
}

// claudeMetaEvent is the subset of a Claude event we read to summarize a session.
type claudeMetaEvent struct {
	Type      string         `json:"type"`
	Summary   string         `json:"summary"`
	AiTitle   string         `json:"aiTitle"`
	Cwd       string         `json:"cwd"`
	GitBranch string         `json:"gitBranch"`
	Message   *claudeMessage `json:"message"`
}

// ── build ─────────────────────────────────────────────────────────────────

// buildClaudeTree scans ~/.claude/projects for Claude sessions active within the
// last `days` days (0 = uncapped), grouped by folder. liveCwds maps a live cwd to
// its running-process count; a folder whose cwd is live is kept regardless of age.
func buildClaudeTree(home, pwd string, days int, now int64, liveCwds map[string]int) sessionTree {
	root := claudeProjectsDir(home)
	entries, _ := os.ReadDir(root)

	var cutoff int64
	if days > 0 {
		cutoff = now - int64(days)*86400
	}
	// Slugs of live cwds — force-keep these even if the dir looks stale (a resumed
	// session doesn't bump the folder's mtime).
	forceKeep := map[string]bool{}
	for cwd := range liveCwds {
		forceKeep[claudeSlug(cwd)] = true
	}

	tree := sessionTree{Now: now, Pwd: pwd, Home: home}
	pwdSlug := claudeSlug(pwd)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		dir := filepath.Join(root, slug)
		// Cheap gate: skip cold folders without opening a single session file.
		if cutoff > 0 && !forceKeep[slug] {
			fi, err := os.Stat(dir)
			if err != nil || fi.ModTime().Unix() < cutoff {
				continue
			}
		}
		sessions := claudeFolderSessions(dir, cutoff, forceKeep[slug])
		if len(sessions) == 0 {
			continue
		}
		folder := treeFolder{
			Slug:     slug,
			Sessions: sessions,
			Mtime:    sessions[0].Mtime,
			Cwd:      firstNonEmpty(sessions[0].cwd, unslugGuess(slug)),
			Dir:      sessions[0].cwd, // real recorded cwd, for `n`
			Expanded: slug == pwdSlug,
		}
		folder.Live = liveCwds[folder.Cwd]
		// Mark the newest N sessions live, where N is the running-process count —
		// those are the panes actively writing. Older sessions in a live folder
		// are colored by their own age, not painted live wholesale.
		for i := range folder.Sessions {
			folder.Sessions[i].Live = i < folder.Live
		}
		tree.Folders = append(tree.Folders, folder)
	}
	sortFolders(tree.Folders)
	return tree
}

// claudeFolderSessions returns the folder's sessions newer than cutoff,
// newest-first. When force is set (a live folder) the newest session is kept even
// if it's older than the window, so a long-idle live pane still shows.
func claudeFolderSessions(dir string, cutoff int64, force bool) []treeSession {
	metas := globWithMtime(filepath.Join(dir, "*.jsonl")) // newest-first
	out := make([]treeSession, 0, len(metas))
	for _, m := range metas {
		if cutoff > 0 && m.mtime < cutoff {
			break // desc order — everything after is older too
		}
		out = append(out, sessionFromMeta(m))
	}
	if force && len(out) == 0 && len(metas) > 0 {
		out = append(out, sessionFromMeta(metas[0]))
	}
	return out
}

func sessionFromMeta(m fileMeta) treeSession {
	s := treeSession{
		Path:  m.path,
		ID:    strings.TrimSuffix(filepath.Base(m.path), ".jsonl"),
		Mtime: m.mtime,
	}
	s.Snippet, s.Branch, s.Msgs, s.cwd = loadClaudeMeta(m.path)
	return s
}

// loadClaudeMeta extracts a display snippet, git branch, and cwd from a Claude
// session. Snippet precedence: the session summary, then its ai-title, then its
// first user prompt — all of which live in the opening events, so this reads only
// the file's head (bounded, with an early-out) rather than the whole transcript.
// That keeps a full-tree build cheap no matter how large individual sessions are.
func loadClaudeMeta(path string) (snippet, branch string, msgs int, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var summary, aiTitle, firstUser string
	const maxLines = 128 // headers/first prompt are near the top; cap the scan
	for i := 0; i < maxLines && sc.Scan(); i++ {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev claudeMetaEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "summary":
			if ev.Summary != "" {
				summary = ev.Summary
			}
		case "ai-title":
			if ev.AiTitle != "" {
				aiTitle = ev.AiTitle
			}
		case "user", "assistant":
			msgs++
			if cwd == "" && ev.Cwd != "" {
				cwd = ev.Cwd
			}
			if branch == "" && ev.GitBranch != "" {
				branch = ev.GitBranch
			}
			if ev.Type == "user" && firstUser == "" {
				firstUser = claudeUserText(ev.Message)
			}
		}
		// Early-out once we have everything the row needs.
		if cwd != "" && branch != "" && firstNonEmpty(summary, aiTitle, firstUser) != "" {
			break
		}
	}
	return collapsePreview(firstNonEmpty(summary, aiTitle, firstUser)), branch, msgs, cwd
}

// claudeUserText pulls plain text from a user message (a bare string, or the
// text blocks of a block array; tool-result-only messages yield "").
func claudeUserText(msg *claudeMessage) string {
	if msg == nil {
		return ""
	}
	var s string
	if json.Unmarshal(msg.Content, &s) == nil {
		return s
	}
	return joinClaudeText(msg.Content)
}

type fileMeta struct {
	path  string
	mtime int64
}

// globWithMtime returns files matching pattern with their mtimes, newest-first
// (ties broken by path descending — matching newestGlobAll's ordering).
func globWithMtime(pattern string) []fileMeta {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	out := make([]fileMeta, 0, len(matches))
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		out = append(out, fileMeta{m, fi.ModTime().Unix()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].mtime != out[j].mtime {
			return out[i].mtime > out[j].mtime
		}
		return out[i].path > out[j].path
	})
	return out
}

// unslugGuess best-effort reverses a project slug to a path for display when no
// session carried a .cwd (rare). Lossy — '-' can't be told from '/'/'.'/' '.
func unslugGuess(slug string) string {
	return strings.ReplaceAll(slug, "-", "/")
}

// sortFolders orders live folders first, then by newest activity descending.
func sortFolders(fs []treeFolder) {
	sort.SliceStable(fs, func(i, j int) bool {
		if li, lj := fs[i].Live > 0, fs[j].Live > 0; li != lj {
			return li
		}
		return fs[i].Mtime > fs[j].Mtime
	})
}

// ── recency tiers ────────────────────────────────────────────────────────────

type recencyTier int

const (
	tierStale      recencyTier = iota // older than a day
	tierRecent                        // within a day
	tierRecentLive                    // within 15 min but not running
	tierLive                          // a process holds this folder right now
)

const (
	recentLiveWindow = 15 * 60   // 15 minutes
	recentWindow     = 24 * 3600 // 1 day
)

func classifyTier(mtime, now int64, live bool) recencyTier {
	if live {
		return tierLive
	}
	switch d := now - mtime; {
	case d < recentLiveWindow:
		return tierRecentLive
	case d < recentWindow:
		return tierRecent
	default:
		return tierStale
	}
}

// tierColor maps a recency tier to a truecolor SGR: bright green (live) → muted
// green (recently live) → near-white (recent) → light grey (stale). Fixed
// truecolors (like the tool-dot palette) rather than theme-derived, so the ramp
// reads consistently across themes.
func tierColor(t recencyTier) string {
	switch t {
	case tierLive:
		return "\x1b[38;2;80;230;120m"
	case tierRecentLive:
		return "\x1b[38;2;95;165;115m"
	case tierRecent:
		return "\x1b[38;2;225;225;225m"
	default:
		// Stale: dimmer than "recent" but still comfortably readable — the theme's
		// DimANSI is too dark/low-contrast for a whole row of text.
		return "\x1b[38;2;192;196;204m"
	}
}

// ── flattening (folders + visible sessions → row list) ─────────────────────

type treeRow struct {
	Folder  int
	Session int // -1 for a folder header
}

// flattenRows produces the visible row list given expand state and a filter.
// A non-empty filter force-expands every folder and keeps only matching folders
// (whole subtree) or matching sessions within a folder.
func flattenRows(t sessionTree, filter string) []treeRow {
	f := strings.ToLower(strings.TrimSpace(filter))
	var rows []treeRow
	for fi := range t.Folders {
		folder := t.Folders[fi]
		matchFolder := f == "" || strings.Contains(strings.ToLower(folder.Cwd), f)
		var kids []int
		for si := range folder.Sessions {
			if f == "" || matchFolder || sessionMatches(folder.Sessions[si], f) {
				kids = append(kids, si)
			}
		}
		if f != "" && !matchFolder && len(kids) == 0 {
			continue
		}
		rows = append(rows, treeRow{Folder: fi, Session: -1})
		if folder.Expanded || f != "" {
			for _, si := range kids {
				rows = append(rows, treeRow{Folder: fi, Session: si})
			}
		}
	}
	return rows
}

func sessionMatches(s treeSession, f string) bool {
	return strings.Contains(strings.ToLower(s.Snippet), f) ||
		strings.Contains(strings.ToLower(s.ID), f) ||
		strings.Contains(strings.ToLower(s.Branch), f)
}

// ── UI state + reducer (pure) ────────────────────────────────────────────────

type treeUI struct {
	Tree            sessionTree
	Theme           Theme
	Rows            []treeRow
	Cursor          int
	Top             int // first visible row (scroll offset)
	Width           int
	Height          int // body rows available (excludes header + footer)
	Filter          string
	Filtering       bool
	Quit            bool
	NewWorkspace    bool        // 'n' → fresh session workspace; ends the loop
	NewWorkspaceDir string      // folder under the cursor when 'n' pressed ("" = $PWD)
	SummaryReq      bool        // 'i' → show the highlighted session's combined info view
	Sel             treeSession // the session captured for the info view
	Chosen          string      // selected session path; non-empty ends the loop
	ChosenCwd       string      // folder cwd of the selection (for the iTerm launcher)
	ChosenID        string      // session id of the selection (for claude --resume)
	ChosenRepo      string      // repo of the selection (to reconstruct a cloud-only transcript)
	Workspace       bool        // selection should open the iTerm workspace, not tail
}

type treeKey int

const (
	kNone treeKey = iota
	kUp
	kDown
	kLeft
	kRight
	kEnter
	kEsc
	kBackspace
	kCtrlC
	kHome
	kEnd
	kPageUp
	kPageDown
	kRune
)

// updateTree is the reducer: it maps a key (plus a rune for kRune) to the next
// state. It never touches the terminal — the driver renders the returned state.
func updateTree(ui treeUI, k treeKey, r rune) treeUI {
	if ui.Filtering {
		switch k {
		case kEsc, kEnter:
			ui.Filtering = false
		case kBackspace:
			if rs := []rune(ui.Filter); len(rs) > 0 {
				ui.Filter = string(rs[:len(rs)-1])
			}
		case kCtrlC:
			ui.Quit = true
		case kUp:
			ui.Cursor--
		case kDown:
			ui.Cursor++
		case kPageUp:
			ui.Cursor -= ui.pageStep()
		case kPageDown:
			ui.Cursor += ui.pageStep()
		case kRune:
			ui.Filter += string(r)
		}
		ui.Rows = flattenRows(ui.Tree, ui.Filter)
		ui.clamp()
		return ui
	}

	switch k {
	case kUp:
		ui.Cursor--
	case kDown:
		ui.Cursor++
	case kHome:
		ui.Cursor = 0
	case kEnd:
		ui.Cursor = len(ui.Rows) - 1
	case kPageUp:
		ui.Cursor -= ui.pageStep()
	case kPageDown:
		ui.Cursor += ui.pageStep()
	case kRight:
		ui.expand()
	case kLeft:
		ui.collapse()
	case kEnter:
		ui.activate()
	case kEsc, kCtrlC:
		ui.Quit = true
	case kRune:
		switch r {
		case 'j':
			ui.Cursor++
		case 'k':
			ui.Cursor--
		case 'l':
			ui.expand()
		case 'h':
			ui.collapse()
		case 'g':
			ui.Cursor = 0
		case 'G':
			ui.Cursor = len(ui.Rows) - 1
		case ' ':
			ui.Cursor += ui.pageStep() // pager convention: space = page down
		case 'n', 'N':
			// Fresh session workspace in the highlighted folder's dir (else $PWD).
			ui.NewWorkspace = true
			if row, ok := ui.current(); ok {
				ui.NewWorkspaceDir = ui.Tree.Folders[row.Folder].Dir
			}
		case 'i', 'I':
			if s, ok := ui.currentSession(); ok {
				ui.Sel, ui.SummaryReq = s, true
			}
		case 't', 'T':
			ui.selectSession(false) // tail in-place, no windowing
		case 'q', 'Q':
			ui.Quit = true
		case '/':
			ui.Filtering = true
		}
	}
	ui.clamp()
	return ui
}

func (ui *treeUI) current() (treeRow, bool) {
	if ui.Cursor < 0 || ui.Cursor >= len(ui.Rows) {
		return treeRow{}, false
	}
	return ui.Rows[ui.Cursor], true
}

// currentSession returns the session under the cursor (false on a folder header).
func (ui *treeUI) currentSession() (treeSession, bool) {
	if row, ok := ui.current(); ok && row.Session >= 0 {
		return ui.Tree.Folders[row.Folder].Sessions[row.Session], true
	}
	return treeSession{}, false
}

func (ui *treeUI) expand() {
	if row, ok := ui.current(); ok && row.Session == -1 {
		ui.Tree.Folders[row.Folder].Expanded = true
		ui.Rows = flattenRows(ui.Tree, ui.Filter)
	}
}

func (ui *treeUI) collapse() {
	row, ok := ui.current()
	if !ok {
		return
	}
	ui.Tree.Folders[row.Folder].Expanded = false
	ui.Rows = flattenRows(ui.Tree, ui.Filter)
	ui.Cursor = ui.folderHeaderIndex(row.Folder)
}

func (ui *treeUI) activate() {
	row, ok := ui.current()
	if !ok {
		return
	}
	if row.Session == -1 {
		ui.Tree.Folders[row.Folder].Expanded = !ui.Tree.Folders[row.Folder].Expanded
		ui.Rows = flattenRows(ui.Tree, ui.Filter)
		return
	}
	ui.selectSession(true) // Enter on a session → open the iTerm workspace
}

// selectSession records the session under the cursor as the choice, ending the
// loop. workspace=true opens the 3-pane iTerm workspace (resume + tail + shell);
// false tails in-place. On a folder header it's a no-op (folders expand via
// activate, not select).
func (ui *treeUI) selectSession(workspace bool) {
	row, ok := ui.current()
	if !ok || row.Session == -1 {
		return
	}
	folder := ui.Tree.Folders[row.Folder]
	s := folder.Sessions[row.Session]
	ui.Chosen = s.Path
	ui.ChosenCwd = folder.Cwd
	ui.ChosenID = s.ID
	ui.ChosenRepo = s.Repo
	ui.Workspace = workspace
}

func (ui *treeUI) folderHeaderIndex(folder int) int {
	for i, r := range ui.Rows {
		if r.Folder == folder && r.Session == -1 {
			return i
		}
	}
	return ui.Cursor
}

// pageStep is how far PageUp/PageDown move — one viewport minus a row of
// overlap, so you keep a line of context across the jump.
func (ui *treeUI) pageStep() int {
	if ui.Height > 1 {
		return ui.Height - 1
	}
	return 1
}

func (ui *treeUI) clamp() {
	if ui.Cursor >= len(ui.Rows) {
		ui.Cursor = len(ui.Rows) - 1
	}
	if ui.Cursor < 0 {
		ui.Cursor = 0
	}
	if ui.Height <= 0 {
		return
	}
	if ui.Cursor < ui.Top {
		ui.Top = ui.Cursor
	}
	if ui.Cursor >= ui.Top+ui.Height {
		ui.Top = ui.Cursor - ui.Height + 1
	}
	if ui.Top < 0 {
		ui.Top = 0
	}
}

// initialCursor puts the cursor on the current repo's group (merged tree), else
// the $PWD folder (local crawl), else row 0.
func initialCursor(ui treeUI) int {
	if g := ui.Tree.CurrentGroup; g != "" {
		for i, r := range ui.Rows {
			if r.Session == -1 && ui.Tree.Folders[r.Folder].Cwd == g {
				return i
			}
		}
	}
	pwdSlug := claudeSlug(ui.Tree.Pwd)
	for i, r := range ui.Rows {
		if r.Session == -1 && ui.Tree.Folders[r.Folder].Slug == pwdSlug {
			return i
		}
	}
	return 0
}

// ── rendering (pure) ─────────────────────────────────────────────────────────

func tildify(path, home string) string {
	if home != "" && (path == home || strings.HasPrefix(path, home+"/")) {
		return "~" + path[len(home):]
	}
	return path
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// formatTokens renders a token count compactly: 300k, 1.2m, 28m ("" for 0).
func formatTokens(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n >= 1_000_000:
		s := fmt.Sprintf("%.1f", float64(n)/1e6)
		return strings.TrimSuffix(s, ".0") + "m"
	case n >= 1_000:
		return strconv.FormatInt((n+500)/1000, 10) + "k"
	default:
		return strconv.FormatInt(n, 10)
	}
}

func liveBadge(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "  ● live"
	default:
		return fmt.Sprintf("  ● live×%d", n)
	}
}

func composeFolderRow(f treeFolder, home string, now int64) string {
	arrow := "▸"
	if f.Expanded {
		arrow = "▾"
	}
	return fmt.Sprintf("%s %s  (%d)  %s%s", arrow, tildify(f.Cwd, home), len(f.Sessions), relAge(f.Mtime, now), liveBadge(f.Live))
}

func composeSessionRow(s treeSession, now int64) string {
	bullet := "○"
	if s.Live {
		bullet = "●"
	}
	branch := ""
	if s.Branch != "" {
		branch = "[" + s.Branch + "] "
	}
	return fmt.Sprintf("    %s %-8s  %-7s %6s  %s%s", bullet, shortID(s.ID), relAge(s.Mtime, now), formatTokens(s.Tokens), branch, s.Snippet)
}

// styleRow applies the cursor marker, recency color, and width truncation.
func styleRow(text string, tier recencyTier, cursor bool, width int) string {
	prefix := "  "
	if cursor {
		prefix = "❯ "
	}
	line := prefix + text
	if width > 0 {
		line = truncateRunes(line, width)
	}
	color := tierColor(tier)
	if cursor {
		return "\x1b[7m" + color + line + reset
	}
	return color + line + reset
}

func renderRow(ui treeUI, i int) string {
	row := ui.Rows[i]
	folder := ui.Tree.Folders[row.Folder]
	cursor := i == ui.Cursor
	if row.Session == -1 {
		tier := classifyTier(folder.Mtime, ui.Tree.Now, folder.Live > 0)
		return styleRow(composeFolderRow(folder, ui.Tree.Home, ui.Tree.Now), tier, cursor, ui.Width)
	}
	s := folder.Sessions[row.Session]
	tier := classifyTier(s.Mtime, ui.Tree.Now, s.Live)
	return styleRow(composeSessionRow(s, ui.Tree.Now), tier, cursor, ui.Width)
}

func composeHeader() string {
	return "  CLAUDE SESSIONS  ↑↓ · → expand · ⏎ workspace↗ · i info · t tail · n new↗ · / filter · q"
}

func composeFooter(ui treeUI) string {
	if ui.Filtering {
		return "  /" + ui.Filter + "▏"
	}
	ns := 0
	for _, f := range ui.Tree.Folders {
		ns += len(f.Sessions)
	}
	return fmt.Sprintf("  %d folders · %d sessions", len(ui.Tree.Folders), ns)
}

// renderTree draws one full frame. It repaints from the home position, clearing
// each line to EOL and everything below the last row — no full-screen clear, so
// redraws don't flicker.
func renderTree(ui treeUI) string {
	var b strings.Builder
	b.WriteString("\x1b[H")

	dim := ui.Theme.DimANSI
	b.WriteString(dim + "\x1b[1m" + truncateRunes(composeHeader(), ui.Width) + reset + "\x1b[K\n")

	end := min(ui.Top+ui.Height, len(ui.Rows))
	shown := 0
	for i := ui.Top; i < end; i++ {
		b.WriteString(renderRow(ui, i) + "\x1b[K\n")
		shown++
	}
	for ; shown < ui.Height; shown++ {
		b.WriteString("\x1b[K\n")
	}

	b.WriteString(dim + truncateRunes(composeFooter(ui), ui.Width) + reset + "\x1b[K")
	b.WriteString("\x1b[J")
	return b.String()
}

// ── static list (`--list`) ──────────────────────────────────────────────────

// renderList writes the tree as a flat, greppable ls-style dump. Color is used
// only when writing to a terminal.
func renderList(w io.Writer, t sessionTree, color bool) {
	for _, folder := range t.Folders {
		hdr := tildify(folder.Cwd, t.Home) + liveBadge(folder.Live)
		writeColored(w, hdr, classifyTier(folder.Mtime, t.Now, folder.Live > 0), color)
		for _, s := range folder.Sessions {
			branch := ""
			if s.Branch != "" {
				branch = "[" + s.Branch + "] "
			}
			line := fmt.Sprintf("  %-8s  %-8s %6s  %s%s", shortID(s.ID), relAge(s.Mtime, t.Now), formatTokens(s.Tokens), branch, s.Snippet)
			writeColored(w, line, classifyTier(s.Mtime, t.Now, s.Live), color)
		}
	}
}

func writeColored(w io.Writer, line string, tier recencyTier, color bool) {
	if color {
		fmt.Fprintf(w, "%s%s%s\n", tierColor(tier), line, reset)
	} else {
		fmt.Fprintln(w, line)
	}
}

// ── driver (imperative tty; not unit-tested) ─────────────────────────────────

// claudeLiveCwds returns each cwd with a running `claude` process mapped to its
// process count, or nil when pgrep/lsof are unavailable.
func claudeLiveCwds() map[string]int {
	if !pickerToolsAvailable() {
		return nil
	}
	m := map[string]int{}
	for _, cc := range activeCwdCounts("claude") {
		m[cc.Cwd] = cc.Count
	}
	return m
}

// treeResult distinguishes how the tree exited: a session was chosen to open as
// an iTerm workspace (resume + tail + shell), a session was chosen to tail
// in-place, the user quit (abort the program), or it never ran (empty tree / no
// tty → fall back).
type treeResult int

const (
	treeWorkspace    treeResult = iota // open the 3-pane iTerm workspace (Enter)
	treeChosen                         // tail the session in-place (t)
	treeNewWorkspace                   // fresh session workspace in $PWD (n)
	treeQuit
	treeNone
)

// treeChoice is what the tree hands back: the picked session plus the folder cwd,
// session id, and repo the iTerm launcher / transcript reconstruction need.
type treeChoice struct {
	Result treeResult
	Path   string
	Cwd    string
	ID     string
	Repo   string
}

// runClaudeTree builds and runs the interactive tree. A treeNone result means the
// tree was empty or no tty was available (caller falls back to discovery);
// treeQuit means the user aborted (caller should exit, matching the old menu's
// `q`); treeChosen/treeResume carry the picked session.
func runClaudeTree(home, pwd string, days int, local, cloud bool, theme Theme) treeChoice {
	tree := buildSessionTree(home, pwd, days, time.Now().Unix(), local, cloud)
	if len(tree.Folders) == 0 {
		return treeChoice{Result: treeNone}
	}
	return runTreeTUI(home, tree, theme)
}

func runTreeTUI(home string, tree sessionTree, theme Theme) treeChoice {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return treeChoice{Result: treeNone}
	}
	defer tty.Close()
	saved, ok := setRaw(tty)
	if !ok {
		return treeChoice{Result: treeNone}
	}
	defer restoreCbreak(tty, saved)

	// Enter alt-screen + hide cursor. If this write fails the terminal never
	// switched, so bail before registering the restore defer (which would
	// otherwise send exit-alt-screen codes to a terminal that never entered it).
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return treeChoice{Result: treeNone}
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	ui := treeUI{Tree: tree, Theme: theme}
	ui.Rows = flattenRows(ui.Tree, "")
	ui.Cursor = initialCursor(ui)

	buf := make([]byte, 16)
	for {
		w, h := termSize(tty)
		ui.Width, ui.Height = w, h-2
		if ui.Height < 1 {
			ui.Height = 1
		}
		ui.clamp()
		io.WriteString(tty, renderTree(ui))

		n, err := tty.Read(buf)
		if err != nil || n == 0 {
			return treeChoice{Result: treeQuit} // read error / EOF (Ctrl-D) → abort
		}
		k, r := decodeKey(buf[:n])
		ui = updateTree(ui, k, r)
		if ui.Quit {
			return treeChoice{Result: treeQuit}
		}
		if ui.SummaryReq {
			ui.SummaryReq = false
			showInfo(tty, ui.Sel, home, theme)
			continue
		}
		if ui.NewWorkspace {
			return treeChoice{Result: treeNewWorkspace, Cwd: firstNonEmpty(ui.NewWorkspaceDir, ui.Tree.Pwd)}
		}
		if ui.Chosen != "" {
			res := treeChosen
			if ui.Workspace {
				res = treeWorkspace
			}
			return treeChoice{Result: res, Path: ui.Chosen, Cwd: ui.ChosenCwd, ID: ui.ChosenID, Repo: ui.ChosenRepo}
		}
	}
}

func termSize(tty *os.File) (int, int) {
	w, h, err := term.GetSize(int(tty.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// decodeKey classifies a raw input chunk into a key token (plus a rune for
// printable input). Letter keys return kRune; updateTree decides their meaning by
// mode, so 'q'/'j' navigate in normal mode but type into the filter.
func decodeKey(b []byte) (treeKey, rune) {
	if len(b) == 0 {
		return kNone, 0
	}
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		switch b[2] {
		case 'A':
			return kUp, 0
		case 'B':
			return kDown, 0
		case 'C':
			return kRight, 0
		case 'D':
			return kLeft, 0
		case 'H', '1', '7':
			return kHome, 0
		case 'F', '4', '8':
			return kEnd, 0
		case '5':
			return kPageUp, 0 // PgUp: ESC [ 5 ~
		case '6':
			return kPageDown, 0 // PgDn: ESC [ 6 ~
		}
		return kNone, 0
	}
	if len(b) == 1 {
		switch b[0] {
		case 0x1b:
			return kEsc, 0
		case '\r', '\n':
			return kEnter, 0
		case 0x7f, 0x08:
			return kBackspace, 0
		case 0x03:
			return kCtrlC, 0
		case 0x06: // Ctrl-F
			return kPageDown, 0
		case 0x02: // Ctrl-B
			return kPageUp, 0
		}
	}
	if r := []rune(string(b)); len(r) > 0 && r[0] >= 0x20 {
		return kRune, r[0]
	}
	return kNone, 0
}
