package main

import "testing"

func TestKeyActionFor(t *testing.T) {
	cases := map[byte]keyAction{
		't':  keyCycleTools,
		'T':  keyCycleTools,
		'c':  keyToggleCollapse,
		'C':  keyToggleCollapse,
		'r':  keyReload,
		'R':  keyReload,
		'q':  keyQuit,
		'Q':  keyQuit,
		0x04: keyQuit,       // Ctrl-D
		0x18: keyBackToTree, // Ctrl-X
		'x':  keyNone,       // plain x is not Ctrl-X
		' ':  keyNone,
		0x03: keyNone, // Ctrl-C is left to the signal handler
	}
	for b, want := range cases {
		if got := keyActionFor(b); got != want {
			t.Errorf("keyActionFor(%q/0x%02x) = %v, want %v", b, b, got, want)
		}
	}
}
