package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tap.go — the API-stream tap: turning a /v1/messages SSE response into
// block-complete events, and the on-disk sidecar the tail reads them from.
//
// Why this exists: Claude Code appends an assistant message's transcript
// records when the message stream ENDS (measured: ~200ms after the wire), so
// for ordinary turns the JSONL is already fast enough. The exception is
// AskUserQuestion — the whole message (the preamble text AND the tool_use) is
// withheld until the user answers, so a blocked question leaves the transcript
// dark indefinitely (measured: 35 minutes and counting on a real session).
// The wire has the preamble the instant it streams, which is what we want.
//
// Every Claude Code request self-identifies its session with the
// x-claude-code-session-id header, so events are keyed by session with no
// correlation guesswork — no pid matching, no launch tokens.
//
// The parser is deliberately pure: bytes in, events out, no IO. tapdaemon.go
// owns the sockets and the files.

// tapEvent is one block-complete event lifted from an SSE stream. It carries
// the provider ids (message id, tool_use id) that also appear in the JSONL, so
// an early-rendered event can be suppressed EXACTLY when its transcript twin
// lands — no content hashing, no near-miss.
type tapEvent struct {
	Ts      int64  `json:"ts"`      // unix millis, when the block completed on the wire
	Session string `json:"session"` // x-claude-code-session-id
	Kind    string `json:"kind"`    // text|tool_use|thinking|message_stop
	MsgID   string `json:"msg,omitempty"`
	Index   int    `json:"idx"`
	Name    string `json:"name,omitempty"`    // tool name (tool_use)
	ToolID  string `json:"tool_id,omitempty"` // toolu_… (tool_use)
	Text    string `json:"text,omitempty"`    // full block text (text)

	// Input is the tool_use input, reassembled from the input_json_delta
	// fragments. Invalid/partial JSON is dropped rather than half-parsed.
	Input json.RawMessage `json:"input,omitempty"`

	// StopReason is set on the message_stop event: "tool_use" while the agent
	// keeps going, "end_turn" when it hands back.
	StopReason string `json:"stop_reason,omitempty"`
}

// tapBlock is a content block being accumulated mid-stream.
type tapBlock struct {
	kind    string
	name    string
	toolID  string
	text    strings.Builder
	partial strings.Builder // input_json_delta fragments
}

// tapParser turns a byte stream of SSE into block-complete tapEvents. Feed it
// arbitrary chunks; it buffers partial lines. One parser per response body.
type tapParser struct {
	session string
	now     func() int64
	emit    func(tapEvent)

	buf    []byte
	msgID  string
	blocks map[int]*tapBlock
	// stopReason from message_delta, reported on the following message_stop.
	stopReason string
}

func newTapParser(session string, now func() int64, emit func(tapEvent)) *tapParser {
	return &tapParser{session: session, now: now, emit: emit, blocks: map[int]*tapBlock{}}
}

// feed consumes a chunk of the SSE body. Only "data:" lines carry payloads; the
// "event:" lines duplicate the type field inside the JSON, so they're ignored.
func (p *tapParser) feed(chunk []byte) {
	p.buf = append(p.buf, chunk...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			return
		}
		line := bytes.TrimSpace(p.buf[:i])
		p.buf = p.buf[i+1:]
		if after, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			p.line(bytes.TrimSpace(after))
		}
	}
}

// sseFrame is the union of the streaming event shapes we care about.
type sseFrame struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID string `json:"id"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

func (p *tapParser) line(data []byte) {
	var f sseFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return
	}
	switch f.Type {
	case "message_start":
		if f.Message != nil {
			p.msgID = f.Message.ID
		}
		p.blocks = map[int]*tapBlock{}
		p.stopReason = ""
	case "content_block_start":
		b := &tapBlock{}
		if f.ContentBlock != nil {
			b.kind, b.name, b.toolID = f.ContentBlock.Type, f.ContentBlock.Name, f.ContentBlock.ID
		}
		p.blocks[f.Index] = b
	case "content_block_delta":
		b := p.blocks[f.Index]
		if b == nil || f.Delta == nil {
			return
		}
		switch f.Delta.Type {
		case "text_delta":
			b.text.WriteString(f.Delta.Text)
		case "thinking_delta":
			b.text.WriteString(f.Delta.Thinking)
		case "input_json_delta":
			b.partial.WriteString(f.Delta.PartialJSON)
		}
	case "content_block_stop":
		b := p.blocks[f.Index]
		if b == nil {
			return
		}
		delete(p.blocks, f.Index)
		ev := tapEvent{
			Ts: p.now(), Session: p.session, Kind: b.kind, MsgID: p.msgID, Index: f.Index,
			Name: b.name, ToolID: b.toolID,
		}
		switch b.kind {
		case "text", "thinking":
			ev.Text = b.text.String()
		case "tool_use":
			if raw := b.partial.String(); json.Valid([]byte(raw)) {
				ev.Input = json.RawMessage(raw)
			}
		}
		p.emit(ev)
	case "message_delta":
		if f.Delta != nil && f.Delta.StopReason != "" {
			p.stopReason = f.Delta.StopReason
		}
	case "message_stop":
		p.emit(tapEvent{
			Ts: p.now(), Session: p.session, Kind: "message_stop",
			MsgID: p.msgID, StopReason: p.stopReason,
		})
	}
}

// ── sidecar files ─────────────────────────────────────────────────────────────

func tapDir(home string) string { return filepath.Join(home, ".claude", "entire-tail", "tap") }

func tapSessionsDir(home string) string { return filepath.Join(tapDir(home), "sessions") }

// tapSidecarPath is where a session's events are appended. Named by session id,
// so the tail finds its own stream with no lookup.
func tapSidecarPath(home, session string) string {
	return filepath.Join(tapSessionsDir(home), session+".ndjson")
}

// appendTapEvent appends one event as a JSON line. Best-effort: a tap that
// can't write must never disturb the proxied request.
func appendTapEvent(home string, ev tapEvent) error {
	if ev.Session == "" {
		return nil
	}
	if err := os.MkdirAll(tapSessionsDir(home), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(tapSidecarPath(home, ev.Session), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ── the render trigger ────────────────────────────────────────────────────────

// tapPendingPrompt is a complete message that ends in a blocking question: the
// preamble text blocks the transcript is withholding, plus the question itself.
type tapPendingPrompt struct {
	MsgID     string
	Ts        int64    // unix millis of the first block on the wire
	Preamble  []string // completed text blocks, in wire order
	QID       string   // the AskUserQuestion tool_use id
	Questions []QuestionItem
}

// formatTapTS renders a wire timestamp in the same local format as a transcript
// record's, so a tap-rendered header is indistinguishable from a JSONL one.
func formatTapTS(ms int64, loc *time.Location) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).In(loc).Format("2006-01-02 15:04:05")
}

// tapPending scans a message's events (in wire order, ending at message_stop)
// and reports a pending prompt when the message hands off to AskUserQuestion.
//
// This narrow trigger is the whole point: it is the ONLY case where the JSONL
// is not merely 200ms late but absent until the user acts. Rendering anything
// else from the tap would duplicate a transcript that is about to arrive
// anyway, and would race the file for ordering.
func tapPending(evs []tapEvent) (tapPendingPrompt, bool) {
	var out tapPendingPrompt
	var stopped bool
	for _, ev := range evs {
		if out.Ts == 0 {
			out.Ts = ev.Ts
		}
		switch ev.Kind {
		case "text":
			if strings.TrimSpace(ev.Text) != "" {
				out.Preamble = append(out.Preamble, ev.Text)
			}
			out.MsgID = ev.MsgID
		case "tool_use":
			out.MsgID = ev.MsgID
			if ev.Name == "AskUserQuestion" {
				out.QID = ev.ToolID
				out.Questions = claudeParseQuestions(ev.Input)
			}
		case "message_stop":
			out.MsgID = firstNonEmpty(out.MsgID, ev.MsgID)
			stopped = ev.StopReason == "tool_use"
		}
	}
	if !stopped || len(out.Questions) == 0 {
		return tapPendingPrompt{}, false
	}
	return out, true
}

// tapWatcher follows one session's sidecar and reports pending prompts as their
// messages complete. Same shape as the transcript follower: a byte offset over an
// append-only file, polled from the render goroutine, so nothing here is
// concurrent and nothing blocks.
type tapWatcher struct {
	path   string
	offset int64
	// carry holds the events of a message still streaming (no message_stop yet),
	// so a group split across two polls is not lost.
	carry []tapEvent
	// reported msg ids, so a prompt renders exactly once even though its events
	// stay in the sidecar forever.
	reported map[string]bool
}

func newTapWatcher(home, session string) *tapWatcher {
	w := &tapWatcher{path: tapSidecarPath(home, session), reported: map[string]bool{}}
	// Start at the end: events already on disk belong to turns the transcript has
	// long since flushed, and replaying them would double-render history.
	if fi, err := os.Stat(w.path); err == nil {
		w.offset = fi.Size()
	}
	return w
}

// rebind points the watcher at a different session's sidecar — used when the
// tail follows a lineage fork or a relocated session mid-run.
func (w *tapWatcher) rebind(home, session string) {
	path := tapSidecarPath(home, session)
	if path == w.path {
		return
	}
	*w = *newTapWatcher(home, session)
}

// poll returns any prompts whose messages completed since the last call.
func (w *tapWatcher) poll() []tapPendingPrompt {
	var fresh []tapEvent
	w.offset = appendStep(w.path, w.offset, func(line []byte) {
		var ev tapEvent
		if json.Unmarshal(line, &ev) == nil {
			fresh = append(fresh, ev)
		}
	})
	if len(fresh) == 0 {
		return nil
	}
	w.carry = append(w.carry, fresh...)
	groups := tapGroupMessages(w.carry)
	if n := len(groups); n > 0 {
		// Everything up to the last message_stop is consumed; the remainder (a
		// message still streaming) stays in carry for the next tick.
		consumed := 0
		for _, g := range groups {
			consumed += len(g)
		}
		w.carry = append([]tapEvent{}, w.carry[consumed:]...)
	}
	var out []tapPendingPrompt
	for _, g := range groups {
		p, ok := tapPending(g)
		if !ok || w.reported[p.MsgID] {
			continue
		}
		w.reported[p.MsgID] = true
		out = append(out, p)
	}
	return out
}

// tapGroupMessages splits a flat event stream into per-message groups, each
// ending at its message_stop. A trailing group with no message_stop is still
// streaming and is not returned — nothing is rendered from a partial message.
func tapGroupMessages(evs []tapEvent) [][]tapEvent {
	var out [][]tapEvent
	var cur []tapEvent
	for _, ev := range evs {
		cur = append(cur, ev)
		if ev.Kind == "message_stop" {
			out = append(out, cur)
			cur = nil
		}
	}
	return out
}
