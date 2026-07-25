package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"golang.org/x/term"
)

// keyAction is what a single keypress means during live follow.
type keyAction int

const (
	keyNone keyAction = iota
	keyCycleTools
	keyCycleTheme
	keyToggleCollapse
	keyReload
	keyQuit
	keyBackToTree
)

// keyActionFor maps a raw input byte to an action. Ctrl-C is left to the signal
// handler (ISIG stays enabled in cbreak mode), so it isn't handled here; Ctrl-D
// (0x04) and Ctrl-X (0x18) arrive as bytes once canonical mode is off (neither is
// a signal-generating control char, so cbreak passes them straight through).
func keyActionFor(b byte) keyAction {
	switch b {
	case 't':
		return keyCycleTools
	case 'T':
		return keyCycleTheme
	case 'c', 'C':
		return keyToggleCollapse
	case 'r', 'R':
		return keyReload
	case 'q', 'Q', 0x04:
		return keyQuit
	case 0x18: // Ctrl-X
		return keyBackToTree
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
func startKeyboard(r *Renderer, treeEnabled bool, codeCh chan<- int, reloadCh chan<- struct{}, themeCh chan<- struct{}, treeCh chan<- struct{}, focusCh chan<- struct{}, resumeCh <-chan struct{}) (func(), *os.File) {
	if !isCharDevice(os.Stdin) {
		return func() {}, nil
	}
	tty, err := openTTY(os.O_RDWR)
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
			case keyBackToTree:
				// Only meaningful for a Claude session on a tty (the tree is
				// Claude-only). Signal the live loop and STOP reading so the
				// picker's tty reader is the sole one, then return.
				if treeEnabled {
					treeCh <- struct{}{}
					return
				}
			case keyCycleTools:
				fmt.Fprintln(os.Stderr, "entire-tail: "+r.cycleTools()+" (press r to re-render history)")
			case keyCycleTheme:
				// Theme swap + re-render must run on the render goroutine (it
				// rebuilds the non-atomic render fn / header state); just signal it.
				select {
				case themeCh <- struct{}{}:
				default: // a theme cycle is already pending; coalesce
				}
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

type termState struct {
	sttyState string
	rawState  *term.State
	fd        int
}

// setCbreak puts tty into cbreak mode via stty or term.MakeRaw fallback.
func setCbreak(tty *os.File) (termState, bool) {
	fd := int(tty.Fd())
	var buf bytes.Buffer
	get := exec.Command("stty", "-g")
	get.Stdin = tty
	get.Stdout = &buf
	if get.Run() == nil {
		saved := strings.TrimSpace(buf.String())
		set := exec.Command("stty", "-icanon", "-echo", "min", "1", "time", "0")
		set.Stdin = tty
		if set.Run() == nil {
			return termState{sttyState: saved, fd: fd}, true
		}
	}
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		return termState{rawState: oldState, fd: fd}, true
	}
	return termState{}, false
}

// setRaw puts tty into raw mode via stty or term.MakeRaw fallback.
func setRaw(tty *os.File) (termState, bool) {
	fd := int(tty.Fd())
	var buf bytes.Buffer
	get := exec.Command("stty", "-g")
	get.Stdin = tty
	get.Stdout = &buf
	if get.Run() == nil {
		saved := strings.TrimSpace(buf.String())
		set := exec.Command("stty", "-icanon", "-echo", "-isig", "min", "1", "time", "0")
		set.Stdin = tty
		if set.Run() == nil {
			return termState{sttyState: saved, fd: fd}, true
		}
	}
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		return termState{rawState: oldState, fd: fd}, true
	}
	return termState{}, false
}

// setRawTimed puts tty into raw mode with a timeout.
func setRawTimed(tty *os.File) (termState, bool) {
	fd := int(tty.Fd())
	var buf bytes.Buffer
	get := exec.Command("stty", "-g")
	get.Stdin = tty
	get.Stdout = &buf
	if get.Run() == nil {
		saved := strings.TrimSpace(buf.String())
		set := exec.Command("stty", "-icanon", "-echo", "-isig", "min", "0", "time", "5")
		set.Stdin = tty
		if set.Run() == nil {
			return termState{sttyState: saved, fd: fd}, true
		}
	}
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		return termState{rawState: oldState, fd: fd}, true
	}
	return termState{}, false
}

func restoreCbreak(tty *os.File, state termState) {
	if state.rawState != nil {
		_ = term.Restore(state.fd, state.rawState)
		return
	}
	if state.sttyState != "" {
		cmd := exec.Command("stty", state.sttyState)
		cmd.Stdin = tty
		_ = cmd.Run()
	}
}
