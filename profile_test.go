package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mkPersonal creates the personal account's projects root under home, which is
// what makes claudeProfiles report a second profile at all.
func mkPersonal(t *testing.T, home string) string {
	t.Helper()
	root := filepath.Join(home, personalConfigDir, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestClaudeProfilesOnlyDefaultWithoutPersonalDir(t *testing.T) {
	home := t.TempDir()
	got := claudeProfiles(home)
	if len(got) != 1 {
		t.Fatalf("want 1 profile on a machine with no second account, got %d: %+v", len(got), got)
	}
	if got[0].Name != "" || got[0].Dir != filepath.Join(home, ".claude") {
		t.Errorf("default profile = %+v", got[0])
	}
	// A ~/.claude-personal that exists but holds no projects/ isn't an account
	// (a half-finished setup, a stray dir) and must not add a root to scan.
	os.MkdirAll(filepath.Join(home, personalConfigDir), 0o755)
	if got := claudeProfiles(home); len(got) != 1 {
		t.Errorf("dir without projects/ counted as a profile: %+v", got)
	}
}

func TestClaudeProfilesDefaultFirst(t *testing.T) {
	home := t.TempDir()
	mkPersonal(t, home)
	got := claudeProfiles(home)
	if len(got) != 2 {
		t.Fatalf("want 2 profiles, got %d: %+v", len(got), got)
	}
	if got[0].Name != "" {
		t.Errorf("default account must come first (it wins ties), got %q", got[0].Name)
	}
	if got[1].Name != personalProfile {
		t.Errorf("second profile = %q, want %q", got[1].Name, personalProfile)
	}
	if want := filepath.Join(home, personalConfigDir, "projects"); got[1].projects() != want {
		t.Errorf("personal projects = %q, want %q", got[1].projects(), want)
	}
}

func TestProfileForPath(t *testing.T) {
	home := t.TempDir()
	mkPersonal(t, home)
	cases := map[string]string{
		filepath.Join(home, ".claude", "projects", "-work-x", "a.jsonl"):           "",
		filepath.Join(home, personalConfigDir, "projects", "-work-x", "a.jsonl"):   personalProfile,
		filepath.Join(home, "elsewhere", "a.jsonl"):                                "",
		filepath.Join(home, ".claude-personal-backup", "projects", "p", "a.jsonl"): "",
	}
	for path, want := range cases {
		if got := profileForPath(home, path); got != want {
			t.Errorf("profileForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestProfileByNameFallsBackToDefault(t *testing.T) {
	home := t.TempDir()
	// An unknown name must degrade to the work account, never to a broken launch.
	if p := profileByName(home, "nope"); p.Name != "" || p.Dir != filepath.Join(home, ".claude") {
		t.Errorf("unknown name = %+v, want the default profile", p)
	}
	if p := profileByName(home, personalProfile); p.Name != "" {
		t.Errorf("personal resolved on a machine without the dir: %+v", p)
	}
	mkPersonal(t, home)
	if p := profileByName(home, personalProfile); p.Name != personalProfile {
		t.Errorf("personal not resolved once its dir exists: %+v", p)
	}
}

func TestProjectsRootOf(t *testing.T) {
	want := "/home/me/.claude-personal/projects"
	if got := projectsRootOf(want + "/-work-x/abc.jsonl"); got != want {
		t.Errorf("projectsRootOf = %q, want %q", got, want)
	}
}

// ── the pink @ ───────────────────────────────────────────────────────────────

func TestProfileMarkIsAFixedWidthCell(t *testing.T) {
	// Both must occupy exactly two visible columns, or ids stop lining up in a
	// folder holding both accounts.
	for _, prof := range []string{"", personalProfile, mixedProfile} {
		got := profileMark(prof, tierColor(tierRecent))
		if vis := len([]rune(stripANSI(got))); vis != 2 {
			t.Errorf("profileMark(%q) visible width = %d, want 2 (%q)", prof, vis, got)
		}
	}
	if got := profileMark("", "x"); got != "  " {
		t.Errorf("default account marked: %q", got)
	}
	if got := profileMark(personalProfile, ""); got != "@ " {
		t.Errorf("uncolored personal mark = %q, want %q", got, "@ ")
	}
}

func TestProfileMarkRestoresRowColor(t *testing.T) {
	restore := tierColor(tierRecent)
	got := profileMark(personalProfile, restore)
	if !strings.HasPrefix(got, pinkANSI) {
		t.Errorf("mark doesn't start pink: %q", got)
	}
	// Without handing the row's color back, everything after the @ would render
	// pink to end-of-line.
	if !strings.Contains(got, "@"+restore) {
		t.Errorf("mark doesn't restore the row color after the @: %q", got)
	}
}

func TestProfileTagOmitsTheCellForWorkFolders(t *testing.T) {
	if got := profileTag("", tierColor(tierRecent)); got != "" {
		t.Errorf("work folder got a tag %q — folder rows must be unchanged", got)
	}
	if got := stripANSI(profileTag(personalProfile, tierColor(tierRecent))); got != "@ " {
		t.Errorf("personal folder tag = %q, want %q", got, "@ ")
	}
}

func TestFolderProfile(t *testing.T) {
	work := treeSession{}
	pers := treeSession{Profile: personalProfile}
	cases := []struct {
		name string
		in   []treeSession
		want string
	}{
		{"all work", []treeSession{work, work}, ""},
		{"all personal", []treeSession{pers, pers}, personalProfile},
		{"mixed", []treeSession{work, pers}, mixedProfile},
		{"empty", nil, ""},
	}
	for _, c := range cases {
		if got := folderProfile(c.in); got != c.want {
			t.Errorf("%s: folderProfile = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestComposeSessionRowKeepsColumnsAligned(t *testing.T) {
	now := int64(1000)
	work := stripANSI(composeSessionRow(treeSession{ID: "aaaabbbb", Mtime: now, Snippet: "x"}, now, ""))
	pers := stripANSI(composeSessionRow(treeSession{ID: "aaaabbbb", Mtime: now, Snippet: "x", Profile: personalProfile}, now, ""))
	if len(work) != len(pers) {
		t.Errorf("rows misalign:\nwork %q (%d)\npers %q (%d)", work, len(work), pers, len(pers))
	}
	if !strings.Contains(pers, "@ aaaabbbb") {
		t.Errorf("personal row missing the @ before the id: %q", pers)
	}
	if strings.Contains(work, "@") {
		t.Errorf("work row carries an @: %q", work)
	}
}

func TestRenderListMarksPersonal(t *testing.T) {
	now := int64(1000)
	tree := sessionTree{Now: now, Home: "/home/me", Folders: []treeFolder{{
		Cwd:   "/work/mine",
		Mtime: now,
		Sessions: []treeSession{
			{ID: "pppppppp", Mtime: now, Profile: personalProfile, Snippet: "personal one"},
			{ID: "wwwwwwww", Mtime: now, Snippet: "work one"},
		},
	}}}
	var b bytes.Buffer
	renderList(&b, tree, false) // piped: no color, but the @ must still print
	out := b.String()
	if strings.Contains(out, "\x1b") {
		t.Errorf("uncolored --list emitted escapes:\n%q", out)
	}
	if !strings.Contains(out, "@ pppppppp") {
		t.Errorf("personal session unmarked in --list:\n%s", out)
	}
	if strings.Contains(out, "@ wwwwwwww") {
		t.Errorf("work session marked in --list:\n%s", out)
	}
	// The folder holds both accounts, so its header is tagged mixed, not personal
	// — a collapsed group must not claim to be wholly personal.
	if !strings.Contains(out, "@ /work/mine") {
		t.Errorf("mixed folder header unmarked:\n%s", out)
	}
}

func TestSummaryCardNamesTheAccount(t *testing.T) {
	now := int64(1000)
	pers := strings.Join(summaryCardLines(treeSession{ID: "x", Profile: personalProfile}, aiSummary{}, false, nil, now), "\n")
	if !strings.Contains(pers, "account    @ personal") {
		t.Errorf("card omits the account:\n%s", pers)
	}
	work := strings.Join(summaryCardLines(treeSession{ID: "x"}, aiSummary{}, false, nil, now), "\n")
	if strings.Contains(work, "account") {
		t.Errorf("card names the default account (noise for everyone with one):\n%s", work)
	}
}

// ── the merged tree ──────────────────────────────────────────────────────────

func TestBuildClaudeTreeMergesAccountsIntoOneFolder(t *testing.T) {
	home := t.TempDir()
	now := int64(10_000_000)
	cwd := "/work/shared"
	slug := claudeSlug(cwd)

	writeSession(t, filepath.Join(home, ".claude", "projects", slug), "w1", []string{
		`{"type":"user","cwd":"/work/shared","gitBranch":"main","message":{"content":"work turn"}}`,
	}, now-500)
	writeSession(t, filepath.Join(home, personalConfigDir, "projects", slug), "p1", []string{
		`{"type":"user","cwd":"/work/shared","gitBranch":"main","message":{"content":"personal turn"}}`,
	}, now-100)

	tree := buildClaudeTree(home, cwd, 7, now, nil)
	if len(tree.Folders) != 1 {
		t.Fatalf("want 1 merged folder, got %d: %+v", len(tree.Folders), tree.Folders)
	}
	f := tree.Folders[0]
	if len(f.Sessions) != 2 {
		t.Fatalf("want both accounts' sessions, got %d", len(f.Sessions))
	}
	// Pooled sessions must be re-sorted: the personal one is newer, so it leads —
	// and folder.Mtime is read off sessions[0].
	if f.Sessions[0].ID != "p1" || f.Sessions[0].Profile != personalProfile {
		t.Errorf("newest session = %q/%q, want p1/personal", f.Sessions[0].ID, f.Sessions[0].Profile)
	}
	if f.Sessions[1].ID != "w1" || f.Sessions[1].Profile != "" {
		t.Errorf("second session = %q/%q, want w1/default", f.Sessions[1].ID, f.Sessions[1].Profile)
	}
	if f.Mtime != now-100 {
		t.Errorf("folder mtime = %d, want the newest session's %d", f.Mtime, now-100)
	}
	if got := folderProfile(f.Sessions); got != mixedProfile {
		t.Errorf("folder profile = %q, want %q", got, mixedProfile)
	}
}

func TestBuildClaudeTreeSeparateCwdsStaySeparate(t *testing.T) {
	home := t.TempDir()
	now := int64(10_000_000)
	writeSession(t, filepath.Join(home, ".claude", "projects", claudeSlug("/work/a")), "w1", []string{
		`{"type":"user","cwd":"/work/a","message":{"content":"work"}}`,
	}, now-100)
	writeSession(t, filepath.Join(home, personalConfigDir, "projects", claudeSlug("/play/b")), "p1", []string{
		`{"type":"user","cwd":"/play/b","message":{"content":"play"}}`,
	}, now-100)

	tree := buildClaudeTree(home, "/work/a", 7, now, nil)
	if len(tree.Folders) != 2 {
		t.Fatalf("want 2 folders, got %d", len(tree.Folders))
	}
	byCwd := map[string]treeFolder{}
	for _, f := range tree.Folders {
		byCwd[f.Cwd] = f
	}
	if got := folderProfile(byCwd["/play/b"].Sessions); got != personalProfile {
		t.Errorf("personal-only folder = %q, want %q", got, personalProfile)
	}
	if got := folderProfile(byCwd["/work/a"].Sessions); got != "" {
		t.Errorf("work-only folder = %q, want the default account", got)
	}
}

// ── cross-root resolution ────────────────────────────────────────────────────

func TestFindSessionClaudeFindsPersonalSessions(t *testing.T) {
	home := t.TempDir()
	base := time.Now()
	dir := filepath.Join(home, personalConfigDir, "projects", claudeSlug("/work/only-personal"))
	os.MkdirAll(dir, 0o755)
	want := filepath.Join(dir, "p.jsonl")
	os.WriteFile(want, []byte("{}"), 0o644)
	os.Chtimes(want, base, base)

	if got := findSessionClaude(home, "/work/only-personal"); got != want {
		t.Errorf("got %q, want the personal session %q", got, want)
	}
}

func TestFindSessionClaudeExactCwdBeatsOtherRootsSameTree(t *testing.T) {
	home := t.TempDir()
	base := time.Now()
	pwd := "/work/proj/sub"

	// Work root has only an ANCESTOR of pwd — a same-tree guess…
	anc := filepath.Join(home, ".claude", "projects", claudeSlug("/work/proj"))
	os.MkdirAll(anc, 0o755)
	ancFile := filepath.Join(anc, "w.jsonl")
	os.WriteFile(ancFile, []byte("{}"), 0o644)
	newer := base.Add(time.Hour)
	os.Chtimes(ancFile, newer, newer) // …and it is NEWER.

	// Personal root has the exact cwd. The stronger tier must win regardless.
	exact := filepath.Join(home, personalConfigDir, "projects", claudeSlug(pwd))
	os.MkdirAll(exact, 0o755)
	want := filepath.Join(exact, "p.jsonl")
	os.WriteFile(want, []byte("{}"), 0o644)
	os.Chtimes(want, base, base)

	if got := findSessionClaude(home, pwd); got != want {
		t.Errorf("got %q, want the exact-cwd personal session %q", got, want)
	}
}

func TestFindSessionClaudeTiesGoToDefaultAccount(t *testing.T) {
	home := t.TempDir()
	stamp := time.Unix(1_700_000_000, 0)
	pwd := "/work/tie"
	var want string
	for i, root := range []string{filepath.Join(home, ".claude", "projects"), filepath.Join(home, personalConfigDir, "projects")} {
		dir := filepath.Join(root, claudeSlug(pwd))
		os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, "s.jsonl")
		os.WriteFile(p, []byte("{}"), 0o644)
		os.Chtimes(p, stamp, stamp)
		if i == 0 {
			want = p
		}
	}
	if got := findSessionClaude(home, pwd); got != want {
		t.Errorf("tie resolved to %q, want the default account's %q", got, want)
	}
}

func TestDetectAgentForFileClaimsPersonalTranscripts(t *testing.T) {
	home := t.TempDir()
	mkPersonal(t, home)
	p := filepath.Join(home, personalConfigDir, "projects", "-work-x", "a.jsonl")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"type":"user","message":{"content":"hi"}}`), 0o644)
	if got := detectAgentForFile(home, p); got != AgentClaude {
		t.Errorf("detectAgentForFile = %v, want AgentClaude", got)
	}
}

func TestResolveClaudeSessionFindsPersonalByID(t *testing.T) {
	home := t.TempDir()
	cwd := "/work/adopted"
	dir := filepath.Join(home, personalConfigDir, "projects", claudeSlug(cwd))
	os.MkdirAll(dir, 0o755)
	id := "11111111-2222-4333-8444-555555555555"
	want := filepath.Join(dir, id+".jsonl")
	os.WriteFile(want, []byte("{}"), 0o644)

	if got := resolveClaudeSession(home, cwd, "claude --resume "+id); got != want {
		t.Errorf("got %q, want the personal transcript %q", got, want)
	}
}

func TestLocalPathForIDSearchesBothRoots(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, personalConfigDir, "projects", "-work-x")
	os.MkdirAll(dir, 0o755)
	want := filepath.Join(dir, "abc.jsonl")
	os.WriteFile(want, []byte("{}"), 0o644)
	if got := localPathForID(home, "abc"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ── filtering + keys ─────────────────────────────────────────────────────────

func TestSessionMatchesAccountFilter(t *testing.T) {
	pers := treeSession{ID: "aaa", Snippet: "an errand", Profile: personalProfile}
	work := treeSession{ID: "bbb", Snippet: "an errand"}

	if !sessionMatches(pers, "@") || sessionMatches(work, "@") {
		t.Error(`"@" must keep exactly the personal sessions`)
	}
	if !sessionMatches(pers, "@pers") || sessionMatches(work, "@pers") {
		t.Error(`"@pers" must keep exactly the personal sessions`)
	}
	if sessionMatches(pers, "@nope") {
		t.Error(`"@nope" is not a prefix of "personal" and must match nothing`)
	}
	// The account filter must not leak into ordinary text filters: "n" appears in
	// "personal", and substring-matching the word would drag every personal
	// session into every such search.
	if !sessionMatches(pers, "errand") || !sessionMatches(work, "errand") {
		t.Error("text filter stopped matching")
	}
	if sessionMatches(treeSession{ID: "ccc", Profile: personalProfile}, "n") {
		t.Error(`filter "n" matched a personal session with no textual hit`)
	}
}

func TestAtKeyStartsAPersonalWorkspace(t *testing.T) {
	ui := treeUI{Tree: sessionTree{Pwd: "/work/x", Folders: []treeFolder{{
		Cwd: "/work/x", Dir: "/work/x", Sessions: []treeSession{{ID: "a"}},
	}}}}
	ui.Rows = flattenRows(ui.Tree, "")

	got := updateTree(ui, kRune, '@')
	if !got.NewWorkspace || got.NewWorkspaceAcc != personalProfile {
		t.Errorf("@ → NewWorkspace=%v acc=%q, want true/%q", got.NewWorkspace, got.NewWorkspaceAcc, personalProfile)
	}
	// `n` must be untouched — it always meant "new session on this account".
	if got := updateTree(ui, kRune, 'n'); !got.NewWorkspace || got.NewWorkspaceAcc != "" {
		t.Errorf("n → acc=%q, want the default account", got.NewWorkspaceAcc)
	}
	// While filtering, @ is a literal.
	filtering := ui
	filtering.Filtering = true
	if got := updateTree(filtering, kRune, '@'); got.NewWorkspace || got.Filter != "@" {
		t.Errorf("@ while filtering: NewWorkspace=%v filter=%q", got.NewWorkspace, got.Filter)
	}
}

func TestSelectSessionCarriesTheAccount(t *testing.T) {
	ui := treeUI{Tree: sessionTree{Folders: []treeFolder{{
		Cwd: "/work/x", Expanded: true,
		Sessions: []treeSession{{ID: "a", Path: "/p/a.jsonl", Profile: personalProfile}},
	}}}}
	ui.Rows = flattenRows(ui.Tree, "")
	ui.Cursor = 1 // the session row
	ui.selectSession(true)
	if ui.ChosenAcc != personalProfile {
		t.Errorf("ChosenAcc = %q, want %q — a resume must use the owning account", ui.ChosenAcc, personalProfile)
	}
}

// ── launching under the right account ────────────────────────────────────────

func TestAccountEnvPrefix(t *testing.T) {
	if got := accountEnvPrefix(claudeProfile{Dir: "/home/me/.claude"}); got != "" {
		t.Errorf("default account emitted env %q — its launch must stay unchanged", got)
	}
	got := accountEnvPrefix(claudeProfile{Name: personalProfile, Dir: "/home/me/.claude-personal"})
	if !strings.Contains(got, "CLAUDE_CONFIG_DIR='/home/me/.claude-personal'") {
		t.Errorf("missing config dir: %q", got)
	}
	// The token must be a command substitution the SHELL runs, so it never lands
	// in argv or in our memory — which needs double quotes, not single.
	if !strings.Contains(got, `CLAUDE_CODE_OAUTH_TOKEN="$(security find-generic-password -s `+personalKeychainService+` -w)"`) {
		t.Errorf("token lookup not a double-quoted substitution: %q", got)
	}
	if !strings.HasSuffix(got, " ") {
		t.Errorf("prefix must end in a space to join the command: %q", got)
	}
}

func TestWorkspaceScriptsCarryTheAccountEnv(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	self := "/usr/local/bin/entire-tail"
	acct := accountEnvPrefix(claudeProfile{Name: personalProfile, Dir: "/home/me/.claude-personal"})

	for name, s := range map[string]string{
		"resume": workspaceScript("/work/proj", id, self, "claude", acct, ""),
		"fresh":  newWorkspaceScript("/work/proj", self, id, "claude", acct, ""),
	} {
		if !strings.Contains(s, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("%s: agent pane missing the account env:\n%s", name, s)
		}
		// Pane B is entire-tail, not an agent: it reads both roots and must not be
		// handed credentials it has no use for.
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, self) && strings.Contains(line, "CLAUDE_CONFIG_DIR=") {
				t.Errorf("%s: tail pane got the account env:\n%s", name, line)
			}
		}
	}
}

func TestWorkspaceScriptsUnchangedForDefaultAccount(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	self := "/usr/local/bin/entire-tail"
	for name, c := range map[string]struct{ script, wantAgent string }{
		"resume": {workspaceScript("/work/proj", id, self, "claude", "", ""), "cd '/work/proj' && 'claude' --resume '" + id + "'"},
		"fresh":  {newWorkspaceScript("/work/proj", self, id, "claude", "", ""), "cd '/work/proj' && 'claude' --session-id '" + id + "'"},
	} {
		if !strings.Contains(c.script, c.wantAgent) {
			t.Errorf("%s: default-account launch changed shape, want %q in:\n%s", name, c.wantAgent, c.script)
		}
	}
}
