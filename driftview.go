package main

import (
	"io"
	"os"
	"strings"
)

// driftview.go — the `d` overlay: run the check, show the card, wait for a key.
//
// Same tty contract as runFocus: the live tail leaves the terminal in cbreak, we
// take raw+timed and the alt screen for the duration, and restore both on the
// way out. Unlike the focus overlay there is nothing to follow, so this reads
// one key and returns.

func runDrift(tty *os.File, path, home string, theme Theme) {
	if tty == nil {
		return
	}
	saved, ok := setRawTimed(tty)
	if !ok {
		return
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	// The model takes a few seconds on-device — long enough to look hung.
	io.WriteString(tty, driftBanner(sessionIDFor(path)))
	report, err := driftCheck(path, home)

	w, _ := termSize(tty)
	lines := driftCardLines(report, err, theme, w)
	lines = append(lines, "", driftIndent+theme.DimANSI+"press any key to go back"+dimReset(theme))
	io.WriteString(tty, "\x1b[H\x1b[2J"+strings.Join(lines, "\r\n"))

	buf := make([]byte, 16)
	for {
		n, err := tty.Read(buf)
		if err != nil && err != io.EOF {
			return
		}
		if n > 0 {
			return
		}
	}
}

func dimReset(theme Theme) string {
	if theme.DimANSI == "" {
		return ""
	}
	return "\x1b[0m"
}

// sessionIDFor recovers the session id from a transcript path: every supported
// agent names the file after the session.
func sessionIDFor(path string) string {
	base := path[strings.LastIndexByte(path, '/')+1:]
	return strings.TrimSuffix(base, ".jsonl")
}
