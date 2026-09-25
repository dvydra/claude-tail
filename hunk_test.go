package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A full-screen child that CRASHES never disables the input modes it turned
// on. Mouse reporting left on means the terminal answers every mouse move with
// an escape sequence, fed straight into the tail's keyboard reader — the tail
// looks possessed and the only cure is `reset`.
func TestInputModeResetDisablesEveryReportingMode(t *testing.T) {
	for name, seq := range map[string]string{
		"mouse (normal)":       "\x1b[?1000l",
		"mouse (button-event)": "\x1b[?1002l",
		"mouse (any-event)":    "\x1b[?1003l",
		"focus reporting":      "\x1b[?1004l",
		"SGR extended coords":  "\x1b[?1006l",
		"bracketed paste":      "\x1b[?2004l",
	} {
		if !strings.Contains(inputModeReset, seq) {
			t.Errorf("inputModeReset doesn't disable %s (%q):\n%q", name, seq, inputModeReset)
		}
	}
	// Every mode is DISABLED — an `h` here would switch one on, which is the
	// one way this constant could make things worse than doing nothing.
	if strings.Contains(inputModeReset, "h") {
		t.Errorf("inputModeReset enables a mode: %q", inputModeReset)
	}
	// The alt screen stays out of it on purpose: undoing `?1049` when the child
	// already left it restores a saved cursor position and would move ours.
	if strings.Contains(inputModeReset, "1049") {
		t.Errorf("inputModeReset touches the alt screen: %q", inputModeReset)
	}
}

func TestHunkKeyRoutesToOverlay(t *testing.T) {
	// `h` has to reach the render goroutine through overlayCh, not actionCh:
	// hunk is a full-screen child process, so the keyboard reader must stop
	// reading and park while it owns the tty.
	for _, b := range []byte{'h', 'H'} {
		if got := keyActionFor(b); got != keyHunk {
			t.Errorf("%q → %v, want keyHunk", b, got)
		}
	}
}

// hunk.dev's install.sh drops a standalone binary in ~/.hunk/bin, which is not
// on PATH — that's where it sits on this machine, and a viewer that only
// consulted PATH would report it missing on a machine that has it.
func TestHunkBinFallsBackToDotHunk(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".hunk", "bin", "hunk")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := func(string) (string, error) { return "", errors.New("not found") }

	if got := hunkBin(home, missing); got != bin {
		t.Errorf("hunkBin = %q, want the ~/.hunk copy %q", got, bin)
	}
	// PATH still wins when it has one — an explicitly installed hunk is the
	// one the user's own shell would run.
	onPath := func(string) (string, error) { return "/opt/homebrew/bin/hunk", nil }
	if got, want := hunkBin(home, onPath), "/opt/homebrew/bin/hunk"; got != want {
		t.Errorf("hunkBin = %q, want %q", got, want)
	}
	if got := hunkBin(t.TempDir(), missing); got != "" {
		t.Errorf("hunkBin = %q with nothing installed, want empty", got)
	}
}

func TestPlanHunk(t *testing.T) {
	for _, c := range []struct {
		name     string
		bin, dir string
		dirOK    bool
		pane     string
		wantRun  bool
		wantMsg  string
	}{
		{
			// Not installed is a message, not a screen hand-over: suspending the
			// bar and handing the tty to nothing would blank the tail for a beat
			// and teach the user that `h` is broken rather than absent.
			name: "not installed", bin: "", dir: "/repo", dirOK: true, pane: "p",
			wantRun: false, wantMsg: "hunk not installed — see hunk.dev",
		},
		{
			// The normal end state of a worktree is that its directory is gone;
			// `hunk diff` there would fail inside the alt-screen where the error
			// is hardest to read.
			name: "folder gone", bin: "/b/hunk", dir: "/gone", dirOK: false, pane: "p",
			wantRun: false, wantMsg: "hunk: session folder is gone",
		},
		{
			// Msg is a refusal only. What to say when hunk is DONE depends on a
			// daemon that hasn't been started yet, so the plan must not guess at
			// it — that's hunkOutcome's job.
			name: "pane found", bin: "/b/hunk", dir: "/repo", dirOK: true, pane: "UUID-1",
			wantRun: true, wantMsg: "",
		},
		{
			// No pane to type into is not a refusal: the review still happens,
			// and the prompt goes to the clipboard so it's one paste away.
			name: "no pane", bin: "/b/hunk", dir: "/repo", dirOK: true, pane: "",
			wantRun: true, wantMsg: "",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := planHunk(c.bin, c.dir, c.dirOK, c.pane)
			if got := p.Bin != ""; got != c.wantRun {
				t.Errorf("runnable = %v, want %v (bin %q)", got, c.wantRun, p.Bin)
			}
			if p.Msg != c.wantMsg {
				t.Errorf("Msg = %q, want %q", p.Msg, c.wantMsg)
			}
			if c.wantRun && p.Pane != c.pane {
				t.Errorf("Pane = %q, want %q carried through to the run", p.Pane, c.pane)
			}
		})
	}
}

// Every outcome says something, and each says a DIFFERENT thing. The one that
// matters is hunkUnclaimed: the review happened but the agent was never told,
// and from the tail's side of the screen that is indistinguishable from
// success. An earlier version reported "agent was told to load the skill"
// whether or not a byte had been sent, because the wording was decided before
// hunk had even started.
func TestHunkOutcomeMessages(t *testing.T) {
	seen := map[string]hunkOutcome{}
	for _, o := range []hunkOutcome{hunkNotRun, hunkPrompted, hunkCopied, hunkUnclaimed} {
		msg := o.msg()
		if msg == "" {
			t.Errorf("outcome %d has no message", o)
		}
		if prev, dup := seen[msg]; dup {
			t.Errorf("outcomes %d and %d share the message %q", prev, o, msg)
		}
		seen[msg] = o
	}
	if got := hunkUnclaimed.msg(); !strings.Contains(got, "wasn't told") {
		t.Errorf("hunkUnclaimed says %q, want it to say the agent wasn't told", got)
	}
	if got := hunkPrompted.msg(); strings.Contains(got, "clipboard") {
		t.Errorf("hunkPrompted says %q, want it not to mention the clipboard", got)
	}
}

func TestHunkClaudePane(t *testing.T) {
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
	// one in the tab — that one is a different session, and typing a review
	// prompt into it would interrupt unrelated work.
	if got, want := hunkClaudePane("w0t1", "/proj/bbb.jsonl", procs, sessionOf), "UUID-B"; got != want {
		t.Errorf("pane = %q, want %q (the claude writing the tailed session)", got, want)
	}

	// No match on the transcript, but only one claude in the tab: that's the
	// agent beside us, same reasoning adopt.go uses for its exactly-one rule.
	solo := []claudeProc{procs[0], procs[2]}
	if got, want := hunkClaudePane("w0t1", "/proj/other.jsonl", solo, sessionOf), "UUID-A"; got != want {
		t.Errorf("pane = %q, want %q (the only claude in the tab)", got, want)
	}

	// Two in the tab and neither is ours — ambiguous, so say nothing rather
	// than type into whichever came back first.
	if got := hunkClaudePane("w0t1", "/proj/other.jsonl", procs, sessionOf); got != "" {
		t.Errorf("pane = %q, want empty when the tab is ambiguous", got)
	}

	// The tab is the whole horizon (nearby.go): a claude in another tab is
	// never typed into, however lonely we are.
	if got := hunkClaudePane("w0t5", "/proj/aaa.jsonl", procs, sessionOf); got != "" {
		t.Errorf("pane = %q, want empty when nothing is in this tab", got)
	}
	// Off iTerm there is no tab, and no pane may be inferred.
	if got := hunkClaudePane("", "/proj/aaa.jsonl", procs, sessionOf); got != "" {
		t.Errorf("pane = %q, want empty off iTerm", got)
	}
}

func TestHunkPaneMatcherAcceptsAmpThreadID(t *testing.T) {
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
	if got := hunkClaudePane("w0t1", "T-target", procs, sessionOf); got != "UUID-B" {
		t.Fatalf("pane=%q", got)
	}
}

func TestHunkNotifyScript(t *testing.T) {
	s := hunkNotifyScript("UUID-1", `say "hi" \o/`)

	for _, want := range []string{
		`(id of s) is "UUID-1"`, // addressed by session id, never by index
		"write text",
		`say \"hi\" \\o/`, // quotes and backslashes escaped for AppleScript
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
	// A pane that has since closed must not make osascript raise — the prompt
	// is a nicety, and the review itself is already on screen.
	if !strings.Contains(s, "try") {
		t.Errorf("script has no try block:\n%s", s)
	}
}

// The prompt is the one hunk's own docs tell you to give the agent. Pinned
// because it is a quotation, not a phrasing of ours to improve.
func TestHunkPromptMatchesTheDocumentedOne(t *testing.T) {
	want := "Load the Hunk skill and use it for this review. Run `hunk skill path` to get the skill path."
	if hunkPrompt != want {
		t.Errorf("hunkPrompt = %q, want the documented %q", hunkPrompt, want)
	}
}

// The session's cwd is the one at the END of the transcript, not the start.
// A session that re-enters a worktree keeps writing to the same transcript from
// its new directory, so the head's cwd names a checkout the agent left behind —
// the same reason tailMeta takes the branch from the tail.
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

// hunkReviewDir is what `h` reviews. The transcript wins because it is the one
// place that knows where the agent is NOW; our own pwd was fixed at startup.
// A resumed workspace opens where the session is now — inside the worktree it
// moved into — and falls back to where it started once that worktree is gone.
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

func TestHunkReviewDirPrefersTheSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := `{"type":"user","cwd":"/repo"}` + "\n" + `{"type":"assistant","cwd":"/repo/.claude/worktrees/task"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := hunkReviewDir(path, "/somewhere/else"), "/repo/.claude/worktrees/task"; got != want {
		t.Errorf("hunkReviewDir = %q, want the session's cwd %q", got, want)
	}
	// No transcript to read (a missing file, or a codex/agy session whose
	// records carry no cwd at all) leaves the pre-existing behaviour intact.
	if got, want := hunkReviewDir(filepath.Join(dir, "gone.jsonl"), "/somewhere/else"), "/somewhere/else"; got != want {
		t.Errorf("hunkReviewDir on a missing file = %q, want the pwd fallback %q", got, want)
	}
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := hunkReviewDir(empty, "/somewhere/else"), "/somewhere/else"; got != want {
		t.Errorf("hunkReviewDir on an empty file = %q, want the pwd fallback %q", got, want)
	}
}
