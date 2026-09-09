package main

import "strings"

// wraptext.go is entire-tail's word wrapper, applied to glamour's ALREADY
// RENDERED output rather than asking glamour to wrap for us.
//
// Why not glamour's own `WithWordWrap`: it wraps through muesli/reflow, whose
// wordwrap writer emits a breakpoint rune ('-') straight into the buffer WITHOUT
// counting it (reflow v0.3.0 wordwrap.go, the `inGroup(w.Breakpoints, c)`
// branch). Every hyphen on a line therefore under-counts that line by a column,
// so the line overshoots the limit — and glamour's later stages break the
// overshoot off as a stub, which is what the "and" / "in" / "that" orphan lines
// looked like on screen: a full line, then a one-word line, then the rest of the
// paragraph. Hyphens are everywhere in our text (flag names, repo names,
// op:// paths), so this fired constantly.
//
// Wrapping the rendered output instead of the markdown source buys three more
// things beyond correctness:
//
//   - No padding to undo. Glamour right-pads every line out to the wrap width
//     only when a width is set; at width 0 it emits none, so the pad-trimming
//     pass this file replaced (and its background-colour guard, which the
//     theme's inline-code background tripped, leaking pad into the clipboard)
//     is simply gone.
//   - Nothing is broken mid-token. We break at spaces only; a word longer than
//     the line overflows and is soft-wrapped by the terminal, which keeps a URL
//     or a shell command copyable. reflow breaks at hyphens, which is how a
//     `op://…/agent-token-elastic-ci-stack-buildkite/token` ended up cut in half.
//   - Continuations line up under their marker (see contPrefix).
//
// Everything here is pure string work over glamour's output, so it's unit-tested
// without a terminal. It runs ONLY when wrap > 0: piped output and --no-wrap
// still hand back glamour's bytes untouched, which is what keeps the goldens
// stable.

// wrapMinAvail is the narrowest usable text column. Below it a prefix has eaten
// so much of the line that wrapping produces worse output than leaving the
// terminal to soft-wrap, so the line is passed through.
const wrapMinAvail = 16

// boxRunes are the box-drawing characters glamour uses for tables and
// horizontal rules. A line carrying any of them (after its blockquote marker,
// which is also '│') is laid out by column and would be shredded by a re-wrap,
// so it's passed through for the terminal to soft-wrap.
const boxRunes = "│┼├┤┬┴┌┐└┘─━═╪"

// wrapANSI word-wraps rendered, ANSI-styled text at limit visible columns.
func wrapANSI(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapANSILine(line, limit)...)
	}
	return strings.Join(out, "\n")
}

// wrapANSILine wraps one rendered line into one or more lines.
func wrapANSILine(line string, limit int) []string {
	if visWidth(line) <= limit {
		return []string{line}
	}
	prefix, cont, rest := splitLinePrefix(line)
	if containsVisibleAny(rest, boxRunes) {
		return []string{line} // a table row or a rule: laid out by column
	}
	avail := limit - visWidth(prefix)
	if avail < wrapMinAvail {
		return []string{line}
	}
	return wrapSegments(prefix, cont, rest, avail)
}

// wrapSegments lays rest out greedily at avail columns, breaking only between
// words. The first line carries prefix, every later line carries cont.
func wrapSegments(prefix, cont, rest string, avail int) []string {
	var (
		out     []string
		cur     strings.Builder // the current line, after its prefix
		curW    int
		word    strings.Builder // the word being read
		wordW   int
		wordSGR string // the SGR state in force where the word starts
		space   strings.Builder
		spaceW  int
		// The prefix's own escapes are part of the colour state the text
		// inherits, so seed from them rather than starting bare — otherwise a
		// paragraph whose colour opens before its first word breaks into a
		// coloured first line and an uncoloured rest.
		active         = sgrState(prefix)
		curHasEscape   = strings.ContainsRune(prefix, 0x1b)
		curLinePrefix  = prefix
		nextLinePrefix = cont
	)

	// takeWord moves the buffered word onto the current line, breaking first if
	// it no longer fits. A word wider than the whole column still goes on a line
	// of its own rather than being cut in half.
	takeWord := func() {
		if wordW == 0 {
			return
		}
		if curW > 0 && curW+spaceW+wordW > avail {
			tail := ""
			if curHasEscape {
				tail = reset // don't leave a colour open across the break
			}
			out = append(out, curLinePrefix+cur.String()+tail)
			curLinePrefix = nextLinePrefix
			cur.Reset()
			curW = 0
			curHasEscape = false
			// The dropped space run may have carried escapes; wordSGR is the
			// state they produced, so re-opening it loses nothing.
			if wordSGR != "" {
				cur.WriteString(wordSGR)
				curHasEscape = true
			}
		}
		if curW > 0 {
			cur.WriteString(space.String())
			curW += spaceW
		}
		space.Reset()
		spaceW = 0
		cur.WriteString(word.String())
		curW += wordW
		word.Reset()
		wordW = 0
	}

	rs := []rune(rest)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b {
			esc, n := readEscape(rs[i:])
			i += n - 1
			active = applySGR(active, esc)
			curHasEscape = true
			if wordW > 0 {
				word.WriteString(esc)
			} else {
				space.WriteString(esc)
			}
			continue
		}
		if rs[i] == ' ' || rs[i] == '\t' {
			takeWord()
			space.WriteRune(rs[i])
			spaceW++
			continue
		}
		if wordW == 0 {
			wordSGR = active
		}
		word.WriteRune(rs[i])
		wordW++
	}
	takeWord()

	last := curLinePrefix + cur.String()
	if curHasEscape && !strings.HasSuffix(last, reset) {
		last += reset
	}
	return append(out, last)
}

// splitLinePrefix separates a rendered line's leading chrome from its text and
// says what a continuation line should start with. Glamour emits list items as
// "• text" and block quotes as "│ text" at wrap 0, with nested lists adding two
// spaces per level. A quote repeats its bar down the block (as glamour does);
// everything else indents continuations to the same column as the text, so a
// wrapped bullet lines up under itself rather than running back to the margin.
func splitLinePrefix(line string) (prefix, cont, rest string) {
	rs := []rune(line)
	var b strings.Builder
	width := 0
	i := 0
	// Leading indent: spaces, plus any escapes among them.
	for i < len(rs) {
		if rs[i] == 0x1b {
			esc, n := readEscape(rs[i:])
			b.WriteString(esc)
			i += n
			continue
		}
		if rs[i] != ' ' {
			break
		}
		b.WriteRune(rs[i])
		width++
		i++
	}
	indent := b.String()
	indentW := width
	// A marker, if one follows the indent.
	mark, n, quote := readMarker(rs[i:])
	if n == 0 {
		return indent, strings.Repeat(" ", indentW), string(rs[i:])
	}
	prefix = indent + mark
	rest = string(rs[i+n:])
	if quote {
		return prefix, prefix, rest
	}
	return prefix, strings.Repeat(" ", visWidth(prefix)), rest
}

// readMarker matches a list bullet ("• "), an ordered-list number ("12. ") or a
// block-quote bar ("│ ") at the head of rs, escapes included, and reports how
// many runes it consumed. quote is true for the bar, which repeats on
// continuation lines rather than becoming blank space.
func readMarker(rs []rune) (mark string, n int, quote bool) {
	var b strings.Builder
	var vis []rune
	i := 0
	for i < len(rs) {
		if rs[i] == 0x1b {
			esc, k := readEscape(rs[i:])
			b.WriteString(esc)
			i += k
			continue
		}
		vis = append(vis, rs[i])
		b.WriteRune(rs[i])
		i++
		switch {
		case len(vis) == 2 && (vis[0] == '•' || vis[0] == '│') && vis[1] == ' ':
			return b.String(), i, vis[0] == '│'
		case vis[len(vis)-1] == ' ' && len(vis) >= 3 && isOrderedMarker(vis):
			return b.String(), i, false
		case len(vis) > 5, !isMarkerRune(vis[len(vis)-1]):
			return "", 0, false
		}
	}
	return "", 0, false
}

// isMarkerRune reports whether r can appear inside a list/quote marker.
func isMarkerRune(r rune) bool {
	return r == '•' || r == '│' || r == ' ' || r == '.' || r == ')' || (r >= '0' && r <= '9')
}

// isOrderedMarker reports whether vis is an ordered-list marker: digits, then
// '.' or ')', then the single trailing space.
func isOrderedMarker(vis []rune) bool {
	if len(vis) < 3 || vis[len(vis)-1] != ' ' {
		return false
	}
	if d := vis[len(vis)-2]; d != '.' && d != ')' {
		return false
	}
	for _, r := range vis[:len(vis)-2] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// containsVisibleAny reports whether any of chars appears in s outside an ANSI
// escape — an escape's own parameter bytes must never count as content.
func containsVisibleAny(s, chars string) bool {
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b {
			_, n := readEscape(rs[i:])
			i += n - 1
			continue
		}
		if strings.ContainsRune(chars, rs[i]) {
			return true
		}
	}
	return false
}

// readEscape copies the whole ANSI escape sequence starting at rs[0] and
// reports how many runes it spans, so a wrap never slices one in half.
func readEscape(rs []rune) (string, int) {
	if len(rs) == 0 || rs[0] != 0x1b {
		return "", 0
	}
	var b strings.Builder
	b.WriteRune(rs[0])
	i := 1
	if i < len(rs) && rs[i] == ']' { // OSC (an OSC 8 hyperlink): ends at BEL or ST
		b.WriteRune(rs[i])
		for i++; i < len(rs); i++ {
			b.WriteRune(rs[i])
			if rs[i] == 0x07 || (rs[i] == '\\' && rs[i-1] == 0x1b) {
				return b.String(), i + 1
			}
		}
		return b.String(), i
	}
	for ; i < len(rs); i++ { // CSI / other: ends at an ASCII-letter final byte
		b.WriteRune(rs[i])
		if (rs[i] >= 'a' && rs[i] <= 'z') || (rs[i] >= 'A' && rs[i] <= 'Z') {
			return b.String(), i + 1
		}
	}
	return b.String(), i
}

// sgrState folds every escape in s into the colour state it leaves behind.
func sgrState(s string) string {
	active := ""
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] != 0x1b {
			continue
		}
		esc, n := readEscape(rs[i:])
		active = applySGR(active, esc)
		i += n - 1
	}
	return active
}

// applySGR folds one escape into the colour state carried across a line break:
// a reset clears it, any other SGR adds to it, and non-SGR sequences (cursor
// moves and the like) don't affect colour at all.
func applySGR(active, esc string) string {
	if !strings.HasSuffix(esc, "m") || !strings.HasPrefix(esc, "\x1b[") {
		return active
	}
	switch esc {
	case "\x1b[m", "\x1b[0m":
		return ""
	}
	return active + esc
}
