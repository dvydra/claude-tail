package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFormatTS(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		in   string
		want string
	}{
		{"2026-06-15T05:26:14.604Z", "2026-06-15 05:26:14"}, // fractional stripped
		{"2026-06-15T05:26:14Z", "2026-06-15 05:26:14"},
		{"", ""},
		{"not-a-date", ""},
	}
	for _, c := range cases {
		if got := formatTS(c.in, utc); got != c.want {
			t.Errorf("formatTS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatTSLocal(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// 2026-06-15T05:26:14Z is 01:26:14 EDT.
	if got := formatTS("2026-06-15T05:26:14Z", loc); got != "2026-06-15 01:26:14" {
		t.Errorf("got %q", got)
	}
}

func TestCollapseBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		t    int
		want string
	}{
		{"disabled", "a\nb\nc", 0, "a\nb\nc"},
		{"under-threshold", "a\nb", 5, "a\nb"},
		{"plural", "a\nb\nc\nd", 2, "a\nb\n\n*… 2 more lines — re-run with --no-collapse to expand*"},
		{"singular", "a\nb\nc", 2, "a\nb\n\n*… 1 more line — re-run with --no-collapse to expand*"},
	}
	for _, c := range cases {
		if got := collapseBody(c.body, c.t); got != c.want {
			t.Errorf("%s: collapseBody(%q,%d) = %q, want %q", c.name, c.body, c.t, got, c.want)
		}
	}
}

func TestCollapseBodyClosesFence(t *testing.T) {
	// Head ends inside an unclosed ``` fence → a closing fence is appended.
	body := "```go\nx := 1\ny := 2\nz := 3"
	got := collapseBody(body, 2)
	want := "```go\nx := 1\n```\n\n*… 2 more lines — re-run with --no-collapse to expand*"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestJqToStringRaw(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"hello"`, "hello"},
		{`42`, "42"},
		{`true`, "true"},
		{`null`, "null"},
		{`{"b":1,"a":2}`, `{"b":1,"a":2}`}, // key order preserved (json.Compact)
		{`[1, 2, 3]`, `[1,2,3]`},
		{`{ "x" : "y" }`, `{"x":"y"}`}, // whitespace compacted
	}
	for _, c := range cases {
		if got := jqToStringRaw(json.RawMessage(c.in)); got != c.want {
			t.Errorf("jqToStringRaw(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnq(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`plain text`, "plain text"}, // not JSON → unchanged
		{`"quoted"`, "quoted"},       // one level of JSON-string unwrapped
		{`"\"double\""`, `"double"`}, // double-encoded → unwrap one level
		{``, ""},                     // empty → empty (bash leaked a jq error here)
	}
	for _, c := range cases {
		if got := unqString(c.in); got != c.want {
			t.Errorf("unqString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnqRaw(t *testing.T) {
	// A raw JSON string value "ls -la" → "ls -la".
	if got := unqRaw(json.RawMessage(`"ls -la"`)); got != "ls -la" {
		t.Errorf("got %q", got)
	}
}

func TestFirstRaw(t *testing.T) {
	m := map[string]json.RawMessage{
		"a": json.RawMessage(`null`),
		"b": json.RawMessage(`"hi"`),
		"c": json.RawMessage(`"yo"`),
	}
	if raw, ok := firstRaw(m, "a", "b", "c"); !ok || string(raw) != `"hi"` {
		t.Errorf("expected to skip null 'a' and pick 'b', got %q ok=%v", raw, ok)
	}
	if _, ok := firstRaw(m, "x", "y"); ok {
		t.Errorf("expected miss for absent keys")
	}
}
