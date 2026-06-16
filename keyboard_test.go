package main

import "testing"

func TestKeyActionFor(t *testing.T) {
	cases := map[byte]keyAction{
		't':  keyCycleTools,
		'T':  keyCycleTools,
		'c':  keyToggleCollapse,
		'C':  keyToggleCollapse,
		'q':  keyQuit,
		'Q':  keyQuit,
		0x04: keyQuit, // Ctrl-D
		'x':  keyNone,
		' ':  keyNone,
		0x03: keyNone, // Ctrl-C is left to the signal handler
	}
	for b, want := range cases {
		if got := keyActionFor(b); got != want {
			t.Errorf("keyActionFor(%q/0x%02x) = %v, want %v", b, b, got, want)
		}
	}
}
