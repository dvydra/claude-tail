package main

import (
	"encoding/json"
	"strings"
	"time"
)

// entire stores each tracked session's transcript in that repo's local git
// checkpoint refs, in its own normalized format: one JSON object per line with a
// top-level `content` array and `ts` (not Claude's nested `message.content` /
// `timestamp`). We reconstruct + tail those for cloud sessions no longer present
// under ~/.claude (see reconstruct.go). The shape is otherwise Claude-like —
// text blocks and `tool_use` blocks carry a name + input — so this adapter reuses
// claudeToolSummary / claudeAskQBody.

type entireEvent struct {
	Type    string          `json:"type"`
	Ts      string          `json:"ts"`
	Content json.RawMessage `json:"content"`
}

type entireBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func normalizeEntireTranscript(line []byte, loc *time.Location) []Record {
	var ev entireEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	var blocks []entireBlock
	if json.Unmarshal(ev.Content, &blocks) != nil {
		return nil
	}
	ts := formatTS(ev.Ts, loc)

	switch ev.Type {
	case "user":
		var texts []string
		for _, b := range blocks {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		body := strings.TrimSpace(strings.Join(texts, " "))
		if body == "" {
			return nil
		}
		return []Record{{Kind: KindUser, Ts: ts, Body: body}}

	case "assistant":
		var out []Record
		for _, b := range blocks {
			switch {
			case b.Name != "":
				switch b.Name {
				case "AskUserQuestion":
					out = append(out, Record{Kind: KindQuestion, Ts: ts, QID: b.ID, Questions: claudeParseQuestions(b.Input)})
				case "Agent", "Task":
					desc, atype := claudeAgentSpawn(b.Input)
					out = append(out, Record{Kind: KindAgentSpawn, Ts: ts, AgentDesc: desc, AgentType: atype})
				default:
					out = append(out, Record{Kind: KindToolUse, Name: b.Name, Summary: claudeToolSummary(b.Name, b.Input)})
				}
			case b.Text != "":
				out = append(out, Record{Kind: KindAssistant, Ts: ts, Body: b.Text})
			}
		}
		return out
	}
	return nil
}
