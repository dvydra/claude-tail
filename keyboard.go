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
	keyReload
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
	case 'r', 'R':
		return keyReload
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
// The returned *os.File is the controlling tty the keyboard goroutine reads (nil
// when there's no usable tty). The focus overlay reuses this SAME fd — while it
// runs, the keyboard goroutine is parked on resumeCh, so there's a single tty
// reader at all times (two fds on the same tty race for input).
func startKeyboard(r *Renderer, codeCh chan<- int, reloadCh chan<- struct{}, focusCh chan<- struct{}, resumeCh <-chan struct{}) (func(), *os.File) {
	if !isCharDevice(os.Stdin) {
		return func() {}, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return func() {}, nil
	}
	saved, ok := setCbreak(tty)
	if !ok {
		tty.Close()
		return func() {}, nil
	}

	var once sync.Once
	restore := func() {
		once.Do(func() {
			restoreCbreak(tty, saved)
			tty.Close()
		})
	}

	go func() {
		buf := make([]byte, 16)
		for {
			n, err := tty.Read(buf)
			if err != nil {
				codeCh <- 0
				return
			}
			if n == 0 {
				continue
			}
			// Multi-byte escape sequence (arrow keys): → enters the subagent
			// focus overlay. We hand the tty to the render goroutine (which runs
			// the alt-screen overlay) and PARK here until it signals done, so
			// there's never two readers on the tty. Other arrows are ignored.
			if n >= 3 && buf[0] == 0x1b {
				if k, _ := decodeKey(buf[:n]); k == kRight {
					// Hand the tty to the render goroutine's overlay and park
					// until it's done — one tty reader at a time.
					focusCh <- struct{}{}
					<-resumeCh
				}
				continue
			}
			switch keyActionFor(buf[0]) {
			case keyQuit:
				codeCh <- 0
				return
			case keyCycleTools:
				fmt.Fprintln(os.Stderr, "entire-tail: "+r.cycleTools()+" (press r to re-render history)")
			case keyToggleCollapse:
				fmt.Fprintln(os.Stderr, "entire-tail: "+r.toggleCollapse()+" (press r to re-render history)")
			case keyReload:
				// Signal the render goroutine; never write stdout from here.
				select {
				case reloadCh <- struct{}{}:
				default: // a reload is already pending; coalesce
				}
			}
		}
	}()
	return restore, tty
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

// setRaw is like setCbreak but also disables signal keys (-isig), so Ctrl-C
// arrives as a byte (0x03) instead of a signal. The alt-screen tree uses this so
// a Ctrl-C is caught by the loop and the terminal is restored cleanly (alt-screen
// off, cursor back) rather than the process dying with the screen left raw.
// Restored with restoreCbreak.
func setRaw(tty *os.File) (saved string, ok bool) {
	var buf bytes.Buffer
	get := exec.Command("stty", "-g")
	get.Stdin = tty
	get.Stdout = &buf
	if get.Run() != nil {
		return "", false
	}
	saved = strings.TrimSpace(buf.String())

	set := exec.Command("stty", "-icanon", "-echo", "-isig", "min", "1", "time", "0")
	set.Stdin = tty
	if set.Run() != nil {
		restoreCbreak(tty, saved)
		return "", false
	}
	return saved, true
}

// setRawTimed is setRaw with a read timeout (min 0, time 5 = 0.5s): tty.Read
// returns n==0 after the timeout even with no keypress. The focus overlay uses
// this to poll the subagent file for new content between keystrokes (live
// follow) on a single goroutine. Restored with restoreCbreak.
func setRawTimed(tty *os.File) (saved string, ok bool) {
	var buf bytes.Buffer
	get := exec.Command("stty", "-g")
	get.Stdin = tty
	get.Stdout = &buf
	if get.Run() != nil {
		return "", false
	}
	saved = strings.TrimSpace(buf.String())

	set := exec.Command("stty", "-icanon", "-echo", "-isig", "min", "0", "time", "5")
	set.Stdin = tty
	if set.Run() != nil {
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
