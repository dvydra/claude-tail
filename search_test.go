package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventConversationText(t *testing.T) {
	mk := func(typ, content string) claudeMetaEvent {
		return claudeMetaEvent{Type: typ, Message: &claudeMessage{Content: json.RawMessage(content)}}
	}
	// A user turn: injected <system-reminder> content must be stripped so it
	// can't false-match, but the real typed text stays.
	u := eventConversationText(mk("user", `"real ask <system-reminder>Messages API for directly passing</system-reminder> here"`))
	if strings.Contains(u, "Messages API") {
		t.Errorf("system-reminder not stripped: %q", u)
	}
	if !strings.Contains(u, "real ask") {
		t.Errorf("dropped the typed text: %q", u)
	}
	// Assistant text blocks are searchable.
	if got := eventConversationText(mk("assistant", `[{"type":"text","text":"done directly"}]`)); !strings.Contains(got, "directly") {
		t.Errorf("assistant text = %q", got)
	}
	// Tool results and non-conversation events contribute nothing.
	if got := eventConversationText(mk("user", `[{"type":"tool_result","content":"noise"}]`)); got != "" {
		t.Errorf("tool_result should be empty, got %q", got)
	}
	if got := eventConversationText(mk("summary", `"x"`)); got != "" {
		t.Errorf("summary should be empty, got %q", got)
	}
}

func TestSearchScoreRanking(t *testing.T) {
	word := &searchHit{wordCount: 1, localCount: 1} // " ectl "
	substr := &searchHit{localCount: 9}             // ectl inside kubectl ×9
	entireOnly := &searchHit{entireHit: true, entireScore: 6}
	both := &searchHit{wordCount: 1, localCount: 1, entireHit: true, entireScore: 6}
	ampFirst := &searchHit{ampHit: true, ampRank: 0}
	ampSecond := &searchHit{ampHit: true, ampRank: 1}

	// A standalone-word match beats any amount of substring-only matches.
	if word.score() <= substr.score() {
		t.Errorf("word (%v) should outrank substring-only (%v)", word.score(), substr.score())
	}
	// Substring-only still beats an entire-only semantic hit, and both-sources wins.
	if !(substr.score() > entireOnly.score() && both.score() > word.score()) {
		t.Errorf("ranking off: both=%v word=%v substr=%v entire=%v",
			both.score(), word.score(), substr.score(), entireOnly.score())
	}
	if (&searchHit{}).score() != 0 {
		t.Error("a hit with no signal should score 0")
	}
	if !(word.score() > ampFirst.score() && ampFirst.score() > substr.score() && ampFirst.score() > ampSecond.score()) {
		t.Errorf("cross-agent ranking off: word=%v amp-first=%v amp-second=%v substring=%v",
			word.score(), ampFirst.score(), ampSecond.score(), substr.score())
	}
}

func TestStandaloneAt(t *testing.T) {
	if !standaloneAt("run ectl now", 4, 4) {
		t.Error("' ectl ' should be standalone")
	}
	if standaloneAt("kubectl", 3, 4) {
		t.Error("'ectl' inside 'kubectl' is not standalone")
	}
	if !standaloneAt("ectl", 0, 4) {
		t.Error("whole string is standalone")
	}
	if standaloneAt("directly", 4, 4) { // "ctly" mid-word — preceded by 'e'
		t.Error("substring inside a word is not standalone")
	}
}

func TestWindow(t *testing.T) {
	line := "0123456789abcdefghijQUERYklmno"
	w := window(line, strings.Index(line, "QUERY"), 5)
	if !strings.Contains(w, "QUERY") {
		t.Errorf("window dropped the match: %q", w)
	}

	long := strings.Repeat("x", 100) + "QUERY" + strings.Repeat("y", 100)
	w2 := window(long, 100, 5)
	if !strings.HasPrefix(w2, "…") {
		t.Errorf("mid-line cut should start with an ellipsis: %q", w2)
	}
	if len([]rune(w2)) > 90 {
		t.Errorf("window not bounded: %d runes", len([]rune(w2)))
	}
}

func TestCleanMatch(t *testing.T) {
	if got := cleanMatch(`a\nb\"c\\d`); got != `a b"c\d` {
		t.Errorf("cleanMatch = %q", got)
	}
}

func TestAmpCachedSearchUsesTitleAndTranscript(t *testing.T) {
	home := t.TempDir()
	listPath := filepath.Join(ampCacheDir(home), "threads.json")
	if err := writeAmpCache(listPath, []byte(`[{"id":"T-title","title":"Database migration","updated":"2026-09-25T10:00:00Z"},{"id":"T-body","title":"Other","updated":"2026-09-25T09:00:00Z"},{"id":"T-miss","title":"Nope","updated":"2026-09-25T08:00:00Z"}]`)); err != nil {
		t.Fatal(err)
	}
	for id, text := range map[string]string{"T-title": "nothing", "T-body": "database rollback", "T-miss": "unrelated"} {
		path := filepath.Join(ampCacheDir(home), "exports", id+".json")
		body := `{"id":"` + id + `","messages":[{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}]}`
		if err := writeAmpCache(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	got := ampCachedSearch(home, "DATABASE")
	if len(got) != 2 || got[0].ID != "T-title" || got[1].ID != "T-body" {
		t.Fatalf("hits=%+v", got)
	}
	if _, err := os.Stat(listPath); err != nil {
		t.Fatal(err)
	}
}
