package main

import (
	"regexp"
	"strings"
)

// Claude Code wraps text the human pasted into the prompt in a
// `<pasted_content id="…">` … `</pasted_content id="…">` pair, each tag alone on
// its line. Two things make that unreadable in a rendered transcript. The pair
// is not valid HTML (the closing tag repeats the attributes), so glamour treats
// it as a raw tag and escapes it — the reader sees `<\pasted_content id="e840">`
// and `<\/pasted_content id="e840">`. And the id it insists on showing is an
// internal handle that means nothing to anyone reading.
//
// So the tags are replaced with the one fact they carry that a reader wants: a
// paste happened, and it was this big. The pasted text itself is passed through
// byte-for-byte — it is the human's words, and it is also what the collapse
// threshold is counting.
var (
	pasteOpenRe  = regexp.MustCompile(`^<pasted_content(?:\s[^>]*)?>$`)
	pasteCloseRe = regexp.MustCompile(`^</pasted_content(?:\s[^>]*)?>$`)
)

// markPastes rewrites each pasted-content wrapper in body as an italic
// `⎘ pasted N lines` marker followed by a blank line, leaving the pasted lines
// untouched. A tag anywhere but alone on its own line is left alone: that is a
// human writing ABOUT the wrapper, not Claude Code emitting one.
//
// Blank lines are inserted around the marker only where the source doesn't
// already have them — without one the marker is absorbed into the paragraph
// above it, and the paste's last line into the prose below.
func markPastes(body string) string {
	if !strings.Contains(body, "<pasted_content") {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines)+4)

	open := -1   // index in out of this paste's marker, -1 when outside one
	count := 0   // pasted lines so far, ignoring trailing blanks
	sep := false // whether a blank line was inserted ahead of the marker

	// finish closes the paste being accumulated. `at` is the index of the source
	// line that ended it, and trailing says whether this end may need a blank
	// line after it (a wrapper cut short by the NEXT wrapper doesn't: that one
	// inserts its own separator).
	finish := func(at int, trailing bool) {
		if open < 0 {
			return
		}
		if count == 0 {
			// Nothing between the tags. Drop the wrapper and any separator
			// reserved for it, leaving the surrounding lines exactly as they were
			// rather than announcing a paste of nothing.
			start := open
			if sep {
				start--
			}
			out = append(out[:start], out[open+2:]...)
		} else {
			out[open] = "*⎘ pasted " + plural(count, "line") + "*"
			if trailing && at+1 < len(lines) && strings.TrimSpace(lines[at+1]) != "" {
				out = append(out, "")
			}
		}
		open, count, sep = -1, 0, false
	}

	for i, l := range lines {
		switch {
		case pasteOpenRe.MatchString(l):
			finish(i, false) // an unterminated wrapper ends where the next starts
			sep = len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != ""
			if sep {
				out = append(out, "")
			}
			open = len(out)
			out = append(out, "", "") // the marker, and the blank line under it
		case pasteCloseRe.MatchString(l):
			finish(i, true)
		default:
			if open >= 0 && strings.TrimSpace(l) != "" {
				// Trailing blanks inside the wrapper aren't pasted lines; counting
				// them would report a one-line paste as two.
				count = len(out) - open - 1
			}
			out = append(out, l)
		}
	}
	finish(len(lines), true)
	return strings.Join(out, "\n")
}
