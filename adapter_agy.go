package main

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Antigravity (agy) CLI transcripts are line-per-step records. We render
// USER_INPUT and PLANNER_RESPONSE; the *_output step types map to TOOLRESULT;
// everything else (CONVERSATION_HISTORY, EPHEMERAL_MESSAGE, …) is ignored.

type agyEvent struct {
	Type      string          `json:"type"`
	CreatedAt string          `json:"created_at"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []agyToolCall   `json:"tool_calls"`
	StepIndex int             `json:"step_index"`
}

type agyToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// agyResultTypes are the tool-output step types, each 1:1 with a tool_call in
// the preceding PLANNER_RESPONSE.
var agyResultTypes = []string{
	"RUN_COMMAND", "VIEW_FILE", "LIST_DIRECTORY", "GREP_SEARCH", "VIEW_CODE_ITEM",
	"WRITE_TO_FILE", "REPLACE_FILE_CONTENT", "EDIT_FILE", "SEARCH_WEB",
	"READ_URL_CONTENT", "GENERIC",
}

var userEnvelopeRe = regexp.MustCompile(`(?s)<USER_REQUEST>\s*(.*?)\s*</USER_REQUEST>`)

func normalizeAgy(line []byte, loc *time.Location) []Record {
	var ev agyEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil
	}
	ts := formatTS(ev.CreatedAt, loc)
	content := rawAsString(ev.Content)

	switch {
	case ev.Type == "USER_INPUT":
		// One USER record per <USER_REQUEST> envelope; none if absent (matches
		// jq's capture(...;"g") | .m, which yields nothing when there's no
		// match, dropping the record).
		var out []Record
		for _, m := range userEnvelopeRe.FindAllStringSubmatch(content, -1) {
			out = append(out, Record{Kind: KindUser, Ts: ts, Body: m[1]})
		}
		return out
	case ev.Type == "PLANNER_RESPONSE":
		var out []Record
		if content != "" {
			out = append(out, Record{Kind: KindAssistant, Ts: ts, Body: content})
		}
		for _, tc := range ev.ToolCalls {
			out = append(out, Record{Kind: KindToolUse, Name: tc.Name, Summary: agyCallSummary(tc)})
		}
		return out
	case slices.Contains(agyResultTypes, ev.Type):
		return []Record{{Kind: KindToolResult, N: 1}}
	}
	return nil
}

func agyCallSummary(tc agyToolCall) string {
	// .args // {} — null/absent args become an empty object.
	argsRaw := tc.Args
	if len(argsRaw) == 0 || isJSONNull(argsRaw) {
		argsRaw = json.RawMessage("{}")
	}
	var a map[string]json.RawMessage
	_ = json.Unmarshal(argsRaw, &a)

	pick := func(keys ...string) string {
		if raw, ok := firstRaw(a, keys...); ok {
			return unqRaw(raw)
		}
		return ""
	}

	var s string
	switch tc.Name {
	case "run_command":
		s = pick("Command", "CommandLine", "cmd")
	case "view_file", "view_code_item":
		s = pick("AbsolutePath", "Path", "file_path")
	case "list_dir":
		s = pick("DirectoryPath", "Path")
	case "grep_search":
		s = pick("SearchPattern", "Query", "Pattern")
	case "write_to_file", "replace_file_content", "edit_file":
		s = pick("TargetFile", "AbsolutePath", "Path")
	case "search_web", "read_url_content":
		s = pick("Url", "URL", "Query")
	default:
		if raw, ok := firstRaw(a, "toolSummary", "toolAction"); ok {
			s = unqRaw(raw)
		} else {
			// $a | tostring → compact JSON of the args object (order preserved).
			s = unqString(jqToStringRaw(argsRaw))
		}
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// rawAsString decodes a JSON value as a string, returning "" for anything that
// isn't a JSON string (matching jq's `.content // ""` for the shapes agy emits).
func rawAsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}
