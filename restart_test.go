package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestRestartArgsPinsClaudeSessionAndSettings(t *testing.T) {
	got := restartArgs(restartState{
		Agent:    AgentClaude,
		Cur:      "/h/.claude/projects/x/abc.jsonl",
		Theme:    "dracula",
		Tools:    toolLines,
		WrapOff:  true,
		Collapse: 0,
		Cfg:      Config{Backfill: "all", ClaudeBin: "happy", ClaudeBinSet: true, NoTap: true},
	})
	want := []string{
		"--agent", "claude", "--no-pick", "--no-hook-install",
		"--theme", "dracula", "--backfill", "all", "--tool-style", "lines",
		"--no-wrap", "--no-collapse", "--claude-bin", "happy", "--no-tap",
		"--", "/h/.claude/projects/x/abc.jsonl",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restartArgs =\n %q\nwant\n %q", got, want)
	}
}

func TestRestartArgsAmpFollowsThread(t *testing.T) {
	const id = "T-0123abcd-0000-4000-8000-000000000000"
	if !validAmpThreadID(id) {
		t.Skip("fixture id no longer a valid Amp thread id")
	}
	got := restartArgs(restartState{
		Agent:    AgentAmp,
		Cur:      "/h/.cache/entire-tail/amp/" + id + ".jsonl",
		Tools:    toolDots,
		Collapse: 40,
	})
	if tail := got[len(got)-2:]; tail[0] != "--follow-session" || tail[1] != id {
		t.Fatalf("amp restart should follow the thread id, got %q", got)
	}
	if !slices.Contains(got, "--wrap") || !slices.Contains(got, "40") {
		t.Fatalf("wrap/collapse state lost: %q", got)
	}
}

func TestRestartArgsRoundTripThroughParser(t *testing.T) {
	args := restartArgs(restartState{Agent: AgentClaude, Cur: "/tmp/s.jsonl", Theme: "dracula", Tools: toolNone, Collapse: 12})
	cfg, action, err := parseCLI(args, func(string) string { return "" }, savedPrefs{})
	if err != nil || action != ActionRun {
		t.Fatalf("parseCLI(%q) = %v, %v", args, action, err)
	}
	if cfg.Pick != "never" || cfg.Theme != "dracula" || cfg.Collapse != "12" || cfg.NoWrap ||
		parseToolStyle(cfg.ToolStyle) != toolNone || !reflect.DeepEqual(cfg.Positional, []string{"/tmp/s.jsonl"}) {
		t.Fatalf("round trip lost state: %+v", cfg)
	}
}

func TestRestartBinaryLooksUpNameAgain(t *testing.T) {
	look := func(name string) (string, error) {
		if name == "entire-tail" {
			return "/new/bin/entire-tail", nil
		}
		return "", errors.New("nope")
	}
	if got := restartBinary("entire-tail", look); got != "/new/bin/entire-tail" {
		t.Fatalf("bare argv0 should be looked up on PATH, got %q", got)
	}
	bin := filepath.Join(t.TempDir(), "entire-tail")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := restartBinary(bin, look); got != bin {
		t.Fatalf("path argv0 should be used as given, got %q", got)
	}
}

// The chord: Ctrl-R arms, `y` confirms and the keyboard stops reading; any
// other key cancels and is swallowed rather than acted on.
func TestKeyboardRestartChord(t *testing.T) {
	run := func(input string) []keyAction {
		rd, wr, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer wr.Close()
		actionCh := make(chan keyAction, 8)
		restore := startKeyboardOn(rd, "", false, make(chan int, 1), actionCh, make(chan keyAction, 1), make(chan struct{}, 1), make(chan struct{}), make(chan string), make(chan []byte, 1))
		defer restore()
		for _, b := range []byte(input) {
			wr.Write([]byte{b})
			time.Sleep(20 * time.Millisecond) // one key per read
		}
		var got []keyAction
		deadline := time.After(200 * time.Millisecond)
		for {
			select {
			case a := <-actionCh:
				got = append(got, a)
			case <-deadline:
				return got
			}
		}
	}
	if got := run("\x12y"); !reflect.DeepEqual(got, []keyAction{keyRestartArm, keyRestart}) {
		t.Errorf("Ctrl-R y = %v", got)
	}
	if got := run("\x12tt"); !reflect.DeepEqual(got, []keyAction{keyRestartArm, keyRestartCancel, keyCycleTools}) {
		t.Errorf("Ctrl-R t t = %v (the t after Ctrl-R must be swallowed)", got)
	}
}
