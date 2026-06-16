package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"
)

const pollInterval = 250 * time.Millisecond

// splitLines splits raw file bytes into lines, dropping a single trailing empty
// line from a final newline.
func splitLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	lines := bytes.Split(data, []byte("\n"))
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	}
	return lines
}

// liveOffset returns the byte position just past the last complete line, so the
// follower resumes at a line boundary (a trailing partial line is re-read once
// the agent finishes writing it).
func liveOffset(data []byte) int64 {
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		return int64(i + 1)
	}
	return 0
}

// appendStep emits any newly-appended complete lines on an append-only session
// file since offset and returns the new offset. It resets to the start if the
// file was truncated/rotated. Called once per poll tick by the live loop (which
// also handles reload/quit), so polling, rendering, and quitting all stay on the
// one render goroutine.
func appendStep(path string, offset int64, emit func([]byte)) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return offset
	}
	if fi.Size() < offset {
		offset = 0 // truncated or rotated
	}
	if fi.Size() > offset {
		offset = readAppended(path, offset, emit)
	}
	return offset
}

// readAppended reads complete lines starting at offset and returns the new
// offset (advanced only past complete, newline-terminated lines).
func readAppended(path string, offset int64, emit func([]byte)) int64 {
	f, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			emit(bytes.TrimRight(line, "\n"))
			offset += int64(len(line))
		}
		if err != nil {
			return offset
		}
	}
}

var stepIndexRe = regexp.MustCompile(`"step_index"\s*:\s*(\d+)`)

// agyStepIndex extracts a step_index from a raw line, mirroring the bash awk
// regex. ok is false when the line has no step_index (such lines are never
// deduped).
func agyStepIndex(line []byte) (int, bool) {
	m := stepIndexRe.FindSubmatch(line)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return 0, false
	}
	return n, true
}

// maxStepIndex returns the largest step_index across lines (absent → 0),
// seeding the live dedup filter — equivalent to the bash MAX_STEP jq.
func maxStepIndex(lines [][]byte) int {
	best := 0
	for _, l := range lines {
		if n, ok := agyStepIndex(l); ok && n > best {
			best = n
		}
	}
	return best
}

// newAgyDedup returns a predicate that keeps a line only if it carries a new
// (greater than seen) step_index; lines without a step_index always pass.
func newAgyDedup(seed int) func([]byte) bool {
	seen := seed
	return func(line []byte) bool {
		n, ok := agyStepIndex(line)
		if !ok {
			return true
		}
		if n <= seen {
			return false
		}
		seen = n
		return true
	}
}
