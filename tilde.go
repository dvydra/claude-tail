package main

import "strings"

// Glamour parses with goldmark's GFM strikethrough, which accepts a SINGLE `~`
// pair as well as `~~`, intraword included. So a path like
// `…/iCloud~md~obsidian/…` (every Obsidian vault in iCloud) rendered with `md`
// struck through and both tildes gone. Glamour gives no hook to configure the
// extension, so the fix is in the input: a lone `~` becomes the entity `&#126;`.
// `~~double~~` still strikes through.
//
// An entity, not `\~`: glamour prints a text node's raw source bytes, so a
// backslash escape shows up as a literal `\~`, while it does run every text
// token through html.UnescapeString (measured, glamour v1.0.0). Places that are
// NOT unescaped are copied verbatim, since an entity there would show as typed:
// fenced and indented code, code spans, autolinks, and bare URLs.
func escapeLoneTildes(s string) string {
	if !strings.Contains(s, "~") {
		return s
	}
	lines := strings.Split(s, "\n")
	var fence string // the open fence's marker run, "" outside a fence
	prevBlank, prevIndented := true, false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		lead := len(line) - len(trimmed)
		if fence != "" {
			if lead < 4 && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]+" \t") == "" {
				fence = ""
			}
			continue
		}
		if lead < 4 {
			if f := fenceMarker(trimmed); f != "" {
				fence = f
				prevBlank, prevIndented = false, false
				continue
			}
		}
		blank := strings.TrimSpace(line) == ""
		indented := !blank && (lead >= 4 || strings.HasPrefix(line, "\t")) && (prevBlank || prevIndented)
		if !indented {
			lines[i] = escapeTildesInline(line)
		}
		prevBlank, prevIndented = blank, indented || (blank && prevIndented)
	}
	return strings.Join(lines, "\n")
}

// fenceMarker returns the opening run of a code fence (``` or ~~~, 3+), or "".
func fenceMarker(line string) string {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return ""
	}
	n := len(line) - len(strings.TrimLeft(line, line[:1]))
	if n < 3 || (line[0] == '`' && strings.Contains(line[n:], "`")) {
		return ""
	}
	return line[:n]
}

func escapeTildesInline(line string) string {
	if !strings.Contains(line, "~") {
		return line
	}
	var b strings.Builder
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			b.WriteString(line[i : i+2])
			i += 2
		case c == '`':
			n := runLen(line, i, '`')
			if end := closingRun(line, i+n, n); end >= 0 {
				b.WriteString(line[i:end])
				i = end
			} else {
				b.WriteString(line[i : i+n])
				i += n
			}
		case c == '<':
			if end := strings.IndexAny(line[i:], "> "); end > 0 && line[i+end] == '>' && strings.Contains(line[i:i+end], ":") {
				b.WriteString(line[i : i+end+1])
				i += end + 1
			} else {
				b.WriteByte(c)
				i++
			}
		case (c == 'h' || c == 'w') && (i == 0 || !isWordByte(line[i-1])) && isURLStart(line[i:]):
			end := strings.IndexAny(line[i:], " \t")
			if end < 0 {
				end = len(line) - i
			}
			b.WriteString(line[i : i+end])
			i += end
		case c == '~':
			n := runLen(line, i, '~')
			if n == 1 {
				b.WriteString("&#126;")
			} else {
				b.WriteString(line[i : i+n])
			}
			i += n
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func runLen(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

// closingRun returns the index just past a backtick run of exactly n starting
// at or after from, or -1.
func closingRun(s string, from, n int) int {
	for j := from; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		m := runLen(s, j, '`')
		if m == n {
			return j + m
		}
		j += m
	}
	return -1
}

func isURLStart(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "www.")
}
