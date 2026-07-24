package main

import (
	"strings"
	"testing"
)

func TestParsePendingMarkerRoundTrip(t *testing.T) {
	raw := []byte(`{"kind":"question","payload":{"questions":[{"question":"Tea or coffee?","header":"Drink","options":[{"label":"Tea","description":"leaf"}]}]},"tool_use_id":"toolu_1","ts":1784862331}`)
	m, ok := parsePendingMarker(raw)
	if !ok {
		t.Fatal("expected ok")
	}
	if m.Kind != "question" || m.ToolUseID != "toolu_1" {
		t.Fatalf("bad parse: %+v", m)
	}
}

func TestParsePendingMarkerRejectsGarbage(t *testing.T) {
	if _, ok := parsePendingMarker([]byte(`{not json`)); ok {
		t.Fatal("expected !ok on garbage")
	}
	if _, ok := parsePendingMarker([]byte(``)); ok {
		t.Fatal("expected !ok on empty")
	}
}

func TestContentKeyStableAndNamespaced(t *testing.T) {
	q1, _ := parsePendingMarker([]byte(`{"kind":"question","payload":{"questions":[{"question":"Q","header":"H","options":[{"label":"A"}]}]}}`))
	q2, _ := parsePendingMarker([]byte(`{"kind":"question","payload":{"questions":[{"question":"Q","header":"H","options":[{"label":"A"}]}]}}`))
	if contentKey(q1) != contentKey(q2) {
		t.Fatal("same content must yield same key")
	}
	p, _ := parsePendingMarker([]byte(`{"kind":"permission","payload":{"tool_name":"Bash","tool_input":{"command":"ls"}}}`))
	if contentKey(p) == contentKey(q1) {
		t.Fatal("kinds must be namespaced apart")
	}
}

func TestPendingActionRendersOnlyOnChange(t *testing.T) {
	m, _ := parsePendingMarker([]byte(`{"kind":"question","payload":{"questions":[{"question":"Q","header":"H"}]}}`))
	render, key := pendingAction("", m, true)
	if !render || key == "" {
		t.Fatal("first sighting must render")
	}
	render2, _ := pendingAction(key, m, true)
	if render2 {
		t.Fatal("same marker must not re-render")
	}
	render3, key3 := pendingAction(key, nil, false)
	if render3 || key3 != "" {
		t.Fatal("absent marker must clear the key and not render")
	}
}

func TestPermissionSummary(t *testing.T) {
	m, ok := parsePendingMarker([]byte(`{"kind":"permission","payload":{"tool_name":"Bash","tool_input":{"command":"git push"}}}`))
	if !ok {
		t.Fatal("expected ok")
	}
	got := permissionSummary(m)
	if !strings.Contains(got, "Bash(") || !strings.Contains(got, "git push") {
		t.Fatalf("want Bash(...git push...), got %q", got)
	}
}

func TestPermissionSummaryUnknownTool(t *testing.T) {
	m, ok := parsePendingMarker([]byte(`{"kind":"permission","payload":{"tool_name":"MysteryTool"}}`))
	if !ok {
		t.Fatal("expected ok")
	}
	got := permissionSummary(m)
	if !strings.Contains(got, "MysteryTool") {
		t.Fatalf("want tool name present even with no input, got %q", got)
	}
}
