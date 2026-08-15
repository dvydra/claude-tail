package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func newYankRenderer(t *testing.T) (*Renderer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	r := newRendererWith(&buf, Theme{}, "dots", 5, func(s string) (string, error) { return s, nil })
	return r, &buf
}

// stubClipboard captures what a yank would copy instead of running pbcopy —
// a test must never replace the developer's real clipboard.
func stubClipboard(t *testing.T) *string {
	t.Helper()
	var got string
	prev := clipboardWrite
	clipboardWrite = func(s string, _ *os.File) error { got = s; return nil }
	t.Cleanup(func() { clipboardWrite = prev })
	return &got
}

func feed(r *Renderer, recs ...Record) {
	for _, rec := range recs {
		r.emit(rec)
	}
}

func user(body string) Record { return Record{Kind: KindUser, Body: body} }
func assistant(body string) Record {
	return Record{Kind: KindAssistant, Body: body}
}
func assistantMsg(id, body string) Record {
	return Record{Kind: KindAssistant, Body: body, MsgID: id}
}

// The unit is one agent MESSAGE, not everything since the last user message: a
// long working turn is dozens of narration lines, and what you paste into Slack
// is the last thing the agent actually said.
func TestYankGroupsByMessage(t *testing.T) {
	r, _ := newYankRenderer(t)
	feed(r,
		user("q1"),
		assistantMsg("m1", "working on it"),
		assistantMsg("m2", "**newest**"),
	)
	if got := len(r.yankMsgs); got != 2 {
		t.Fatalf("got %d messages, want 2", got)
	}
	if text, n := r.yankText(1); n != 1 || text != "*newest*" {
		t.Errorf("yankText(1) = %q, %d; want %q, 1", text, n, "*newest*")
	}
	text, n := r.yankText(2)
	if n != 2 || !strings.Contains(text, "working on it") || !strings.Contains(text, "*newest*") {
		t.Errorf("yankText(2) = %q, %d", text, n)
	}
	if strings.Contains(text, "q1") {
		t.Errorf("user turns must not be yanked:\n%s", text)
	}
}

// One message can arrive as two text records (Claude does this); the provider's
// message id keeps them one yankable unit.
func TestYankMergesRecordsOfOneMessage(t *testing.T) {
	r, _ := newYankRenderer(t)
	feed(r, user("q"), assistantMsg("m1", "first half"), assistantMsg("m1", "second half"))
	if got := len(r.yankMsgs); got != 1 {
		t.Fatalf("got %d messages, want 1 (same message id)", got)
	}
	if text, _ := r.yankText(1); text != "first half\n\nsecond half" {
		t.Errorf("merged text = %q", text)
	}
}

// Agents that report no message id (codex/agy) get one message per record.
func TestYankWithoutMessageIDs(t *testing.T) {
	r, _ := newYankRenderer(t)
	feed(r, assistant("one"), assistant("two"))
	if got := len(r.yankMsgs); got != 2 {
		t.Errorf("got %d messages, want 2", got)
	}
}

// Asking for more messages than exist yanks what there is, and reports it,
// rather than reporting a count it did not copy.
func TestYankTextClampsToWhatExists(t *testing.T) {
	r, _ := newYankRenderer(t)
	feed(r, user("q"), assistant("only"))
	if text, n := r.yankText(5); n != 1 || text != "only" {
		t.Errorf("yankText(5) = %q, %d; want %q, 1", text, n, "only")
	}
	r2, _ := newYankRenderer(t)
	if text, n := r2.yankText(1); n != 0 || text != "" {
		t.Errorf("empty buffer = %q, %d; want \"\", 0", text, n)
	}
}

// A re-render (reload / theme swap / rollover) re-emits the whole transcript;
// without clearing, every turn would be buffered a second time.
func TestYankBufferClearedOnReset(t *testing.T) {
	r, _ := newYankRenderer(t)
	feed(r, user("q"), assistant("a"))
	r.reset()
	feed(r, user("q"), assistant("a"))
	if got := len(r.yankMsgs); got != 1 {
		t.Errorf("got %d messages after reset+replay, want 1", got)
	}
}

func TestYankEmptyBodiesIgnored(t *testing.T) {
	r, _ := newYankRenderer(t)
	feed(r, user("q"), assistant("   \n "), assistant("real"))
	if got := len(r.yankMsgs); got != 1 {
		t.Fatalf("blank body was buffered: %#v", r.yankMsgs)
	}
}

// Repeated presses inside the window extend by a message; a pause starts over.
func TestYankRepeatExtends(t *testing.T) {
	r, _ := newYankRenderer(t)
	feed(r, user("q1"), assistant("one"), assistant("two"), assistant("three"))
	copied := stubClipboard(t)
	tty := devNull(t)
	t0 := time.Now()

	if msg := r.yank(tty, t0); !strings.Contains(msg, "1 message") {
		t.Errorf("first press: %q", msg)
	}
	if *copied != "three" {
		t.Errorf("first press copied %q, want the newest message", *copied)
	}
	if msg := r.yank(tty, t0.Add(time.Second)); !strings.Contains(msg, "2 messages") {
		t.Errorf("second press: %q", msg)
	}
	if *copied != "two\n\nthree" {
		t.Errorf("second press copied %q, want both messages oldest-first", *copied)
	}
	if msg := r.yank(tty, t0.Add(2*time.Second)); !strings.Contains(msg, "3 messages") {
		t.Errorf("third press: %q", msg)
	}
	// Past the window it starts over.
	if msg := r.yank(tty, t0.Add(2*time.Second+yankExtendWindow+time.Millisecond)); !strings.Contains(msg, "1 message") {
		t.Errorf("after the window: %q", msg)
	}
	// And it stops growing at the number of messages that exist.
	at := t0.Add(time.Minute)
	for range 5 {
		at = at.Add(time.Second)
		r.yank(tty, at)
	}
	if r.yankN != 3 {
		t.Errorf("yankN = %d, want it clamped to the 3 messages available", r.yankN)
	}
}

func TestYankWithNothingBuffered(t *testing.T) {
	r, _ := newYankRenderer(t)
	stubClipboard(t)
	if msg := r.yank(devNull(t), time.Now()); !strings.Contains(msg, "nothing to yank") {
		t.Errorf("got %q", msg)
	}
}

// OSC 52 is the fallback when there's no clipboard helper; it must carry the
// text base64'd, to the tty, and never to stdout.
func TestCopyToClipboardOSC52(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "osc52")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t.Setenv("PATH", t.TempDir()) // no pbcopy/xclip on this PATH
	if err := copyToClipboard("hello", f); err != nil {
		t.Fatalf("copyToClipboard: %v", err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.HasPrefix(got, "\x1b]52;c;") || !strings.Contains(got, "aGVsbG8=") {
		t.Errorf("OSC 52 payload = %q", got)
	}
}

func TestYankKeysAreWired(t *testing.T) {
	for _, k := range []byte{'y', 'Y'} {
		if got := keyActionFor(k); got != keyYank {
			t.Errorf("%q → %v, want keyYank", k, got)
		}
	}
	for _, k := range []byte{'m', 'M'} {
		if got := keyActionFor(k); got != keyToggleMrkdwn {
			t.Errorf("%q → %v, want keyToggleMrkdwn", k, got)
		}
	}
}

// `m` swaps agent bodies to mrkdwn source; user turns keep rendering normally.
func TestMrkdwnModeRendersAgentBodiesAsSource(t *testing.T) {
	r, buf := newYankRenderer(t)
	r.toggleMrkdwn()
	feed(r, user("**user text**"), assistant("**agent text**"))
	r.endLine()
	out := buf.String()
	if !strings.Contains(out, "*agent text*") || strings.Contains(out, "**agent text**") {
		t.Errorf("agent body should be mrkdwn source:\n%s", out)
	}
	if !strings.Contains(out, "**user text**") {
		t.Errorf("user body should render normally:\n%s", out)
	}
	if r.toggleMrkdwn(); r.mrkdwn.Load() {
		t.Error("toggleMrkdwn should flip back off")
	}
}

func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
