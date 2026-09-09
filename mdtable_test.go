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

// …but a long cell blows the alignment on its own, however few columns there
// are: one wide column pushes every other one off the pane.
func TestTableBlocksSwitchesOnALongCell(t *testing.T) {
	long := "| flag | meaning |\n|---|---|\n| --mark-continuation | write a forward pointer into the stopped file |\n| --no-wrap | leave it |"
	got := tableBlocks(strings.Split(long, "\n"))
	if got == nil {
		t.Fatal("a table with a 46-character cell stayed a table")
	}
	if joined := strings.Join(got, "\n"); !strings.Contains(joined, "*--mark-continuation*") {
		t.Errorf("first column should be the heading:\n%s", joined)
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
	in := `| Service | Env | Region | Cluster | Replicas | CPU req | Status |
|---|---|---|---|---|---|---|
| **a** | prod | us-east-1 | ` + "`eks-prod-use1`" + ` | 6 | 500m | healthy |
| b | prod | eu-west-1 | ` + "`eks-prod-euw1`" + ` | 4 | 250m | healthy |
| c | stg | us-east-1 | ` + "`eks-stg-use1`" + ` | 2 | 100m | degraded |`
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

// A table inside a code fence is someone's output, not a table to reformat.
func TestToSlackMrkdwnLeavesFencedTablesAlone(t *testing.T) {
	in := "```\n" + wideTable + "\n```"
	if got := toSlackMrkdwn(in); got != in {
		t.Errorf("a table inside a fence was collapsed:\n%s", got)
	}
}
