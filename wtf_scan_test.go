package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func scanFixtureDeps(now time.Time, transcript, first, source, metadata string) wtfScanDeps {
	return wtfScanDeps{
		Inventory: wtfInventoryDeps{
			Today: func(string, int64, *time.Location) []handoverItem {
				return []handoverItem{{Agent: AgentClaude, SessionID: "one", Repo: "acme/api", Cwd: first, Path: transcript, LastActivity: now.Unix()}}
			},
			Live: func(string) []liveSession {
				return []liveSession{{Agent: AgentClaude, SessionID: "one", Cwd: first, Path: transcript, Status: "busy", UpdatedAt: now.UnixMilli()}}
			},
		},
		Run: func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
			if name == "entire" {
				return []byte(metadata), nil
			}
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "worktree list --porcelain"):
				return []byte("worktree " + first + "\nHEAD a\nbranch refs/heads/feat/first\n\nworktree " + source + "\nHEAD b\nbranch refs/heads/feat/source\n"), nil
			case strings.Contains(joined, "symbolic-ref"):
				return []byte("refs/remotes/origin/main\n"), nil
			case strings.Contains(joined, "rev-list --count"):
				return []byte("0\n"), nil
			default:
				return nil, nil
			}
		},
		Summarize: func(wtfSession, string, wtfSummaryCache) (wtfSummary, wtfSummaryCache, error) {
			return wtfSummary{}, wtfSummaryCache{}, nil
		},
		Now: func() time.Time { return now },
	}
}

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

	got, err := inspectRepoWorktrees(context.Background(), "acme/repo", currentPath, 42, prior, run)
	if err != nil {
		t.Fatal(err)
	}
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

	got, err := inspectRepoWorktrees(context.Background(), "acme/repo", currentPath, 42, prior, run)
	if err != nil {
		t.Fatal(err)
	}
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
	got, err := inspectRepoWorktrees(context.Background(), "acme/repo", repo, 42, nil, run)
	if err != nil {
		t.Fatal(err)
	}
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

func TestFetchTrailMetadataCommandAndBranchPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		wantSource string
	}{
		{"original branch fallback", `{"number":1223,"url":"https://entire.io/gh/entirehq/entiredb/trails/1223","branch":"","original_branch":"shallow","base":"main","title":"A trail","status":"open"}`, "shallow"},
		{"branch wins", `{"number":1223,"branch":"current","original_branch":"shallow","base":"main"}`, "current"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trail := wtfTrail{Key: "entirehq/entiredb#1223", Owner: "entirehq", Repo: "entiredb", Number: 1223, FirstSeen: 1, LastSeen: 2, MetadataError: "old", MetadataAttempts: 3, MetadataNextRetry: 500}
			run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
				if dir != "" || name != "entire" || strings.Join(args, " ") != "trail show 1223 --repo gh/entirehq/entiredb --json" {
					t.Fatalf("command: dir=%q name=%q args=%q", dir, name, args)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 12*time.Second || time.Until(deadline) < 11*time.Second {
					t.Fatalf("deadline does not apply 12-second timeout: %v, %v", deadline, ok)
				}
				return []byte(tt.response), nil
			}
			got, err := fetchTrailMetadata(context.Background(), trail, 600, run)
			if err != nil {
				t.Fatal(err)
			}
			if got.SourceBranch != tt.wantSource || got.TargetBranch != "main" || got.MetadataUpdatedAt != 600 || got.MetadataError != "" || got.MetadataAttempts != 0 || got.MetadataNextRetry != 0 {
				t.Fatalf("decoded metadata: %#v", got)
			}
			if got.FirstSeen != 1 || got.LastSeen != 2 {
				t.Fatalf("local evidence changed: %#v", got)
			}
		})
	}
}

func TestFetchTrailMetadataFailureRetainsSuccessfulMetadataAndBacksOff(t *testing.T) {
	for _, attempt := range []struct {
		prior int
		delay int64
	}{{0, 60}, {1, 120}, {2, 240}, {3, 480}, {8, 3600}} {
		trail := wtfTrail{Owner: "acme", Repo: "api", Number: 7, Title: "kept", Status: "open", SourceBranch: "feat", TargetBranch: "main", MetadataUpdatedAt: 50, MetadataAttempts: attempt.prior, FirstClaim: &wtfClaim{Evidence: "local"}, Associations: []wtfAssociation{{Evidence: "local"}}}
		got, err := fetchTrailMetadata(context.Background(), trail, 1000, func(context.Context, string, string, ...string) ([]byte, error) {
			return nil, errors.New("offline")
		})
		if err == nil || got.Title != "kept" || got.Status != "open" || got.SourceBranch != "feat" || got.TargetBranch != "main" || got.MetadataUpdatedAt != 50 || got.MetadataAttempts != attempt.prior+1 || got.MetadataNextRetry != 1000+attempt.delay || got.MetadataError == "" || got.FirstClaim == nil || len(got.Associations) != 1 {
			t.Fatalf("prior attempts %d: %#v, err=%v", attempt.prior, got, err)
		}
	}
}

func TestTrailMetadataDue(t *testing.T) {
	const now = int64(100000)
	tests := []struct {
		name   string
		trail  wtfTrail
		active bool
		want   bool
	}{
		{"new", wtfTrail{}, false, true},
		{"active before ten minutes", wtfTrail{MetadataUpdatedAt: now - 599}, true, false},
		{"active after ten minutes", wtfTrail{MetadataUpdatedAt: now - 600}, true, true},
		{"merged before 24 hours", wtfTrail{Status: "merged", MetadataUpdatedAt: now - 86399}, false, false},
		{"merged after 24 hours", wtfTrail{Status: "merged", MetadataUpdatedAt: now - 86400}, false, true},
		{"failed before retry", wtfTrail{MetadataNextRetry: now + 1}, true, false},
		{"failed at retry", wtfTrail{MetadataNextRetry: now}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trailMetadataDue(tt.trail, tt.active, now); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
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
		{"entire-hosted url", "see https://entire.io/et/acme/api/trails/22", "x/y", nil, "acme/api#22", true},
		{"qualified", "acme/api#13", "x/y", nil, "acme/api#13", true},
		{"repo shorthand current", "api#14", "acme/api", nil, "acme/api#14", true},
		{"repo shorthand unique known", "api#15", "", []string{"acme/api"}, "acme/api#15", true},
		{"repo shorthand ambiguous", "api#16", "", []string{"acme/api", "other/api"}, "", false},
		{"bare current", "trail #17", "acme/api", nil, "acme/api#17", true},
		{"bare no current", "trail 18", "", []string{"acme/api"}, "", false},
		{"bare after sentence word", "we are on trail 23", "acme/api", nil, "acme/api#23", true},
		{"bare named current repo", "api trail 24", "acme/api", nil, "acme/api#24", true},
		{"bare named known repo", "web trail 25", "acme/api", []string{"acme/api", "other/web"}, "other/web#25", true},
		{"bare named unknown repo", "company-knowledge trail 11", "acme/api", nil, "", false},
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
			if !tt.wantResolved && tt.wantKey == "" && tt.name != "repo shorthand ambiguous" && tt.name != "bare no current" && tt.name != "bare named unknown repo" {
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

// Reading a file that mentions a trail is not working on it: a memory index,
// a spec or a grep hit names trails the session never touches, and counting
// those as claims warned two read-only sessions off a trail neither had used.
func TestExtractTrailEvidenceIgnoresToolText(t *testing.T) {
	got := extractTrailEvidence([]trailTextEvent{
		{At: 100, Source: "tool result", Text: "acme/api#7"},
		{At: 101, Source: "tool input", Text: "acme/api#8"},
		{At: 200, Source: "assistant", Text: "acme/api#7"},
	}, trailContext{})
	if len(got) != 1 || got[0].Key != "acme/api#7" || got[0].Source != "assistant" || got[0].At != 200 {
		t.Fatalf("tool text counted as a claim: %#v", got)
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
	got, err := claudeTrailEvents(filepath.Join("testdata", "wtf", "claude-trails.jsonl"), 999)
	if err != nil {
		t.Fatal(err)
	}
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

func TestTranscriptTrailEventsReportsUnreadableSources(t *testing.T) {
	for _, session := range []wtfSession{
		{Agent: AgentClaude, ID: "c", Transcript: filepath.Join(t.TempDir(), "missing.jsonl")},
		{Agent: AgentAmp, ID: "a", Transcript: filepath.Join(t.TempDir(), "missing.json")},
	} {
		if _, err := transcriptTrailEvents(session, t.TempDir(), 1); err == nil || !strings.Contains(err.Error(), string(session.Agent)+" transcript") {
			t.Fatalf("%s error=%v", session.Agent, err)
		}
	}
}

func TestReconcileTrailsChoosesEarliestClaimAndPreservesIt(t *testing.T) {
	sessions := []wtfSession{
		{Agent: AgentClaude, ID: "later", Repo: "acme/api", Cwd: "/wt/later", Active: true},
		{Agent: AgentClaude, ID: "earlier", Repo: "acme/api", Cwd: "/wt/earlier", Active: true},
	}
	evidence := map[string][]trailEvidence{
		"claude:later":   {{Key: "acme/api#7", Owner: "acme", Repo: "api", Number: 7, URL: "url", Matched: "api#7", Source: "user", At: 200, Resolved: true}},
		"claude:earlier": {{Key: "acme/api#7", Owner: "acme", Repo: "api", Number: 7, URL: "url", Matched: "api#7", Source: "user", At: 100, Resolved: true}},
	}
	worktrees := map[string]wtfWorktree{
		"/wt/later":   {Repo: "acme/api", Path: "/wt/later", Exists: true},
		"/wt/earlier": {Repo: "acme/api", Path: "/wt/earlier", Exists: true},
	}

	got := reconcileTrails(wtfState{}, sessions, evidence, worktrees, 300)
	trail := got.Trails["acme/api#7"]
	if trail.OwnerSession != "claude:earlier" || trail.FirstClaim == nil || trail.FirstClaim.At != 100 || trail.CanonicalWorktree != "/wt/earlier" {
		t.Fatalf("first claim: %#v", trail)
	}

	restarted := reconcileTrails(got, []wtfSession{sessions[1], sessions[0]}, evidence, worktrees, 400)
	trail = restarted.Trails["acme/api#7"]
	if trail.OwnerSession != "claude:earlier" || trail.CanonicalWorktree != "/wt/earlier" || trail.FirstClaim.At != 100 {
		t.Fatalf("persisted claim changed: %#v", trail)
	}
}

// Naming another repo's trail is talking about it, not working on it: the
// session's worktree holds a different repo, so nothing there can be the
// trail's work. A claim of that shape made one session the "owner" of a
// ci-webhooks trail with an entire-tail checkout as its canonical worktree.
func TestReconcileTrailsIgnoresClaimsFromAnotherRepo(t *testing.T) {
	sessions := []wtfSession{{Agent: AgentClaude, ID: "elsewhere", Repo: "acme/tool", Cwd: "/wt/tool", Active: true}}
	evidence := map[string][]trailEvidence{"claude:elsewhere": {{Key: "acme/api#7", Owner: "acme", Repo: "api", Number: 7, Matched: "acme/api#7", Source: "assistant", At: 100, Resolved: true}}}
	worktrees := map[string]wtfWorktree{"/wt/tool": {Repo: "acme/tool", Path: "/wt/tool", Exists: true}}

	trail := reconcileTrails(wtfState{}, sessions, evidence, worktrees, 200).Trails["acme/api#7"]
	if trail.OwnerSession != "" || trail.FirstClaim != nil || trail.CanonicalWorktree != "" {
		t.Fatalf("cross-repo mention claimed the trail: %#v", trail)
	}

	stored := wtfState{Trails: map[string]wtfTrail{"acme/api#7": {
		Key: "acme/api#7", Owner: "acme", Repo: "api", Number: 7,
		OwnerSession: "claude:elsewhere", CanonicalWorktree: "/wt/tool",
		FirstClaim: &wtfClaim{SessionKey: "claude:elsewhere", Worktree: "/wt/tool", At: 100, Evidence: "acme/api#7"},
	}}}
	trail = reconcileTrails(stored, sessions, evidence, worktrees, 300).Trails["acme/api#7"]
	if trail.OwnerSession != "" || trail.FirstClaim != nil || trail.CanonicalWorktree != "" {
		t.Fatalf("stored cross-repo claim kept its ownership: %#v", trail)
	}
}

func TestDetectWTFFindingsIgnoresSessionsInAnotherRepo(t *testing.T) {
	state := findingState()
	state.Sessions["claude:a"] = wtfSession{Active: true, Cwd: "/wt/a"}
	state.Sessions["claude:other"] = wtfSession{Active: true, Cwd: "/wt/other"}
	state.Worktrees["/wt/a"] = wtfWorktree{Path: "/wt/a", Repo: "acme/api", Exists: true}
	state.Worktrees["/wt/other"] = wtfWorktree{Path: "/wt/other", Repo: "acme/tool", Exists: true}
	trail := state.Trails["acme/api#7"]
	trail.Associations = []wtfAssociation{{SessionKey: "claude:a", Worktree: "/wt/a"}, {SessionKey: "claude:other", Worktree: "/wt/other"}}
	state.Trails[trail.Key] = trail

	if got := detectWTFFindings(state, 100); findingOfKind(got, "duplicate-active-claim") != nil {
		t.Fatalf("a session in another repo counted as a claimant: %#v", got)
	}
}

// A registry written before tool text stopped counting still holds claims
// made by reading a trail's number. Left in place they keep the warnings
// coming, so a reconcile drops them and whatever ownership they decided.
func TestReconcileTrailsDropsStoredToolClaims(t *testing.T) {
	read := wtfAssociation{SessionKey: "claude:reader", Worktree: "/wt/main", At: 100, Evidence: "trail 7", Source: "tool result"}
	prior := wtfState{Trails: map[string]wtfTrail{"acme/api#7": {
		Key: "acme/api#7", Owner: "acme", Repo: "api", Number: 7,
		OwnerSession: "claude:reader", CanonicalWorktree: "/wt/main",
		FirstClaim:   &wtfClaim{SessionKey: "claude:reader", Worktree: "/wt/main", At: 100, Evidence: "trail 7"},
		Associations: []wtfAssociation{read, {Worktree: "/wt/feature", At: 50, Evidence: "feature", Source: "source branch"}},
	}}}
	sessions := []wtfSession{{Agent: AgentClaude, ID: "worker", Repo: "acme/api", Cwd: "/wt/feature", Active: true}}
	evidence := map[string][]trailEvidence{"claude:worker": {{Key: "acme/api#7", Owner: "acme", Repo: "api", Number: 7, Matched: "api#7", Source: "user", At: 200, Resolved: true}}}
	worktrees := map[string]wtfWorktree{
		"/wt/main":    {Repo: "acme/api", Path: "/wt/main", Exists: true},
		"/wt/feature": {Repo: "acme/api", Path: "/wt/feature", Exists: true},
	}

	trail := reconcileTrails(prior, sessions, evidence, worktrees, 300).Trails["acme/api#7"]
	for _, association := range trail.Associations {
		if association.SessionKey == "claude:reader" {
			t.Fatalf("stored tool claim survived: %#v", trail.Associations)
		}
	}
	if trail.OwnerSession != "claude:worker" || trail.FirstClaim == nil || trail.FirstClaim.At != 200 || trail.CanonicalWorktree != "/wt/feature" {
		t.Fatalf("ownership still decided by the tool claim: %#v", trail)
	}
}

func TestReconcileTrailsClaimsRequireActiveLocalWorktree(t *testing.T) {
	e := trailEvidence{Key: "acme/api#8", Owner: "acme", Repo: "api", Number: 8, Matched: "acme/api#8", Source: "user", At: 100, Resolved: true}
	sessions := []wtfSession{
		{Agent: AgentClaude, ID: "remote", Repo: "acme/api", Active: true},
		{Agent: AgentClaude, ID: "one", Repo: "acme/api", Cwd: "/wt/shared", Active: true},
		{Agent: AgentAmp, ID: "two", Repo: "acme/api", Cwd: "/wt/shared", Active: true},
	}
	evidence := map[string][]trailEvidence{"claude:remote": {e}, "claude:one": {e}, "amp:two": {{Key: e.Key, Owner: e.Owner, Repo: e.Repo, Number: e.Number, Matched: e.Matched, Source: e.Source, At: 200, Resolved: true}}}
	got := reconcileTrails(wtfState{}, sessions, evidence, map[string]wtfWorktree{"/wt/shared": {Repo: "acme/api", Path: "/wt/shared", Exists: true}}, 300)
	trail := got.Trails[e.Key]
	if trail.OwnerSession != "claude:one" || trail.FirstClaim.Worktree != "/wt/shared" || len(trail.Associations) != 3 {
		t.Fatalf("claims and associations: %#v", trail)
	}
}

func TestRemoteAndMissingAssociationsAreRelatedButNeverClaims(t *testing.T) {
	e := trailEvidence{Key: "acme/api#8", Owner: "acme", Repo: "api", Number: 8, Matched: "api#8", At: 1, Resolved: true}
	sessions := []wtfSession{{Agent: AgentAmp, ID: "remote", Repo: "acme/api", Active: true}, {Agent: AgentClaude, ID: "missing", Repo: "acme/api", Cwd: "/missing", Active: true}}
	state := reconcileTrails(wtfState{}, sessions, map[string][]trailEvidence{"amp:remote": {e}, "claude:missing": {e}}, map[string]wtfWorktree{"/missing": {Repo: "acme/api", Path: "/missing", Exists: false, GitError: "worktree path missing"}}, 2)
	trail := state.Trails[e.Key]
	if trail.FirstClaim != nil || trail.OwnerSession != "" || len(trail.Associations) != 2 || len(detectWTFFindings(state, 2)) != 0 {
		t.Fatalf("remote/missing became claims: trail=%#v findings=%#v", trail, detectWTFFindings(state, 2))
	}
}

func TestErroredExistingWorktreeNeitherClaimsNorTriggersFindings(t *testing.T) {
	e := trailEvidence{Key: "acme/api#9", Owner: "acme", Repo: "api", Number: 9, Matched: "api#9", Source: "user", At: 1, Resolved: true}
	sessions := []wtfSession{{Agent: AgentClaude, ID: "one", Repo: "acme/api", Cwd: "/wt/bad", Active: true}}
	worktrees := map[string]wtfWorktree{"/wt/bad": {Repo: "acme/api", Path: "/wt/bad", Exists: true, GitError: "git diff failed", Branch: "main", DefaultBranch: "origin/main"}}
	state := reconcileTrails(wtfState{}, sessions, map[string][]trailEvidence{"claude:one": {e}}, worktrees, 2)
	trail := state.Trails[e.Key]
	if trail.FirstClaim != nil || trail.OwnerSession != "" {
		t.Fatalf("errored worktree became a claim: %#v", trail)
	}
	if findings := detectWTFFindings(state, 2); len(findings) != 0 {
		t.Fatalf("errored worktree triggered findings: %#v", findings)
	}
}

func TestPerWorktreeGitFailureMakesScanLocallyIncomplete(t *testing.T) {
	for _, failure := range []string{"status", "default", "rev-list", "log", "diff"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			deps := emptyWTFDeps(time.Unix(100, 0))
			deps.Inventory.Today = func(string, int64, *time.Location) []handoverItem {
				return []handoverItem{{Agent: AgentClaude, SessionID: "one", Repo: "acme/api", Cwd: dir}}
			}
			deps.Run = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				switch {
				case strings.Contains(joined, "worktree list"):
					return []byte("worktree " + dir + "\nHEAD abc\nbranch refs/heads/feat\n\n"), nil
				case strings.Contains(joined, "status --porcelain"):
					if failure == "status" {
						return nil, errors.New("status failed")
					}
					return nil, nil
				case strings.Contains(joined, "symbolic-ref"):
					if failure == "default" {
						return nil, errors.New("default failed")
					}
					return []byte("refs/remotes/origin/main\n"), nil
				case strings.Contains(joined, "show-ref"):
					return nil, errors.New("ref absent")
				case strings.Contains(joined, "rev-list --count"):
					if failure == "rev-list" {
						return nil, errors.New("rev-list failed")
					}
					return []byte("0\n"), nil
				case strings.Contains(joined, " log "):
					if failure == "log" {
						return nil, errors.New("log failed")
					}
					return nil, nil
				case strings.Contains(joined, " diff "):
					if failure == "diff" {
						return nil, errors.New("diff failed")
					}
					return nil, nil
				}
				return nil, nil
			}
			saves := 0
			_, next, err := reconcileWTFDashboard(context.Background(), t.TempDir(), newWTFState(1), deps, func(wtfState) error { saves++; return nil })
			if err == nil || saves != 0 || next.Worktrees[dir].GitError == "" {
				t.Fatalf("err=%v saves=%d worktree=%#v", err, saves, next.Worktrees[dir])
			}
		})
	}
}

func TestReconcileTrailsProducesStableAssociationJSON(t *testing.T) {
	trail := wtfTrail{Key: "acme/api#9", Owner: "acme", Repo: "api", Number: 9, SourceBranch: "feat/9"}
	makeState := func(reverse bool) []byte {
		worktrees := map[string]wtfWorktree{}
		items := []wtfWorktree{{Repo: "acme/api", Path: "/b", Branch: "feat/9", FirstSeen: 2, Exists: true}, {Repo: "acme/api", Path: "/a", GitEvidence: []wtfGitEvidence{{Source: "branch", Text: "acme/api#9"}}, FirstSeen: 1, Exists: true}}
		if reverse {
			items[0], items[1] = items[1], items[0]
		}
		for _, w := range items {
			worktrees[w.Path] = w
		}
		got := reconcileTrails(wtfState{Trails: map[string]wtfTrail{trail.Key: trail}}, nil, nil, worktrees, 3)
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if a, b := makeState(false), makeState(true); !reflect.DeepEqual(a, b) {
		t.Fatalf("state differs:\n%s\n%s", a, b)
	}
}

func TestScanWTFMetadataSelectsInitialCanonicalInSameScan(t *testing.T) {
	home, first, source := t.TempDir(), t.TempDir(), t.TempDir()
	transcript := filepath.Join(home, "c.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","timestamp":"2026-09-29T09:00:00Z","message":{"content":"acme/api#44"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	deps := scanFixtureDeps(now, transcript, first, source, `{"number":44,"branch":"feat/source"}`)
	got, err := scanWTF(context.Background(), home, newWTFState(now.Unix()), deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trails["acme/api#44"].CanonicalWorktree != source {
		t.Fatalf("trail=%#v", got.Trails["acme/api#44"])
	}
}

func TestReconcileTrailsMovingSessionKeepsOwnerAndAddsAssociation(t *testing.T) {
	e := trailEvidence{Key: "acme/api#9", Owner: "acme", Repo: "api", Number: 9, Matched: "api#9", Source: "user", At: 100, Resolved: true}
	first := reconcileTrails(wtfState{}, []wtfSession{{Agent: AgentClaude, ID: "one", Repo: "acme/api", Cwd: "/wt/one", Active: true}}, map[string][]trailEvidence{"claude:one": {e}}, map[string]wtfWorktree{"/wt/one": {Repo: "acme/api", Path: "/wt/one", Exists: true}}, 200)
	second := reconcileTrails(first, []wtfSession{{Agent: AgentClaude, ID: "one", Repo: "acme/api", Cwd: "/wt/two", Active: true}}, map[string][]trailEvidence{"claude:one": {e}}, map[string]wtfWorktree{"/wt/two": {Repo: "acme/api", Path: "/wt/two", Exists: true}}, 300)
	trail := second.Trails[e.Key]
	if trail.OwnerSession != "claude:one" || trail.CanonicalWorktree != "/wt/one" || len(trail.Associations) != 2 {
		t.Fatalf("moved session: %#v", trail)
	}
}

func TestReconcileTrailsInactiveCannotIntroduceOrClaim(t *testing.T) {
	e := trailEvidence{Key: "acme/api#10", Owner: "acme", Repo: "api", Number: 10, Matched: "api#10", Source: "user", At: 100, Resolved: true}
	ended := []wtfSession{{Agent: AgentClaude, ID: "ended", Repo: "acme/api", Cwd: "/wt/ended", State: "ended"}}
	worktrees := map[string]wtfWorktree{"/wt/ended": {Repo: "acme/api", Path: "/wt/ended"}}
	if got := reconcileTrails(wtfState{}, ended, map[string][]trailEvidence{"claude:ended": {e}}, worktrees, 200); len(got.Trails) != 0 {
		t.Fatalf("inactive session introduced trail: %#v", got.Trails)
	}
	prior := wtfState{Trails: map[string]wtfTrail{e.Key: {Key: e.Key, Owner: e.Owner, Repo: e.Repo, Number: e.Number, OwnerSession: "claude:old", FirstClaim: &wtfClaim{SessionKey: "claude:old", Worktree: "/wt/old", At: 50}, CanonicalWorktree: "/wt/old"}}}
	got := reconcileTrails(prior, ended, map[string][]trailEvidence{"claude:ended": {e}}, worktrees, 200)
	if trail := got.Trails[e.Key]; trail.OwnerSession != "claude:old" || trail.FirstClaim.SessionKey != "claude:old" || len(trail.Associations) != 0 {
		t.Fatalf("inactive session changed persisted claim: %#v", trail)
	}
}

func TestReconcileTrailsAssociationsAreIdempotentAndRejectBareGitNumbers(t *testing.T) {
	trail := wtfTrail{Key: "acme/api#11", Owner: "acme", Repo: "api", Number: 11}
	prior := wtfState{Trails: map[string]wtfTrail{trail.Key: trail}}
	worktrees := map[string]wtfWorktree{"/wt/git": {Repo: "acme/api", Path: "/wt/git", Exists: true, GitEvidence: []wtfGitEvidence{{Source: "branch", Text: "fix trail 11 and acme/api#11"}}}}
	first := reconcileTrails(prior, nil, nil, worktrees, 200)
	second := reconcileTrails(first, nil, nil, worktrees, 300)
	if got := second.Trails[trail.Key].Associations; len(got) != 1 || got[0].Evidence != "acme/api#11" {
		t.Fatalf("git associations: %#v", got)
	}
}

func TestChooseInitialCanonical(t *testing.T) {
	claims := []wtfClaim{{SessionKey: "claude:first", Worktree: "/wt/first", At: 100}, {SessionKey: "claude:later", Worktree: "/wt/match", At: 200}}
	worktrees := map[string]wtfWorktree{"/wt/first": {Path: "/wt/first", Branch: "feat/other"}, "/wt/match": {Path: "/wt/match", Branch: "feat/1223"}}
	if got := chooseInitialCanonical(wtfTrail{SourceBranch: "feat/1223"}, claims, worktrees); got != "/wt/match" {
		t.Fatalf("source branch match: got %q", got)
	}
	if got := chooseInitialCanonical(wtfTrail{}, claims, worktrees); got != "/wt/first" {
		t.Fatalf("first claim fallback: got %q", got)
	}
	prior := wtfState{Trails: map[string]wtfTrail{"acme/api#12": {Key: "acme/api#12", Owner: "acme", Repo: "api", Number: 12, CanonicalWorktree: "/wt/first", SourceBranch: "feat/1223"}}}
	got := reconcileTrails(prior, nil, nil, worktrees, 300)
	if got.Trails["acme/api#12"].CanonicalWorktree != "/wt/first" {
		t.Fatalf("persisted canonical moved: %#v", got.Trails["acme/api#12"])
	}
}

func TestReconcileTrailsCanonicalFallbackUsesClaimsOnly(t *testing.T) {
	const key = "acme/api#13"
	claimEvidence := trailEvidence{Key: key, Owner: "acme", Repo: "api", Number: 13, Matched: "api#13", Source: "user", At: 100, Resolved: true}
	session := wtfSession{Agent: AgentClaude, ID: "claim", Repo: "acme/api", Cwd: "/wt/claim", Active: true}

	for _, test := range []struct {
		name         string
		sourceBranch string
		want         string
	}{
		{name: "unmatched association cannot beat claim", sourceBranch: "feat/source", want: "/wt/claim"},
		{name: "source matching association wins", sourceBranch: "feat/git", want: "/wt/git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			prior := wtfState{Trails: map[string]wtfTrail{key: {Key: key, Owner: "acme", Repo: "api", Number: 13, SourceBranch: test.sourceBranch}}}
			worktrees := map[string]wtfWorktree{
				"/wt/git":   {Repo: "acme/api", Path: "/wt/git", Branch: "feat/git", FirstSeen: 50, Exists: true, GitEvidence: []wtfGitEvidence{{Source: "unmerged subjects", Text: key}}},
				"/wt/claim": {Repo: "acme/api", Path: "/wt/claim", Branch: "feat/claim", FirstSeen: 90, Exists: true},
			}

			got := reconcileTrails(prior, []wtfSession{session}, map[string][]trailEvidence{"claude:claim": {claimEvidence}}, worktrees, 200)
			if trail := got.Trails[key]; trail.CanonicalWorktree != test.want {
				t.Fatalf("canonical worktree=%q, want %q: %#v", trail.CanonicalWorktree, test.want, trail)
			}
		})
	}
}

func TestScanWTFIntegration(t *testing.T) {
	home := t.TempDir()
	oldWorktree := t.TempDir()
	activeWorktree := t.TempDir()
	exportPath := filepath.Join(home, "active.json")
	if err := os.WriteFile(exportPath, []byte(`{"v":1,"id":"T-active","messages":[{"role":"user","createdAt":"2026-09-29T09:00:00Z","content":[{"type":"text","text":"Continue acme/api#1223"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	oldAt := now.Add(-48 * time.Hour).Unix()
	oldClaim := wtfClaim{SessionKey: "claude:old", Worktree: oldWorktree, At: oldAt, Evidence: "acme/api#1223"}
	prior := newWTFState(oldAt)
	prior.Sessions["claude:old"] = wtfSession{Agent: AgentClaude, ID: "old", Repo: "acme/api", Cwd: oldWorktree, State: "ended", LastActivity: oldAt}
	prior.Trails["acme/api#1223"] = wtfTrail{
		Key: "acme/api#1223", Owner: "acme", Repo: "api", Number: 1223,
		OwnerSession: "claude:old", CanonicalWorktree: oldWorktree, FirstClaim: &oldClaim,
		Associations: []wtfAssociation{{SessionKey: "claude:old", Worktree: oldWorktree, At: oldAt, Evidence: oldClaim.Evidence, Source: "user"}},
		FirstSeen:    oldAt, LastSeen: oldAt,
	}
	prior.Worktrees[oldWorktree] = wtfWorktree{Repo: "acme/api", Path: oldWorktree, Branch: "feat/old", Exists: true, DirtyFiles: 1, UnmergedCommits: 0, TrailKeys: []string{"acme/api#1223"}, SessionKeys: []string{"claude:old"}, FirstSeen: oldAt, LastSeen: oldAt, LastWIPAt: oldAt}

	var commands []string
	run := func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if name == "entire" {
			return []byte(`{"number":1223,"branch":"feat/old","base":"main","title":"Registry","status":"open"}`), nil
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + oldWorktree + "\nHEAD old\nbranch refs/heads/feat/old\n\nworktree " + activeWorktree + "\nHEAD active\nbranch refs/heads/feat/new\n"), nil
		case strings.Contains(joined, "status --porcelain") && dir == oldWorktree:
			return []byte(" M old.go\n"), nil
		case strings.Contains(joined, "status --porcelain"):
			return nil, nil
		case strings.Contains(joined, "symbolic-ref --quiet refs/remotes/origin/HEAD"):
			return []byte("refs/remotes/origin/main\n"), nil
		case strings.Contains(joined, "rev-list --count"):
			return []byte("0\n"), nil
		case strings.Contains(joined, "log --format=%s"), strings.Contains(joined, "diff --no-ext-diff"):
			return nil, nil
		default:
			return nil, exec.ErrNotFound
		}
	}
	summaryCalls := 0
	deps := wtfScanDeps{
		Inventory: wtfInventoryDeps{
			Today: func(string, int64, *time.Location) []handoverItem {
				return []handoverItem{{Agent: AgentAmp, SessionID: "T-active", Repo: "acme/api", Cwd: activeWorktree, Branch: "feat/new", Path: exportPath, LastActivity: now.Unix()}}
			},
			Live: func(string) []liveSession {
				return []liveSession{{Agent: AgentAmp, SessionID: "T-active", Cwd: activeWorktree, Branch: "feat/new", Path: exportPath, Status: "busy", UpdatedAt: now.UnixMilli()}}
			},
		},
		Run: run,
		Summarize: func(session wtfSession, _ string, _ wtfSummaryCache) (wtfSummary, wtfSummaryCache, error) {
			summaryCalls++
			return wtfSummary{Summary: "Active challenger"}, wtfSummaryCache{InputHash: "active", Value: wtfSummary{Summary: "Active challenger"}}, nil
		},
		Now: func() time.Time { return now },
	}

	got, err := scanWTF(context.Background(), home, prior, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 1 || !got.Sessions["amp:T-active"].Active {
		t.Fatalf("sessions: %#v", got.Sessions)
	}
	if len(got.Trails) != 1 || len(got.Worktrees) != 2 {
		t.Fatalf("registry sizes: trails=%d worktrees=%d", len(got.Trails), len(got.Worktrees))
	}
	trail := got.Trails["acme/api#1223"]
	if trail.OwnerSession != "claude:old" || trail.FirstClaim == nil || trail.FirstClaim.SessionKey != "claude:old" || trail.CanonicalWorktree != oldWorktree {
		t.Fatalf("persisted ownership changed: %#v", trail)
	}
	var activeKinds []string
	for _, finding := range got.Findings {
		if finding.Active {
			activeKinds = append(activeKinds, finding.Kind)
		}
	}
	sort.Strings(activeKinds)
	if want := []string{"existing-wip-elsewhere", "outside-canonical"}; !reflect.DeepEqual(activeKinds, want) {
		t.Fatalf("active findings=%v, want %v; all findings: %#v", activeKinds, want, got.Findings)
	}
	if summaryCalls != 1 {
		t.Fatalf("summary calls=%d, want 1", summaryCalls)
	}
	for _, command := range commands {
		if strings.Contains(command, "trail list") {
			t.Fatalf("global trail listing invoked: %s", command)
		}
	}
}

func TestScanWTFChoosesExistingPriorRepoSeedDeterministically(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	existing := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	orders := [][]string{{missing, existing}, {existing, missing}}
	for i, order := range orders {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			prior := newWTFState(now.Unix())
			for _, path := range order {
				prior.Worktrees[path] = wtfWorktree{Repo: "acme/api", Path: path}
			}
			var dirs []string
			deps := wtfScanDeps{
				Inventory: wtfInventoryDeps{Today: func(string, int64, *time.Location) []handoverItem { return nil }, Live: func(string) []liveSession { return nil }},
				Run: func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
					joined := strings.Join(args, " ")
					if name == "git" && strings.Contains(joined, "worktree list --porcelain") {
						dirs = append(dirs, dir)
						return []byte("worktree " + existing + "\nHEAD abc\nbranch refs/heads/main\n"), nil
					}
					switch {
					case strings.Contains(joined, "status --porcelain"), strings.Contains(joined, " log "), strings.Contains(joined, " diff "):
						return nil, nil
					case strings.Contains(joined, "symbolic-ref"):
						return []byte("refs/remotes/origin/main\n"), nil
					case strings.Contains(joined, "rev-list --count"):
						return []byte("0\n"), nil
					}
					return nil, exec.ErrNotFound
				},
				Summarize: func(wtfSession, string, wtfSummaryCache) (wtfSummary, wtfSummaryCache, error) {
					return wtfSummary{}, wtfSummaryCache{}, nil
				},
				Now: func() time.Time { return now },
			}
			if _, err := scanWTF(context.Background(), t.TempDir(), prior, deps); err != nil {
				t.Fatal(err)
			}
			if want := []string{existing}; !reflect.DeepEqual(dirs, want) {
				t.Fatalf("worktree-list dirs=%v, want deterministic existing seed %v", dirs, want)
			}
		})
	}
}

func TestScanWTFReportsNewRepoWorktreeListFailureAndKeepsSession(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	cwd := t.TempDir()
	deps := wtfScanDeps{
		Inventory: wtfInventoryDeps{
			Today: func(string, int64, *time.Location) []handoverItem {
				return []handoverItem{{Agent: AgentAmp, SessionID: "T-new", Repo: "acme/new", Cwd: cwd, LastActivity: now.Unix()}}
			},
			Live: func(string) []liveSession {
				return []liveSession{{Agent: AgentAmp, SessionID: "T-new", Cwd: cwd, Status: "busy"}}
			},
		},
		Run: func(context.Context, string, string, ...string) ([]byte, error) {
			return nil, errors.New("git unavailable")
		},
		Summarize: func(wtfSession, string, wtfSummaryCache) (wtfSummary, wtfSummaryCache, error) {
			return wtfSummary{}, wtfSummaryCache{}, nil
		},
		Now: func() time.Time { return now },
	}

	got, err := scanWTF(context.Background(), t.TempDir(), newWTFState(now.Unix()), deps)
	if err == nil || !strings.Contains(err.Error(), "git worktrees for acme/new") || !strings.Contains(err.Error(), "git unavailable") {
		t.Fatalf("error=%v, want repo/source-named git worktree failure", err)
	}
	if session, ok := got.Sessions["amp:T-new"]; !ok || !session.Active || session.Cwd != cwd {
		t.Fatalf("partial session state: %#v", got.Sessions)
	}
	if worktree, ok := got.Worktrees[cwd]; ok {
		t.Fatalf("unavailable worktree was presented as known: %#v", worktree)
	}
	if len(got.Findings) != 0 {
		t.Fatalf("unknown worktree facts produced findings: %#v", got.Findings)
	}
}

func TestDetectWTFFindingsDuplicateActiveClaim(t *testing.T) {
	state := findingState()
	state.Sessions["claude:a"] = wtfSession{Active: true, Cwd: "/wt/a"}
	state.Sessions["amp:b"] = wtfSession{Active: true, Cwd: "/wt/b"}
	state.Worktrees["/wt/a"] = wtfWorktree{Path: "/wt/a", Exists: true}
	state.Worktrees["/wt/b"] = wtfWorktree{Path: "/wt/b", Exists: true}
	trail := state.Trails["acme/api#7"]
	trail.Associations = []wtfAssociation{{SessionKey: "amp:b", Worktree: "/wt/b"}, {SessionKey: "claude:a", Worktree: "/wt/a"}, {SessionKey: "claude:a", Worktree: "/wt/a"}}
	state.Trails[trail.Key] = trail

	got := detectWTFFindings(state, 100)
	finding := onlyFinding(t, got, "duplicate-active-claim")
	wantID := wtfFindingID("duplicate-active-claim", trail.Key, []string{"amp:b", "claude:a"}, []string{"/wt/a", "/wt/b"})
	if finding.ID != wantID || finding.Severity != 3 {
		t.Fatalf("finding: %#v, want ID %q severity 3", finding, wantID)
	}
	if reversed := wtfFindingID("duplicate-active-claim", trail.Key, []string{"claude:a", "amp:b"}, []string{"/wt/b", "/wt/a"}); reversed != wantID {
		t.Fatalf("ID changed with input ordering: %q != %q", reversed, wantID)
	}

	trail.Associations = trail.Associations[:1]
	state.Trails[trail.Key] = trail
	if got := detectWTFFindings(state, 100); findingOfKind(got, "duplicate-active-claim") != nil {
		t.Fatalf("one active session triggered duplicate claim: %#v", got)
	}
}

func TestDetectWTFFindingsExistingWIPElsewhere(t *testing.T) {
	for _, wip := range []wtfWorktree{
		{Path: "/wt/old", Exists: true, DirtyFiles: 2, LastSeen: 100 - 2*24*60*60},
		{Path: "/wt/old", Exists: true, UnmergedCommits: 3, LastSeen: 100 - 2*24*60*60},
	} {
		state := findingState()
		state.Sessions["claude:new"] = wtfSession{Active: true, Cwd: "/wt/new"}
		state.Worktrees["/wt/new"] = wtfWorktree{Path: "/wt/new", Exists: true}
		state.Worktrees["/wt/old"] = wip
		trail := state.Trails["acme/api#7"]
		trail.Associations = []wtfAssociation{{SessionKey: "claude:new", Worktree: "/wt/new"}, {Worktree: "/wt/old"}}
		state.Trails[trail.Key] = trail
		finding := onlyFinding(t, detectWTFFindings(state, 100), "existing-wip-elsewhere")
		if finding.Severity != 2 || !strings.Contains(finding.Explanation, "/wt/old") {
			t.Fatalf("finding: %#v", finding)
		}

		clean := state.Worktrees["/wt/old"]
		clean.DirtyFiles, clean.UnmergedCommits = 0, 0
		state.Worktrees["/wt/old"] = clean
		if got := detectWTFFindings(state, 100); findingOfKind(got, "existing-wip-elsewhere") != nil {
			t.Fatalf("clean associated worktree triggered finding: %#v", got)
		}
	}
}

func TestDetectWTFFindingsExistingWIPElsewhereIgnoresUnknownGitState(t *testing.T) {
	state := findingState()
	state.Sessions["claude:new"] = wtfSession{Active: true, Cwd: "/wt/new"}
	state.Worktrees["/wt/new"] = wtfWorktree{Path: "/wt/new", Exists: true}
	state.Worktrees["/wt/old"] = wtfWorktree{Path: "/wt/old", Exists: true, DirtyFiles: 2, GitError: "git status failed"}
	trail := state.Trails["acme/api#7"]
	trail.Associations = []wtfAssociation{{SessionKey: "claude:new", Worktree: "/wt/new"}, {Worktree: "/wt/old"}}
	state.Trails[trail.Key] = trail

	if got := detectWTFFindings(state, 100); findingOfKind(got, "existing-wip-elsewhere") != nil {
		t.Fatalf("unknown worktree state triggered finding: %#v", got)
	}
}

func TestDetectWTFFindingsOutsideCanonical(t *testing.T) {
	state := findingState()
	state.Sessions["claude:owner"] = wtfSession{Active: true, Cwd: "/wt/moved"}
	state.Worktrees["/wt/moved"] = wtfWorktree{Path: "/wt/moved", Exists: true}
	state.Worktrees["/wt/canonical"] = wtfWorktree{Path: "/wt/canonical", Exists: true}
	trail := state.Trails["acme/api#7"]
	trail.OwnerSession = "claude:owner"
	trail.Associations = []wtfAssociation{{SessionKey: "claude:owner", Worktree: "/wt/moved"}}
	state.Trails[trail.Key] = trail
	finding := onlyFinding(t, detectWTFFindings(state, 100), "outside-canonical")
	if finding.Severity != 2 || finding.Owner != "claude:owner" || finding.Challenger != "claude:owner" {
		t.Fatalf("finding: %#v", finding)
	}

	state.Sessions["claude:owner"] = wtfSession{Active: true, Cwd: "/wt/canonical"}
	if got := detectWTFFindings(state, 100); findingOfKind(got, "outside-canonical") != nil {
		t.Fatalf("canonical session triggered finding: %#v", got)
	}
}

func TestDetectWTFFindingsDefaultBranch(t *testing.T) {
	state := findingState()
	state.Sessions["claude:a"] = wtfSession{Active: true, Cwd: "/wt/a"}
	state.Worktrees["/wt/a"] = wtfWorktree{Path: "/wt/a", Exists: true, Branch: "main", DefaultBranch: "origin/main"}
	trail := state.Trails["acme/api#7"]
	trail.Associations = []wtfAssociation{{SessionKey: "claude:a", Worktree: "/wt/a"}}
	state.Trails[trail.Key] = trail
	if finding := onlyFinding(t, detectWTFFindings(state, 100), "default-branch"); finding.Severity != 3 {
		t.Fatalf("finding: %#v", finding)
	}

	worktree := state.Worktrees["/wt/a"]
	worktree.Branch = "feat/a"
	state.Worktrees["/wt/a"] = worktree
	if got := detectWTFFindings(state, 100); findingOfKind(got, "default-branch") != nil {
		t.Fatalf("feature branch triggered finding: %#v", got)
	}
}

func TestDetectWTFFindingsMissingCanonical(t *testing.T) {
	state := findingState()
	state.Sessions["claude:a"] = wtfSession{Active: true, Cwd: "/wt/a"}
	state.Worktrees["/wt/a"] = wtfWorktree{Path: "/wt/a", Exists: true}
	state.Worktrees["/wt/canonical"] = wtfWorktree{Path: "/wt/canonical", Exists: false}
	trail := state.Trails["acme/api#7"]
	trail.Associations = []wtfAssociation{{SessionKey: "claude:a", Worktree: "/wt/a"}}
	state.Trails[trail.Key] = trail
	if finding := onlyFinding(t, detectWTFFindings(state, 100), "missing-canonical"); finding.Severity != 1 {
		t.Fatalf("finding: %#v", finding)
	}

	state.Sessions["claude:a"] = wtfSession{Cwd: "/wt/a"}
	state.Worktrees["/wt/a"] = wtfWorktree{Path: "/wt/a", Exists: true}
	if got := detectWTFFindings(state, 100); findingOfKind(got, "missing-canonical") != nil {
		t.Fatalf("inactive clean association triggered finding: %#v", got)
	}
}

func TestDetectWTFFindingsMissingCanonicalIgnoresUnknownGitState(t *testing.T) {
	state := findingState()
	state.Worktrees["/wt/canonical"] = wtfWorktree{Path: "/wt/canonical", Exists: false}
	state.Worktrees["/wt/other"] = wtfWorktree{Path: "/wt/other", Exists: true, DirtyFiles: 1, GitError: "git diff failed"}
	trail := state.Trails["acme/api#7"]
	trail.Associations = []wtfAssociation{{Worktree: "/wt/other"}}
	state.Trails[trail.Key] = trail

	if got := detectWTFFindings(state, 100); findingOfKind(got, "missing-canonical") != nil {
		t.Fatalf("unknown worktree state triggered finding: %#v", got)
	}
}

func TestDetectWTFFindingsMissingCanonicalAfterActiveSessionMoves(t *testing.T) {
	state := findingState()
	state.Sessions["claude:a"] = wtfSession{Active: true, Cwd: "/wt/new"}
	state.Worktrees["/wt/canonical"] = wtfWorktree{Path: "/wt/canonical", Exists: false}
	state.Worktrees["/wt/old"] = wtfWorktree{Path: "/wt/old", Exists: true}
	state.Worktrees["/wt/new"] = wtfWorktree{Path: "/wt/new", Exists: true}
	trail := state.Trails["acme/api#7"]
	trail.Associations = []wtfAssociation{{SessionKey: "claude:a", Worktree: "/wt/old"}}
	state.Trails[trail.Key] = trail

	finding := onlyFinding(t, detectWTFFindings(state, 100), "missing-canonical")
	if !containsString(finding.Worktrees, "/wt/new") || !strings.Contains(finding.Explanation, "/wt/new") {
		t.Fatalf("moved active worktree missing from finding: %#v", finding)
	}
}

func TestMergeWTFFindingsClearRecurrenceAndContinuousDelivery(t *testing.T) {
	current := map[string]wtfFinding{"f": {ID: "f", Kind: "default-branch", Active: true, FirstSeen: 10, LastSeen: 10, Occurrence: 1}}
	first := mergeWTFFindings(nil, current, 10)
	first["f"] = withFindingDelivery(first["f"])
	continuous := mergeWTFFindings(first, current, 20)
	if continuous["f"].Occurrence != 1 || len(continuous["f"].Delivery) != 1 || continuous["f"].FirstSeen != 10 {
		t.Fatalf("continuous finding lost history: %#v", continuous["f"])
	}
	cleared := mergeWTFFindings(continuous, nil, 30)
	if cleared["f"].Active || cleared["f"].LastSeen != 20 || len(cleared) != 1 {
		t.Fatalf("cleared finding: %#v", cleared["f"])
	}
	recurred := mergeWTFFindings(cleared, current, 40)
	if !recurred["f"].Active || recurred["f"].Occurrence != 2 || recurred["f"].FirstSeen != 10 || recurred["f"].LastSeen != 40 || len(recurred["f"].Delivery) != 0 {
		t.Fatalf("recurred finding: %#v", recurred["f"])
	}
}

func findingState() wtfState {
	return wtfState{
		Sessions: map[string]wtfSession{},
		Worktrees: map[string]wtfWorktree{
			"/wt/canonical": {Path: "/wt/canonical", Exists: true},
		},
		Trails: map[string]wtfTrail{
			"acme/api#7": {Key: "acme/api#7", OwnerSession: "claude:owner", CanonicalWorktree: "/wt/canonical"},
		},
	}
}

func onlyFinding(t *testing.T, findings map[string]wtfFinding, kind string) wtfFinding {
	t.Helper()
	if finding := findingOfKind(findings, kind); finding != nil {
		return *finding
	}
	t.Fatalf("missing %s finding in %#v", kind, findings)
	return wtfFinding{}
}

func findingOfKind(findings map[string]wtfFinding, kind string) *wtfFinding {
	for _, finding := range findings {
		if finding.Kind == kind {
			copy := finding
			return &copy
		}
	}
	return nil
}

func withFindingDelivery(finding wtfFinding) wtfFinding {
	finding.Delivery = map[string]wtfDeliveryStatus{"desktop": {State: "sent", Attempts: 1}}
	return finding
}
