package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ampMessageLines converts Amp's snapshot export into the line-oriented input
// consumed by normalize. The envelope carries snapshot-only context that is not
// present on an individual message, notably whether the final assistant turn is
// idle (and therefore genuinely done).
func ampMessageLines(export []byte) [][]byte {
	var snapshot struct {
		State json.RawMessage `json:"state"`
		Meta  struct {
			LastKnownAgentState struct {
				State     string `json:"state"`
				MessageID string `json:"messageID"`
			} `json:"lastKnownAgentState"`
		} `json:"meta"`
		Messages []ampMessage `json:"messages"`
	}
	if json.Unmarshal(export, &snapshot) != nil {
		return nil
	}
	idle := ampExportIdle(snapshot.State) || snapshot.Meta.LastKnownAgentState.State == "idle"
	doneAssistant := -1
	for i := range snapshot.Messages {
		if snapshot.Messages[i].Role == "assistant" && (snapshot.Meta.LastKnownAgentState.MessageID == "" || snapshot.Messages[i].ProtocolMessageID == snapshot.Meta.LastKnownAgentState.MessageID) {
			doneAssistant = i
		}
	}
	childIDs := make(map[string]string)
	for _, m := range snapshot.Messages {
		for _, b := range m.Content {
			if b.Type == "tool_result" {
				if id := ampChildID(b.Run); id != "" {
					childIDs[b.ToolUseID] = id
				}
			}
		}
	}
	lines := make([][]byte, 0, len(snapshot.Messages))
	for i, message := range snapshot.Messages {
		for j := range message.Content {
			b := &message.Content[j]
			if b.Type == "tool_use" && b.Name == "create_thread" {
				b.ChildThreadID = childIDs[b.ID]
			}
		}
		env := ampEnvelope{Agent: AgentAmp, Message: message, IdleFinal: idle && i == doneAssistant}
		if line, err := json.Marshal(env); err == nil {
			lines = append(lines, line)
		}
	}
	return lines
}

type ampEnvelope struct {
	Agent     Agent      `json:"agent,omitempty"`
	Message   ampMessage `json:"message"`
	IdleFinal bool       `json:"idleFinal,omitempty"`
}

type ampMessage struct {
	Role              string          `json:"role"`
	CreatedAt         string          `json:"createdAt"`
	ProtocolMessageID string          `json:"protocolMessageID"`
	State             ampMessageState `json:"state"`
	Content           []ampBlock      `json:"content"`
}

type ampMessageState struct {
	Type       string `json:"type"`
	StopReason string `json:"stopReason"`
}

type ampBlock struct {
	Type          string          `json:"type"`
	Text          string          `json:"text"`
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Input         json.RawMessage `json:"input"`
	ToolUseID     string          `json:"toolUseID"`
	Run           json.RawMessage `json:"run"`
	ChildThreadID string          `json:"childThreadID,omitempty"`
}

func normalizeAmp(line []byte, loc *time.Location) []Record {
	var env ampEnvelope
	if json.Unmarshal(line, &env) != nil || env.Message.Role == "" {
		return nil
	}
	m := env.Message
	ts := formatTS(m.CreatedAt, loc)
	// Amp user messages and tool-result messages are immutable and currently
	// omit state. Assistant messages carry state while streaming, so only those
	// need the completion gate.
	if m.Role == "assistant" && m.State.Type != "complete" {
		return nil
	}
	var out []Record
	switch m.Role {
	case "user":
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				out = append(out, Record{Kind: KindUser, Ts: ts, Body: markPastes(b.Text)})
			case "tool_result":
				out = append(out, Record{Kind: KindToolResult, N: 1, Result: ampToolResult(b.Run)})
			}
		}
	case "assistant":
		done := env.IdleFinal
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				out = append(out, Record{Kind: KindAssistant, Ts: ts, Body: b.Text, Done: done, MsgID: m.ProtocolMessageID})
				done = false
			case "tool_use":
				questions := ampParseQuestions(b.Input)
				switch b.Name {
				case "ask_user_choice", "AskUserQuestion":
					out = append(out, Record{Kind: KindQuestion, Ts: ts, QID: b.ID, Questions: questions})
				case "Task":
					desc, atype := claudeAgentSpawn(b.Input)
					out = append(out, Record{Kind: KindAgentSpawn, Ts: ts, AgentDesc: desc, AgentType: atype})
				case "create_thread":
					out = append(out, Record{Kind: KindAgentSpawn, Ts: ts, AgentDesc: ampThreadTitle(b.Input, b.ChildThreadID), AgentType: "thread"})
				default:
					if len(questions) > 0 {
						out = append(out, Record{Kind: KindQuestion, Ts: ts, QID: b.ID, Questions: questions})
					} else {
						out = append(out, Record{Kind: KindToolUse, Name: b.Name, Summary: ampToolSummary(b.Name, b.Input)})
					}
				}
			}
		}
	}
	return out
}

func ampExportIdle(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == "idle"
	}
	var state struct{ Type, Status string }
	_ = json.Unmarshal(raw, &state)
	return state.Type == "idle" || state.Status == "idle"
}

func ampParseQuestions(raw json.RawMessage) []QuestionItem {
	claude := claudeParseQuestions(raw)
	if len(claude) > 0 {
		return claude
	}
	var in struct {
		Question   string            `json:"question"`
		Options    []json.RawMessage `json:"options"`
		AllowOther bool              `json:"allowOther"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Question == "" {
		return nil
	}
	q := QuestionItem{Question: strings.ReplaceAll(in.Question, "\n", " ")}
	for _, option := range in.Options {
		var label string
		if json.Unmarshal(option, &label) != nil {
			var o struct{ Label, Description string }
			_ = json.Unmarshal(option, &o)
			label = o.Label
			if o.Description != "" {
				label += " — " + o.Description
			}
		}
		if label != "" {
			q.Options = append(q.Options, strings.ReplaceAll(label, "\n", " "))
		}
	}
	if in.AllowOther {
		q.Options = append(q.Options, "Other")
	}
	return []QuestionItem{q}
}

func ampThreadTitle(input json.RawMessage, childID string) string {
	var in struct{ Title, Description string }
	_ = json.Unmarshal(input, &in)
	title := in.Title
	if title == "" {
		title = in.Description
	}
	if childID != "" {
		if title != "" {
			title += " "
		}
		title += childID
	}
	return strings.ReplaceAll(title, "\n", " ")
}

func ampToolSummary(name string, input json.RawMessage) string {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(input, &m)
	str := func(keys ...string) string {
		if raw, ok := firstRaw(m, keys...); ok {
			return jqToStringRaw(raw)
		}
		return ""
	}

	var summary string
	switch name {
	case "shell_command":
		summary = str("command")
	case "apply_patch":
		for _, line := range strings.Split(str("patchText"), "\n") {
			const update = "*** Update File: "
			const add = "*** Add File: "
			if strings.HasPrefix(line, update) {
				summary = strings.TrimPrefix(line, update)
				break
			}
			if strings.HasPrefix(line, add) {
				summary = strings.TrimPrefix(line, add)
				break
			}
		}
	case "skill":
		summary = str("name")
	case "Task":
		summary = str("description")
	case "finder", "librarian":
		summary = str("query")
	case "read_web_page":
		summary = str("url")
	case "view_media":
		summary = str("path")
	case "web_search":
		summary = str("objective")
	case "create_thread":
		summary = str("title", "prompt")
	default:
		summary = claudeToolSummary(name, input)
	}
	return strings.ReplaceAll(summary, "\n", " ")
}

func ampChildID(run json.RawMessage) string {
	var v any
	if json.Unmarshal(run, &v) != nil {
		return ""
	}
	return findAmpThreadID(v)
}

func findAmpThreadID(v any) string {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "T-") {
			return x
		}
	case []any:
		for _, item := range x {
			if id := findAmpThreadID(item); id != "" {
				return id
			}
		}
	case map[string]any:
		for _, key := range []string{"threadID", "threadId", "id", "result", "output", "summary"} {
			if id := findAmpThreadID(x[key]); id != "" {
				return id
			}
		}
	}
	return ""
}

func ampToolResult(raw json.RawMessage) *ToolResult {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var run struct {
		Output   any             `json:"output"`
		Stdout   string          `json:"stdout"`
		Stderr   string          `json:"stderr"`
		Error    any             `json:"error"`
		Duration json.RawMessage `json:"duration"`
		Summary  string          `json:"summary"`
		Status   string          `json:"status"`
		Result   struct {
			Output   any  `json:"output"`
			Content  any  `json:"content"`
			Error    any  `json:"error"`
			ExitCode *int `json:"exitCode"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &run) != nil {
		return nil
	}
	parts := make([]string, 0, 4)
	add := func(v string) {
		if strings.TrimSpace(v) != "" {
			parts = append(parts, v)
		}
	}
	add(ampValueString(run.Output))
	add(ampValueString(run.Result.Output))
	add(ampContentString(run.Result.Content))
	add(run.Stdout)
	add(run.Stderr)
	add(ampValueString(run.Error))
	add(ampValueString(run.Result.Error))
	result := &ToolResult{Summary: run.Summary, Output: outputLines(strings.Join(parts, "\n"))}
	if result.Summary == "" && run.Result.ExitCode != nil {
		result.Summary = fmt.Sprintf("exit %d", *run.Result.ExitCode)
	}
	if result.Summary == "" && run.Status != "" && run.Status != "done" {
		result.Summary = run.Status
	}
	if result.Summary == "" && len(run.Duration) > 0 && string(run.Duration) != "null" {
		result.Summary = "duration " + strings.Trim(string(run.Duration), `"`)
	}
	if result.Summary == "" && len(result.Output) == 0 {
		return nil
	}
	return result
}

func ampContentString(v any) string {
	items, ok := v.([]any)
	if !ok {
		return ampValueString(v)
	}
	var parts []string
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			parts = append(parts, ampValueString(item))
			continue
		}
		if text, ok := m["text"].(string); ok {
			parts = append(parts, text)
		} else if content := ampValueString(m["content"]); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n")
}

func ampValueString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
