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
	Type          string          `json:"type"`
	Timestamp     string          `json:"timestamp"`
	Message       *claudeMessage  `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"` // rich result detail (full mode)
	// IsSidechain marks a subagent's records, which are interleaved into the
	// main transcript. A subagent finishing is not the main agent finishing.
	IsSidechain bool `json:"isSidechain"`
	// PromptSource/Origin distinguish a message the human typed ("typed"/"queued",
	// origin.kind "human") from one Claude Code injected on their behalf
	// ("system") — see isTaskNote.
	PromptSource string `json:"promptSource"`
	Origin       struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	// IsMeta marks a record Claude Code wrote as scaffolding — an injected skill
	// body, a local-command caveat — never something the human typed.
	IsMeta bool `json:"isMeta"`
}

type claudeMessage struct {
	Content json.RawMessage `json:"content"`
	ID      string          `json:"id"`
	// StopReason is the API stop reason, repeated on every jsonl line of the
	// message. "tool_use" means the agent is continuing; anything terminal
	// means it just handed control back to the user.
	StopReason string `json:"stop_reason"`
}

// claudeTurnDone reports whether a stop reason ends the agent's turn. Claude
// Code writes "tool_use" for ~90% of assistant records (the agent keeps going)
// and "end_turn" for the rest; "stop_sequence"/"max_tokens"/"refusal" are rare
// but equally hand control back. An empty/unknown value is treated as
// not-done, so older transcripts (no stop_reason) simply render as before.
func claudeTurnDone(stopReason string) bool {
	switch stopReason {
	case "end_turn", "stop_sequence", "max_tokens", "refusal":
		return true
	}
	return false
}

type claudeBlock struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"` // tool_use id (used to dedup question bells)
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
			// A background-task notification is a `user` record the human never
			// typed; show it as a marker, not as their turn.
			if isTaskNote(ev.Origin.Kind, ev.PromptSource, s) {
				line := taskNoteLine(s)
				if line == "" {
					return nil
				}
				return []Record{{Kind: KindTaskNote, Ts: ts, Body: line}}
			}
			if isSyntheticUser(ev.Origin.Kind, ev.PromptSource, ev.IsMeta) {
				return nil
			}
			return []Record{{Kind: KindUser, Ts: ts, Body: unwrapCommand(s)}}
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
			return []Record{{Kind: KindToolResult, N: len(results), Result: parseClaudeToolResult(ev.ToolUseResult)}}
		}
		return nil

	case "assistant":
		var blocks []claudeBlock
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return nil
		}
		// The turn ends here only if the API said so AND this is the main agent
		// (a sidechain's end_turn is just a subagent finishing). Carried on the
		// text block, so the banner lands at the top of the closing message.
		done := !ev.IsSidechain && claudeTurnDone(ev.Message.StopReason)

		var out []Record
		for _, b := range blocks {
			switch b.Type {
			case "text":
				out = append(out, Record{Kind: KindAssistant, Ts: ts, Body: b.Text, Done: done, MsgID: ev.Message.ID})
				done = false // at most one banner per message
			case "tool_use":
				switch b.Name {
				case "AskUserQuestion":
					out = append(out, Record{Kind: KindQuestion, Ts: ts, QID: b.ID, Questions: claudeParseQuestions(b.Input)})
				case "Agent", "Task":
					desc, atype := claudeAgentSpawn(b.Input)
					out = append(out, Record{Kind: KindAgentSpawn, Ts: ts, AgentDesc: desc, AgentType: atype})
				default:
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

// claudeAgentSpawn pulls the task description + subagent type from an Agent/Task
// tool_use input.
func claudeAgentSpawn(input json.RawMessage) (desc, atype string) {
	var in struct {
		Description  string `json:"description"`
		SubagentType string `json:"subagent_type"`
	}
	_ = json.Unmarshal(input, &in)
	return strings.ReplaceAll(in.Description, "\n", " "), in.SubagentType
}

// claudeParseQuestions extracts the structured questions from an AskUserQuestion
// tool_use input. Each option's short description is folded onto its label so the
// card is self-explanatory without being verbose.
func claudeParseQuestions(input json.RawMessage) []QuestionItem {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(input, &in)

	out := make([]QuestionItem, 0, len(in.Questions))
	for _, q := range in.Questions {
		it := QuestionItem{Header: q.Header, Question: strings.ReplaceAll(q.Question, "\n", " ")}
		for _, o := range q.Options {
			label := o.Label
			if o.Description != "" {
				label += " — " + o.Description
			}
			it.Options = append(it.Options, strings.ReplaceAll(label, "\n", " "))
		}
		out = append(out, it)
	}
	return out
}
