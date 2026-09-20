package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
			name: "pane found", bin: "/b/hunk", dir: "/repo", dirOK: true, pane: "UUID-1",
			wantRun: true, wantMsg: "hunk ended — claude was told to load the skill",
		},
		{
			// No pane to type into is not a failure: the prompt goes to the
			// clipboard so it's one paste away wherever claude actually is.
			name: "no pane", bin: "/b/hunk", dir: "/repo", dirOK: true, pane: "",
			wantRun: true, wantMsg: "hunk ended — prompt for claude copied to the clipboard",
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
		})
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
