package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSignalQuitExitsEvenIfTheTailNeverDrains(t *testing.T) {
	oldExit, oldGrace := signalExit, signalQuitGrace
	t.Cleanup(func() { signalExit, signalQuitGrace = oldExit, oldGrace })
	exited := make(chan int, 1)
	signalExit = func(code int) { exited <- code }
	signalQuitGrace = 50 * time.Millisecond

	codeCh := make(chan int, 3)
	installSignals(codeCh)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("SIGTERM reported code %d, want 0", code)
		}
	case <-time.After(time.Second):
		t.Fatal("SIGTERM was not reported on codeCh")
	}
	// Nothing drains the quit here, standing in for a tail whose final flush
	// blocks on a stalled terminal.
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("forced exit code %d, want 0", code)
		}
	case <-time.After(time.Second):
		t.Fatal("a quit that never completed did not force an exit")
	}
}
