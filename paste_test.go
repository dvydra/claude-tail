package main

import "testing"

func TestMarkPastes(t *testing.T) {
	// The shape Claude Code actually writes: the tags sit alone on their own
	// lines, and the CLOSING one repeats the attributes (`</pasted_content
	// id="e840">`), which is not valid HTML — glamour treats the pair as an HTML
	// tag and escapes it, so the reader was shown `<\pasted_content id="e840">`
	// and the id, which means nothing to them.
	in := "the session in \n\n<pasted_content id=\"e840\">\n/Users/dvydra/src/entirehq/entire-ci-webhooks\n</pasted_content id=\"e840\">\n\n just now failed to follow a /clear."
	want := "the session in \n\n*⎘ pasted 1 line*\n\n/Users/dvydra/src/entirehq/entire-ci-webhooks\n\n just now failed to follow a /clear."
	if got := markPastes(in); got != want {
		t.Fatalf("markPastes =\n%q\nwant\n%q", got, want)
	}
}

func TestMarkPastesCountsAndPlural(t *testing.T) {
	in := "<pasted_content id=\"x\">\nalpha\nbeta\ngamma\n</pasted_content>"
	want := "*⎘ pasted 3 lines*\n\nalpha\nbeta\ngamma"
	if got := markPastes(in); got != want {
		t.Fatalf("markPastes =\n%q\nwant\n%q", got, want)
	}
}

func TestMarkPastesLeavesProseAlone(t *testing.T) {
	// A tag the human typed mid-sentence is them talking ABOUT the wrapper, not
	// a wrapper. Only a tag alone on its line is one, which is the only shape
	// Claude Code emits.
	for _, s := range []string{
		"also the <pasted_content id thing needs to be parsed correctly",
		"see <pasted_content id=\"e840\"> above",
		"no tags here at all",
	} {
		if got := markPastes(s); got != s {
			t.Errorf("markPastes(%q) = %q, want it untouched", s, got)
		}
	}
}

func TestMarkPastesUnclosedAndMultiple(t *testing.T) {
	// An unterminated wrapper still loses its tag — leaving the raw one on
	// screen is the bug being fixed, and the count is what is known so far.
	if got, want := markPastes("<pasted_content id=\"a\">\nonly\n"), "*⎘ pasted 1 line*\n\nonly\n"; got != want {
		t.Errorf("unclosed: markPastes = %q, want %q", got, want)
	}
	in := "<pasted_content id=\"a\">\none\n</pasted_content id=\"a\">\nmid\n<pasted_content id=\"b\">\ntwo\nthree\n</pasted_content id=\"b\">"
	want := "*⎘ pasted 1 line*\n\none\n\nmid\n\n*⎘ pasted 2 lines*\n\ntwo\nthree"
	if got := markPastes(in); got != want {
		t.Fatalf("two pastes: markPastes =\n%q\nwant\n%q", got, want)
	}
}

func TestMarkPastesEmptyPaste(t *testing.T) {
	// Nothing between the tags: drop the wrapper entirely rather than announce a
	// paste of nothing, and leave the surrounding lines as they were.
	if got := markPastes("before\n<pasted_content id=\"a\">\n</pasted_content id=\"a\">\nafter"); got != "before\nafter" {
		t.Fatalf("empty paste: markPastes = %q", got)
	}
}
