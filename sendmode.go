package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// sendmode.go is Ctrl-T: type into the tail and have the keys land in the agent
// pane, without moving focus. Ctrl-T again hands the keyboard back to the tail.
//
// The keyboard goroutine does the forwarding itself, so the render goroutine
// keeps streaming the transcript and you see the agent answer while you type.
// The render goroutine's only part is working out WHICH pane (it owns cur), and
// it answers on sendPaneCh. The pane is hunk's: the claude (or amp) in this iTerm
// tab writing the tailed session, or a lone one in the tab, and "" when that
// isn't certain, because keys typed into the wrong pane go into someone's work.
//
// Keys go through `write text … newline NO`, addressed by session id (hunk.go's
// rule: indexes shift). One osascript per keystroke would lag behind a typist,
// so a sender goroutine batches whatever arrived while the last call ran.
// Ctrl-C still signals this process (cbreak keeps ISIG); Esc is what interrupts
// the agent, and that is forwarded.

const sendToggle = 0x14 // Ctrl-T

// splitAtToggle returns the bytes before a Ctrl-T and whether one was there.
// Anything after it in the same read is dropped: it was typed after leaving.
func splitAtToggle(b []byte) ([]byte, bool) {
	if i := bytes.IndexByte(b, sendToggle); i >= 0 {
		return b[:i], true
	}
	return b, false
}

// sendBytes undoes the tty's ICRNL: cbreak delivers Enter as \n, and the agent
// submits on \r (a \n there is a soft newline in the prompt).
func sendBytes(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte{'\n'}, []byte{'\r'})
}

// completeUTF8 splits b into the part ending on a whole rune and an incomplete
// trailing rune to hold for the next batch. A paste read 16 bytes at a time can
// cut a rune in half, and each half alone is invalid text to AppleScript.
func completeUTF8(b []byte) (send, rest []byte) {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if !utf8.FullRune(b[i:]) {
			return b[:i], b[i:]
		}
		break
	}
	return b, nil
}

// asKeysExpr is b as an AppleScript string expression: printable runs quoted,
// control bytes (Esc, Backspace, Enter) as `character id N`, joined with &.
func asKeysExpr(b []byte) string {
	var parts []string
	var run []byte
	flush := func() {
		if len(run) > 0 {
			parts = append(parts, `"`+asEscape(string(run))+`"`)
			run = nil
		}
	}
	for _, c := range b {
		if c < 0x20 || c == 0x7f {
			flush()
			parts = append(parts, "(character id "+strconv.Itoa(int(c))+")")
			continue
		}
		run = append(run, c)
	}
	flush()
	return "(" + strings.Join(parts, " & ") + ")"
}

// sendKeysScript types b into the pane with session id pane, as keystrokes
// (newline NO: an Enter in b is the user's, not ours). Wrapped in try because
// the pane may close while send mode is on.
func sendKeysScript(pane string, b []byte) string {
	var s strings.Builder
	s.WriteString("tell application \"iTerm2\"\n")
	s.WriteString("\trepeat with w in windows\n")
	s.WriteString("\t\trepeat with t in tabs of w\n")
	s.WriteString("\t\t\trepeat with s in sessions of t\n")
	s.WriteString("\t\t\t\tif (id of s) is \"" + asEscape(pane) + "\" then\n")
	s.WriteString("\t\t\t\t\ttry\n")
	s.WriteString("\t\t\t\t\t\ttell s to write text " + asKeysExpr(b) + " newline NO\n")
	s.WriteString("\t\t\t\t\tend try\n")
	s.WriteString("\t\t\t\tend if\n")
	s.WriteString("\t\t\tend repeat\n")
	s.WriteString("\t\tend repeat\n")
	s.WriteString("\tend repeat\n")
	s.WriteString("end tell\n")
	return s.String()
}

// sendRun is the osascript seam, so a test never types into a real pane.
var sendRun = osaRun

// startSender runs the batching sender for pane. send queues bytes; stop
// flushes what's queued and waits, so the last keys land before the tail
// takes the keyboard back.
func startSender(pane string) (send func([]byte), stop func()) {
	ch := make(chan []byte, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf []byte
		for b := range ch {
			buf = append(buf, b...)
		drain:
			for {
				select {
				case more, ok := <-ch:
					if !ok {
						break drain
					}
					buf = append(buf, more...)
				default:
					break drain
				}
			}
			var out []byte
			out, buf = completeUTF8(buf)
			if len(out) > 0 {
				_ = sendRun(sendKeysScript(pane, out))
			}
			buf = append([]byte(nil), buf...)
		}
	}()
	return func(b []byte) { ch <- append([]byte(nil), b...) }, func() { close(ch); <-done }
}

// forwardKeys is send mode's read loop, run on the keyboard goroutine. It
// returns nil when Ctrl-T ends the mode and the read error otherwise.
func forwardKeys(tty *os.File, pane string) error {
	send, stop := startSender(pane)
	defer stop()
	buf := make([]byte, 256)
	for {
		n, err := tty.Read(buf)
		if err != nil {
			return err
		}
		b, toggled := splitAtToggle(buf[:n])
		if len(b) > 0 {
			send(sendBytes(b))
		}
		if toggled {
			return nil
		}
	}
}

// sendPaneMsg is the note for entering send mode, or for why it didn't start.
func sendPaneMsg(pane string, agent Agent) string {
	if pane == "" {
		return "send mode: no " + string(firstNonEmptyAgent(agent, AgentClaude)) + " pane found in this iTerm tab"
	}
	return "send mode on: keys go to the " + string(firstNonEmptyAgent(agent, AgentClaude)) + " pane · ctrl-t to stop"
}
