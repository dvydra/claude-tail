package main

import (
	"strings"
	"testing"
	"time"
)

func TestRerenderPrefix(t *testing.T) {
	// A pipe must get nothing: the goldens are generated that way, so anything
	// emitted here would rewrite every one of them.
	if got := rerenderPrefix(false); got != "" {
		t.Fatalf("rerenderPrefix(pipe) = %q, want empty", got)
	}
	got := rerenderPrefix(true)
	for _, want := range []string{"\x1b[H", "\x1b[2J", "\x1b[3J"} {
		if !strings.Contains(got, want) {
			t.Errorf("rerenderPrefix(tty) = %q, missing %q", got, want)
		}
	}
	// Scrollback last: erasing it before the screen leaves the screen's copy to
	// scroll straight back into the buffer we just emptied.
	if strings.Index(got, "\x1b[2J") > strings.Index(got, "\x1b[3J") {
		t.Errorf("rerenderPrefix(tty) = %q, want the screen erased before the scrollback", got)
	}
}

// TestUserPasteRendersWithoutRawTags is the end-to-end half of markPastes: the
// bug was glamour escaping the wrapper into `<\pasted_content id="e840">`, which
// only shows up once the body has been through the real renderer.
func TestUserPasteRendersWithoutRawTags(t *testing.T) {
	line := `{"type":"user","timestamp":"2026-09-22T06:32:49Z","message":{"role":"user","content":"the session in \n\n<pasted_content id=\"e840\">\n/Users/dvydra/src/entirehq/entire-ci-webhooks\n</pasted_content id=\"e840\">\n\njust now failed to follow a /clear."}}`
	recs := normalizeClaude([]byte(line), time.UTC)
	if len(recs) != 1 || recs[0].Kind != KindUser {
		t.Fatalf("normalizeClaude gave %d records, want one USER", len(recs))
	}

	th, err := loadTheme("tokyo-night", "")
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	r, err := newRenderer(&buf, th, "dots", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.emit(recs[0])
	r.endLine()
	out := buf.String()

	if strings.Contains(out, "pasted_content") {
		t.Errorf("the raw wrapper survived to the screen:\n%s", out)
	}
	if !strings.Contains(out, "pasted 1 line") {
		t.Errorf("no paste marker in:\n%s", out)
	}
	if !strings.Contains(out, "entire-ci-webhooks") {
		t.Errorf("the pasted text itself is missing from:\n%s", out)
	}
}
