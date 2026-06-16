package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
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

func TestFollowAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	emit := func(b []byte) {
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}
	stop := make(chan struct{})
	go followAppend(path, 6, emit, stop) // start past "line1\n"

	// Append two lines.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("line2\nline3\n")
	f.Close()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	})
	close(stop)
	if !reflect.DeepEqual(got, []string{"line2", "line3"}) {
		t.Errorf("got %v", got)
	}
}

func TestFollowAppendTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	os.WriteFile(path, []byte("aaaa\nbbbb\n"), 0o644)

	var mu sync.Mutex
	var got []string
	emit := func(b []byte) { mu.Lock(); got = append(got, string(b)); mu.Unlock() }
	stop := make(chan struct{})
	go followAppend(path, 10, emit, stop) // start at EOF

	// Truncate + rewrite shorter → follower resets to 0 and re-reads.
	os.WriteFile(path, []byte("new\n"), 0o644)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, g := range got {
			if g == "new" {
				return true
			}
		}
		return false
	})
	close(stop)
}

func TestFollowRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	os.WriteFile(path, []byte(`{"step_index":1}`+"\n"), 0o644)

	var mu sync.Mutex
	var got []int
	keep := newAgyDedup(1) // seed: step 1 already rendered
	emit := func(b []byte) {
		if keep(b) {
			n, _ := agyStepIndex(b)
			mu.Lock()
			got = append(got, n)
			mu.Unlock()
		}
	}
	stop := make(chan struct{})
	go followRewrite(path, emit, stop)

	// Whole-file rewrite adding step 2.
	os.WriteFile(path, []byte(`{"step_index":1}`+"\n"+`{"step_index":2}`+"\n"), 0o644)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1 && got[0] == 2
	})
	close(stop)
}

// waitFor polls cond until true or a timeout fires.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
