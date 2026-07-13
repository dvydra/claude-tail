package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
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

// sessionLink is an entire trail or GitHub PR referenced somewhere in a
// session's transcript, split into its owner/repo/id parts + canonical URL.
type sessionLink struct {
	Kind        string // "trail" | "PR"
	Owner, Repo string
	ID          string
	URL         string
}

// The two URL shapes we surface. Segments are constrained to real path atoms so
// documentation placeholders ({owner}, <repo>, &lt;number&gt;) don't match.
var (
	reTrailURL = regexp.MustCompile(`https://entire\.io/gh/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/trails/([A-Za-z0-9._-]+)`)
	rePRURL    = regexp.MustCompile(`https://github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/pull/([0-9]+)`)
)

// extractLinks scans a transcript file for entire-trail and GitHub-PR URLs and
// returns them deduped in first-seen order (trails before PRs). The URL is
// rebuilt from the captured owner/repo/id so it's canonical regardless of how it
// appeared (trailing slash, query string, …).
func extractLinks(path string) []sessionLink {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := string(data)
	seen := map[string]bool{}
	var out []sessionLink
	add := func(kind, owner, repo, id, url string) {
		if seen[url] {
			return
		}
		seen[url] = true
		out = append(out, sessionLink{Kind: kind, Owner: owner, Repo: repo, ID: id, URL: url})
	}
	for _, m := range reTrailURL.FindAllStringSubmatch(s, -1) {
		add("trail", m[1], m[2], m[3], "https://entire.io/gh/"+m[1]+"/"+m[2]+"/trails/"+m[3]+"/")
	}
	for _, m := range rePRURL.FindAllStringSubmatch(s, -1) {
		add("PR", m[1], m[2], m[3], "https://github.com/"+m[1]+"/"+m[2]+"/pull/"+m[3])
	}
	return out
}

// osc8 wraps label as a clickable terminal hyperlink (OSC 8) pointing at url.
// Terminals without OSC 8 just show the label text.
func osc8(url, label string) string {
	return "\x1b]8;;" + url + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

// showSummary shows a card for the session: an on-device Apple Intelligence
// summary (when fm + the model are available), the trails/PRs referenced in the
// transcript, and entire's metadata. Falls back to metadata only.
func showSummary(tty *os.File, s treeSession, home string) {
	// Resolve a transcript (local, else reconstructed) once — used for both the
	// on-device summary and the trail/PR scan.
	path := s.Path
	if path == "" {
		if tmp, ok := reconstructTranscript(home, s.ID, s.Repo); ok {
			path = tmp
		}
	}

	var ai aiSummary
	haveAI := false
	if fmAvailable() && path != "" {
		io.WriteString(tty, "\x1b[H\x1b[2J\n  Summarizing with Apple Intelligence…")
		ai, haveAI = aiSummarize(transcriptText(path, home))
	}
	var links []sessionLink
	if path != "" {
		links = extractLinks(path)
	}

	pager(tty, summaryCardLines(s, ai, haveAI, links, time.Now().Unix()), "SUMMARY "+shortID(s.ID), false)
}

// summaryCardLines builds the `i` card body: the AI summary (or snippet), the
// trails/PRs found in the transcript as clickable links, then entire's metadata.
// Pure so the layout is unit-tested without a tty; now is the reference time for
// the relative "activity" age.
func summaryCardLines(s treeSession, ai aiSummary, haveAI bool, links []sessionLink, now int64) []string {
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
	} else {
		add("")
		add("  %s", firstNonEmpty(s.Snippet, "(untitled session)"))
	}

	// Trails & PRs referenced anywhere in the transcript, as clickable links.
	if len(links) > 0 {
		add("")
		add("  ── trails & prs ────────────────────")
		for _, ln := range links {
			tag, label := "PR   ", fmt.Sprintf("%s/%s #%s", ln.Owner, ln.Repo, ln.ID)
			if ln.Kind == "trail" {
				tag, label = "trail", fmt.Sprintf("%s/%s · %s", ln.Owner, ln.Repo, ln.ID)
			}
			add("  %s  %s", tag, osc8(ln.URL, label))
		}
	}

	if haveAI || len(links) > 0 {
		add("")
		add("  ── entire ─────────────────────────")
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
		add("  activity   %s", relAge(s.Mtime, now))
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
	return L
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
		if rs[i] == 0x1b { // copy the whole escape without counting it
			b.WriteRune(rs[i])
			i++
			if i >= len(rs) {
				break
			}
			if rs[i] == ']' { // OSC (e.g. an OSC 8 hyperlink): ends at BEL or ST (ESC \)
				b.WriteRune(rs[i])
				for i++; i < len(rs); i++ {
					b.WriteRune(rs[i])
					if rs[i] == 0x07 || (rs[i] == '\\' && rs[i-1] == 0x1b) {
						break
					}
				}
				continue
			}
			// CSI / other: ends at an ASCII-letter final byte
			for ; i < len(rs); i++ {
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
