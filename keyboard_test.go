package main

import "testing"

func TestKeyActionFor(t *testing.T) {
	cases := map[byte]keyAction{
		't':  keyCycleTools,
		'T':  keyCycleTheme, // shift-t cycles the theme (lowercase t stays tools)
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

// A read is only a keypress when it holds exactly one: a burst is input the
// human didn't type key by key. Measured live: an iTerm click sent ~14 KB of
// `ESC [ D`, the 16-byte reads split it on a 3-byte stride, and every third
// read began with a bare `D` — the drift key — so the tail ran an on-device
// model check back to back for minutes.
func TestLiveKeyIgnoresBursts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want keyAction
	}{
		{"single key", "d", keyDrift},
		{"ctrl-x", "\x18", keyBackToTree},
		{"right arrow", "\x1b[C", keyFocus},
		{"left arrow", "\x1b[D", keyNone},
		{"arrow flood", "\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b", keyNone},
		{"split flood tail", "D\x1b[D\x1b[D\x1b[D\x1b[D\x1b[", keyNone},
		{"split flood middle", "[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b", keyNone},
		{"right arrow then more", "\x1b[C\x1b[C", keyNone},
		{"pasted text", "dddd", keyNone},
		{"pasted quit", "qq", keyNone},
		{"empty", "", keyNone},
	}
	for _, c := range cases {
		if got := liveKey([]byte(c.in)); got != c.want {
			t.Errorf("%s: liveKey(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
