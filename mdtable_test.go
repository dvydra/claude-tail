package main

import (
	"strings"
	"testing"
)

// wideTable is the shape the feature exists for: eleven columns, unreadable in a
// Slack code box at any window width.
const wideTable = `| Service | Env | Region | Cluster | Replicas | CPU req | Mem req | Image tag | Owner | Last deploy | Status |
|---------|-----|--------|---------|----------|---------|---------|-----------|-------|-------------|--------|
| api-gateway | prod | us-east-1 | eks-prod-use1 | 6 | 500m | 1Gi | v2.14.3 | platform | 2026-09-08 14:02 | healthy |
| auth | prod | eu-west-1 | eks-prod-euw1 | 4 | 250m | 512Mi | v1.9.0 | identity | 2026-09-07 09:41 | healthy |
| mirror-worker | staging | us-east-1 | eks-stg-use1 | 2 | 1000m | 2Gi | v0.31.1-rc2 | core | 2026-09-09 08:15 | degraded |
| webhook-relay | prod | us-east-1 | eks-prod-use1 | 3 | 200m | 256Mi | v3.2.0 | core | 2026-09-02 17:30 | healthy |`

func TestTableBlocksWideTable(t *testing.T) {
	got := strings.Join(tableBlocks(strings.Split(wideTable, "\n")), "\n")

	// One block per row, keyed on the first column, with the facet columns —
	// short, repeating, word-like — on the heading beside it.
	for _, want := range []string{
		"*api-gateway* · prod · us-east-1 · healthy",
		"*mirror-worker* · staging · us-east-1 · degraded",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing heading %q:\n%s", want, got)
		}
	}
	// Every other column keeps its name, so a line still reads on its own once
	// the header row is long gone.
	for _, want := range []string{
		"Cluster: eks-prod-use1", "Replicas: 6", "CPU req: 500m",
		"Image tag: v0.31.1-rc2", "Owner: identity", "Last deploy: 2026-09-02 17:30",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing field %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n*"); n != 3 { // 4 headings, 3 of them after a newline
		t.Errorf("expected 4 blocks, found %d headings:\n%s", n+1, got)
	}
}

// Nothing may be lost in the transposition: every cell in the table has to come
// out the other side somewhere.
func TestTableBlocksKeepsEveryCell(t *testing.T) {
	lines := strings.Split(wideTable, "\n")
	got := strings.Join(tableBlocks(lines), "\n")
	for _, l := range lines[2:] {
		for _, cell := range splitTableRow(l) {
			if cell != "" && !strings.Contains(got, cell) {
				t.Errorf("cell %q vanished:\n%s", cell, got)
			}
		}
	}
}

// Under the threshold a fenced table is the better answer — compact, aligned,
// and still a table — so tableBlocks declines and mrkdwn.go fences it.
func TestTableBlocksDeclinesNarrowTables(t *testing.T) {
	narrow := "| stage | before | after |\n|---|---|---|\n| plan | 40s | 12s |\n| apply | 3m | 1m |"
	if got := tableBlocks(strings.Split(narrow, "\n")); got != nil {
		t.Errorf("narrow table was collapsed:\n%s", strings.Join(got, "\n"))
	}
	if out := toSlackMrkdwn(narrow); !strings.HasPrefix(out, "```") {
		t.Errorf("narrow table lost its fence:\n%s", out)
	}
}

// …and two columns are enough to blow the budget on their own, if one of them
// is long. Padding can't rescue a table that doesn't fit.
func TestTableBlocksSwitchesOnAWideTwoColumnTable(t *testing.T) {
	long := "| flag | meaning |\n|---|---|\n" +
		"| --mark-continuation | write a forward-pointer record into the file the session stopped in |\n" +
		"| --no-wrap | leave it |"
	got := tableBlocks(strings.Split(long, "\n"))
	if got == nil {
		t.Fatal("a 93-column table stayed a table")
	}
	if joined := strings.Join(got, "\n"); !strings.Contains(joined, "*--mark-continuation*") {
		t.Errorf("first column should be the heading:\n%s", joined)
	}
}

// A table that fits, however long a single cell is, stays a table: the padded
// form is compact and still reads as rows.
func TestTableBlocksKeepsATableThatFits(t *testing.T) {
	in := "| Agent | Discovery | Notes |\n|---|---|---|\n" +
		"| Claude Code | ~/.claude/projects/*.jsonl | default |\n" +
		"| Codex | ~/.codex/sessions/ | cwd from session_meta |"
	if got := tableBlocks(strings.Split(in, "\n")); got != nil {
		t.Errorf("a 70-column table was collapsed:\n%s", strings.Join(got, "\n"))
	}
}

// A column that says the same thing on every row is context, not data: it's
// stated once above the blocks and dropped from all of them.
func TestTableBlocksHoistsConstantColumns(t *testing.T) {
	in := `| Service | Env | Region | Cluster | Replicas | CPU req | Mem req | Status |
|---|---|---|---|---|---|---|---|
| api-gateway | prod | us-east-1 | eks-prod-use1 | 6 | 500m | 1Gi | healthy |
| auth | prod | eu-west-1 | eks-prod-euw1 | 4 | 250m | 512Mi | healthy |
| relay | prod | us-east-1 | eks-prod-use1 | 3 | 200m | 256Mi | healthy |`
	got := strings.Join(tableBlocks(strings.Split(in, "\n")), "\n")
	if !strings.HasPrefix(got, "_Same for every row: Env prod · Status healthy_") {
		t.Errorf("constants not hoisted:\n%s", got)
	}
	if strings.Contains(got, "Env: prod") || strings.Contains(got, "Status: healthy") {
		t.Errorf("a hoisted column was repeated in the blocks:\n%s", got)
	}
}

// The heading only takes columns whose values still read with the column name
// removed. A bare "6" or "500m" in a heading is a riddle; so is a value that's
// missing on some rows.
func TestHeadingExtrasRejectsWhatWouldntRead(t *testing.T) {
	in := `| Service | Env | Replicas | Size | Cluster | Owner | Note | Status |
|---|---|---|---|---|---|---|---|
| a | prod | 6 | 500m | eks-prod-use1x | platform |  | healthy |
| b | prod | 4 | 250m | eks-prod-euw1x | identity | x | healthy |
| c | stg | 2 | 100m | eks-stg-use1xx | core |  | degraded |
| d | prod | 3 | 200m | eks-prod-use1x | core | y | healthy |`
	head, rows, ok := parseMDTable(strings.Split(in, "\n"))
	if !ok {
		t.Fatal("parse failed")
	}
	got := headingExtras(head, rows, map[int]string{})
	names := []string{}
	for _, c := range got {
		names = append(names, head[c])
	}
	// Env (repeats, word) and Status (repeats, word) qualify. Replicas and Size
	// don't start with a letter, Cluster is 14 chars, Owner has 3 distinct in 4
	// rows, Note is blank on two rows.
	if strings.Join(names, ",") != "Env,Status" {
		t.Errorf("heading extras = %v, want [Env Status]", names)
	}
}

// A table too small to tell enums from data gets a bare heading rather than a
// guessed one: with two rows, every column trivially has "few" distinct values.
func TestHeadingExtrasStaysQuietOnTinyTables(t *testing.T) {
	in := `| Service | Env | Region | Cluster | Replicas | CPU req | Mem req | Status |
|---|---|---|---|---|---|---|---|
| a | prod | us-east-1 | eks-prod-use1 | 6 | 500m | 1Gi | healthy |
| b | stg | eu-west-1 | eks-prod-euw1 | 4 | 250m | 512Mi | degraded |`
	got := strings.Join(tableBlocks(strings.Split(in, "\n")), "\n")
	if !strings.Contains(got, "*a*\n") {
		t.Errorf("expected a bare heading:\n%s", got)
	}
	if strings.Contains(got, "*a* ·") {
		t.Errorf("two rows can't identify a facet, but one was used:\n%s", got)
	}
}

// An empty cell is left out entirely rather than emitted as a name with nothing
// after it.
func TestTableBlocksSkipsEmptyCells(t *testing.T) {
	in := `| Service | Env | Region | Cluster | Replicas | CPU req | Owner | Status |
|---|---|---|---|---|---|---|---|
| a | prod | us-east-1 | eks-prod-use1 |  | 500m | platform | healthy |
| b | prod | eu-west-1 | eks-prod-euw1 | 4 |  | identity | healthy |
| c | stg | us-east-1 | eks-stg-use1 | 2 | 100m | core | degraded |`
	got := strings.Join(tableBlocks(strings.Split(in, "\n")), "\n")
	if strings.Contains(got, "Replicas: \n") || strings.Contains(got, "Replicas: ·") {
		t.Errorf("an empty cell was emitted:\n%s", got)
	}
	if !strings.Contains(got, "Replicas: 4") {
		t.Errorf("a filled cell in the same column was dropped:\n%s", got)
	}
}

// Cells are markdown, and go through the same inline conversion as the rest of
// the body — a bolded cell must not arrive in Slack still wearing `**`.
func TestTableBlocksConvertsCellMarkdown(t *testing.T) {
	in := `| Service | Env | Region | Cluster | Replicas | CPU req | Mem req | Status |
|---|---|---|---|---|---|---|---|
| **a** | prod | us-east-1 | ` + "`eks-prod-use1`" + ` | 6 | 500m | 1Gi | healthy |
| b | prod | eu-west-1 | ` + "`eks-prod-euw1`" + ` | 4 | 250m | 512Mi | healthy |
| c | stg | us-east-1 | ` + "`eks-stg-use1`" + ` | 2 | 100m | 2Gi | degraded |`
	got := strings.Join(tableBlocks(strings.Split(in, "\n")), "\n")
	if strings.Contains(got, "**a**") {
		t.Errorf("cell markdown not converted:\n%s", got)
	}
	if !strings.Contains(got, "`eks-prod-use1`") {
		t.Errorf("code span in a cell was rewritten:\n%s", got)
	}
}

func TestPackFields(t *testing.T) {
	got := packFields([]string{"aaaa: 1", "bbbb: 2", "cccc: 3", "dddd: 4"}, 20)
	// "aaaa: 1 · bbbb: 2" is 17; adding "cccc: 3" would make 27.
	want := []string{"aaaa: 1 · bbbb: 2", "cccc: 3 · dddd: 4"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("packFields = %q, want %q", got, want)
	}
	// A field wider than the budget gets a line rather than being cut in half.
	long := strings.Repeat("x", 40)
	if got := packFields([]string{"a: 1", long}, 20); len(got) != 2 || got[1] != long {
		t.Errorf("an over-budget field was not given its own line: %q", got)
	}
}

func TestSplitTableRow(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"outer pipes", "| a | b | c |", []string{"a", "b", "c"}},
		{"no outer pipes", "a | b | c", []string{"a", "b", "c"}},
		{"escaped pipe stays in the cell", `| a \| b | c |`, []string{"a | b", "c"}},
		{"empty cells", "|  | b |", []string{"", "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := splitTableRow(c.in); strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
				t.Errorf("splitTableRow(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The delimiter row is what tells a table from prose with pipes in it, so its
// alignment forms all have to be recognised — and a run of rows that isn't one
// must not parse as a table.
func TestParseMDTableDelimiter(t *testing.T) {
	ok := "| a | b | c |\n|:--|:-:|--:|\n| 1 | 2 | 3 |"
	if _, _, got := parseMDTable(strings.Split(ok, "\n")); !got {
		t.Error("aligned delimiter row not recognised")
	}
	notATable := "| a | b |\n| 1 | 2 |\n| 3 | 4 |"
	if _, _, got := parseMDTable(strings.Split(notATable, "\n")); got {
		t.Error("pipe rows with no delimiter parsed as a table")
	}
	headerOnly := "| a | b |\n|---|---|"
	if _, _, got := parseMDTable(strings.Split(headerOnly, "\n")); got {
		t.Error("a table with no body rows parsed")
	}
}

// The whole point is the `y`/`m` output, so check it end to end — including that
// the blocks land where the table was, with the prose around them intact.
func TestToSlackMrkdwnCollapsesWideTables(t *testing.T) {
	out := toSlackMrkdwn("before\n\n" + wideTable + "\n\nafter")
	if !strings.HasPrefix(out, "before\n\n*api-gateway* · prod") {
		t.Errorf("blocks did not replace the table in place:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n\nafter") {
		t.Errorf("text after the table was lost:\n%s", out)
	}
	if strings.Contains(out, "```") {
		t.Errorf("a collapsed table should not also be fenced:\n%s", out)
	}
	if strings.Contains(out, "|--") {
		t.Errorf("the delimiter row leaked into the output:\n%s", out)
	}
}

// The fence is only worth adding if the columns actually line up inside it, and
// a source table almost never arrives padded — an agent writes `|---|---|` with
// cells of whatever width the content happened to be.
func TestFormatMDTablePadsColumns(t *testing.T) {
	in := "| Agent | Discovery | Notes |\n|---|---|---|\n| Claude Code | projects/*.jsonl | default |\n| agy | brain logs | id lookup |"
	got := strings.Join(formatMDTable(strings.Split(in, "\n")), "\n")
	want := "| Agent       | Discovery        | Notes     |\n" +
		"|-------------|------------------|-----------|\n" +
		"| Claude Code | projects/*.jsonl | default   |\n" +
		"| agy         | brain logs       | id lookup |"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Every row is the same width, which is the only thing the reader sees.
	var widths []int
	for _, l := range strings.Split(got, "\n") {
		widths = append(widths, cellWidth(l))
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Errorf("row %d is %d columns, want %d:\n%s", i, w, widths[0], got)
		}
	}
}

// Already-padded input comes back unchanged — the pass is idempotent, so a table
// that was fine doesn't churn.
func TestFormatMDTableIsIdempotent(t *testing.T) {
	in := "| stage | before |\n|-------|--------|\n| plan  | 40s    |"
	once := formatMDTable(strings.Split(in, "\n"))
	if strings.Join(once, "\n") != in {
		t.Errorf("an aligned table was rewritten:\n%s", strings.Join(once, "\n"))
	}
	if twice := formatMDTable(once); strings.Join(twice, "\n") != strings.Join(once, "\n") {
		t.Errorf("not idempotent:\n%s", strings.Join(twice, "\n"))
	}
}

// Alignment markers are the author's instruction about the column, so they
// survive — and they decide which side the padding goes on.
func TestFormatMDTableKeepsAlignment(t *testing.T) {
	in := "| stage | ms | note |\n|:--|--:|:-:|\n| plan | 40 | ok |\n| apply | 1200 | slow |"
	got := strings.Join(formatMDTable(strings.Split(in, "\n")), "\n")
	want := "| stage |   ms | note |\n" +
		"|:------|-----:|:----:|\n" +
		"| plan  |   40 |  ok  |\n" +
		"| apply | 1200 | slow |"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Padding is measured in display columns, not runes: a CJK glyph or an emoji
// takes two cells in a monospaced box, and counting it as one puts every column
// after it out by one.
func TestFormatMDTableMeasuresDisplayWidth(t *testing.T) {
	in := "| name | status |\n|---|---|\n| 日本語テスト | ok |\n| ascii | ✅ done |"
	got := formatMDTable(strings.Split(in, "\n"))
	first := cellWidth(got[0])
	for i, l := range got {
		if w := cellWidth(l); w != first {
			t.Errorf("row %d is %d columns, want %d:\n%s", i, w, first, strings.Join(got, "\n"))
		}
	}
}

// Content is never touched — only the whitespace between cells. A command in a
// cell still has to run when it's pasted out of the code box.
func TestFormatMDTableDoesNotRewriteCells(t *testing.T) {
	in := "| what | cmd |\n|---|---|\n| build | `go build -o entire-tail .` |\n| test | **run** it |"
	got := strings.Join(formatMDTable(strings.Split(in, "\n")), "\n")
	for _, want := range []string{"`go build -o entire-tail .`", "**run** it"} {
		if !strings.Contains(got, want) {
			t.Errorf("cell content was rewritten, %q missing:\n%s", want, got)
		}
	}
}

// A row with MORE cells than the header would lose the extras. GFM says to
// ignore them; a converter on the way to someone's clipboard does not get to.
func TestTableWithExtraCellsIsPassedThroughUntouched(t *testing.T) {
	in := "| a | b |\n|---|---|\n| 1 | 2 | 3 |"
	lines := strings.Split(in, "\n")
	if got := formatMDTable(lines); got != nil {
		t.Errorf("a row with extra cells was reformatted:\n%s", strings.Join(got, "\n"))
	}
	if got := tableBlocks(lines); got != nil {
		t.Errorf("a row with extra cells was collapsed:\n%s", strings.Join(got, "\n"))
	}
	if got := toSlackMrkdwn(in); !strings.Contains(got, "| 1 | 2 | 3 |") {
		t.Errorf("the extra cell did not survive:\n%s", got)
	}
}

// End to end: a narrow table comes out fenced AND padded.
func TestToSlackMrkdwnPadsFencedTables(t *testing.T) {
	got := toSlackMrkdwn("| Agent | Discovery |\n|---|---|\n| Claude Code | projects/*.jsonl |\n| agy | brain logs |")
	want := "```\n" +
		"| Agent       | Discovery        |\n" +
		"|-------------|------------------|\n" +
		"| Claude Code | projects/*.jsonl |\n" +
		"| agy         | brain logs       |\n" +
		"```"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A table inside a code fence is someone's output, not a table to reformat.
func TestToSlackMrkdwnLeavesFencedTablesAlone(t *testing.T) {
	in := "```\n" + wideTable + "\n```"
	if got := toSlackMrkdwn(in); got != in {
		t.Errorf("a table inside a fence was collapsed:\n%s", got)
	}
}
