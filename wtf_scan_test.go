package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseGitWorktreePorcelain(t *testing.T) {
	data := []byte("worktree /repo\nHEAD abc123\nbranch refs/heads/feat/x\n\nworktree /repo/wt\nHEAD def456\ndetached\n")
	want := []gitWorktreeEntry{
		{Path: "/repo", Head: "abc123", Branch: "feat/x"},
		{Path: "/repo/wt", Head: "def456", Detached: true},
	}
	if got := parseGitWorktreePorcelain(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("entries:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestResolveRemoteDefault(t *testing.T) {
	tests := []struct {
		name string
		run  wtfCommandRunner
		want string
	}{
		{"symbolic ref", gitOutputRunner(map[string]string{"symbolic-ref --quiet refs/remotes/origin/HEAD": "refs/remotes/origin/main\n"}), "origin/main"},
		{"main fallback", gitOutputRunner(map[string]string{"show-ref --verify --quiet refs/remotes/origin/main": ""}), "origin/main"},
		{"master fallback", gitOutputRunner(map[string]string{"show-ref --verify --quiet refs/remotes/origin/master": ""}), "origin/master"},
		{"unknown", gitOutputRunner(nil), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveRemoteDefault(context.Background(), "/repo", tt.run); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInspectWorktreeCapturesBoundedEvidence(t *testing.T) {
	dir := t.TempDir()
	dirty := make([]string, 12)
	for i := range dirty {
		dirty[i] = "?? file" + string(rune('a'+i))
	}
	subjects := make([]string, 55)
	for i := range subjects {
		subjects[i] = "subject"
	}
	run := gitOutputRunner(map[string]string{
		"status --porcelain":                            strings.Join(dirty, "\n") + "\n",
		"symbolic-ref --quiet refs/remotes/origin/HEAD": "refs/remotes/origin/main\n",
		"rev-list --count origin/main..HEAD":            "55\n",
		"log --format=%s origin/main..HEAD":             strings.Join(subjects, "\n") + "\n",
		"diff --no-ext-diff --unified=0 HEAD --":        strings.Repeat("x", 140*1024),
	})
	got := inspectWorktree(context.Background(), "acme/repo", gitWorktreeEntry{Path: dir, Head: "abc", Branch: "feat/x"}, 42, run)
	if !got.Exists || got.DirtyFiles != 12 || len(got.DirtySummary) != 10 || got.DefaultBranch != "origin/main" || got.UnmergedCommits != 55 {
		t.Fatalf("inspection fields: %#v", got)
	}
	if len(got.GitEvidence) != 3 || got.GitEvidence[0] != (wtfGitEvidence{Source: "branch", Text: "feat/x"}) {
		t.Fatalf("evidence sources: %#v", got.GitEvidence)
	}
	if got.GitEvidence[1].Source != "unmerged subjects" || strings.Count(got.GitEvidence[1].Text, "subject") != 50 {
		t.Fatalf("subjects not bounded: %#v", got.GitEvidence[1])
	}
	if got.GitEvidence[2].Source != "diff" || len(got.GitEvidence[2].Text) != 128*1024 {
		t.Fatalf("diff not bounded: source=%q bytes=%d", got.GitEvidence[2].Source, len(got.GitEvidence[2].Text))
	}
}

func TestInspectWorktreeUnknownAndMissingAreWIP(t *testing.T) {
	dir := t.TempDir()
	unknown := inspectWorktree(context.Background(), "acme/repo", gitWorktreeEntry{Path: dir}, 42, gitOutputRunner(nil))
	if unknown.DefaultBranch != "" || unknown.UnmergedCommits != -1 || !worktreeHasWIP(unknown) {
		t.Fatalf("unknown state treated as clean: %#v", unknown)
	}

	missing := inspectWorktree(context.Background(), "acme/repo", gitWorktreeEntry{Path: filepath.Join(dir, "gone"), Head: "old", Branch: "feat/old"}, 43, gitOutputRunner(nil))
	if missing.Exists || missing.GitError != "worktree path missing" || missing.Head != "old" || missing.Branch != "feat/old" || !worktreeHasWIP(missing) {
		t.Fatalf("missing worktree record: %#v", missing)
	}
}

func TestInspectWorktreeCountsPorcelainLines(t *testing.T) {
	dir := t.TempDir()
	run := gitOutputRunner(map[string]string{
		"status --porcelain": " M tracked\n?? untracked\n",
		"show-ref --verify --quiet refs/remotes/origin/main": "",
		"rev-list --count origin/main..HEAD":                 "0\n",
		"log --format=%s origin/main..HEAD":                  "",
		"diff --no-ext-diff --unified=0 HEAD --":             "",
	})
	if got := inspectWorktree(context.Background(), "acme/repo", gitWorktreeEntry{Path: dir}, 42, run); got.DirtyFiles != 2 {
		t.Fatalf("dirty files=%d, want 2", got.DirtyFiles)
	}
}

func TestInspectRepoWorktreesPreservesPriorRecordsMissingFromPorcelain(t *testing.T) {
	currentPath := t.TempDir()
	missingPath := filepath.Join(t.TempDir(), "removed")
	prior := []wtfWorktree{
		{
			Repo:        "acme/repo",
			Path:        currentPath,
			FirstSeen:   10,
			SessionKeys: []string{"claude:current"},
			TrailKeys:   []string{"acme/repo#1"},
		},
		{
			Repo:            "acme/repo",
			Path:            missingPath,
			Branch:          "feat/removed",
			Head:            "old-head",
			Exists:          true,
			DirtyFiles:      2,
			DirtySummary:    []string{" M old"},
			UnmergedCommits: 3,
			SessionKeys:     []string{"claude:old"},
			TrailKeys:       []string{"acme/repo#2"},
			GitEvidence:     []wtfGitEvidence{{Source: "unmerged subjects", Text: "old work"}},
			FirstSeen:       11,
			LastSeen:        20,
			LastWIPAt:       20,
		},
	}
	run := gitOutputRunner(map[string]string{
		"worktree list --porcelain":                          "worktree " + currentPath + "\nHEAD new-head\nbranch refs/heads/main\n",
		"status --porcelain":                                 "",
		"show-ref --verify --quiet refs/remotes/origin/main": "",
		"rev-list --count origin/main..HEAD":                 "0\n",
		"log --format=%s origin/main..HEAD":                  "",
		"diff --no-ext-diff --unified=0 HEAD --":             "",
	})

	got := inspectRepoWorktrees(context.Background(), "acme/repo", currentPath, 42, prior, run)
	if len(got) != 2 {
		t.Fatalf("worktrees=%d, want 2: %#v", len(got), got)
	}
	byPath := make(map[string]wtfWorktree, len(got))
	for _, worktree := range got {
		byPath[worktree.Path] = worktree
	}
	current := byPath[currentPath]
	if current.Head != "new-head" || current.Branch != "main" || current.FirstSeen != 10 ||
		!reflect.DeepEqual(current.SessionKeys, prior[0].SessionKeys) || !reflect.DeepEqual(current.TrailKeys, prior[0].TrailKeys) {
		t.Fatalf("current worktree did not refresh facts and preserve history: %#v", current)
	}
	missing := byPath[missingPath]
	if missing.Exists || missing.GitError != "worktree path missing" || missing.DirtyFiles != -1 || missing.UnmergedCommits != -1 ||
		missing.LastSeen != 42 || missing.LastWIPAt != 42 || missing.FirstSeen != 11 || missing.Branch != "feat/removed" || missing.Head != "old-head" ||
		!reflect.DeepEqual(missing.SessionKeys, prior[1].SessionKeys) || !reflect.DeepEqual(missing.TrailKeys, prior[1].TrailKeys) ||
		!reflect.DeepEqual(missing.GitEvidence, prior[1].GitEvidence) || !reflect.DeepEqual(missing.DirtySummary, prior[1].DirtySummary) {
		t.Fatalf("missing worktree did not preserve WIP history: %#v", missing)
	}
}

func TestInspectRepoWorktreesDistinguishesUnlistedAndMissingPriorPaths(t *testing.T) {
	currentPath := t.TempDir()
	unlistedPath := t.TempDir()
	missingPath := filepath.Join(t.TempDir(), "removed")
	prior := []wtfWorktree{
		{Repo: "acme/repo", Path: unlistedPath, Exists: true, DirtyFiles: 1, UnmergedCommits: 2},
		{Repo: "acme/repo", Path: missingPath, Exists: true, DirtyFiles: 3, UnmergedCommits: 4},
	}
	run := gitOutputRunner(map[string]string{
		"worktree list --porcelain":                          "worktree " + currentPath + "\nHEAD new-head\nbranch refs/heads/main\n",
		"status --porcelain":                                 "",
		"show-ref --verify --quiet refs/remotes/origin/main": "",
		"rev-list --count origin/main..HEAD":                 "0\n",
		"log --format=%s origin/main..HEAD":                  "",
		"diff --no-ext-diff --unified=0 HEAD --":             "",
	})

	got := inspectRepoWorktrees(context.Background(), "acme/repo", currentPath, 42, prior, run)
	byPath := make(map[string]wtfWorktree, len(got))
	for _, worktree := range got {
		byPath[worktree.Path] = worktree
	}
	unlisted := byPath[unlistedPath]
	if !unlisted.Exists || unlisted.GitError != "worktree not listed by git" || unlisted.DirtyFiles != -1 || unlisted.UnmergedCommits != -1 || !worktreeHasWIP(unlisted) {
		t.Fatalf("existing unlisted worktree: %#v", unlisted)
	}
	missing := byPath[missingPath]
	if missing.Exists || missing.GitError != "worktree path missing" || missing.DirtyFiles != -1 || missing.UnmergedCommits != -1 || !worktreeHasWIP(missing) {
		t.Fatalf("missing worktree: %#v", missing)
	}
}

func TestInspectWorktreeRealRepositoryDistinguishesDirtyAndUnmerged(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(root, "worktree")
	gitRun(t, root, "init", "--bare", origin)
	gitRun(t, root, "clone", origin, repo)
	gitRun(t, repo, "config", "user.email", "test@example.com")
	gitRun(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "initial"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "initial")
	gitRun(t, repo, "commit", "-m", "initial")
	gitRun(t, repo, "branch", "-M", "main")
	gitRun(t, repo, "push", "-u", "origin", "main")
	gitRun(t, repo, "worktree", "add", "-b", "feat/test", worktree)
	if err := os.WriteFile(filepath.Join(worktree, "committed"), []byte("commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, worktree, "add", "committed")
	gitRun(t, worktree, "commit", "-m", "one ahead")
	if err := os.WriteFile(filepath.Join(worktree, "dirty"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		return cmd.Output()
	}
	got := inspectRepoWorktrees(context.Background(), "acme/repo", repo, 42, nil, run)
	var found *wtfWorktree
	for i := range got {
		if got[i].Branch == "feat/test" {
			found = &got[i]
		}
	}
	if found == nil || found.DirtyFiles != 1 || found.UnmergedCommits != 1 {
		t.Fatalf("linked worktree: %#v", found)
	}
}

func gitOutputRunner(outputs map[string]string) wtfCommandRunner {
	return func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "-C" {
			args = args[2:]
		}
		key := strings.Join(args, " ")
		if output, ok := outputs[key]; ok {
			return []byte(output), nil
		}
		return nil, exec.ErrNotFound
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func TestExtractTrailEvidence(t *testing.T) {
	tests := []struct {
		name, text, current string
		known               []string
		wantKey             string
		wantResolved        bool
	}{
		{"url", "see https://entire.io/gh/acme/api/trails/12", "x/y", nil, "acme/api#12", true},
		{"qualified", "acme/api#13", "x/y", nil, "acme/api#13", true},
		{"repo shorthand current", "api#14", "acme/api", nil, "acme/api#14", true},
		{"repo shorthand unique known", "api#15", "", []string{"acme/api"}, "acme/api#15", true},
		{"repo shorthand ambiguous", "api#16", "", []string{"acme/api", "other/api"}, "", false},
		{"bare current", "trail #17", "acme/api", nil, "acme/api#17", true},
		{"bare no current", "trail 18", "", []string{"acme/api"}, "", false},
		{"ordinary hash", "color #123", "acme/api", nil, "", false},
		{"email boundary", "xapi#19@example.com", "acme/api", nil, "", false},
		{"url boundary", "xhttps://entire.io/gh/acme/api/trails/20", "", nil, "", false},
		{"numeric boundary", "acme/api#21x", "", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTrailEvidence([]trailTextEvent{{At: 42, Source: "user", Text: tt.text}}, trailContext{CurrentRepo: tt.current, KnownRepos: tt.known})
			if tt.wantKey == "" && tt.wantResolved && len(got) == 0 {
				t.Fatal("expected evidence")
			}
			if !tt.wantResolved && tt.wantKey == "" && tt.name != "repo shorthand ambiguous" && tt.name != "bare no current" {
				if len(got) != 0 {
					t.Fatalf("got false-positive evidence: %#v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d evidence entries, want 1: %#v", len(got), got)
			}
			if got[0].Key != tt.wantKey || got[0].Resolved != tt.wantResolved {
				t.Fatalf("got key=%q resolved=%v, want key=%q resolved=%v", got[0].Key, got[0].Resolved, tt.wantKey, tt.wantResolved)
			}
			if got[0].Matched == "" || got[0].Source != "user" || got[0].At != 42 || got[0].Resolution == "" {
				t.Fatalf("evidence metadata not preserved: %#v", got[0])
			}
		})
	}
}

func TestExtractTrailEvidenceDeduplicatesByEarliestTimestamp(t *testing.T) {
	got := extractTrailEvidence([]trailTextEvent{
		{At: 200, Source: "assistant", Text: "ACME/API#7"},
		{At: 100, Source: "user", Text: "https://entire.io/gh/acme/api/trails/7"},
	}, trailContext{})
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].At != 100 || got[0].Source != "user" || got[0].Matched != "https://entire.io/gh/acme/api/trails/7" {
		t.Fatalf("did not preserve earliest evidence: %#v", got[0])
	}
}

func TestExtractTrailEvidenceFindsAdjacentReferences(t *testing.T) {
	tests := []struct {
		name string
		text string
		ctx  trailContext
		want []string
	}{
		{"qualified", "acme/api#1,other/web#2", trailContext{}, []string{"acme/api#1", "other/web#2"}},
		{"repo shorthand", "api#3,web#4", trailContext{KnownRepos: []string{"acme/api", "other/web"}}, []string{"acme/api#3", "other/web#4"}},
		{"bare", "trail #5,trail #6", trailContext{CurrentRepo: "acme/api"}, []string{"acme/api#5", "acme/api#6"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTrailEvidence([]trailTextEvent{{At: 42, Source: "user", Text: tt.text}}, tt.ctx)
			keys := make([]string, len(got))
			for i := range got {
				keys[i] = got[i].Key
			}
			if !reflect.DeepEqual(keys, tt.want) {
				t.Fatalf("keys: got %q, want %q", keys, tt.want)
			}
		})
	}
}

func TestExtractTrailEvidenceRejectsOverflow(t *testing.T) {
	got := extractTrailEvidence([]trailTextEvent{{At: 42, Source: "user", Text: "acme/api#999999999999999999999999999999999999"}}, trailContext{})
	if len(got) != 1 {
		t.Fatalf("got %d entries, want unresolved evidence", len(got))
	}
	if got[0].Resolved || got[0].Key != "" || got[0].Number != 0 {
		t.Fatalf("overflow resolved: %#v", got[0])
	}
}

func TestClaudeTrailEvents(t *testing.T) {
	got := claudeTrailEvents(filepath.Join("testdata", "wtf", "claude-trails.jsonl"), 999)
	want := []trailTextEvent{
		{At: 1790672400, Source: "user", Text: "user acme/api#1"},
		{At: 1790672400, Source: "user", Text: "user block acme/api#10"},
		{At: 1790672401, Source: "assistant", Text: "assistant acme/api#2"},
		{At: 1790672402, Source: "tool input", Text: "input acme/api#3"},
		{At: 1790672403, Source: "tool result", Text: "result acme/api#4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestAmpTrailEvents(t *testing.T) {
	data := mustReadFile(t, filepath.Join("testdata", "wtf", "amp-trails.json"))
	var export ampExport
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatal(err)
	}
	got := ampTrailEvents(export, 999)
	want := []trailTextEvent{
		{At: 1790672400, Source: "user", Text: "user acme/api#5"},
		{At: 1790672401, Source: "assistant", Text: "assistant acme/api#6"},
		{At: 1790672402, Source: "tool input", Text: "input acme/api#7"},
		{At: 1790672403, Source: "tool result", Text: "result acme/api#8"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n got: %#v\nwant: %#v", got, want)
	}
}
