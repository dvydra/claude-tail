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

// preview.go renders the tree's `i` sub-view: a combined info card (AI summary +
// trails/PRs + metadata) fixed at the top, a divider, then the session's recent
// transcript in a scrollable pane below. It runs inside the alt-screen (the tty
// is already in raw mode) and returns to the tree on q/Esc. It reads/execs
// (transcript files, reconstruction), so it lives in the driver, not the reducer.

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
	r.endLine() // close a trailing dot-streak bracket / deferred newline
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

// showInfo shows the combined `i` view: an info card (on-device Apple
// Intelligence summary when fm + the model are available, the trails/PRs
// referenced in the transcript, and entire's metadata) fixed at the top, then
// the session's recent transcript in a scrollable pane below the divider.
func showInfo(tty *os.File, s treeSession, home string, theme Theme) {
	// Resolve a transcript (local, else reconstructed) once — used for the
	// on-device summary, the trail/PR scan, and the preview pane.
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
	card := summaryCardLines(s, ai, haveAI, links, time.Now().Unix())

	var preview []string
	if path == "" {
		preview = []string{"  No local transcript for this session, and its repo isn't checked out here."}
	} else {
		preview = renderPreviewLines(path, home, theme)
	}

	pagerSplit(tty, card, preview, "INFO "+shortID(s.ID)+"  "+s.Snippet, theme)
}

// minPreviewRows is the minimum scrollable preview height; the fixed card is
// clipped before the preview drops below it.
const minPreviewRows = 5

// splitPaneHeights divides a terminal of h rows between the fixed card pane and
// the scrollable preview pane, reserving 3 rows of chrome (title + divider +
// footer) and always leaving the preview at least minPreviewRows — so the card
// (which carries path/updated) is what gets clipped when space is tight.
func splitPaneHeights(h, cardLen int) (cardH, body int) {
	avail := max(h-3, 1)
	cardH = min(cardLen, max(avail-minPreviewRows, 1))
	body = max(avail-cardH, 1)
	return cardH, body
}

// pagerSplit renders a fixed top pane (the info card), a divider, and a
// scrollable bottom pane (the transcript preview, started at the latest turns).
// The card is clipped if the terminal is too short to show it whole and still
// leave room to scroll; only the preview scrolls. q/Esc returns.
func pagerSplit(tty *os.File, card, preview []string, title string, theme Theme) {
	buf := make([]byte, 16)
	top := len(preview) // clamped to the bottom on the first render
	for {
		w, h := termSize(tty)
		cardH, body := splitPaneHeights(h, len(card))
		maxTop := max(len(preview)-body, 0)
		top = min(max(top, 0), maxTop)

		var b strings.Builder
		b.WriteString("\x1b[H")
		b.WriteString("\x1b[1m" + truncVisible("  "+title, w) + reset + "\x1b[K\n")
		for i := range cardH {
			line := card[i]
			if i == cardH-1 && cardH < len(card) { // card clipped → mark it
				line = fmt.Sprintf("%s  ⋯ (%d more — resize taller for the full card)%s", theme.DimANSI, len(card)-cardH+1, reset)
			}
			b.WriteString(truncVisible(line, w) + "\x1b[K\n")
		}
		b.WriteString(theme.DimANSI + strings.Repeat("─", max(w, 1)) + reset + "\x1b[K\n")
		shown := 0
		for i := top; i < top+body && i < len(preview); i++ {
			b.WriteString(truncVisible(preview[i], w) + "\x1b[K\n")
			shown++
		}
		for ; shown < body; shown++ {
			b.WriteString("\x1b[K\n")
		}
		fmt.Fprintf(&b, "\x1b[2m  preview %d–%d of %d · ↑↓/PgUp/PgDn scroll · q/Esc back"+reset,
			min(top+1, len(preview)), min(top+body, len(preview)), len(preview))
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

// summaryCardLines builds the `i` card body: the AI summary (or snippet), then
// entire's metadata (repo/model/tokens/activity/updated/path), then the
// trails/PRs found in the transcript as clickable links. Metadata comes before
// the (capped) link list so the high-value facts survive if the card is clipped.
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

	// entire's metadata first — including path + last-updated, the highest-value
	// facts — so they stay visible even if a long card is clipped in the info view.
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
	if s.Msgs > 0 {
		add("  checkpoints %d", s.Msgs)
	}
	if s.Mtime > 0 {
		add("  activity   %s", relAge(s.Mtime, now))
		add("  updated    %s", time.Unix(s.Mtime, 0).Format("2006-01-02 15:04"))
	}
	if s.Path != "" {
		add("  path       %s", s.Path)
	}

	// Trails & PRs referenced in the transcript, as clickable links — after the
	// metadata (they're also visible in the preview), capped so a long list
	// doesn't push the card off-screen.
	if len(links) > 0 {
		add("")
		add("  ── trails & prs ────────────────────")
		const maxShownLinks = 8
		for i, ln := range links {
			if i >= maxShownLinks {
				add("  … +%d more", len(links)-maxShownLinks)
				break
			}
			tag, label := "PR   ", fmt.Sprintf("%s/%s #%s", ln.Owner, ln.Repo, ln.ID)
			if ln.Kind == "trail" {
				tag, label = "trail", fmt.Sprintf("%s/%s · %s", ln.Owner, ln.Repo, ln.ID)
			}
			add("  %s  %s", tag, osc8(ln.URL, label))
		}
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
