package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// subagents.go discovers the subagent transcripts spawned by a Claude session.
// Each spawn writes a sidecar file next to the main transcript:
//
//	<project>/<sessionId>.jsonl                      ← main thread
//	<project>/<sessionId>/subagents/agent-<id>.jsonl ← one per subagent
//	<project>/<sessionId>/subagents/agent-<id>.meta.json
//
// The subagent files are standard Claude JSONL, so the normal renderer handles
// them; discovery just enumerates them + their metadata for the focus overlay.

// subagentChannel is one subagent transcript the focus overlay can open.
type subagentChannel struct {
	AgentID     string
	Description string // task description (from meta.json)
	AgentType   string // e.g. "general-purpose"
	Path        string // absolute path to the subagent .jsonl
	SpawnTs     int64  // unix seconds of its first record (for stable ordering)
}

type subagentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	ToolUseID   string `json:"toolUseId"`
	SpawnDepth  int    `json:"spawnDepth"`
}

// subagentsDir returns the sidecar directory for a main transcript path, i.e.
// "<dir>/<sessionId>/subagents".
func subagentsDir(mainPath string) string {
	base := strings.TrimSuffix(mainPath, filepath.Ext(mainPath))
	return filepath.Join(base, "subagents")
}

// discoverSubagents enumerates a session's subagent channels, ordered by spawn
// time (oldest first). Returns nil when there are none (or the path isn't a
// Claude session with a sidecar dir).
func discoverSubagents(mainPath string) []subagentChannel {
	dir := subagentsDir(mainPath)
	matches, err := filepath.Glob(filepath.Join(dir, "agent-*.jsonl"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	var chans []subagentChannel
	for _, p := range matches {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "agent-"), ".jsonl")
		ch := subagentChannel{AgentID: id, Path: p}
		if m, ok := readSubagentMeta(strings.TrimSuffix(p, ".jsonl") + ".meta.json"); ok {
			ch.Description, ch.AgentType = m.Description, m.AgentType
		}
		if ch.Description == "" {
			ch.Description = "subagent " + shortID(id)
		}
		ch.SpawnTs = firstRecordTS(p)
		chans = append(chans, ch)
	}
	sort.SliceStable(chans, func(i, j int) bool {
		if chans[i].SpawnTs != chans[j].SpawnTs {
			return chans[i].SpawnTs < chans[j].SpawnTs
		}
		return chans[i].Path < chans[j].Path
	})
	return chans
}

func readSubagentMeta(path string) (subagentMeta, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return subagentMeta{}, false
	}
	var m subagentMeta
	if json.Unmarshal(data, &m) != nil {
		return subagentMeta{}, false
	}
	return m, true
}

// status reports whether the subagent is still being written to (best-effort:
// its file was touched within the last few seconds) and its elapsed run time
// (first → last record timestamp).
func (c subagentChannel) status(now int64) (running bool, dur time.Duration) {
	fi, err := os.Stat(c.Path)
	if err != nil {
		return false, 0
	}
	running = now-fi.ModTime().Unix() < 8
	first, last := c.SpawnTs, lastRecordTS(c.Path)
	if first > 0 && last >= first {
		dur = time.Duration(last-first) * time.Second
	}
	return running, dur
}

// tsRe-free timestamp helpers: read the first / last record's "timestamp".

func firstRecordTS(path string) int64 {
	lines := headLines(path, 1)
	if len(lines) == 0 {
		return 0
	}
	return recordEpoch(lines[0])
}

func lastRecordTS(path string) int64 {
	lines := tailLines(path, 1)
	if len(lines) == 0 {
		return 0
	}
	return recordEpoch(lines[len(lines)-1])
}

// recordEpoch parses the "timestamp" field of a Claude JSONL line to unix
// seconds; 0 when absent/unparseable.
func recordEpoch(line []byte) int64 {
	var ev struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Timestamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, ev.Timestamp)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// headLines returns the first n complete lines of a file (bounded read for big
// transcripts).
func headLines(path string, n int) [][]byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out [][]byte
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for len(out) < n {
		m, err := f.Read(tmp)
		if m > 0 {
			buf = append(buf, tmp[:m]...)
			for {
				i := indexByte(buf, '\n')
				if i < 0 {
					break
				}
				out = append(out, append([]byte(nil), buf[:i]...))
				buf = buf[i+1:]
				if len(out) >= n {
					break
				}
			}
		}
		if err != nil {
			break
		}
	}
	return out
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}
