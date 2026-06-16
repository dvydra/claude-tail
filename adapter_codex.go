package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Codex CLI rollouts wrap everything in {timestamp, type, payload}. Only
// response_item entries are rendered; event_msg (UI state), turn_context
// (boundary marker), and session_meta (used during discovery) are ignored.

type codexEvent struct {
	Type      string        `json:"type"`
	Timestamp string        `json:"timestamp"`
	Payload   *codexPayload `json:"payload"`
}

type codexPayload struct {
	Type      string         `json:"type"`
	Role      string         `json:"role"`
	Content   []codexContent `json:"content"`
	Name      string         `json:"name"`
	Arguments string         `json:"arguments"` // JSON string for function_call
	Cwd       string         `json:"cwd"`       // session_meta only
	Message   string         `json:"message"`   // event_msg/agent_message (preview only)
}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func normalizeCodex(line []byte, loc *time.Location) []Record {
	var ev codexEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Payload == nil {
		return nil
	}
	if ev.Type != "response_item" {
		return nil
	}
	ts := formatTS(ev.Timestamp, loc)
	p := ev.Payload

	switch p.Type {
	case "message":
		switch p.Role {
		case "user":
			// role=developer entries are the system prefill at session start —
			// not user input — and are intentionally dropped.
			return []Record{{Kind: KindUser, Ts: ts, Body: codexJoinText(p.Content, "input_text", "text")}}
		case "assistant":
			return []Record{{Kind: KindAssistant, Ts: ts, Body: codexJoinText(p.Content, "output_text", "text")}}
		}
		return nil
	case "function_call", "custom_tool_call", "local_shell_call":
		name := p.Name
		if name == "" {
			name = "tool"
		}
		return []Record{{Kind: KindToolUse, Name: name, Summary: codexToolSummary(p)}}
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
		return []Record{{Kind: KindToolResult, N: 1}}
	}
	return nil
}

func codexJoinText(content []codexContent, types ...string) string {
	var texts []string
	for _, c := range content {
		if slices.Contains(types, c.Type) {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func codexToolSummary(p *codexPayload) string {
	var args map[string]json.RawMessage
	parsed := json.Unmarshal([]byte(p.Arguments), &args) == nil
	if !parsed {
		args = map[string]json.RawMessage{}
	}

	str := func(key string) string {
		if raw, ok := firstRaw(args, key); ok {
			return jqToStringRaw(raw)
		}
		return ""
	}

	var s string
	switch p.Name {
	case "exec_command":
		s = str("cmd")
	case "apply_patch":
		s = firstNonEmptyLine(str("input"))
	case "update_plan":
		var plan []json.RawMessage
		if raw, ok := args["plan"]; ok {
			_ = json.Unmarshal(raw, &plan)
		}
		s = strconv.Itoa(len(plan)) + " steps"
	case "view_image":
		s = str("path")
	case "shell":
		var cmd []string
		if raw, ok := args["command"]; ok {
			_ = json.Unmarshal(raw, &cmd)
		}
		s = strings.Join(cmd, " ")
	default:
		if parsed {
			var buf bytes.Buffer
			if json.Compact(&buf, []byte(p.Arguments)) == nil {
				s = buf.String()
			} else {
				s = p.Arguments
			}
		} else {
			s = "{}"
		}
	}
	return strings.ReplaceAll(s, "\n", " ")
}

func firstNonEmptyLine(s string) string {
	for l := range strings.SplitSeq(s, "\n") {
		if len(l) > 0 {
			return l
		}
	}
	return ""
}
