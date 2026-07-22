package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSession writes a Claude session jsonl with the given lines and mtime,
// returning its path.
func writeSession(t *testing.T, dir, id string, lines []string, mtime int64) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(mtime, 0)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClassifyTier(t *testing.T) {
	now := int64(1_000_000)
	cases := []struct {
		name string
		mt   int64
		live bool
		want recencyTier
	}{
		{"live wins regardless of age", 0, true, tierLive},
		{"5 min ago", now - 300, false, tierRecentLive},
		{"1 hour ago", now - 3600, false, tierRecent},
		{"3 days ago", now - 3*86400, false, tierStale},
		{"exactly 15m boundary → recent", now - recentLiveWindow, false, tierRecent},
	}
	for _, c := range cases {
		if got := classifyTier(c.mt, now, c.live); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestLoadClaudeMeta(t *testing.T) {
	dir := t.TempDir()
	user := `{"type":"user","cwd":"/tmp/proj","gitBranch":"feat/x","message":{"content":"first prompt here"}}`
	asst := `{"type":"assistant","cwd":"/tmp/proj","gitBranch":"feat/x","message":{"content":[{"type":"text","text":"reply"}]}}`

	// summary wins over ai-title and first prompt.
	p := writeSession(t, dir, "a", []string{
		`{"type":"summary","summary":"THE SUMMARY"}`,
		`{"type":"ai-title","aiTitle":"the title"}`,
		user, asst,
	}, 100)
	snip, branch, msgs, cwd, _, _ := loadClaudeMeta(p)
	if snip != "THE SUMMARY" {
		t.Errorf("snippet = %q, want summary", snip)
	}
	if branch != "feat/x" {
		t.Errorf("branch = %q", branch)
	}
	if msgs < 1 {
		t.Errorf("msgs = %d, want >= 1 (head-bounded count)", msgs)
	}
	if cwd != "/tmp/proj" {
		t.Errorf("cwd = %q", cwd)
	}

	// no summary → ai-title.
	p = writeSession(t, dir, "b", []string{`{"type":"ai-title","aiTitle":"just a title"}`, user}, 100)
	if snip, _, _, _, _, _ := loadClaudeMeta(p); snip != "just a title" {
		t.Errorf("snippet = %q, want ai-title", snip)
	}

	// no summary/title → first user prompt.
	p = writeSession(t, dir, "c", []string{user, asst}, 100)
	if snip, _, _, _, _, _ := loadClaudeMeta(p); snip != "first prompt here" {
		t.Errorf("snippet = %q, want first prompt", snip)
	}

	// the newest last-prompt (current message) wins over summary/title/first prompt,
	// even though it lives at the tail and an earlier last-prompt is stale.
	p = writeSession(t, dir, "d", []string{
		`{"type":"summary","summary":"THE SUMMARY"}`,
		`{"type":"ai-title","aiTitle":"the title"}`,
		`{"type":"last-prompt","lastPrompt":"stale opening prompt"}`,
		user, asst,
		`{"type":"last-prompt","lastPrompt":"what I asked most recently"}`,
	}, 100)
	if snip, _, _, _, _, _ := loadClaudeMeta(p); snip != "what I asked most recently" {
		t.Errorf("snippet = %q, want newest last-prompt", snip)
	}

	// ai-title is reached as a fallback even when the first user event (which sets
	// cwd/branch) precedes it — the early-out must not stop on firstUser alone.
	p = writeSession(t, dir, "e", []string{user, asst, `{"type":"ai-title","aiTitle":"late title"}`}, 100)
	if snip, _, _, _, _, _ := loadClaudeMeta(p); snip != "late title" {
		t.Errorf("snippet = %q, want late title (early-out must not skip it)", snip)
	}

	// branch reflects where the session ENDED, not where it started — a session
	// can hop branches (e.g. main → a worktree), so the tail branch wins.
	p = writeSession(t, dir, "f", []string{
		`{"type":"user","cwd":"/tmp/proj","gitBranch":"main","message":{"content":"start"}}`,
		`{"type":"assistant","cwd":"/tmp/proj","gitBranch":"main","message":{"content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"user","cwd":"/tmp/proj","gitBranch":"feature/wt","message":{"content":"end"}}`,
	}, 100)
	if _, br, _, _, _, _ := loadClaudeMeta(p); br != "feature/wt" {
		t.Errorf("branch = %q, want end branch feature/wt", br)
	}

	// the newest pr-link (number + url) is picked up from the tail, even when a
	// stale earlier one exists and more chatter follows the PR.
	p = writeSession(t, dir, "g", []string{
		user,
		`{"type":"pr-link","prNumber":21,"prUrl":"https://github.com/o/r/pull/21"}`,
		asst,
		`{"type":"pr-link","prNumber":22,"prUrl":"https://github.com/o/r/pull/22"}`,
		`{"type":"last-prompt","lastPrompt":"after the PR"}`,
	}, 100)
	if _, _, _, _, prNum, prURL := loadClaudeMeta(p); prNum != 22 || prURL != "https://github.com/o/r/pull/22" {
		t.Errorf("pr = %d %q, want 22 + pull/22 url", prNum, prURL)
	}
}

func TestPrCellAndRowSurvivesTruncation(t *testing.T) {
	// No PR → the column is still reserved (all spaces) so branches line up.
	if got := prCell(treeSession{}); got != strings.Repeat(" ", prColWidth) {
		t.Errorf("prCell(no PR) = %q, want %d spaces", got, prColWidth)
	}
	// With a URL → an OSC-8 hyperlink wrapping #22, right-aligned in the column.
	s := treeSession{ID: "aaaa1111", Mtime: 900, Snippet: "do the thing", PrNumber: 22, PrURL: "https://github.com/o/r/pull/22"}
	cell := prCell(s)
	if want := osc8(s.PrURL, "#22"); !strings.Contains(cell, want) {
		t.Errorf("prCell = %q, want it to contain the OSC-8 link %q", cell, want)
	}
	if vis := len([]rune(stripANSI(cell))); vis != prColWidth {
		t.Errorf("prCell visible width = %d, want %d (right-aligned)", vis, prColWidth)
	}
	if !strings.HasPrefix(stripANSI(cell), "   #22") {
		t.Errorf("prCell should right-align: visible %q", stripANSI(cell))
	}
	// The PR cell sits before the branch in the row.
	row := stripANSI(composeSessionRow(treeSession{PrNumber: 22, PrURL: s.PrURL, Branch: "feat/x", Snippet: "x"}, 1000))
	if strings.Index(row, "#22") >= strings.Index(row, "[feat/x]") {
		t.Errorf("PR number should precede the branch: %q", row)
	}

	// styleRow truncates with an OSC-8-aware truncator: the hyperlink's escape
	// sequence must survive intact (both its opening and closing OSC-8 markers
	// present), never sliced mid-escape. Width 40 cuts into the snippet, well
	// after the link.
	styled := styleRow(composeSessionRow(s, 1000), tierRecent, false, 40)
	openMark := "\x1b]8;;" + s.PrURL + "\x1b\\"
	closeMark := "\x1b]8;;\x1b\\"
	if !strings.Contains(styled, openMark) || !strings.Contains(styled, closeMark) {
		t.Errorf("truncated row lost an intact OSC-8 hyperlink:\n%q", styled)
	}
	if vis := len([]rune(stripANSI(styled))); vis > 40 {
		t.Errorf("visible width = %d, want <= 40", vis)
	}
}

func TestBuildClaudeTree(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "projects")
	now := int64(10_000_000)

	// A recent folder with two sessions.
	cwdA := "/work/alpha"
	dirA := filepath.Join(root, claudeSlug(cwdA))
	writeSession(t, dirA, "a1", []string{
		`{"type":"summary","summary":"alpha newest"}`,
		`{"type":"user","cwd":"/work/alpha","gitBranch":"main","message":{"content":"hi"}}`,
	}, now-100)
	writeSession(t, dirA, "a2", []string{
		`{"type":"user","cwd":"/work/alpha","gitBranch":"main","message":{"content":"older one"}}`,
	}, now-5000)

	// A cold folder well outside the 7-day window (both dir + file backdated).
	cwdC := "/work/cold"
	dirC := filepath.Join(root, claudeSlug(cwdC))
	writeSession(t, dirC, "c1", []string{
		`{"type":"user","cwd":"/work/cold","gitBranch":"main","message":{"content":"ancient"}}`,
	}, now-100*86400)
	old := time.Unix(now-100*86400, 0)
	os.Chtimes(dirC, old, old)

	tree := buildClaudeTree(home, cwdA, 7, now, nil)

	if len(tree.Folders) != 1 {
		t.Fatalf("want 1 folder (cold dropped by window), got %d", len(tree.Folders))
	}
	f := tree.Folders[0]
	if f.Cwd != cwdA {
		t.Errorf("cwd = %q, want %q", f.Cwd, cwdA)
	}
	if !f.Expanded {
		t.Error("pwd folder should start expanded")
	}
	if len(f.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(f.Sessions))
	}
	if f.Sessions[0].Snippet != "alpha newest" {
		t.Errorf("newest snippet = %q", f.Sessions[0].Snippet)
	}
	if f.Sessions[0].Mtime < f.Sessions[1].Mtime {
		t.Error("sessions should be newest-first")
	}

	// Live union: mark the cold folder's cwd live → it's force-kept despite age.
	tree = buildClaudeTree(home, cwdA, 7, now, map[string]int{cwdC: 1})
	var sawCold bool
	for _, folder := range tree.Folders {
		if folder.Cwd == cwdC {
			sawCold = true
			if folder.Live != 1 {
				t.Errorf("cold folder Live = %d, want 1", folder.Live)
			}
		}
	}
	if !sawCold {
		t.Error("live cwd should be force-kept even when stale")
	}
	// Live folders sort ahead of non-live.
	if tree.Folders[0].Cwd != cwdC {
		t.Errorf("live folder should sort first, got %q", tree.Folders[0].Cwd)
	}
}

func TestBuildClaudeTreeUncapped(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "projects")
	now := int64(10_000_000)
	cwd := "/work/ancient"
	dir := filepath.Join(root, claudeSlug(cwd))
	writeSession(t, dir, "x", []string{
		`{"type":"user","cwd":"/work/ancient","gitBranch":"main","message":{"content":"old"}}`,
	}, now-100*86400)
	old := time.Unix(now-100*86400, 0)
	os.Chtimes(dir, old, old)

	if got := buildClaudeTree(home, "/elsewhere", 0, now, nil); len(got.Folders) != 1 {
		t.Fatalf("days=0 (uncapped) should keep the ancient folder, got %d folders", len(got.Folders))
	}
}

func sampleTree() sessionTree {
	return sessionTree{
		Now:  1000,
		Home: "/home/me",
		Pwd:  "/home/me/a",
		Folders: []treeFolder{
			{Cwd: "/home/me/a", Slug: claudeSlug("/home/me/a"), Mtime: 900, Expanded: true, Sessions: []treeSession{
				{ID: "aaaa1111", Mtime: 900, Snippet: "add login form", Branch: "main"},
				{ID: "bbbb2222", Mtime: 800, Snippet: "fix logout bug", Branch: "hotfix"},
			}},
			{Cwd: "/home/me/b", Slug: claudeSlug("/home/me/b"), Mtime: 700, Expanded: false, Sessions: []treeSession{
				{ID: "cccc3333", Mtime: 700, Snippet: "refactor parser", Branch: "main"},
			}},
		},
	}
}

func TestFlattenRows(t *testing.T) {
	tr := sampleTree()

	// No filter: folder A expanded (header + 2 sessions), folder B collapsed (header only).
	rows := flattenRows(tr, "")
	if len(rows) != 4 {
		t.Fatalf("want 4 rows, got %d", len(rows))
	}
	if rows[0].Session != -1 || rows[1].Session != 0 || rows[2].Session != 1 || rows[3].Session != -1 {
		t.Errorf("unexpected row shape: %+v", rows)
	}

	// Filter matching a session snippet in the collapsed folder → folder force-expands.
	rows = flattenRows(tr, "parser")
	if len(rows) != 2 || rows[0].Folder != 1 || rows[1].Session != 0 {
		t.Errorf("filter 'parser': %+v", rows)
	}

	// Filter matching a folder path keeps its whole subtree.
	rows = flattenRows(tr, "me/b")
	if len(rows) != 2 {
		t.Errorf("filter 'me/b' should show folder b + its session, got %d rows", len(rows))
	}

	// Filter matching nothing → empty.
	if rows = flattenRows(tr, "zzzzz"); len(rows) != 0 {
		t.Errorf("no-match filter should be empty, got %d", len(rows))
	}
}

func TestUpdateTreeNavigation(t *testing.T) {
	ui := treeUI{Tree: sampleTree(), Height: 20}
	ui.Rows = flattenRows(ui.Tree, "")

	ui = updateTree(ui, kDown, 0)
	if ui.Cursor != 1 {
		t.Errorf("down → cursor %d, want 1", ui.Cursor)
	}
	ui = updateTree(ui, kUp, 0)
	ui = updateTree(ui, kUp, 0) // clamp at 0
	if ui.Cursor != 0 {
		t.Errorf("up clamps to 0, got %d", ui.Cursor)
	}

	// Collapse folder A from a session row → folder collapses, cursor returns to header.
	ui = treeUI{Tree: sampleTree(), Height: 20}
	ui.Rows = flattenRows(ui.Tree, "")
	ui.Cursor = 2 // second session of folder A
	ui = updateTree(ui, kLeft, 0)
	if ui.Tree.Folders[0].Expanded {
		t.Error("left should collapse folder A")
	}
	if r, _ := ui.current(); r.Session != -1 {
		t.Error("cursor should land on the folder header after collapse")
	}

	// 'q' quits.
	ui = updateTree(ui, kRune, 'q')
	if !ui.Quit {
		t.Error("q should quit")
	}
}

func TestUpdateTreePaging(t *testing.T) {
	ui := treeUI{Height: 10}
	ui.Rows = make([]treeRow, 30) // step = Height-1 = 9

	ui = updateTree(ui, kPageDown, 0)
	if ui.Cursor != 9 {
		t.Errorf("page down → %d, want 9", ui.Cursor)
	}
	ui = updateTree(ui, kPageDown, 0)
	if ui.Cursor != 18 {
		t.Errorf("page down again → %d, want 18", ui.Cursor)
	}
	ui = updateTree(ui, kPageUp, 0)
	if ui.Cursor != 9 {
		t.Errorf("page up → %d, want 9", ui.Cursor)
	}
	// Clamp at both ends.
	ui.Cursor = 2
	if ui = updateTree(ui, kPageUp, 0); ui.Cursor != 0 {
		t.Errorf("page up near top → %d, want 0", ui.Cursor)
	}
	ui.Cursor = 28
	if ui = updateTree(ui, kPageDown, 0); ui.Cursor != 29 {
		t.Errorf("page down near bottom → %d, want 29", ui.Cursor)
	}
	// Space pages down too.
	ui.Cursor = 0
	if ui = updateTree(ui, kRune, ' '); ui.Cursor != 9 {
		t.Errorf("space → %d, want 9", ui.Cursor)
	}
}

func TestUpdateTreeEnterWorkspace(t *testing.T) {
	tr := sampleTree()
	tr.Folders[0].Sessions[0].Path = "/sessions/aaaa1111.jsonl"
	ui := treeUI{Tree: tr, Height: 20}
	ui.Rows = flattenRows(ui.Tree, "")
	ui.Cursor = 1 // first session of folder A

	// Enter → open the iTerm workspace: carries path + cwd + id, Workspace set.
	ui = updateTree(ui, kEnter, 0)
	if ui.Chosen != "/sessions/aaaa1111.jsonl" || !ui.Workspace {
		t.Errorf("Enter should select+workspace: chosen=%q workspace=%v", ui.Chosen, ui.Workspace)
	}
	if ui.ChosenCwd != "/home/me/a" || ui.ChosenID != "aaaa1111" {
		t.Errorf("workspace needs cwd+id: cwd=%q id=%q", ui.ChosenCwd, ui.ChosenID)
	}
}

func TestUpdateTreeNewWorkspace(t *testing.T) {
	tr := sampleTree()
	tr.Folders[0].Dir = "/work/alpha"
	ui := treeUI{Tree: tr, Height: 20}
	ui.Rows = flattenRows(ui.Tree, "")

	// 'n' on folder A's header → new workspace in that folder's dir.
	ui.Cursor = 0
	ui = updateTree(ui, kRune, 'n')
	if !ui.NewWorkspace || ui.NewWorkspaceDir != "/work/alpha" {
		t.Errorf("'n' on folder → new=%v dir=%q, want /work/alpha", ui.NewWorkspace, ui.NewWorkspaceDir)
	}

	// 'n' on a folder with no local dir → empty (driver falls back to $PWD).
	ui2 := treeUI{Tree: sampleTree(), Height: 20} // folders have no Dir
	ui2.Rows = flattenRows(ui2.Tree, "")
	ui2.Cursor = 0
	ui2 = updateTree(ui2, kRune, 'n')
	if !ui2.NewWorkspace || ui2.NewWorkspaceDir != "" {
		t.Errorf("'n' with no dir → dir=%q, want empty", ui2.NewWorkspaceDir)
	}
}

func TestUpdateTreeTailInPlace(t *testing.T) {
	tr := sampleTree()
	tr.Folders[0].Sessions[0].Path = "/sessions/aaaa1111.jsonl"
	ui := treeUI{Tree: tr, Height: 20}
	ui.Rows = flattenRows(ui.Tree, "")
	ui.Cursor = 1 // first session

	// 't' → tail in-place: chosen set, but Workspace stays false.
	ui = updateTree(ui, kRune, 't')
	if ui.Chosen != "/sessions/aaaa1111.jsonl" || ui.Workspace {
		t.Errorf("'t' should tail in-place: chosen=%q workspace=%v", ui.Chosen, ui.Workspace)
	}

	// Enter on a folder header starts a fresh session workspace (same as `n`).
	ui2 := sampleTree()
	ui2.Folders[0].Dir = "/work/alpha"
	uiA := treeUI{Tree: ui2, Height: 20}
	uiA.Rows = flattenRows(uiA.Tree, "")
	uiA.Cursor = 0 // folder header
	uiA = updateTree(uiA, kEnter, 0)
	if uiA.Chosen != "" || uiA.Workspace {
		t.Errorf("Enter on folder should not select a session: chosen=%q", uiA.Chosen)
	}
	if !uiA.NewWorkspace || uiA.NewWorkspaceDir != "/work/alpha" {
		t.Errorf("Enter on folder → new=%v dir=%q, want /work/alpha", uiA.NewWorkspace, uiA.NewWorkspaceDir)
	}
}

func TestUpdateTreeFilterTyping(t *testing.T) {
	ui := treeUI{Tree: sampleTree(), Height: 20}
	ui.Rows = flattenRows(ui.Tree, "")

	ui = updateTree(ui, kRune, '/') // enter filter mode
	if !ui.Filtering {
		t.Fatal("'/' should start filtering")
	}
	for _, r := range "parser" {
		ui = updateTree(ui, kRune, r)
	}
	if ui.Filter != "parser" {
		t.Errorf("filter = %q", ui.Filter)
	}
	// 'q' while filtering is literal text, not quit.
	ui = updateTree(ui, kRune, 'q')
	if ui.Quit || ui.Filter != "parserq" {
		t.Errorf("q in filter should type, got filter=%q quit=%v", ui.Filter, ui.Quit)
	}
	ui = updateTree(ui, kBackspace, 0)
	if ui.Filter != "parser" {
		t.Errorf("backspace → %q", ui.Filter)
	}
	ui = updateTree(ui, kEsc, 0)
	if ui.Filtering {
		t.Error("Esc should leave filter mode")
	}
}

func TestDecodeKey(t *testing.T) {
	cases := []struct {
		in   []byte
		want treeKey
		r    rune
	}{
		{[]byte{0x1b, '[', 'A'}, kUp, 0},
		{[]byte{0x1b, '[', 'B'}, kDown, 0},
		{[]byte{0x1b, '[', 'C'}, kRight, 0},
		{[]byte{0x1b, '[', 'D'}, kLeft, 0},
		{[]byte{0x1b, '[', '5', '~'}, kPageUp, 0},
		{[]byte{0x1b, '[', '6', '~'}, kPageDown, 0},
		{[]byte{0x06}, kPageDown, 0}, // Ctrl-F
		{[]byte{0x02}, kPageUp, 0},   // Ctrl-B
		{[]byte{'\r'}, kEnter, 0},
		{[]byte{'\n'}, kEnter, 0},
		{[]byte{0x7f}, kBackspace, 0},
		{[]byte{0x03}, kCtrlC, 0},
		{[]byte{0x1b}, kEsc, 0},
		{[]byte{'j'}, kRune, 'j'},
		{[]byte("é"), kRune, 'é'},
	}
	for _, c := range cases {
		k, r := decodeKey(c.in)
		if k != c.want || r != c.r {
			t.Errorf("decodeKey(%v) = (%v,%q), want (%v,%q)", c.in, k, r, c.want, c.r)
		}
	}
}

func TestTildify(t *testing.T) {
	cases := map[string]string{
		"/home/me/src/x": "~/src/x",
		"/home/me":       "~",
		"/other/place":   "/other/place",
		"/home/menu":     "/home/menu", // prefix but not a path boundary
	}
	for in, want := range cases {
		if got := tildify(in, "/home/me"); got != want {
			t.Errorf("tildify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("b7dd3e4a-103e-4737"); got != "b7dd3e4a" {
		t.Errorf("got %q", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("short id unchanged: %q", got)
	}
}

func TestRenderListFormat(t *testing.T) {
	tr := sampleTree()
	tr.Folders[0].Live = 1
	var b strings.Builder
	renderList(&b, tr, false)
	out := b.String()
	for _, want := range []string{"~/a", "● live", "aaaa1111", "add login form", "[hotfix]", "~/b", "refactor parser"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestComposeFolderRowEmpty(t *testing.T) {
	// The injected current-dir group (no sessions) shows a fixed ▸ and an n hint.
	row := composeFolderRow(treeFolder{Cwd: "/home/me/here", Dir: "/home/me/here"}, "/home/me", 1000)
	if !strings.Contains(row, "~/here") || !strings.Contains(row, "no sessions") || !strings.Contains(row, "n to start") {
		t.Errorf("empty folder row = %q", row)
	}
}

func TestRenderTreeFrame(t *testing.T) {
	ui := treeUI{Tree: sampleTree(), Width: 100, Height: 20}
	ui.Rows = flattenRows(ui.Tree, "")
	frame := stripANSI(renderTree(ui))
	for _, want := range []string{"CLAUDE SESSIONS", "~/a", "add login form", "2 folders", "3 sessions"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}
}

func TestResolveDays(t *testing.T) {
	cases := []struct {
		in   string
		def  int
		want int
		err  bool
	}{
		{"", 7, 7, false},
		{"", 0, 0, false},
		{"14", 7, 14, false},
		{"all", 7, 0, false},
		{"0", 7, 0, false},
		{"-3", 7, 0, true},
		{"nope", 7, 0, true},
	}
	for _, c := range cases {
		got, err := resolveDays(c.in, c.def)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("resolveDays(%q,%d) = (%d,%v), want (%d,err=%v)", c.in, c.def, got, err, c.want, c.err)
		}
	}
}
