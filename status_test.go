package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func testStatusInfo() statusInfo {
	return statusInfo{
		Agent:    AgentClaude,
		Repo:     "claude-tail",
		Session:  "ac2925b3-28c3-414d-afd8-8b76f5b38f9a",
		Turns:    14,
		LastAt:   time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
		Tools:    toolDots,
		Theme:    "tokyo-night",
		Collapse: 5,
	}
}

func statusNowTime() time.Time { return time.Date(2026, 8, 15, 12, 0, 45, 0, time.UTC) }

func TestStatusLineShowsSessionAndModes(t *testing.T) {
	got := stripANSI(statusLine(testStatusInfo(), "", 120, statusNowTime()))
	for _, want := range []string{"claude", "claude-tail", "ac2925b3", "14 turns", "45s ago", "dots", "tokyo-night", "collapse 5", "? settings"} {
		if !strings.Contains(got, want) {
			t.Errorf("status line missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "mrkdwn") {
		t.Errorf("mrkdwn shown while off: %q", got)
	}
}

// The `w` toggle is an off-normal mode: prose suddenly running off the right
// edge is exactly what you'd otherwise read as a bug, so the bar says so.
func TestStatusLineNoWrapFlag(t *testing.T) {
	info := testStatusInfo()
	if got := stripANSI(statusLine(info, "", 120, statusNowTime())); strings.Contains(got, "nowrap") {
		t.Errorf("nowrap shown while wrapping: %q", got)
	}
	info.NoWrap = true
	if got := stripANSI(statusLine(info, "", 120, statusNowTime())); !strings.Contains(got, "nowrap") {
		t.Errorf("nowrap not shown while suspended: %q", got)
	}
}

func TestStatusLineMrkdwnFlag(t *testing.T) {
	info := testStatusInfo()
	info.Mrkdwn = true
	if got := stripANSI(statusLine(info, "", 120, statusNowTime())); !strings.Contains(got, "mrkdwn") {
		t.Errorf("mrkdwn not shown while on: %q", got)
	}
}

// A blocked agent is the one thing a tail exists to tell you about, so it
// replaces the idle age rather than sitting beside it.
func TestStatusLinePendingReplacesAge(t *testing.T) {
	info := testStatusInfo()
	info.Pending = true
	got := stripANSI(statusLine(info, "", 120, statusNowTime()))
	if !strings.Contains(got, "waiting for you") {
		t.Errorf("pending not shown: %q", got)
	}
	if strings.Contains(got, "ago") {
		t.Errorf("age shown alongside pending: %q", got)
	}
}

// A keypress message takes the whole line, in yellow, and nothing else.
func TestStatusLineMessageTakesTheLine(t *testing.T) {
	line := statusLine(testStatusInfo(), "copied 1 message as slack mrkdwn", 120, statusNowTime())
	if !strings.HasPrefix(line, statusYellow) {
		t.Errorf("message not yellow: %q", line)
	}
	got := stripANSI(line)
	if !strings.Contains(got, "copied 1 message") {
		t.Errorf("message missing: %q", got)
	}
	if strings.Contains(got, "? settings") || strings.Contains(got, "tokyo-night") {
		t.Errorf("message line should not carry the persistent half: %q", got)
	}
}

// The bar must never be wider than the terminal, or it wraps and eats a row of
// transcript. The right half goes first — "which session is this" outlives
// "what theme is it".
func TestStatusLineFitsAnyWidth(t *testing.T) {
	for _, w := range []int{200, 120, 80, 60, 40, 20} {
		line := statusLine(testStatusInfo(), "", w, statusNowTime())
		if got := visWidth(line); got > w {
			t.Errorf("w=%d: line is %d cols: %q", w, got, stripANSI(line))
		}
		if w >= 40 && !strings.Contains(stripANSI(line), "ac2925b3") {
			t.Errorf("w=%d: dropped the session id: %q", w, stripANSI(line))
		}
	}
	long := strings.Repeat("x", 400)
	if got := visWidth(statusLine(testStatusInfo(), long, 80, statusNowTime())); got > 80 {
		t.Errorf("long message not truncated: %d cols", got)
	}
}

func TestFmtAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "just now"},                      // the moment the bar appears
		{900 * time.Millisecond, "just now"}, //
		{45 * time.Second, "45s ago"},        //
		{90 * time.Second, "1m ago"},         //
		{125 * time.Minute, "2h5m ago"},      // fmtDur would say "125m"
		{25 * time.Hour, "25h0m ago"},        //
	}
	for _, c := range cases {
		if got := fmtAge(c.d); got != c.want {
			t.Errorf("fmtAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// A worktree's own basename is a task name; the bar has to say which repo.
func TestStatusRepo(t *testing.T) {
	cases := []struct{ cwd, want string }{
		{"/Users/d/src/dvydra/claude-tail", "claude-tail"},
		{"/Users/d/src/dvydra/claude-tail/.claude/worktrees/status-line", "claude-tail@status-line"},
		{"/Users/d/src/dvydra/claude-tail/.claude/worktrees/status-line/sub/dir", "claude-tail@status-line"},
		{"", ""},
	}
	for _, c := range cases {
		if got := statusRepo(c.cwd); got != c.want {
			t.Errorf("statusRepo(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

// A toggle re-renders only what's on screen; the renderer still processes the
// whole transcript, so only the printed output is trimmed.
func TestTailLinesOf(t *testing.T) {
	const body = "a\nb\nc\nd"
	if got, trimmed := tailLinesOf(body, 2); got != "c\nd" || !trimmed {
		t.Errorf("tailLinesOf(2) = %q, %v", got, trimmed)
	}
	if got, trimmed := tailLinesOf(body, 4); got != body || trimmed {
		t.Errorf("exactly-fits should not trim: %q, %v", got, trimmed)
	}
	if got, trimmed := tailLinesOf(body, 0); got != body || trimmed {
		t.Errorf("0 means all: %q, %v", got, trimmed)
	}
	// The last line carries no trailing newline (the renderer defers it so a dot
	// streak can ride the end of a turn) — that must survive the trim.
	if got, _ := tailLinesOf("a\nb\nopen", 2); strings.HasSuffix(got, "\n") {
		t.Errorf("trailing newline added: %q", got)
	}
	if got, trimmed := tailLinesOf("", 5); got != "" || trimmed {
		t.Errorf("empty = %q, %v", got, trimmed)
	}
}

func TestParseCursorReport(t *testing.T) {
	cases := []struct {
		in   string
		row  int
		ok   bool
		name string
	}{
		{"\x1b[24;80R", 24, true, "plain"},
		{"\x1b[1;1R", 1, true, "top left"},
		{"junk\x1b[7;3R", 7, true, "leading noise"},
		{"\x1b[24;80", 0, false, "truncated"},
		{"", 0, false, "empty"},
		{"nonsense", 0, false, "no escape"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row, ok := parseCursorReport([]byte(c.in))
			if row != c.row || ok != c.ok {
				t.Errorf("parseCursorReport(%q) = %d, %v; want %d, %v", c.in, row, ok, c.row, c.ok)
			}
		})
	}
}

// The reserve MUST leave the cursor above the freed row. A newline on the
// bottom row scrolls the screen but leaves the cursor on that same row, so a
// single "\n" reserves nothing: every later write lands on the bar's row,
// outside the scrolling region, and the bar's next repaint erases it — the
// transcript flashes and vanishes and the tail looks dead. Shipped that way
// once; this pins the sequence.
func TestScrollUpOneRowLeavesCursorAboveTheFreedRow(t *testing.T) {
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	scrollUpOneRow(wr)
	wr.Close()

	buf := make([]byte, 32)
	n, _ := rd.Read(buf)
	if got, want := string(buf[:n]), "\n\n\x1b[A"; got != want {
		t.Errorf("scrollUpOneRow wrote %q, want %q (two newlines and a cursor-up)", got, want)
	}
}

// Every method has to be safe on a nil bar — that's the no-tty / --no-status
// path, and it runs through the same live loop as everything else.
func TestNilStatusBarIsInert(t *testing.T) {
	var s *statusBar
	s.setMessage("hi", time.Now())
	s.update(testStatusInfo(), time.Now())
	s.resize()
	s.suspend()
	s.resume()
	s.close()
}

func TestStatusKeysRouteToActions(t *testing.T) {
	// Everything that changes the view (or copies) is applied on the render
	// goroutine, so it must map to an action rather than being handled inline.
	for _, c := range []struct {
		b    byte
		want keyAction
	}{
		{'t', keyCycleTools},
		{'T', keyCycleTheme},
		{'c', keyToggleCollapse},
		{'m', keyToggleMrkdwn},
		{'y', keyYank},
		{'r', keyReload},
		{'w', keyToggleWrap},
		{'W', keyToggleWrap},
	} {
		if got := keyActionFor(c.b); got != c.want {
			t.Errorf("%q → %v, want %v", c.b, got, c.want)
		}
	}
}
