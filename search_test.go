package main

import (
	"encoding/json"
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
	both := &searchHit{localCount: 2, entireHit: true, entireScore: 6}
	localOnly := &searchHit{localCount: 2}
	entireOnly := &searchHit{entireHit: true, entireScore: 6}

	// Matching both sources ranks above an exact local-only match, which ranks
	// above an entire-only (semantic) match.
	if !(both.score() > localOnly.score() && localOnly.score() > entireOnly.score()) {
		t.Errorf("ranking off: both=%v local=%v entire=%v", both.score(), localOnly.score(), entireOnly.score())
	}
	if (&searchHit{}).score() != 0 {
		t.Error("a hit with no signal should score 0")
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
