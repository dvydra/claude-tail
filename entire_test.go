package main

import "testing"

func TestParseEntireTime(t *testing.T) {
	if got := parseEntireTime("2026-07-10T00:00:00Z"); got != 1783641600 {
		t.Errorf("RFC3339 → %d", got)
	}
	if got := parseEntireTime("not a time"); got != 0 {
		t.Errorf("bad time should be 0, got %d", got)
	}
}

func TestParseGitRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git":       "owner/repo",
		"https://github.com/owner/repo.git":   "owner/repo",
		"https://github.com/owner/repo":       "owner/repo",
		"ssh://git@github.com/owner/repo.git": "owner/repo",
		"git@host:deep/nested/owner/repo.git": "owner/repo", // last two segments
		"":                                    "",
		"nonsense":                            "",
	}
	for in, want := range cases {
		if got := parseGitRemote(in); got != want {
			t.Errorf("parseGitRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func folderByCwd(tr sessionTree, cwd string) *treeFolder {
	for i := range tr.Folders {
		if tr.Folders[i].Cwd == cwd {
			return &tr.Folders[i]
		}
	}
	return nil
}

func TestEnsureCurrentDirFolder(t *testing.T) {
	// Absent → injected with Dir=pwd and no sessions, so it can be `n`-ed.
	tr := sessionTree{Folders: []treeFolder{{Cwd: "/other", Slug: claudeSlug("/other"), Mtime: 100}}}
	ensureCurrentDirFolder(&tr, "/work/empty", 555)
	f := folderByCwd(tr, "/work/empty")
	if f == nil || f.Dir != "/work/empty" || len(f.Sessions) != 0 {
		t.Fatalf("current dir not injected: %+v", tr.Folders)
	}
	// Already present → not duplicated.
	before := len(tr.Folders)
	ensureCurrentDirFolder(&tr, "/work/empty", 999)
	if len(tr.Folders) != before {
		t.Errorf("duplicate injection: %d folders", len(tr.Folders))
	}
}

func TestMergeEntireShowsCurrentDir(t *testing.T) {
	// Even with no local sessions and no cloud data, the current directory's group
	// is present (and is the CurrentGroup the cursor starts on) so it can be `n`-ed.
	tree := mergeEntire(sessionTree{Pwd: "/work/here"}, nil, "/home/me", 0, 1000)
	f := folderByCwd(tree, "/work/here")
	if f == nil || f.Dir != "/work/here" {
		t.Fatalf("current dir group missing: %+v", tree.Folders)
	}
	if tree.CurrentGroup != "/work/here" {
		t.Errorf("CurrentGroup = %q, want /work/here", tree.CurrentGroup)
	}
}

func TestMergeEntire(t *testing.T) {
	now := parseEntireTime("2026-07-10T01:00:00Z")
	// Local base: two sessions in one folder — 'a' is tracked by entire, 'u' isn't.
	local := sessionTree{
		Pwd: "/p",
		Folders: []treeFolder{{
			Cwd: "/home/me/work/infra",
			Sessions: []treeSession{
				{ID: "a", Snippet: "raw local snippet", Mtime: 200, Path: "/c/a.jsonl"},
				{ID: "u", Snippet: "untracked one", Mtime: 190, Path: "/c/u.jsonl"},
			},
		}},
	}
	entire := []entireSession{
		{SessionID: "a", DisplayName: "Entire Title A", Repo: "org/infra", LastActivityAt: "2026-07-10T00:59:00Z"},
		{SessionID: "cloud", DisplayName: "Cloud Only", Repo: "org/infra", LastActivityAt: "2026-07-10T00:58:00Z"},
	}
	tree := mergeEntire(local, entire, "/home/me", 0, now)

	// 'a' regrouped under entire's repo, title overlaid, local path preserved.
	infra := folderByCwd(tree, "org/infra")
	if infra == nil {
		t.Fatal("no org/infra group")
	}
	ids := map[string]treeSession{}
	for _, s := range infra.Sessions {
		ids[s.ID] = s
	}
	if a, ok := ids["a"]; !ok || a.Snippet != "Entire Title A" || a.Path != "/c/a.jsonl" {
		t.Errorf("tracked local session a = %+v", a)
	}
	// cloud-only session appended, no local path.
	if c, ok := ids["cloud"]; !ok || c.Path != "" {
		t.Errorf("cloud-only session = %+v (want empty path)", c)
	}

	// Untracked local session falls back to its folder path as the group (no git
	// repo in the test), and keeps its raw snippet — never dropped.
	fallback := folderByCwd(tree, "~/work/infra")
	if fallback == nil || len(fallback.Sessions) != 1 || fallback.Sessions[0].ID != "u" {
		t.Fatalf("untracked session should group under ~/work/infra, got %+v", fallback)
	}
	if fallback.Sessions[0].Snippet != "untracked one" {
		t.Errorf("untracked snippet overwritten: %q", fallback.Sessions[0].Snippet)
	}
}

func TestWorktreeParent(t *testing.T) {
	cases := map[string]string{
		// The reported case: a finished worktree whose directory is gone.
		"/Users/me/tools/sched/.claude/worktrees/ios-install": "/Users/me/tools/sched",
		// A session that ran in a SUBDIRECTORY of a worktree still resolves to the
		// checkout, not to the worktree.
		"/Users/me/src/eph/.claude/worktrees/tok/tasks/2026-split": "/Users/me/src/eph",
		// Not a worktree path.
		"/Users/me/src/plain":                   "",
		"/Users/me/src/plain/.claude/settings":  "",
		"/Users/me/src/plain/.claude/worktrees": "", // the dir itself, no <name>
		"":                                      "",
	}
	for in, want := range cases {
		if got := worktreeParent(in); got != want {
			t.Errorf("worktreeParent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeEntireSkipsDeadDirsForNewSessions(t *testing.T) {
	// Now that finished worktrees collapse into their repo group, a group's newest
	// session is often one whose directory has since been deleted. `n` must not cd
	// into it — Dir has to come from a session whose cwd still exists.
	live := t.TempDir()
	local := sessionTree{
		Pwd: "/p",
		Folders: []treeFolder{{
			Cwd: "/home/me/work/infra",
			Sessions: []treeSession{
				{ID: "gone", Mtime: 200, cwd: "/home/me/work/infra/.claude/worktrees/merged-away"},
				{ID: "here", Mtime: 100, cwd: live},
			},
		}},
	}
	entire := []entireSession{
		{SessionID: "gone", Repo: "org/infra", LastActivityAt: "2026-07-10T00:59:00Z"},
		{SessionID: "here", Repo: "org/infra", LastActivityAt: "2026-07-10T00:58:00Z"},
	}
	tree := mergeEntire(local, entire, "/home/me", 0, parseEntireTime("2026-07-10T01:00:00Z"))

	infra := folderByCwd(tree, "org/infra")
	if infra == nil {
		t.Fatal("no org/infra group")
	}
	if infra.Dir != live {
		t.Errorf("group Dir = %q, want the existing dir %q (a deleted worktree is not an `n` target)", infra.Dir, live)
	}
}

func TestMergeEntirePwdWinsOverNewestSessionDir(t *testing.T) {
	// A session records the cwd it STARTED in, so one that opened in a
	// subdirectory and then moved into a worktree hands the repo group that
	// subdirectory as its Dir. Standing in the checkout, `n` must cd to $PWD.
	pwd := t.TempDir()
	sub := t.TempDir()
	local := sessionTree{
		Pwd: pwd,
		Folders: []treeFolder{{
			Cwd:      pwd + "/.claude/worktrees/reimport",
			Sessions: []treeSession{{ID: "moved", Mtime: 200, cwd: sub}},
		}},
	}
	entire := []entireSession{{SessionID: "moved", Repo: "org/infra", LastActivityAt: "2026-07-10T00:59:00Z"}}
	tree := mergeEntire(local, entire, "/home/me", 0, parseEntireTime("2026-07-10T01:00:00Z"))

	// $PWD isn't in a git repo here, so its group is the tilde'd path, not org/infra.
	cur := folderByCwd(tree, tree.CurrentGroup)
	if cur == nil || cur.Dir != pwd {
		t.Fatalf("current group Dir = %+v, want %q", cur, pwd)
	}
	// The same group, reached the second way: sessions already present in it.
	local.Folders[0].Cwd = pwd
	tree = mergeEntire(local, nil, "/home/me", 0, parseEntireTime("2026-07-10T01:00:00Z"))
	cur = folderByCwd(tree, tree.CurrentGroup)
	if cur == nil || cur.Dir != pwd {
		t.Errorf("group with sessions: Dir = %+v, want $PWD %q, not the session's %q", cur, pwd, sub)
	}
	if len(cur.Sessions) != 1 {
		t.Errorf("session dropped: %+v", cur.Sessions)
	}
}
