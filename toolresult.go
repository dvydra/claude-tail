package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// patchHunk is one entry of Claude's toolUseResult.structuredPatch.
type patchHunk struct {
	OldStart int      `json:"oldStart"`
	NewStart int      `json:"newStart"`
	Lines    []string `json:"lines"` // each prefixed with ' ', '+', or '-'
}

// parseClaudeToolResult turns Claude's toolUseResult into renderable detail for
// full mode. It recognizes the common shapes — a structured diff (Edit/Write),
// command output (Bash), and a read summary — and returns nil for anything
// without useful detail (so the renderer just omits the ⎿ line).
func parseClaudeToolResult(raw json.RawMessage) *ToolResult {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return nil
	}
	switch t[0] {
	case '{':
		var o struct {
			StructuredPatch []patchHunk `json:"structuredPatch"`
			Stdout          string      `json:"stdout"`
			Stderr          string      `json:"stderr"`
			File            *struct {
				NumLines int `json:"numLines"`
			} `json:"file"`
		}
		if json.Unmarshal(t, &o) != nil {
			return nil
		}
		switch {
		case len(o.StructuredPatch) > 0:
			diff, added, removed := buildDiff(o.StructuredPatch)
			return &ToolResult{Summary: diffSummary(added, removed), Diff: diff}
		case o.Stdout != "" || o.Stderr != "":
			out := o.Stdout
			if out == "" {
				out = o.Stderr
			}
			return &ToolResult{Output: outputLines(out)}
		case o.File != nil:
			return &ToolResult{Summary: "Read " + plural(o.File.NumLines, "line")}
		}
		return nil
	case '"':
		var s string
		if json.Unmarshal(t, &s) == nil && strings.TrimSpace(s) != "" {
			return &ToolResult{Output: outputLines(s)}
		}
	}
	return nil
}

// buildDiff walks the patch hunks into displayable diff lines, tracking the
// old/new line numbers (Claude shows the new-file number for context/additions
// and the old-file number for removals).
func buildDiff(hunks []patchHunk) (lines []DiffLine, added, removed int) {
	for _, h := range hunks {
		oldNum, newNum := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			sign := byte(' ')
			text := l
			if len(l) > 0 && (l[0] == '+' || l[0] == '-' || l[0] == ' ') {
				sign, text = l[0], l[1:]
			}
			switch sign {
			case '+':
				lines = append(lines, DiffLine{'+', newNum, text})
				newNum++
				added++
			case '-':
				lines = append(lines, DiffLine{'-', oldNum, text})
				oldNum++
				removed++
			default:
				lines = append(lines, DiffLine{' ', newNum, text})
				oldNum++
				newNum++
			}
		}
	}
	return
}

func diffSummary(added, removed int) string {
	switch {
	case added > 0 && removed > 0:
		return "Added " + plural(added, "line") + ", removed " + plural(removed, "line")
	case added > 0:
		return "Added " + plural(added, "line")
	case removed > 0:
		return "Removed " + plural(removed, "line")
	}
	return "No changes"
}

func plural(n int, word string) string {
	s := strconv.Itoa(n) + " " + word
	if n != 1 {
		s += "s"
	}
	return s
}

// outputLines splits command output into all its lines (full mode renders
// everything — no truncation; that's the point of "full"). A single trailing
// newline is dropped.
func outputLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
