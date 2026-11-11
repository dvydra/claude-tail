package main

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

func stubTabTitle(t *testing.T, out string, err error) *[][2]string {
	t.Helper()
	prev := tabTitleRun
	var calls [][2]string
	tabTitleRun = func(sid, title string) ([]byte, error) {
		calls = append(calls, [2]string{sid, title})
		return []byte(out), err
	}
	t.Cleanup(func() { tabTitleRun = prev })
	return &calls
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestThreadsTitleOutsideITermIsAQuietNoOp(t *testing.T) {
	calls := stubTabTitle(t, "", nil)
	var errOut bytes.Buffer
	if code := threadsTitle([]string{"Mirror", "Checks"}, envOf(nil), &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(*calls) != 0 || errOut.Len() != 0 {
		t.Fatalf("calls %v, stderr %q", *calls, errOut.String())
	}
}

func TestThreadsTitleSetsTheTabOfThisSession(t *testing.T) {
	calls := stubTabTitle(t, "", nil)
	var errOut bytes.Buffer
	env := envOf(map[string]string{"ITERM_SESSION_ID": "w0t3p1:ABCD-1234"})
	if code := threadsTitle([]string{"Mirror", "Checks"}, env, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if len(*calls) != 1 || (*calls)[0] != [2]string{"ABCD-1234", "Mirror Checks"} {
		t.Fatalf("calls %v", *calls)
	}
}

func TestThreadsTitleNeedsATitle(t *testing.T) {
	stubTabTitle(t, "", nil)
	var errOut bytes.Buffer
	if code := threadsTitle([]string{"  "}, envOf(nil), &errOut); code != threadsExitUsage {
		t.Fatalf("exit %d", code)
	}
}

func TestThreadsTitleReportsAFailedCall(t *testing.T) {
	stubTabTitle(t, "no tab for session X\n", errors.New("exit status 1"))
	var errOut bytes.Buffer
	env := envOf(map[string]string{"ITERM_SESSION_ID": "w0t0p0:X"})
	if code := threadsTitle([]string{"x"}, env, &errOut); code != threadsExitMiss {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "no tab for session X") {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestThreadsTitleIsASubcommand(t *testing.T) {
	stubTabTitle(t, "", nil)
	t.Setenv("ITERM_SESSION_ID", "")
	code, _, _ := runCmd(t, t.TempDir(), "title", "Slug", "Timeouts")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestReassessPromptSetsTheTabTitle(t *testing.T) {
	p := threadReassessPrompt(thread{Dir: "/v/x", Meta: threadMeta{Name: "x"}})
	for _, w := range []string{"`entire-tail threads title <title>`", "title alone on its first line", "Apart from the tab title"} {
		if !strings.Contains(p, w) {
			t.Errorf("prompt lacks %q", w)
		}
	}
}

func TestThreadTitleSkillMentionsOnlyRealSubcommands(t *testing.T) {
	known := map[string]bool{}
	for _, s := range threadsSubcommands {
		known[s] = true
	}
	data, err := os.ReadFile("docs/skills/thread-title/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "—") {
		t.Error("skill contains an em dash")
	}
	for _, m := range regexp.MustCompile(`entire-tail threads ([a-z]+)`).FindAllStringSubmatch(text, -1) {
		if !known[m[1]] {
			t.Errorf("skill mentions unknown subcommand %q", m[1])
		}
	}
	if !strings.Contains(text, "entire-tail threads title") {
		t.Error("skill never sets the title")
	}
}
