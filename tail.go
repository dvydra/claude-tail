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

// followAppend tails an append-only session file from offset, emitting each
// newly-appended complete line. It resets to the start if the file is truncated.
// It loops until stop is closed.
func followAppend(path string, offset int64, emit func([]byte), stop <-chan struct{}) {
	for {
		if fi, err := os.Stat(path); err == nil {
			size := fi.Size()
			if size < offset {
				offset = 0 // truncated or rotated
			}
			if size > offset {
				offset = readAppended(path, offset, emit)
			}
		}
		if sleepOrStop(stop) {
			return
		}
	}
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

// followRewrite tails a session file that is rewritten wholesale on each change
// (agy atomically renames or truncates-in-place). It re-reads the whole file on
// every change and emits all lines; emit is expected to dedup by step index.
func followRewrite(path string, emit func([]byte), stop <-chan struct{}) {
	var lastSize int64 = -1
	var lastMtime int64 = -1
	for {
		if fi, err := os.Stat(path); err == nil {
			size, mtime := fi.Size(), fi.ModTime().UnixNano()
			if size != lastSize || mtime != lastMtime {
				lastSize, lastMtime = size, mtime
				if data, err := os.ReadFile(path); err == nil {
					for _, line := range splitLines(data) {
						emit(line)
					}
				}
			}
		}
		if sleepOrStop(stop) {
			return
		}
	}
}

func sleepOrStop(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	case <-time.After(pollInterval):
		return false
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
