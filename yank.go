package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// yank.go is the `y` key: put the last agent turn(s) on the clipboard as Slack
// mrkdwn.
//
// It converts from the transcript's RAW markdown, not from the screen. Copying
// the screen with the mouse gets you glamour's soft-wrapped, indented, ANSI-
// colored rendering of the text; the yank gets the text itself.
//
// Repeated presses extend the selection backwards a turn at a time
// (yankExtendWindow). The counter lives on the render goroutine — the keyboard
// only signals — so nothing here races the renderer.

// yankExtendWindow is how long a second `y` still counts as "…and the one
// before that" rather than starting over.
const yankExtendWindow = 3 * time.Second

// yankMaxMsgs bounds the buffer: agent messages are the only thing kept, and a
// long session shouldn't grow it without limit.
const yankMaxMsgs = 40

// yankMsg is one agent MESSAGE, which is the unit `y` copies — deliberately not
// "everything since the last user message". An agent turn can run for twenty
// minutes of narration around tool calls; what you want in Slack is almost
// always the last thing it actually said, and a repeated `y` walks back from
// there. Coarser grouping could never give you just the summary.
//
// The id is the provider's message id, which is how a message that arrives as
// two text records (Claude does this) stays one message. An agent that doesn't
// report one (codex/agy) gets one message per record, which is the same thing
// for those transcripts.
type yankMsg struct {
	id     string
	bodies []string
}

// recordYank appends an assistant body to the buffer. Called from emit on the
// render goroutine (the same goroutine that reads it for the copy).
func (r *Renderer) recordYank(body, msgID string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	if n := len(r.yankMsgs); n > 0 && msgID != "" && r.yankMsgs[n-1].id == msgID {
		r.yankMsgs[n-1].bodies = append(r.yankMsgs[n-1].bodies, body)
		return
	}
	r.yankMsgs = append(r.yankMsgs, yankMsg{id: msgID, bodies: []string{body}})
	if len(r.yankMsgs) > yankMaxMsgs {
		r.yankMsgs = r.yankMsgs[len(r.yankMsgs)-yankMaxMsgs:]
	}
}

// yankText returns the last n agent messages as Slack mrkdwn, newest last, and
// how many it actually found (which is less than n near the start of a
// session).
func (r *Renderer) yankText(n int) (string, int) {
	if len(r.yankMsgs) == 0 {
		return "", 0
	}
	n = min(n, len(r.yankMsgs))
	var parts []string
	for _, m := range r.yankMsgs[len(r.yankMsgs)-n:] {
		parts = append(parts, strings.Join(m.bodies, "\n\n"))
	}
	return toSlackMrkdwn(strings.Join(parts, "\n\n")), n
}

// yank is the whole `y` action: work out how many turns this press means,
// convert, copy, and report. Runs on the render goroutine.
func (r *Renderer) yank(tty *os.File, now time.Time) string {
	if now.Sub(r.lastYankAt) <= yankExtendWindow {
		r.yankN++
	} else {
		r.yankN = 1
	}
	r.lastYankAt = now

	text, got := r.yankText(r.yankN)
	if got == 0 {
		r.yankN = 0
		return "nothing to yank yet — no agent message in this window"
	}
	// Asking for more messages than exist stops growing rather than silently
	// re-copying the same thing under a bigger number.
	r.yankN = got
	if err := clipboardWrite(text, tty); err != nil {
		return "copy failed: " + err.Error()
	}
	return fmt.Sprintf("copied %s as slack mrkdwn (%s — press y again to add the one before)",
		plural(got, "message"), byteCount(len(text)))
}

func byteCount(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d chars", n)
	}
	return fmt.Sprintf("%.1fk chars", float64(n)/1024)
}

// clipboardWrite is the seam the tests stub. A test that exercised the real
// path would run pbcopy and silently replace whatever the developer had
// copied — a `go test` must not touch the system clipboard.
var clipboardWrite = copyToClipboard

// copyToClipboard puts s on the system clipboard: the platform helper when
// there is one, else OSC 52 written to the tty (which also covers ssh/tmux, and
// is why the tty is threaded through here).
func copyToClipboard(s string, tty *os.File) error {
	if bin, args := clipboardCmd(); bin != "" {
		if path, err := exec.LookPath(bin); err == nil {
			cmd := exec.Command(path, args...)
			cmd.Stdin = strings.NewReader(s)
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
	}
	if tty == nil {
		return fmt.Errorf("no clipboard helper and no tty")
	}
	_, err := io.WriteString(tty, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(s))+"\a")
	return err
}

// clipboardCmd names the platform's clipboard writer (empty when there is none
// and OSC 52 is the only route).
func clipboardCmd() (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "pbcopy", nil
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return "wl-copy", nil
		}
		return "xclip", []string{"-selection", "clipboard"}
	}
	return "", nil
}
