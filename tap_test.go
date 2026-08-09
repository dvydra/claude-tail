package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The frames below mirror a real captured /v1/messages stream (a text block
// followed by a tool_use block, stop_reason=tool_use) — the exact shape a
// blocked AskUserQuestion produces.
func sseLines(frames ...string) []byte {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("event: x\ndata: " + f + "\n\n")
	}
	return []byte(b.String())
}

func collectTap(t *testing.T, body []byte, chunk int) []tapEvent {
	t.Helper()
	var got []tapEvent
	ts := int64(0)
	p := newTapParser("sess-1", func() int64 { ts++; return ts }, func(ev tapEvent) { got = append(got, ev) })
	for i := 0; i < len(body); i += chunk {
		p.feed(body[i:min(i+chunk, len(body))])
	}
	return got
}

const (
	frameMsgStart  = `{"type":"message_start","message":{"id":"msg_01AAA","role":"assistant"}}`
	frameTextStart = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	frameTextD1    = `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Full surface map "}}`
	frameTextD2    = `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"now verified."}}`
	frameTextStop  = `{"type":"content_block_stop","index":0}`
	frameToolStart = `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01BBB","name":"AskUserQuestion","input":{}}}`
	frameToolD1    = `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"questions\":[{\"question\":\"Which do I build next?\",\"header\":\"Next\",\"opt"}}`
	frameToolD2    = `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"ions\":[{\"label\":\"4a\",\"description\":\"cheap\"},{\"label\":\"3\"}]}]}"}}`
	frameToolStop  = `{"type":"content_block_stop","index":1}`
	frameMsgDelta  = `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	frameMsgStop   = `{"type":"message_stop"}`
)

func questionStream() []byte {
	return sseLines(frameMsgStart, frameTextStart, frameTextD1, frameTextD2, frameTextStop,
		frameToolStart, frameToolD1, frameToolD2, frameToolStop, frameMsgDelta, frameMsgStop)
}

func TestTapParserBlocks(t *testing.T) {
	got := collectTap(t, questionStream(), 4096)
	if len(got) != 3 {
		t.Fatalf("want 3 events (text, tool_use, message_stop), got %d: %+v", len(got), got)
	}

	if got[0].Kind != "text" || got[0].Text != "Full surface map now verified." {
		t.Errorf("text block = %+v", got[0])
	}
	if got[0].MsgID != "msg_01AAA" || got[0].Index != 0 || got[0].Session != "sess-1" {
		t.Errorf("text ids = %+v", got[0])
	}
	if got[1].Kind != "tool_use" || got[1].Name != "AskUserQuestion" || got[1].ToolID != "toolu_01BBB" {
		t.Errorf("tool block = %+v", got[1])
	}
	qs := claudeParseQuestions(got[1].Input)
	if len(qs) != 1 || qs[0].Question != "Which do I build next?" || len(qs[0].Options) != 2 {
		t.Errorf("reassembled input parsed to %+v", qs)
	}
	if got[2].Kind != "message_stop" || got[2].StopReason != "tool_use" {
		t.Errorf("stop = %+v", got[2])
	}
}

// The tap reads a live socket, so blocks arrive split at arbitrary byte
// boundaries — including mid-line and mid-JSON-fragment.
func TestTapParserChunkSplitting(t *testing.T) {
	want := collectTap(t, questionStream(), 4096)
	for _, chunk := range []int{1, 3, 7, 64, 500} {
		got := collectTap(t, questionStream(), chunk)
		if len(got) != len(want) {
			t.Fatalf("chunk=%d: got %d events, want %d", chunk, len(got), len(want))
		}
		for i := range got {
			if got[i].Text != want[i].Text || got[i].Kind != want[i].Kind || string(got[i].Input) != string(want[i].Input) {
				t.Errorf("chunk=%d: event %d differs: %+v vs %+v", chunk, i, got[i], want[i])
			}
		}
	}
}

func TestTapParserIgnoresJunk(t *testing.T) {
	body := []byte("event: ping\ndata: not json\n\n: comment\n\ndata: {\"type\":\"unknown\"}\n\n")
	body = append(body, questionStream()...)
	if got := collectTap(t, body, 17); len(got) != 3 {
		t.Fatalf("junk lines changed the event count: %d", len(got))
	}
}

// A tool_use whose input never completes must not be reported as valid JSON —
// half-parsed args would render a bogus card.
func TestTapParserDropsPartialInput(t *testing.T) {
	body := sseLines(frameMsgStart, frameToolStart, frameToolD1, frameToolStop, frameMsgDelta, frameMsgStop)
	got := collectTap(t, body, 4096)
	if got[0].Kind != "tool_use" {
		t.Fatalf("want tool_use first, got %+v", got[0])
	}
	if got[0].Input != nil {
		t.Errorf("partial input should be dropped, got %s", got[0].Input)
	}
}

func TestTapPendingQuestion(t *testing.T) {
	evs := collectTap(t, questionStream(), 4096)
	p, ok := tapPending(evs)
	if !ok {
		t.Fatal("a message ending in AskUserQuestion must be reported as pending")
	}
	if p.MsgID != "msg_01AAA" || p.QID != "toolu_01BBB" {
		t.Errorf("ids = %+v", p)
	}
	if len(p.Preamble) != 1 || p.Preamble[0] != "Full surface map now verified." {
		t.Errorf("preamble = %q", p.Preamble)
	}
	if len(p.Questions) != 1 {
		t.Errorf("questions = %+v", p.Questions)
	}
}

// Ordinary turns must NOT trigger the tap render path: their transcript records
// land ~200ms later, and rendering both would duplicate or reorder them.
func TestTapPendingIgnoresOrdinaryTurns(t *testing.T) {
	cases := map[string][]byte{
		"plain text end_turn": sseLines(frameMsgStart, frameTextStart, frameTextD1, frameTextStop,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, frameMsgStop),
		"text then Bash": sseLines(frameMsgStart, frameTextStart, frameTextD1, frameTextStop,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"Bash","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
			`{"type":"content_block_stop","index":1}`, frameMsgDelta, frameMsgStop),
	}
	for name, body := range cases {
		if _, ok := tapPending(collectTap(t, body, 4096)); ok {
			t.Errorf("%s: must not be treated as a pending prompt", name)
		}
	}
}

// A message still streaming has no message_stop yet — nothing may render from
// it, or a half-built question card would flash on screen.
func TestTapGroupMessagesWithholdsPartial(t *testing.T) {
	full := collectTap(t, questionStream(), 4096)
	groups := tapGroupMessages(full)
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("want one complete group of 3, got %+v", groups)
	}

	partial := full[:2] // text + tool_use, no message_stop
	if g := tapGroupMessages(partial); len(g) != 0 {
		t.Errorf("partial message must yield no group, got %+v", g)
	}

	two := append(append([]tapEvent{}, full...), full...)
	if g := tapGroupMessages(two); len(g) != 2 {
		t.Errorf("want 2 groups for 2 messages, got %d", len(g))
	}
}

// ── the activity overlay (tree.go applyTapActivity) ──────────────────────────

func TestApplyTapActivity(t *testing.T) {
	const nowSec = int64(1786318800)
	nowMs := nowSec * 1000

	newTree := func() sessionTree {
		return sessionTree{Now: nowSec, Folders: []treeFolder{{
			Cwd: "/repo", Live: 0,
			Sessions: []treeSession{
				{ID: "busy", Mtime: nowSec - 30},
				{ID: "idle-recent", Mtime: nowSec - 600},
				{ID: "idle-old", Mtime: nowSec - 90000},
				{ID: "untapped-guessed-live", Mtime: nowSec - 60, Live: true},
			},
		}}}
	}
	find := func(tr sessionTree, id string) treeSession {
		for _, s := range tr.Folders[0].Sessions {
			if s.ID == id {
				return s
			}
		}
		t.Fatalf("session %s missing", id)
		return treeSession{}
	}

	act := tapActive{Sessions: map[string]tapSessionStatus{
		"busy":        {InFlight: 1, LastStart: nowMs - 3000},
		"idle-recent": {LastEvent: nowMs - 60_000, LastEnd: nowMs - 60_000},
		"idle-old":    {LastEvent: nowMs - 6*3600*1000, LastEnd: nowMs - 6*3600*1000},
	}}
	tr := newTree()
	applyTapActivity(&tr, act, nowMs)

	if s := find(tr, "busy"); !s.Generating || !s.Live {
		t.Errorf("in-flight session: Generating=%v Live=%v, want both true", s.Generating, s.Live)
	}
	if s := find(tr, "idle-recent"); s.Generating || !s.Live {
		t.Errorf("recently-active session: Generating=%v Live=%v, want false/true", s.Generating, s.Live)
	}
	// Old traffic proves nothing about now, so the row keeps whatever the crawl said.
	if s := find(tr, "idle-old"); s.Generating || s.Live {
		t.Errorf("long-idle session should be left alone, got Generating=%v Live=%v", s.Generating, s.Live)
	}
	// A session the tap has never seen must not be demoted — pgrep's guess stands.
	if s := find(tr, "untapped-guessed-live"); !s.Live {
		t.Error("a session absent from the tap table must keep its guessed Live marker")
	}
	if tr.Folders[0].Live == 0 {
		t.Error("a folder with a tap-confirmed live session should carry the live badge")
	}

	// No daemon / empty table → exact no-op.
	base, over := newTree(), newTree()
	applyTapActivity(&over, tapActive{}, nowMs)
	for i := range base.Folders[0].Sessions {
		if base.Folders[0].Sessions[i] != over.Folders[0].Sessions[i] {
			t.Errorf("empty activity table changed session %d", i)
		}
	}
	if base.Folders[0].Live != over.Folders[0].Live {
		t.Error("empty activity table changed the folder badge")
	}
}

func TestComposeSessionRowGeneratingGlyph(t *testing.T) {
	now := int64(1786318800)
	plain := composeSessionRow(treeSession{ID: "abc12345", Mtime: now}, now)
	live := composeSessionRow(treeSession{ID: "abc12345", Mtime: now, Live: true}, now)
	gen := composeSessionRow(treeSession{ID: "abc12345", Mtime: now, Live: true, Generating: true}, now)

	if !strings.Contains(plain, "○") || strings.Contains(plain, "●") {
		t.Errorf("idle row = %q", plain)
	}
	if !strings.Contains(live, "●") {
		t.Errorf("live row = %q", live)
	}
	if !strings.Contains(gen, "◉") || strings.Contains(gen, "●") {
		t.Errorf("generating row = %q", gen)
	}
}

func TestAppendTapEventRoundTrip(t *testing.T) {
	home := t.TempDir()
	ev := tapEvent{Ts: 42, Session: "sess-9", Kind: "text", MsgID: "msg_1", Text: "hi"}
	if err := appendTapEvent(home, ev); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := appendTapEvent(home, tapEvent{Session: "sess-9", Kind: "message_stop", StopReason: "tool_use"}); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	path := tapSidecarPath(home, "sess-9")
	if want := filepath.Join(home, ".claude", "entire-tail", "tap", "sessions", "sess-9.ndjson"); path != want {
		t.Errorf("sidecar path = %s, want %s", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 ndjson lines, got %d", len(lines))
	}
	var back tapEvent
	if err := json.Unmarshal([]byte(lines[0]), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Text != "hi" || back.MsgID != "msg_1" || back.Ts != 42 {
		t.Errorf("round-tripped to %+v", back)
	}

	// A sessionless event (header missing) is dropped, not written to a stray file.
	if err := appendTapEvent(home, tapEvent{Kind: "text", Text: "orphan"}); err != nil {
		t.Errorf("sessionless append should no-op, got %v", err)
	}
	if entries, _ := os.ReadDir(tapSessionsDir(home)); len(entries) != 1 {
		t.Errorf("want exactly one sidecar file, got %d", len(entries))
	}
}
