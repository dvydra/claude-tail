//go:build windows

package main

import (
	"os"
	"testing"
)

func TestTTYWindows(t *testing.T) {
	t.Logf("isCharDevice(os.Stdout): %v", isCharDevice(os.Stdout))
	t.Logf("isCharDevice(os.Stdin): %v", isCharDevice(os.Stdin))
	t.Logf("ttyUsable(): %v", ttyUsable())

	r, err := openTTY(os.O_RDONLY)
	t.Logf("openTTY(RDONLY): err=%v, nil?=%v", err, r == nil)
	if r != nil && r != os.Stdin {
		r.Close()
	}

	w, err := openTTY(os.O_WRONLY)
	t.Logf("openTTY(WRONLY): err=%v, nil?=%v", err, w == nil)
	if w != nil && w != os.Stdout {
		w.Close()
	}

	rw, err := openTTY(os.O_RDWR)
	t.Logf("openTTY(RDWR): err=%v, nil?=%v", err, rw == nil)
	if rw != nil {
		state, ok := setRaw(rw)
		t.Logf("setRaw(rw): ok=%v, state=%+v", ok, state)
		if ok {
			restoreCbreak(rw, state)
		}
		if rw != os.Stdin && rw != os.Stdout {
			rw.Close()
		}
	}
}
