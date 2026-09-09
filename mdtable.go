package main

import (
	"regexp"
	"strings"
	"unicode"
)

// mdtable.go collapses a WIDE markdown table into sectioned blocks for the Slack
// mrkdwn conversion (`y` and `m`).
//
// Slack renders no tables at all, so mrkdwn.go's fallback is to wrap the table
// in a code fence — inside a code box the font is monospaced and the columns at
// least line up. That works right up until the table is wider than the message
// pane, at which point a reader gets a horizontally-scrolling box of numbers
// with the header row long out of sight.
//
// So past a threshold the table is turned inside out into one block per row,
// which is what a person does when they read a wide table aloud:
//
//	*api-gateway* · prod · us-east-1 · healthy
//	• Cluster: eks-prod-use1 · Replicas: 6
//	• CPU req: 500m · Mem req: 1Gi · Image tag: v2.14.3
//
// The rules, and how faithfully each one is mechanised:
//
//  1. One block per row; the first column is the heading. Exact.
//  2. The columns you'd FILTER on join the heading line, bare — they're enum-ish
//     values (prod, us-east-1, healthy) that read fine without their names.
//     Approximated by "short and low-cardinality" (headingExtras), which is what
//     a filterable column looks like from the outside.
//  3. Related columns share a line. Genuinely semantic — a converter can't know
//     that CPU belongs with memory — so instead the remaining fields are packed
//     onto lines up to a width budget. Different grouping, same effect: three
//     lines instead of seven.
//  4. Column names are restated inline, so a line still reads once the header is
//     off screen. Exact — that's why the body is `Name: value`.
//  5. A column with the same value in every row is stated once, above the
//     blocks, and dropped from them. Exact.
//
// The threshold is the same one: about seven columns, or sooner if any cell runs
// past twenty characters. Under it a fenced table is genuinely nicer — compact,
// aligned, and still a table — so narrow tables are left alone.

const (
	// tableWideCols / tableWideCell are the switch: at this many columns, or with
	// a cell this long, an aligned table stops fitting a Slack message pane.
	tableWideCols = 7
	tableWideCell = 20
	// tableHeadExtras caps how many columns join the heading line; past three it
	// stops being a heading and becomes another row of data.
	tableHeadExtras = 3
	// tableHeadCellW is the widest a value may be to sit on the heading line
	// unlabelled. Longer than this and it needs its column name to make sense.
	tableHeadCellW = 12
	// tableFieldBudget is how wide a packed body line may get before it wraps to
	// the next bullet.
	tableFieldBudget = 60
)

// mdDelimCellRe matches one cell of a table's delimiter row (`---`, `:--`, `-:`).
var mdDelimCellRe = regexp.MustCompile(`^:?-+:?$`)

// tableBlocks turns a markdown table into sectioned blocks, or returns nil to
// leave it as a table — which the caller then fences.
func tableBlocks(lines []string) []string {
	head, rows, ok := parseMDTable(lines)
	if !ok || !tableIsWide(head, rows) {
		return nil
	}

	// Rule 5: a column that says the same thing on every row is context, not
	// data. It goes above the blocks, once.
	constant := map[int]string{}
	if len(rows) > 1 {
		for c := 1; c < len(head); c++ {
			v := rows[0][c]
			if v == "" {
				continue
			}
			same := true
			for _, row := range rows[1:] {
				if row[c] != v {
					same = false
					break
				}
			}
			if same {
				constant[c] = v
			}
		}
	}
	extras := headingExtras(head, rows, constant)

	var out []string
	if len(constant) > 0 {
		var parts []string
		for c := 1; c < len(head); c++ {
			if v, ok := constant[c]; ok {
				parts = append(parts, mrkdwnInline(head[c])+" "+mrkdwnInline(v))
			}
		}
		out = append(out, "_Same for every row: "+strings.Join(parts, " · ")+"_")
		out = append(out, "")
	}

	for i, row := range rows {
		if i > 0 {
			out = append(out, "")
		}
		heading := boldCell(row[0])
		for _, c := range extras {
			if row[c] != "" {
				heading += " · " + mrkdwnInline(row[c])
			}
		}
		out = append(out, heading)

		var fields []string
		for c := 1; c < len(head); c++ {
			if _, skip := constant[c]; skip || contains(extras, c) || row[c] == "" {
				continue
			}
			fields = append(fields, mrkdwnInline(head[c])+": "+mrkdwnInline(row[c]))
		}
		for _, line := range packFields(fields, tableFieldBudget) {
			out = append(out, "• "+line)
		}
	}
	return out
}

// tableIsWide reports whether a table has outgrown an aligned code box: too many
// columns to fit a message pane, or a cell long enough to stretch one column
// past everything beside it.
func tableIsWide(head []string, rows [][]string) bool {
	if len(head) >= tableWideCols {
		return true
	}
	for _, cell := range head {
		if len([]rune(cell)) > tableWideCell {
			return true
		}
	}
	for _, row := range rows {
		for _, cell := range row {
			if len([]rune(cell)) > tableWideCell {
				return true
			}
		}
	}
	return false
}

// headingExtras picks the columns that join the heading line unlabelled: the
// short, repeating, word-like ones. That's what a column you'd filter or group
// by looks like from the outside — an environment, a region, a status — and
// it's exactly the set whose values still read on their own once the column
// name is gone.
//
// Three tests, and each one is there because dropping it puts something
// meaningless in the heading:
//
//   - It must REPEAT (fewer distinct values than rows, and not many of them).
//     A column with a value per row is data, not a facet. Strictly fewer, not
//     "at most half", because with two or three rows every column trivially has
//     few distinct values — a small table can't tell you which of its columns
//     are enums, so it gets a bare heading rather than a guessed one.
//   - It must be short, or it needs its name to make sense.
//   - It must start with a LETTER. `prod` and `us-east-1` read alone; `6` and
//     `500m` do not — a bare number in a heading is a riddle.
func headingExtras(head []string, rows [][]string, constant map[int]string) []int {
	maxDistinct := max(2, (len(rows)+1)/2)
	var picked []int
	for c := 1; c < len(head) && len(picked) < tableHeadExtras; c++ {
		if _, skip := constant[c]; skip {
			continue
		}
		seen := map[string]bool{}
		widest := 0
		ok := true
		for _, row := range rows {
			seen[row[c]] = true
			widest = max(widest, len([]rune(row[c])))
			// An empty cell would leave a gap in the heading with nothing to
			// explain it, so a column that isn't filled in everywhere stays down
			// in the body with its name attached.
			ok = ok && row[c] != "" && startsWithLetter(row[c])
		}
		if ok && len(seen) < len(rows) && len(seen) <= maxDistinct && widest <= tableHeadCellW {
			picked = append(picked, c)
		}
	}
	return picked
}

// boldCell converts a cell and makes it the block's heading. A key that already
// carries emphasis is left as it is: `**a**` converts to `*a*`, and wrapping
// THAT in another pair gives `**a**` back — which mrkdwn renders as two literal
// asterisks around a word, not as bold.
func boldCell(cell string) string {
	s := mrkdwnInline(cell)
	if strings.Contains(s, "*") {
		return s
	}
	return "*" + s + "*"
}

// startsWithLetter reports whether s opens with a letter — the cheap test for
// "this value is a word, not a measurement".
func startsWithLetter(s string) bool {
	for _, r := range s {
		return unicode.IsLetter(r)
	}
	return false
}

// packFields greedily fills lines up to budget columns, joining with " · ". A
// field wider than the budget gets a line to itself rather than being cut.
func packFields(fields []string, budget int) []string {
	var (
		out  []string
		cur  string
		curW int
	)
	for _, f := range fields {
		w := len([]rune(f))
		if cur != "" && curW+3+w > budget {
			out = append(out, cur)
			cur, curW = "", 0
		}
		if cur == "" {
			cur, curW = f, w
			continue
		}
		cur += " · " + f
		curW += 3 + w
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// parseMDTable splits a table's lines into its header and body cells. Rows are
// padded (or truncated) to the header's width so every later step can index by
// column without checking.
func parseMDTable(lines []string) (head []string, rows [][]string, ok bool) {
	if len(lines) < 3 { // header + delimiter + at least one row
		return nil, nil, false
	}
	head = splitTableRow(lines[0])
	if len(head) < 2 || !isTableDelim(lines[1], len(head)) {
		return nil, nil, false
	}
	for _, l := range lines[2:] {
		cells := splitTableRow(l)
		row := make([]string, len(head))
		copy(row, cells)
		rows = append(rows, row)
	}
	return head, rows, len(rows) > 0
}

// isTableDelim reports whether a line is a table's `|---|:--:|` separator.
func isTableDelim(line string, cols int) bool {
	cells := splitTableRow(line)
	if len(cells) != cols {
		return false
	}
	for _, c := range cells {
		if !mdDelimCellRe.MatchString(c) {
			return false
		}
	}
	return true
}

// splitTableRow splits one `| a | b |` row into trimmed cells, honouring the
// `\|` escape (a pipe inside a cell is not a column break — and unescaping it
// here is what keeps it a plain pipe once the table is no longer a table).
func splitTableRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	var (
		cells []string
		cur   strings.Builder
	)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) && rs[i+1] == '|' {
			cur.WriteRune('|')
			i++
			continue
		}
		if rs[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteRune(rs[i])
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

// contains reports whether xs holds n.
func contains(xs []int, n int) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}
