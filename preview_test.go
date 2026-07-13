package main

import (
	"strings"
	"testing"
)

func TestFormatTokens(t *testing.T) {
	cases := map[int64]string{
		0:          "",
		500:        "500",
		300_000:    "300k",
		800_000:    "800k",
		1_200_000:  "1.2m",
		2_000_000:  "2m",
		28_620_503: "28.6m",
	}
	for n, want := range cases {
		if got := formatTokens(n); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTruncVisible(t *testing.T) {
	// Truncates on visible columns, keeping color codes intact.
	got := truncVisible("\x1b[31mhello\x1b[0m world", 5)
	if v := stripANSI(got); v != "hello" {
		t.Errorf("visible = %q, want %q", v, "hello")
	}
	// Short strings pass through unchanged.
	if got := truncVisible("hi", 40); got != "hi" {
		t.Errorf("short = %q", got)
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("aaaa bbbb cccc dddd", 9)
	for _, line := range got {
		if len(line) > 9 {
			t.Errorf("line over width: %q", line)
		}
	}
	if strings.Join(got, " ") != "aaaa bbbb cccc dddd" {
		t.Errorf("words not preserved: %v", got)
	}
}
