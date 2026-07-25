package main

import (
	"runtime"
	"testing"
)

func TestWTAvailable(t *testing.T) {
	got := wtAvailable()
	if runtime.GOOS != "windows" && got {
		t.Errorf("wtAvailable() on non-Windows should be false, got true")
	}
}
