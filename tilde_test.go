package main

import (
	"strings"
	"testing"
)

func TestEscapeLoneTildes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"icloud path", "/Library/Mobile Documents/iCloud~md~obsidian/Documents",
			"/Library/Mobile Documents/iCloud&#126;md&#126;obsidian/Documents"},
		{"double tilde stays strikethrough", "~~gone~~", "~~gone~~"},
		{"triple run untouched", "a ~~~ b", "a ~~~ b"},
		{"already escaped", `a\~b~c`, `a\~b&#126;c`},
		{"code span verbatim", "`iCloud~md~obsidian` and x~y~z", "`iCloud~md~obsidian` and x&#126;y&#126;z"},
		{"double backtick span", "``a ` ~b~``", "``a ` ~b~``"},
		{"unclosed backticks", "`a ~b~", "`a &#126;b&#126;"},
		{"tilde fence verbatim", "~~~\nx~y~z\n~~~\nx~y", "~~~\nx~y~z\n~~~\nx&#126;y"},
		{"backtick fence verbatim", "```sh\nls ~/a~b\n```", "```sh\nls ~/a~b\n```"},
		{"url verbatim", "see https://h/~u/x~y and ~a~", "see https://h/~u/x~y and &#126;a&#126;"},
		{"autolink verbatim", "<https://h/~u~>", "<https://h/~u~>"},
		{"indented code verbatim", "para\n\n    a~b~c\n", "para\n\n    a~b~c\n"},
		{"no tilde", "plain text", "plain text"},
	}
	for _, c := range cases {
		if got := escapeLoneTildes(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// The bug as reported: glamour's GFM strikethrough takes a single `~` pair, so an
// iCloud path rendered with `md` struck through and the tildes gone.
func TestRenderKeepsTildesInPaths(t *testing.T) {
	th, err := loadTheme("tokyo-night", "")
	if err != nil {
		t.Fatal(err)
	}
	render, err := newGlamour(th.StyleJSON, 0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := render("/Users/x/Library/Mobile Documents/iCloud~md~obsidian/Documents/a.md\n\n~~struck~~\n")
	if err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "iCloud~md~obsidian") {
		t.Errorf("tildes lost:\n%s", plain)
	}
	if strings.Contains(plain, "&#126;") {
		t.Errorf("escape leaked:\n%s", plain)
	}
	// The crossed-out SGR arrives fused with the colour: `…;245;9m`.
	if strings.Contains(out, ";9mmd") {
		t.Errorf("md struck through:\n%q", out)
	}
	if !strings.Contains(out, ";9mstruck") {
		t.Errorf("~~double~~ should still strike through:\n%q", out)
	}
}
