package main

import "strings"

// termtable.go turns a markdown table too wide for the terminal into one record
// per row, the way Claude Code's own TUI does:
//
//	#: 1
//	ID: ENT-3629
//	What's actually needed: Russell switched both issuers…
//
//	────────────────────────────
//
//	#: 2
//	…
//
// A box-drawn table wider than the pane is unreadable either way: the terminal
// soft-wraps every row, so each record smears across several lines and the
// column rules stop lining up. Records keep the column names next to the values
// and let a long cell wrap as ordinary prose.
//
// This is the TERMINAL path. mdtable.go's tableBlocks is the Slack one and packs
// fields differently, because a Slack message pane is not a terminal.
//
// It runs on the markdown source before glamour parses it, so the cells keep
// their inline markdown (code spans, bold, links) and glamour styles them as
// usual. A table that fits, or any table when there's no terminal width (a pipe,
// the goldens), is left alone.

// flattenWideTables rewrites every table in md whose laid-out width exceeds
// cols. cols <= 0 disables it. Fenced code is passed through untouched.
func flattenWideTables(md string, cols int) string {
	if cols <= 0 || !strings.Contains(md, "|") {
		return md
	}
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	changed := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if mdFenceRe.MatchString(line) {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if !inFence {
			if n := mdTableRun(lines[i:]); n > 0 {
				if recs := tableRecords(lines[i:i+n], cols); recs != nil {
					out = append(out, recs...)
					changed = true
					i += n - 1
					continue
				}
			}
		}
		out = append(out, line)
	}
	if !changed {
		return md
	}
	return strings.Join(out, "\n")
}

// tableRecords renders a table as records, or returns nil when it fits in cols
// (or can't be parsed, in which case glamour gets it as it came).
func tableRecords(lines []string, cols int) []string {
	head, rows, ok := parseMDTable(lines)
	if !ok || tableRenderedWidth(tableColumnWidths(head, rows)) <= cols {
		return nil
	}
	out := []string{""}
	for i, row := range rows {
		if i > 0 {
			out = append(out, "", "---", "")
		}
		for c, cell := range row {
			field := head[c] + ": " + cell
			if c < len(row)-1 {
				field += `\` // hard break: one field per line
			}
			out = append(out, field)
		}
	}
	return append(out, "")
}
