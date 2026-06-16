package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a\nb\nc\n", []string{"a", "b", "c"}},
		{"a\nb\nc", []string{"a", "b", "c"}}, // no trailing newline
		{"", nil},
		{"\n", []string{""}},
	}
	for _, c := range cases {
		got := splitLines([]byte(c.in))
		var gotStr []string
		for _, b := range got {
			gotStr = append(gotStr, string(b))
		}
		if !reflect.DeepEqual(gotStr, c.want) {
			t.Errorf("splitLines(%q) = %v, want %v", c.in, gotStr, c.want)
		}
	}
}

func TestLiveOffset(t *testing.T) {
	if got := liveOffset([]byte("a\nb\n")); got != 4 {
		t.Errorf("got %d", got)
	}
	if got := liveOffset([]byte("a\nb")); got != 2 { // past last complete line
		t.Errorf("got %d", got)
	}
	if got := liveOffset([]byte("nonewline")); got != 0 {
		t.Errorf("got %d", got)
	}
}

func TestAgyStepIndex(t *testing.T) {
	if n, ok := agyStepIndex([]byte(`{"step_index": 42, "x":1}`)); !ok || n != 42 {
		t.Errorf("got %d,%v", n, ok)
	}
	if _, ok := agyStepIndex([]byte(`{"x":1}`)); ok {
		t.Errorf("expected no step_index")
	}
}

func TestMaxStepIndex(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"step_index":3}`),
		[]byte(`{"no_step":true}`),
		[]byte(`{"step_index":7}`),
		[]byte(`{"step_index":5}`),
	}
	if got := maxStepIndex(lines); got != 7 {
		t.Errorf("got %d", got)
	}
	if got := maxStepIndex(nil); got != 0 {
		t.Errorf("got %d", got)
	}
}

func TestAgyDedup(t *testing.T) {
	keep := newAgyDedup(5)
	cases := []struct {
		line string
		want bool
	}{
		{`{"step_index":3}`, false}, // <= seed
		{`{"step_index":5}`, false}, // == seed
		{`{"step_index":6}`, true},  // new
		{`{"step_index":6}`, false}, // already seen
		{`{"step_index":8}`, true},  // new
		{`{"no_step":true}`, true},  // no step_index → always kept
	}
	for _, c := range cases {
		if got := keep([]byte(c.line)); got != c.want {
			t.Errorf("keep(%s) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestAppendStep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []string
	emit := func(b []byte) { got = append(got, string(b)) }

	// Start past "line1\n": nothing new yet.
	off := appendStep(path, 6, emit)
	if len(got) != 0 || off != 6 {
		t.Fatalf("expected no new lines, got %v off=%d", got, off)
	}
	// Append two lines → appendStep emits them and advances the offset.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("line2\nline3\n")
	f.Close()
	off = appendStep(path, off, emit)
	if !reflect.DeepEqual(got, []string{"line2", "line3"}) {
		t.Errorf("got %v", got)
	}
	if off != int64(len("line1\nline2\nline3\n")) {
		t.Errorf("offset = %d", off)
	}
	// No further appends → no more lines.
	got = nil
	appendStep(path, off, emit)
	if len(got) != 0 {
		t.Errorf("expected nothing new, got %v", got)
	}
}

func TestAppendStepTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	os.WriteFile(path, []byte("aaaa\nbbbb\n"), 0o644)
	var got []string
	emit := func(b []byte) { got = append(got, string(b)) }

	// Offset past EOF, then the file is rewritten shorter → reset to 0 and re-read.
	os.WriteFile(path, []byte("new\n"), 0o644)
	appendStep(path, 10, emit)
	if !slices.Contains(got, "new") {
		t.Errorf("expected re-read after truncation, got %v", got)
	}
}
