package main

import (
	"testing"
	"time"
)

func TestFmtDur(t *testing.T) {
	cases := map[int]string{0: "", 5: "5s", 42: "42s", 60: "1m", 180: "3m", 527: "8m47s"}
	for secs, want := range cases {
		if got := fmtDur(time.Duration(secs) * time.Second); got != want {
			t.Errorf("fmtDur(%ds) = %q, want %q", secs, got, want)
		}
	}
}

func TestFocusBody(t *testing.T) {
	if got := focusBody(24); got != 21 {
		t.Errorf("focusBody(24) = %d, want 21", got)
	}
	if got := focusBody(2); got != 1 { // never below 1
		t.Errorf("focusBody(2) = %d, want 1", got)
	}
}

func TestAnsiClip(t *testing.T) {
	// SGR escapes pass through uncounted; only visible runes count.
	got := ansiClip("\x1b[31mhello\x1b[0m world", 5)
	if v := stripANSI(got); v != "hello" {
		t.Errorf("clipped visible = %q, want %q", v, "hello")
	}
	if got := ansiClip("hi", 40); got != "hi" {
		t.Errorf("short unchanged = %q", got)
	}
}
