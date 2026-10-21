package main

import (
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// sendecho.go draws what you type in send mode (sendmode.go) at the bottom of
// the tail:
//
//	chat chat chat
//	--- to the agent · ctrl-t to stop
//	> my text here
//
// The transcript keeps streaming while you type, so the compose area has to stay
// BELOW it. composeWriter sits under the renderer's bufio writer: while send mode
// is on, every write erases the compose area, writes the transcript where it left
// off, and draws the compose area again. Only the render goroutine writes, so
// there is no locking.
//
// Erasing relies on the cursor save/restore (ESC 7 / ESC 8) at the end of the
// transcript. A saved position does not move when the screen scrolls, so the
// compose rows are reserved first with IND (ESC D, which scrolls when needed and
// keeps the column) and the cursor moved back up before saving. Drawing then
// never scrolls, and ESC 8 lands exactly where the transcript stopped, which is
// also where an open body line (render.go's lineOpen) continues.
//
// The echo is an approximation of the agent's prompt, not a copy of it: arrow
// keys and other escape sequences are dropped, Backspace and Ctrl-U edit, Enter
// clears (the agent's own transcript shows the message once it lands).

// composeLine is the text typed since the last Enter.
type composeLine struct {
	text string
	esc  int // 0 none, 1 after ESC, 2 inside CSI, 3 after SS3
}

func (c *composeLine) apply(b []byte) {
	for len(b) > 0 {
		ch := b[0]
		switch c.esc {
		case 1:
			switch ch {
			case '[':
				c.esc = 2
			case 'O':
				c.esc = 3
			default:
				c.esc = 0 // Alt+key: dropped with its ESC
			}
			b = b[1:]
			continue
		case 2:
			if ch >= 0x40 && ch <= 0x7e {
				c.esc = 0
			}
			b = b[1:]
			continue
		case 3:
			c.esc = 0
			b = b[1:]
			continue
		}
		switch {
		case ch == 0x1b:
			c.esc = 1
		case ch == '\r' || ch == '\n' || ch == 0x15: // Enter submits; Ctrl-U clears
			c.text = ""
		case ch == 0x7f || ch == 0x08:
			_, n := utf8.DecodeLastRuneInString(c.text)
			c.text = c.text[:len(c.text)-n]
		case ch < 0x20:
		default:
			_, n := utf8.DecodeRune(b)
			c.text += string(b[:n])
			b = b[n:]
			continue
		}
		b = b[1:]
	}
	// An ESC that ends a read is the Esc key on its own (an escape sequence
	// arrives in one read), so it must not swallow the next key typed.
	if c.esc == 1 {
		c.esc = 0
	}
}

// composeRows is how many rows below the transcript the compose area takes: the
// divider, plus the prompt line and its wraps. Over-reserving by one when the
// prompt exactly fills a row is harmless; under-reserving would scroll.
func composeRows(text string, width int) int {
	if width <= 0 {
		return 2
	}
	return 2 + (2+visWidth(text))/width
}

const (
	escSave    = "\x1b7"
	escRestore = "\x1b8"
	escClear   = "\x1b[J"
	escIndex   = "\x1bD"
)

type composeWriter struct {
	w      io.Writer
	width  func() int
	active bool
	line   composeLine
	dim    string
	user   string
}

func (c *composeWriter) Write(p []byte) (int, error) {
	if !c.active {
		return c.w.Write(p)
	}
	io.WriteString(c.w, escRestore+escClear)
	n, err := c.w.Write(p)
	c.draw()
	return n, err
}

// around runs a write that bypasses this writer (a note on stderr) with the
// compose area out of its way.
func (c *composeWriter) around(write func()) {
	if !c.active {
		write()
		return
	}
	io.WriteString(c.w, escRestore+escClear)
	write()
	c.draw()
}

func (c *composeWriter) start(dim, user string) {
	c.dim, c.user = dim, user
	c.line = composeLine{}
	c.active = true
	c.draw()
}

func (c *composeWriter) stop() {
	if !c.active {
		return
	}
	io.WriteString(c.w, escRestore+escClear)
	c.active = false
}

// redraw repaints the compose area after the typed line changed.
func (c *composeWriter) redraw() {
	if !c.active {
		return
	}
	io.WriteString(c.w, escRestore+escClear)
	c.draw()
}

func (c *composeWriter) draw() {
	rows := composeRows(c.line.text, c.width())
	var b strings.Builder
	b.WriteString(strings.Repeat(escIndex, rows))
	b.WriteString("\x1b[" + strconv.Itoa(rows) + "A" + escSave)
	b.WriteString("\r\n" + c.dim + "--- to the agent · ctrl-t to stop" + resetIf(c.dim))
	b.WriteString("\r\n" + c.user + "> " + resetIf(c.user) + c.line.text)
	io.WriteString(c.w, b.String())
}

func resetIf(color string) string {
	if color == "" {
		return ""
	}
	return reset
}
