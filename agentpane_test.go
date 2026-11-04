package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTabAgentPane(t *testing.T) {
	// sessionOf stands in for resolveClaudeSession: which transcript each
	// running claude is writing.
	procs := []claudeProc{
		{pid: 1, itermID: "w0t1p0:UUID-A"},
		{pid: 2, itermID: "w0t1p1:UUID-B"},
		{pid: 3, itermID: "w0t9p0:UUID-FAR"},
	}
	sessionOf := func(p claudeProc) string {
		switch p.pid {
		case 1:
			return "/proj/aaa.jsonl"
		case 2:
			return "/proj/bbb.jsonl"
		}
		return "/proj/far.jsonl"
	}

	// The claude writing the transcript we're tailing wins, even with a second
	// one in the tab: that one is a different session.
	if got, want := tabAgentPane("w0t1", "/proj/bbb.jsonl", procs, sessionOf), "UUID-B"; got != want {
		t.Errorf("pane = %q, want %q (the claude writing the tailed session)", got, want)
	}

	// No match on the transcript, but only one claude in the tab: that's the
	// agent beside us, same reasoning adopt.go uses for its exactly-one rule.
	solo := []claudeProc{procs[0], procs[2]}
	if got, want := tabAgentPane("w0t1", "/proj/other.jsonl", solo, sessionOf), "UUID-A"; got != want {
		t.Errorf("pane = %q, want %q (the only claude in the tab)", got, want)
	}

	// Two in the tab and neither is ours: ambiguous, so say nothing.
	if got := tabAgentPane("w0t1", "/proj/other.jsonl", procs, sessionOf); got != "" {
		t.Errorf("pane = %q, want empty when the tab is ambiguous", got)
	}

	// The tab is the whole horizon (nearby.go): a claude in another tab is
	// never typed into.
	if got := tabAgentPane("w0t5", "/proj/aaa.jsonl", procs, sessionOf); got != "" {
		t.Errorf("pane = %q, want empty when nothing is in this tab", got)
	}
	// Off iTerm there is no tab, and no pane may be inferred.
	if got := tabAgentPane("", "/proj/aaa.jsonl", procs, sessionOf); got != "" {
		t.Errorf("pane = %q, want empty off iTerm", got)
	}
}

func TestTabAgentPaneAcceptsAmpThreadID(t *testing.T) {
	procs := []claudeProc{
		{pid: 1, itermID: "w0t1p0:UUID-A"},
		{pid: 2, itermID: "w0t1p1:UUID-B"},
	}
	sessionOf := func(p claudeProc) string {
		if p.pid == 2 {
			return "T-target"
		}
		return "T-other"
	}
	if got := tabAgentPane("w0t1", "T-target", procs, sessionOf); got != "UUID-B" {
		t.Fatalf("pane=%q", got)
	}
}

// The session's cwd is the one at the END of the transcript, not the start.
// A session that re-enters a worktree keeps writing to the same transcript from
// its new directory, so the head's cwd names a checkout the agent left behind.
func TestTailCwdTakesTheNewestOne(t *testing.T) {
	lines := splitLines([]byte(`{"type":"user","cwd":"/repo","gitBranch":"main"}
{"type":"assistant","cwd":"/repo"}
{"type":"user","cwd":"/repo/.claude/worktrees/task","gitBranch":"wt"}
{"type":"assistant","cwd":"/repo/.claude/worktrees/task"}
`))
	if got, want := tailCwd(lines), "/repo/.claude/worktrees/task"; got != want {
		t.Errorf("tailCwd = %q, want %q", got, want)
	}
}

// A tail window rarely starts on a line boundary, and records without a cwd
// (tool results, summaries, the pr-link) are the bulk of what's in it.
func TestTailCwdSkipsUnusableLines(t *testing.T) {
	lines := splitLines([]byte(`{"type":"user","cwd":"/re` + "\n" + `{"type":"user","cwd":"/repo"}
{"type":"summary","summary":"a session"}
{"type":"pr-link","prNumber":7}

not json at all
`))
	if got, want := tailCwd(lines), "/repo"; got != want {
		t.Errorf("tailCwd = %q, want %q", got, want)
	}
	if got := tailCwd(nil); got != "" {
		t.Errorf("tailCwd(nil) = %q, want empty", got)
	}
}

// A resumed workspace opens where the session is now (inside the worktree it
// moved into) and falls back to where it started once that worktree is gone.
func TestWorkspaceCwd(t *testing.T) {
	dir := t.TempDir()
	start, wt := filepath.Join(dir, "repo"), filepath.Join(dir, "repo", ".claude", "worktrees", "task")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	body := `{"type":"user","cwd":"` + start + `"}` + "\n" + `{"type":"assistant","cwd":"` + wt + `"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := workspaceCwd(path); got != wt {
		t.Errorf("workspaceCwd = %q, want the worktree the session is in now %q", got, wt)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if got := workspaceCwd(path); got != start {
		t.Errorf("worktree removed: workspaceCwd = %q, want where the session started %q", got, start)
	}
}
