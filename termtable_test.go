package main

import (
	"strings"
	"testing"
)

const wideTableMD = "## Needs you (2), quickest first\n\n" +
	"| # | ID | Customer | What's actually needed | Time |\n" +
	"|---|----|----------|------------------------|------|\n" +
	"| 1 | ENT-3629 | netlify | Russell switched both issuers to the urn form and added `deployments:write`. Tell Justin he's good to go. | 2 min |\n" +
	"| 2 | ENT-3630 | zenori | Jan already merged his stack with gh-stack, so nothing is blocked. | 5 min |\n"

func TestFlattenWideTables(t *testing.T) {
	got := flattenWideTables(wideTableMD, 80)
	want := "## Needs you (2), quickest first\n\n\n" +
		"#: 1\\\nID: ENT-3629\\\nCustomer: netlify\\\n" +
		"What's actually needed: Russell switched both issuers to the urn form and added `deployments:write`. Tell Justin he's good to go.\\\n" +
		"Time: 2 min\n\n---\n\n" +
		"#: 2\\\nID: ENT-3630\\\nCustomer: zenori\\\n" +
		"What's actually needed: Jan already merged his stack with gh-stack, so nothing is blocked.\\\n" +
		"Time: 5 min\n\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFlattenWideTablesLeavesFittingAndFenced(t *testing.T) {
	narrow := "| a | b |\n|---|---|\n| 1 | 2 |\n"
	for name, tc := range map[string]struct {
		md   string
		cols int
	}{
		"fits":       {narrow, 80},
		"no width":   {wideTableMD, 0},
		"in a fence": {"```\n" + wideTableMD + "```\n", 40},
	} {
		if got := flattenWideTables(tc.md, tc.cols); got != tc.md {
			t.Errorf("%s: rewritten:\n%s", name, got)
		}
	}
}

func TestWideTableRendersAsRecords(t *testing.T) {
	th, err := loadTheme("catppuccin-mocha", "")
	if err != nil {
		t.Fatal(err)
	}
	render, err := newGlamourCols(th.StyleJSON, 0, 80)
	if err != nil {
		t.Fatal(err)
	}
	out, err := render(wideTableMD)
	if err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(out)
	if strings.Contains(plain, "│") {
		t.Errorf("wide table still box-drawn:\n%s", plain)
	}
	for _, want := range []string{"#: 1\n", "ID: ENT-3629\n", "Time: 5 min", "deployments:write", "────"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
}
