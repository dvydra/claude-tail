package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestSampleTurns(t *testing.T) {
	// Fits in budget → returned whole.
	short := []string{"User: hi", "Assistant: hello"}
	if got := sampleTurns(short, 1000); got != "User: hi\nAssistant: hello" {
		t.Errorf("short = %q", got)
	}

	// Over budget → head + tail with the middle elided.
	var turns []string
	for i := 0; i < 200; i++ {
		turns = append(turns, fmt.Sprintf("User: message number %d with padding to add length", i))
	}
	got := sampleTurns(turns, 500)
	if !strings.Contains(got, "elided") {
		t.Error("long transcript should elide the middle")
	}
	if !strings.Contains(got, "number 0 ") {
		t.Error("should keep the opening turn (the goal)")
	}
	if !strings.Contains(got, "number 199 ") {
		t.Error("should keep the final turn (the outcome)")
	}
	if len(got) > 700 { // budget 500 + elision marker + a turn's slack
		t.Errorf("sample over budget: %d chars", len(got))
	}
}
