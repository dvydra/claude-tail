package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Claude Code stores one JSON object per line under ~/.claude/projects/<slug>/.
// We render `user` and `assistant` events; everything else (ai-title, mode,
// system, file-history-snapshot, …) is ignored.

type claudeEvent struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Message   *claudeMessage `json:"message"`
}

type claudeMessage struct {
	Content json.RawMessage `json:"content"`
}

type claudeBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

const answerPrefix = "User has answered your questions:"

var answerSuffixRe = regexp.MustCompile(`\. You can now continue with the user.s answers in mind\.\s*$`)

func normalizeClaude(line []byte, loc *time.Location) []Record {
	var ev claudeEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Message == nil {
		return nil
	}
	ts := formatTS(ev.Timestamp, loc)

	switch ev.Type {
	case "user":
		// Content is either a plain string (a typed message) or an array of
		// blocks (tool results, and sometimes an AskUserQuestion answer).
		var s string
		if json.Unmarshal(ev.Message.Content, &s) == nil {
			return []Record{{Kind: KindUser, Ts: ts, Body: s}}
		}
		var blocks []claudeBlock
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return nil
		}
		var results []claudeBlock
		for _, b := range blocks {
			if b.Type == "tool_result" {
				results = append(results, b)
			}
		}
		// An AskUserQuestion answer comes back as a tool_result with a
		// fixed-template string; surface it as a real USER turn with the
		// boilerplate trimmed instead of a single tool_result dot.
		for _, r := range results {
			var content string
			if json.Unmarshal(r.Content, &content) == nil && strings.HasPrefix(content, answerPrefix) {
				body := strings.TrimPrefix(content, answerPrefix+" ")
				body = answerSuffixRe.ReplaceAllString(body, "")
				return []Record{{Kind: KindUser, Ts: ts, Body: body}}
			}
		}
		if len(results) > 0 {
			return []Record{{Kind: KindToolResult, N: len(results)}}
		}
		return nil

	case "assistant":
		var blocks []claudeBlock
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return nil
		}
		var out []Record
		for _, b := range blocks {
			switch b.Type {
			case "text":
				out = append(out, Record{Kind: KindAssistant, Ts: ts, Body: b.Text})
			case "tool_use":
				if b.Name == "AskUserQuestion" {
					out = append(out, Record{Kind: KindAssistant, Ts: ts, Body: claudeAskQBody(b.Input)})
				} else {
					out = append(out, Record{Kind: KindToolUse, Name: b.Name, Summary: claudeToolSummary(b.Name, b.Input)})
				}
			}
		}
		return out
	}
	return nil
}

func claudeToolSummary(name string, input json.RawMessage) string {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(input, &m) // nil map on failure is fine; lookups miss

	str := func(keys ...string) string {
		if raw, ok := firstRaw(m, keys...); ok {
			return jqToStringRaw(raw)
		}
		return ""
	}

	var s string
	switch name {
	case "Bash":
		s = str("command")
	case "Read", "Edit", "Write", "NotebookEdit", "MultiEdit":
		s = str("file_path")
	case "Grep":
		s = str("pattern")
		if raw, ok := firstRaw(m, "path"); ok {
			s += " in " + jqToStringRaw(raw)
		}
	case "Glob":
		s = str("pattern")
	case "WebFetch":
		s = str("url")
	case "WebSearch":
		s = str("query")
	case "TodoWrite":
		var todos []json.RawMessage
		if raw, ok := m["todos"]; ok {
			_ = json.Unmarshal(raw, &todos)
		}
		s = strconv.Itoa(len(todos)) + " todos"
	case "Task":
		s = str("description", "subagent_type")
	default:
		s = jqToStringRaw(input)
	}
	return strings.ReplaceAll(s, "\n", " ")
}

func claudeAskQBody(input json.RawMessage) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(input, &in)

	parts := make([]string, 0, len(in.Questions))
	for _, q := range in.Questions {
		var b strings.Builder
		b.WriteString("**❓ ")
		b.WriteString(q.Question)
		b.WriteString("**")
		if len(q.Options) > 0 {
			b.WriteString("\n\n")
			opts := make([]string, 0, len(q.Options))
			for _, o := range q.Options {
				line := "- **" + o.Label + "**"
				if o.Description != "" {
					line += " — " + o.Description
				}
				opts = append(opts, line)
			}
			b.WriteString(strings.Join(opts, "\n"))
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n\n")
}
