package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// preview.go renders the two in-picker sub-views reached from the tree: `p`
// previews a session's recent transcript, `i` shows a summary card. Both run
// inside the alt-screen (the tty is already in raw mode) and return to the tree
// on q/Esc. They read/exec (transcript files, reconstruction), so they live in
// the driver rather than the pure reducer.

// showPreview renders the tail of a session's transcript and pages through it.
func showPreview(tty *os.File, s treeSession, home string, theme Theme) {
	path := s.Path
	if path == "" { // cloud-only — pull it from the repo's git checkpoint refs
		if tmp, ok := reconstructTranscript(home, s.ID, s.Repo); ok {
			path = tmp
		}
	}
	var lines []string
	if path == "" {
		lines = []string{"", "  No local transcript for this session, and its repo isn't checked out here."}
	} else {
		lines = renderPreviewLines(path, home, theme)
	}
	pager(tty, lines, "PREVIEW "+shortID(s.ID)+"  "+s.Snippet, true) // start at the latest turns
}

// renderPreviewLines renders the last chunk of a transcript to ANSI lines via the
// normal renderer (dots + collapse, so a preview stays compact).
func renderPreviewLines(path, home string, theme Theme) []string {
	var buf bytes.Buffer
	r, err := newRenderer(&buf, theme, "dots", 5)
	if err != nil {
		return []string{"  (cannot render this session)"}
	}
	agent := detectAgentForFile(home, path)
	loc := time.Local
	for _, l := range tailLines(path, 400) { // recent turns; bounded for big sessions
		for _, rec := range normalize(agent, l, loc) {
			r.emit(rec)
		}
	}
	out := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(out) == 1 && out[0] == "" {
		return []string{"  (nothing to preview)"}
	}
	return out
}

// showSummary shows a card for the session: an on-device Apple Intelligence
// summary (when the helper is built and the model is available) plus entire's
// metadata. Falls back to metadata only.
func showSummary(tty *os.File, s treeSession, home string) {
	// Resolve a transcript (local, else reconstructed) and summarize it on-device.
	var ai aiSummary
	haveAI := false
	if fmAvailable() {
		path := s.Path
		if path == "" {
			if tmp, ok := reconstructTranscript(home, s.ID, s.Repo); ok {
				path = tmp
			}
		}
		if path != "" {
			io.WriteString(tty, "\x1b[H\x1b[2J\n  Summarizing with Apple Intelligence…")
			ai, haveAI = aiSummarize(transcriptText(path, home))
		}
	}

	var L []string
	add := func(f string, a ...any) { L = append(L, fmt.Sprintf(f, a...)) }

	if haveAI {
		add("")
		add("  %s", firstNonEmpty(ai.Headline, s.Snippet))
		add("")
		for _, w := range wrapText(ai.Summary, 76) {
			add("  %s", w)
		}
		if len(ai.KeyPoints) > 0 {
			add("")
			for _, kp := range ai.KeyPoints {
				add("  • %s", kp)
			}
		}
		if ai.Outcome != "" {
			add("")
			add("  → %s", ai.Outcome)
		}
		add("")
		add("  ── entire ─────────────────────────")
	} else {
		add("")
		add("  %s", firstNonEmpty(s.Snippet, "(untitled session)"))
	}
	add("")
	add("  session    %s", s.ID)
	if s.Repo != "" {
		add("  repo       %s", s.Repo)
	}
	if s.Branch != "" {
		add("  branch     %s", s.Branch)
	}
	if s.Model != "" {
		add("  model      %s", s.Model)
	}
	var usage []string
	if s.Tokens > 0 {
		usage = append(usage, formatTokens(s.Tokens)+" tokens")
	}
	if s.Msgs > 0 {
		usage = append(usage, fmt.Sprintf("%d checkpoints", s.Msgs))
	}
	if len(usage) > 0 {
		add("  usage      %s", strings.Join(usage, " · "))
	}
	if s.Mtime > 0 {
		add("  activity   %s", relAge(s.Mtime, time.Now().Unix()))
	}
	if s.Prompt != "" {
		add("")
		add("  opening prompt")
		for _, w := range wrapText(s.Prompt, 76) {
			add("    %s", w)
		}
	}
	if s.Tokens == 0 && s.Model == "" && s.Prompt == "" {
		add("")
		add("  (not tracked by entire — only local metadata available)")
	}
	pager(tty, L, "SUMMARY "+shortID(s.ID), false)
}

// pager is a minimal scroll view over pre-rendered lines. q/Esc returns.
// atBottom starts scrolled to the end (for previews — show the latest turns).
func pager(tty *os.File, lines []string, title string, atBottom bool) {
	buf := make([]byte, 16)
	top := 0
	if atBottom {
		top = len(lines) // clamped to maxTop on the first render below
	}
	for {
		w, h := termSize(tty)
		body := max(h-2, 1)
		maxTop := max(len(lines)-body, 0)
		top = min(max(top, 0), maxTop)

		var b strings.Builder
		b.WriteString("\x1b[H")
		b.WriteString("\x1b[1m" + truncVisible("  "+title, w) + reset + "\x1b[K\n")
		shown := 0
		for i := top; i < top+body && i < len(lines); i++ {
			b.WriteString(truncVisible(lines[i], w) + "\x1b[K\n")
			shown++
		}
		for ; shown < body; shown++ {
			b.WriteString("\x1b[K\n")
		}
		fmt.Fprintf(&b, "\x1b[2m  %d–%d of %d · ↑↓/PgUp/PgDn scroll · q/Esc back"+reset,
			min(top+1, len(lines)), min(top+body, len(lines)), len(lines))
		b.WriteString("\x1b[K\x1b[J")
		io.WriteString(tty, b.String())

		n, err := tty.Read(buf)
		if err != nil || n == 0 {
			return
		}
		k, r := decodeKey(buf[:n])
		switch {
		case k == kEsc, k == kCtrlC, k == kRune && (r == 'q' || r == 'Q'):
			return
		case k == kUp, k == kRune && r == 'k':
			top--
		case k == kDown, k == kRune && r == 'j':
			top++
		case k == kPageUp:
			top -= body - 1
		case k == kPageDown, k == kRune && r == ' ':
			top += body - 1
		case k == kHome, k == kRune && r == 'g':
			top = 0
		case k == kEnd, k == kRune && r == 'G':
			top = maxTop
		}
	}
}

// truncVisible truncates s to w visible columns, passing ANSI escapes through
// uncounted so color codes aren't sliced mid-sequence.
func truncVisible(s string, w int) string {
	if w <= 0 {
		return s
	}
	var b strings.Builder
	vis := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b { // copy the whole escape (…letter terminator) without counting
			b.WriteRune(rs[i])
			for i++; i < len(rs); i++ {
				b.WriteRune(rs[i])
				if (rs[i] >= 'a' && rs[i] <= 'z') || (rs[i] >= 'A' && rs[i] <= 'Z') {
					break
				}
			}
			continue
		}
		if vis >= w {
			return b.String() + reset
		}
		b.WriteRune(rs[i])
		vis++
	}
	return b.String()
}

// wrapText soft-wraps a string to width w on word boundaries.
func wrapText(s string, w int) []string {
	s = strings.Join(strings.Fields(s), " ")
	var out []string
	for len(s) > w {
		cut := strings.LastIndex(s[:w], " ")
		if cut <= 0 {
			cut = w
		}
		out = append(out, s[:cut])
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
