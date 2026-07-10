package main

import (
	"strings"
	"testing"
)

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
