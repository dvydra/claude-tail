package main

import (
	"regexp"
	"strings"
)

// mrkdwn.go converts the transcript's markdown to Slack **mrkdwn** — the `y`
// yank and the `m` view mode both go through it.
//
// The target is the Slack COMPOSER (a human pasting into the message box), not
// the Web API, and that decides two things the API docs would tell you to do
// differently:
//
//   - No HTML escaping. `&amp;`/`&lt;` are how the API accepts `&`/`<`; pasted
//     into the composer they show up as the literal five characters.
//   - No `<url|text>` links. That form is parsed for API-posted messages; a
//     user-typed `<` is escaped server-side, so a pasted one renders literally.
//     Markdown's own `[text](url)` is passed through untouched — the composer
//     understands it.
//
// Everything else is the ordinary mrkdwn dialect: `*bold*` (one asterisk),
// `_italic_`, `~strike~`, no headings, no tables.
//
// Code is passed through byte-for-byte — inside a fence or backticks nothing is
// converted, because the whole point of copying a command is that it still runs
// when you paste it somewhere else. Fences keep their fence (Slack renders it as
// a code box, and copying out of that box gives the raw command back) but lose
// the language tag, which mrkdwn ignores.

var (
	mdBoldRe   = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	mdStrikeRe = regexp.MustCompile(`~~([^~\n]+)~~`)
	// Italic requires non-space just inside the asterisks (as markdown itself
	// does), so an arithmetic "2 * x * 3" isn't emphasised, and a non-word char
	// just outside, so **bold** never matches here — see the ordering note below.
	mdItalicRe   = regexp.MustCompile(`(^|[^\w*])\*([^*\s]|[^*\s][^*\n]*[^*\s])\*($|[^\w*])`)
	mdLinkRe     = regexp.MustCompile(`!?\[([^\]\n]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	mdHeadingRe  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*$`)
	mdBulletRe   = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	mdTaskRe     = regexp.MustCompile(`^\[([ xX])\]\s+(.*)$`)
	mdTableRowRe = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	mdFenceRe    = regexp.MustCompile("^\\s*```")
	// RE2 has no backreferences, so the three rule characters are spelled out.
	mdRuleRe = regexp.MustCompile(`^\s*((-\s*){3,}|(\*\s*){3,}|(_\s*){3,})$`)
)

// toSlackMrkdwn converts a markdown body to Slack mrkdwn.
func toSlackMrkdwn(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !mdFenceRe.MatchString(line) {
			if inFence {
				out = append(out, line) // verbatim: this is someone's command
				continue
			}
			// Slack has no tables. A narrow one still reads as a table inside a
			// code box (monospaced, so the columns line up), and that's the better
			// answer while it fits the pane — so the rows go through untouched,
			// alignment being the point. Past that, tableBlocks turns it inside out
			// into one block per row instead of a box nobody can scroll.
			if n := mdTableRun(lines[i:]); n > 0 {
				if blocks := tableBlocks(lines[i : i+n]); blocks != nil {
					out = append(out, blocks...)
				} else {
					out = append(out, "```")
					out = append(out, lines[i:i+n]...)
					out = append(out, "```")
				}
				i += n - 1
				continue
			}
			out = append(out, mrkdwnLine(line))
			continue
		}
		// A fence line. Slack needs each ``` on a line of its OWN — a one-line
		// ```code``` renders as literal backticks — so the fences are always
		// re-emitted as their own rows with the body between them.
		rest := strings.TrimPrefix(strings.TrimSpace(line), "```")
		if inFence {
			out = append(out, "```")
			inFence = false
			continue
		}
		if i := strings.Index(rest, "```"); i >= 0 {
			// Whole fence on one line: split it into three.
			out = append(out, "```", rest[:i], "```")
			if tail := strings.TrimSpace(rest[i+3:]); tail != "" {
				out = append(out, mrkdwnLine(tail))
			}
			continue
		}
		// Opening fence. The language tag is dropped (mrkdwn has none, and a
		// stray "sh" would show up as the first line inside the code box), but
		// anything after it on the same line is body and must survive.
		out = append(out, "```")
		inFence = true
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			if body := strings.TrimSpace(rest[i:]); body != "" {
				out = append(out, body)
			}
		}
	}
	if inFence { // unterminated fence: close it so Slack doesn't swallow the rest
		out = append(out, "```")
	}
	return strings.Join(out, "\n")
}

// mdTableRun returns the number of leading lines of a markdown table (header +
// separator + rows), or 0 if lines doesn't start with one. Two pipe rows are
// the minimum — a single one is more likely prose that happens to hold pipes.
func mdTableRun(lines []string) int {
	n := 0
	for _, l := range lines {
		if !mdTableRowRe.MatchString(l) {
			break
		}
		n++
	}
	if n < 2 {
		return 0
	}
	return n
}

// mrkdwnLine converts a single non-code line.
func mrkdwnLine(line string) string {
	// A heading has no mrkdwn equivalent; bold is the closest thing that still
	// reads as a heading in a Slack message.
	if m := mdHeadingRe.FindStringSubmatch(line); m != nil {
		if body := strings.TrimSpace(m[2]); body != "" {
			return "*" + mrkdwnInline(body) + "*"
		}
		return ""
	}
	// A horizontal rule renders as literal dashes in Slack; a box-drawing run at
	// least looks like the divider it was.
	if mdRuleRe.MatchString(line) {
		return strings.Repeat("─", 24)
	}
	if m := mdBulletRe.FindStringSubmatch(line); m != nil {
		indent, item := m[1], m[2]
		// Slack only renders real lists in rich_text blocks, so a pasted "- " stays
		// a hyphen. An explicit bullet glyph is what the reader would have seen
		// anyway, and nested levels get the hollow one.
		bullet := "•"
		if len(indent) >= 2 {
			bullet = "◦"
		}
		if t := mdTaskRe.FindStringSubmatch(item); t != nil {
			// Both glyphs must have EMOJI presentation in Slack or the list looks
			// like two different lists: ☑ renders as a tile, but its natural
			// partner ☐ (and the text ✓) render as tiny outlines beside it. The
			// unticked half is Slack's own :black_square_button:, which is the
			// same weight as ☑. Chosen from a live paste, not from the chart.
			box := ":black_square_button:"
			if t[1] != " " {
				box = "☑"
			}
			return indent + bullet + " " + box + " " + mrkdwnInline(t[2])
		}
		return indent + bullet + " " + mrkdwnInline(item)
	}
	return mrkdwnInline(line)
}

// mrkdwnInline converts the inline spans of one line, leaving `code` alone.
func mrkdwnInline(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			b.WriteString(mrkdwnSpans(s))
			return b.String()
		}
		// A backtick run (``…``) closes on a run of the same length.
		n := 1
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		tick := s[i : i+n]
		rest := s[i+n:]
		j := strings.Index(rest, tick)
		if j < 0 { // unpaired backtick: not code, just a character
			b.WriteString(mrkdwnSpans(s[:i+n]))
			s = rest
			continue
		}
		b.WriteString(mrkdwnSpans(s[:i]))
		// EXACTLY three: a longer run is how markdown writes inline code that
		// itself contains backticks (``` ``` ``` ```), and turning that into a
		// block mangles the sentence around it.
		if n == 3 {
			// A ``` block that started mid-line still has to become a block: Slack
			// renders a one-line ```code``` as literal backticks, so it is broken
			// out onto its own lines, taking the text around it with it.
			cur := strings.TrimRight(b.String(), " \t")
			b.Reset()
			b.WriteString(cur)
			if cur != "" && !strings.HasSuffix(cur, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("```\n" + strings.Trim(rest[:j], "\n") + "\n```")
			s = rest[j+n:]
			if strings.TrimSpace(s) != "" {
				b.WriteString("\n")
				s = strings.TrimLeft(s, " \t")
			}
			continue
		}
		b.WriteString(tick + rest[:j] + tick) // verbatim
		s = rest[j+n:]
	}
}

// mrkdwnSpans converts a code-free run, leaving `[text](url)` links intact.
//
// Slack's composer understands the markdown link form, so it is passed through
// as written — only the API needs `<url|text>`, and that form pastes literally
// (a user-typed `<` is escaped server-side), so it is never emitted. The URL is
// held out of the emphasis passes, which would otherwise eat a path containing
// `__` or `*`; the label still gets converted.
func mrkdwnSpans(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range mdLinkRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(mrkdwnEmphasis(s[last:m[0]]))
		b.WriteString(s[m[0]:m[2]])                 // "[" or "!["
		b.WriteString(mrkdwnEmphasis(s[m[2]:m[3]])) // the label
		// The URL only — a markdown title, [t](url "title"), is not understood by
		// the composer: it renders the whole thing as literal text with a bare
		// link in the middle. Verified in a real paste.
		b.WriteString("](" + s[m[4]:m[5]] + ")")
		last = m[1]
	}
	b.WriteString(mrkdwnEmphasis(s[last:]))
	return b.String()
}

// mrkdwnEmphasis converts bold/italic/strike in a run with no code and no links.
func mrkdwnEmphasis(s string) string {
	s = mdStrikeRe.ReplaceAllString(s, "~$1~")
	// Italic BEFORE bold: single-asterisk italic is the one form that would be
	// eaten by the bold pass (which turns **x** into *x*, making it look italic).
	s = mdItalicRe.ReplaceAllString(s, "${1}_${2}_${3}")
	s = mdBoldRe.ReplaceAllString(s, "*$1$2*") // one of the two groups is empty
	// _italic_ is already mrkdwn; leave it.
	return s
}
