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

func TestWTPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
	if !wtAvailable() {
		t.Skip("wt.exe not available")
	}
	p, err := findWTPath()
	if err != nil {
		t.Fatalf("findWTPath failed: %v", err)
	}
	t.Logf("findWTPath: %s", p)
}
