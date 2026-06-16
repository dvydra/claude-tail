package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// keyAction is what a single keypress means during live follow.
type keyAction int

const (
	keyNone keyAction = iota
	keyCycleTools
	keyToggleCollapse
	keyQuit
)

// keyActionFor maps a raw input byte to an action. Ctrl-C is left to the signal
// handler (ISIG stays enabled in cbreak mode), so it isn't handled here; Ctrl-D
// (0x04) arrives as a byte once canonical mode is off.
func keyActionFor(b byte) keyAction {
	switch b {
	case 't', 'T':
		return keyCycleTools
	case 'c', 'C':
		return keyToggleCollapse
	case 'q', 'Q', 0x04:
		return keyQuit
	}
	return keyNone
}

// startKeyboard wires single-key live controls when stdin is a terminal: it puts
// the controlling tty into cbreak mode (single-key, no echo, but output
// processing and signals left intact, so the stream doesn't staircase and Ctrl-C
// still signals), then reads keys and flips the renderer's display flags (which
// are atomic, so this is race-free with the render goroutine). A quit key
// reports exit code 0 on codeCh. Returns a restore func the caller must run
// before exit; it's a no-op when there's no usable tty.
func startKeyboard(r *Renderer, codeCh chan<- int) func() {
	if !isCharDevice(os.Stdin) {
		return func() {}
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return func() {}
	}
	saved, ok := setCbreak(tty)
	if !ok {
		tty.Close()
		return func() {}
	}

	var once sync.Once
	restore := func() {
		once.Do(func() {
			restoreCbreak(tty, saved)
			tty.Close()
		})
	}

	go func() {
		buf := make([]byte, 1)
		for {
			n, err := tty.Read(buf)
			if err != nil {
				codeCh <- 0
				return
			}
			if n == 0 {
				continue
			}
			switch keyActionFor(buf[0]) {
			case keyQuit:
				codeCh <- 0
				return
			case keyCycleTools:
				fmt.Fprintln(os.Stderr, "entire-tail: "+r.cycleTools())
			case keyToggleCollapse:
				fmt.Fprintln(os.Stderr, "entire-tail: "+r.toggleCollapse())
			}
		}
	}()
	return restore
}

// setCbreak puts tty into cbreak mode via stty (which handles the BSD/Linux
// termios differences itself) and returns the prior settings for restore. Only
// canonical mode and echo are disabled — output post-processing and signal keys
// are left on. ok is false if stty isn't usable.
func setCbreak(tty *os.File) (saved string, ok bool) {
	var buf bytes.Buffer
	get := exec.Command("stty", "-g")
	get.Stdin = tty
	get.Stdout = &buf
	if get.Run() != nil {
		return "", false
	}
	saved = strings.TrimSpace(buf.String())

	set := exec.Command("stty", "-icanon", "-echo", "min", "1", "time", "0")
	set.Stdin = tty
	if set.Run() != nil {
		// Best-effort restore of whatever we read, then report failure.
		restoreCbreak(tty, saved)
		return "", false
	}
	return saved, true
}

func restoreCbreak(tty *os.File, saved string) {
	if saved == "" {
		return
	}
	cmd := exec.Command("stty", saved)
	cmd.Stdin = tty
	_ = cmd.Run()
}
