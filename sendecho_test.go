package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestComposeApply(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []string // one string per read
		want string
	}{
		{"typing", []string{"hello"}, "hello"},
		{"backspace", []string{"helxx\x7f\x7flo"}, "hello"},
		{"backspace a rune", []string{"café\x7fe"}, "cafe"},
		{"enter clears", []string{"sent\r", "next"}, "next"},
		{"lf clears too", []string{"sent\n"}, ""},
		{"ctrl-u clears", []string{"oops\x15ok"}, "ok"},
		{"arrow dropped", []string{"a\x1b[Db"}, "ab"},
		{"ss3 dropped", []string{"a\x1bOAb"}, "ab"},
		{"csi split across reads", []string{"a\x1b[", "1;5Cb"}, "ab"},
		{"lone esc keeps next read", []string{"a", "\x1b", "b"}, "ab"},
		{"other control ignored", []string{"a\x01b"}, "ab"},
	} {
		var cl composeLine
		for _, s := range c.in {
			cl.apply([]byte(s))
		}
		if cl.text != c.want {
			t.Errorf("%s: %q, want %q", c.name, cl.text, c.want)
		}
	}
}

func TestComposeRows(t *testing.T) {
	for _, c := range []struct {
		text  string
		width int
		want  int
	}{
		{"", 80, 2},
		{strings.Repeat("x", 70), 80, 2},
		{strings.Repeat("x", 78), 80, 3}, // "> " + 78 fills the row; reserve the wrap
		{strings.Repeat("x", 200), 80, 4},
		{"abc", 0, 2}, // unknown width
	} {
		if got := composeRows(c.text, c.width); got != c.want {
			t.Errorf("composeRows(len %d, %d) = %d, want %d", len(c.text), c.width, got, c.want)
		}
	}
}

func newTestComposeWriter(buf *bytes.Buffer) *composeWriter {
	return &composeWriter{w: buf, width: func() int { return 80 }}
}

// Off, the writer is a pass-through: nothing else in the tail changes.
func TestComposeWriterPassThroughWhenOff(t *testing.T) {
	var buf bytes.Buffer
	cw := newTestComposeWriter(&buf)
	cw.Write([]byte("body"))
	if buf.String() != "body" {
		t.Errorf("got %q", buf.String())
	}
}

// On, transcript output erases the compose area, lands where the transcript
// left off, and the compose area is redrawn below it.
func TestComposeWriterKeepsComposeBelowOutput(t *testing.T) {
	var buf bytes.Buffer
	cw := newTestComposeWriter(&buf)
	cw.start("", "")
	cw.line.apply([]byte("hi"))
	cw.redraw()
	buf.Reset()

	cw.Write([]byte("more"))
	got := buf.String()
	erase := strings.Index(got, "\x1b8\x1b[J")
	body := strings.Index(got, "more")
	draw := strings.LastIndex(got, "> hi")
	if erase != 0 || body < erase || draw < body {
		t.Errorf("want erase, body, redraw in order; got %q", got)
	}
	// Rows are reserved with IND before the cursor is saved, so drawing never
	// scrolls the saved position away.
	if !strings.Contains(got[body:], "\x1bD\x1bD\x1b[2A\x1b7") {
		t.Errorf("missing reserve+save in %q", got)
	}
}

func TestComposeWriterStopErases(t *testing.T) {
	var buf bytes.Buffer
	cw := newTestComposeWriter(&buf)
	cw.start("", "")
	buf.Reset()
	cw.stop()
	if buf.String() != "\x1b8\x1b[J" {
		t.Errorf("stop wrote %q", buf.String())
	}
	buf.Reset()
	cw.Write([]byte("x"))
	if buf.String() != "x" {
		t.Errorf("after stop: %q", buf.String())
	}
}

// A note on stderr goes through around(), so it lands above the compose area
// rather than through the middle of it.
func TestComposeWriterAround(t *testing.T) {
	var buf bytes.Buffer
	cw := newTestComposeWriter(&buf)
	cw.start("", "")
	buf.Reset()
	cw.around(func() { buf.WriteString("NOTE\n") })
	got := buf.String()
	if !strings.HasPrefix(got, "\x1b8\x1b[J") || !strings.Contains(got, "NOTE\n") || !strings.HasSuffix(got, "> ") {
		t.Errorf("around: %q", got)
	}
}
