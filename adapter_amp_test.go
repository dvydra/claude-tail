package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ampLine(t *testing.T, message string, idleFinal bool) []byte {
	t.Helper()
	var m ampMessage
	if err := json.Unmarshal([]byte(message), &m); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ampEnvelope{Message: m, IdleFinal: idleFinal})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNormalizeAmpTextThinkingAndDone(t *testing.T) {
	line := ampLine(t, `{"role":"assistant","createdAt":"2026-09-25T10:00:01Z","protocolMessageID":"msg-1","state":{"type":"complete","stopReason":"end_turn"},"content":[{"type":"thinking","text":"hidden"},{"type":"text","text":"Done."}]}`, true)
	want := []Record{{Kind: KindAssistant, Ts: "2026-09-25 10:00:01", Body: "Done.", Done: true, MsgID: "msg-1"}}
	if got := normalizeAmp(line, time.UTC); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}

	line = ampLine(t, `{"role":"assistant","createdAt":"2026-09-25T10:00:01Z","protocolMessageID":"msg-1","state":{"type":"complete"},"content":[{"type":"text","text":"Done."}]}`, false)
	if got := normalizeAmp(line, time.UTC); len(got) != 1 || got[0].Done {
		t.Fatalf("non-idle message marked done: %+v", got)
	}
}

func TestAmpSuggestedRepliesStayOutOfDisplayAndYank(t *testing.T) {
	for _, body := range []string{
		"Investigation finished.\n\n<suggested-replies>\n**Draft a reply** to Nina\nAdd diagnostics\n</suggested-replies>",
		"Investigation finished.\n\n<suggested-replies>\nDraft a reply",
	} {
		text, _ := json.Marshal(body)
		line := ampLine(t, `{"role":"assistant","state":{"type":"complete"},"content":[{"type":"text","text":`+string(text)+`}]}`, true)
		recs := normalizeAmp(line, time.UTC)
		var buf strings.Builder
		r := newRendererWith(&buf, testTheme(), "dots", 0, identityRender)
		for _, rec := range recs {
			r.emit(rec)
		}
		yank, _ := r.yankText(1)
		if strings.Contains(buf.String(), "Draft a reply") || strings.Contains(yank, "Draft a reply") || !strings.Contains(yank, "Investigation finished.") {
			t.Fatalf("button text leaked: display=%q yank=%q", buf.String(), yank)
		}
	}
	// Literal markup in code and human messages is not UI metadata.
	body := "```xml\n<suggested-replies>\nExample\n</suggested-replies>\n```"
	text, _ := json.Marshal(body)
	for _, role := range []string{"assistant", "user"} {
		recs := normalizeAmp(ampLine(t, `{"role":"`+role+`","state":{"type":"complete"},"content":[{"type":"text","text":`+string(text)+`}]}`, false), time.UTC)
		if len(recs) != 1 || recs[0].Body != body {
			t.Fatalf("literal changed: %+v", recs)
		}
	}
}

func TestAmpAutomationIsNotAHumanTurn(t *testing.T) {
	body := "A background subagent you started has ended. This message comes from Amp, not from the user.\nID: TU-123\nDescription: Check credentials\nStatus: completed\nTo continue this subagent, call Task.\n\nResult:\nPrivate plumbing"
	text, _ := json.Marshal(body)
	for _, meta := range []string{`,"meta":{"fromAutomation":true}`, "", `,"meta":{"fromAutomation":false}`} {
		lines := ampMessageLines([]byte(`{"messages":[{"role":"user"` + meta + `,"content":[{"type":"text","text":` + string(text) + `}]}]}`))
		recs := normalizeAmp(lines[0], time.UTC)
		if strings.Contains(meta, "false") {
			if len(recs) != 1 || recs[0].Kind != KindUser || recs[0].Body != body {
				t.Fatalf("human quote lost: %+v", recs)
			}
			continue
		}
		out := renderRecords("dots", 0, false, recs...)
		if strings.Contains(out, "USER") || strings.Contains(out, "Private plumbing") || !strings.Contains(out, "Check credentials") || !strings.Contains(out, "completed") {
			t.Fatalf("bad automation rendering: %q", out)
		}
	}
}

func TestAmpQuestionAnswersVisibleInEveryToolStyle(t *testing.T) {
	q := ampLine(t, `{"role":"assistant","state":{"type":"complete"},"content":[{"type":"tool_use","id":"q1","name":"ask_user_choice","input":{"question":"Which transport?","options":["HTTPS","Entire"]}}]}`, false)
	for _, answer := range []string{"User selected option 2: Entire", "User selected: what do you mean?"} {
		text, _ := json.Marshal(answer)
		result := ampLine(t, `{"role":"user","content":[{"type":"tool_result","toolUseID":"q1","run":{"status":"done","result":{"output":`+string(text)+`}}}]}`, false)
		recs := append(normalizeAmp(q, time.UTC), normalizeAmp(result, time.UTC)...)
		for _, style := range []string{"dots", "hidden", "full"} {
			out := renderRecords(style, 0, false, recs...)
			if strings.Count(out, answer) != 1 || !strings.Contains(out, "USER") {
				t.Fatalf("%s lost answer: %q", style, out)
			}
			// A different tool printing identical words must remain ordinary output.
			other := strings.ReplaceAll(string(result), `"q1"`, `"shell1"`)
			out = renderRecords("hidden", 0, false, append(normalizeAmp(q, time.UTC), normalizeAmp([]byte(other), time.UTC)...)...)
			if strings.Contains(out, answer) {
				t.Fatalf("unrelated tool became a user: %q", out)
			}
		}
	}
}

func TestNormalizeAmpToolsQuestionsAndResults(t *testing.T) {
	line := ampLine(t, `{"role":"assistant","createdAt":"2026-09-25T10:00:01Z","state":{"type":"complete"},"content":[{"type":"tool_use","id":"q1","name":"ask_user_choice","input":{"question":"Ship it?","options":["Yes","No"],"allowOther":true}},{"type":"tool_use","name":"Task","input":{"description":"Check tests","subagent_type":"reviewer"}},{"type":"tool_use","name":"shell_command","input":{"command":"go test ./..."}}]}`, false)
	got := normalizeAmp(line, time.UTC)
	if len(got) != 3 || got[0].Kind != KindQuestion || got[1].Kind != KindAgentSpawn || got[2].Summary != "go test ./..." {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(got[0].Questions[0].Options, []string{"Yes", "No", "Other"}) {
		t.Fatalf("options %+v", got[0].Questions)
	}

	result := ampLine(t, `{"role":"user","createdAt":"2026-09-25T10:00:02Z","content":[{"type":"tool_result","toolUseID":"b1","run":{"result":{"output":"ok\npass","exitCode":0},"status":"done"}}]}`, false)
	r := normalizeAmp(result, time.UTC)[0]
	if r.Kind != KindToolResult || r.Result == nil || r.Result.Summary != "exit 0" || !reflect.DeepEqual(r.Result.Output, []string{"ok", "pass"}) {
		t.Fatalf("result %+v", r)
	}
}

func TestNormalizeAmpQuestionRequiresQuestionTool(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		kind        Kind
	}{
		{"read_thread", `{"question":"What remains to finish trail 3951?"}`, KindToolUse},
		{"other_tool", `{"questions":[{"question":"Which approach?","options":[{"label":"A"},{"label":"B"}]}]}`, KindToolUse},
		{"ask_user_choice", `{"question":"Ship it?","options":["Yes","No"]}`, KindQuestion},
		{"AskUserQuestion", `{"questions":[{"question":"Which approach?","options":[{"label":"A"},{"label":"B"}]}]}`, KindQuestion},
	} {
		t.Run(tt.name, func(t *testing.T) {
			line := ampLine(t, `{"role":"assistant","state":{"type":"complete"},"content":[{"type":"tool_use","id":"q1","name":"`+tt.name+`","input":`+tt.input+`}]}`, false)
			got := normalizeAmp(line, time.UTC)
			if len(got) != 1 || got[0].Kind != tt.kind {
				t.Fatalf("got %+v, want kind %v", got, tt.kind)
			}
			for _, style := range []string{"dots", "full", "hidden"} {
				out := renderRecords(style, 0, true, got...)
				wantQuestion := tt.kind == KindQuestion
				if strings.Contains(out, "WAITING FOR YOUR ANSWER") != wantQuestion || strings.Contains(out, "\a") != wantQuestion {
					t.Errorf("style %s: incorrect question alert: %q", style, out)
				}
			}
		})
	}
}

func TestAmpToolSummariesUseNativeInputs(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"apply_patch", `{"patchText":"*** Begin Patch\n*** Update File: main.go\n@@\n-old\n+new\n*** End Patch"}`, "main.go"},
		{"skill", `{"name":"zoekt"}`, "zoekt"},
		{"finder", `{"query":"Find the session source"}`, "Find the session source"},
		{"read_web_page", `{"url":"https://ampcode.com/docs"}`, "https://ampcode.com/docs"},
		{"view_media", `{"path":"/tmp/screen.png"}`, "/tmp/screen.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ampToolSummary(tt.name, json.RawMessage(tt.input)); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestAmpToolResultFailureAndContent(t *testing.T) {
	r := ampToolResult(json.RawMessage(`{"result":{"content":[{"type":"text","text":"permission denied"}],"exitCode":17},"status":"error"}`))
	if r == nil || r.Summary != "exit 17" || !reflect.DeepEqual(r.Output, []string{"permission denied"}) {
		t.Fatalf("result %+v", r)
	}
}

func TestAmpMessageLinesAddsIdleAndChildThread(t *testing.T) {
	export := []byte(`{"meta":{"lastKnownAgentState":{"state":"idle"}},"messages":[{"role":"assistant","createdAt":"2026-09-25T10:00:01Z","protocolMessageID":"m1","state":{"type":"complete"},"content":[{"type":"tool_use","id":"c1","name":"create_thread","input":{"title":"Research"}}]},{"role":"user","createdAt":"2026-09-25T10:00:02Z","state":{"type":"complete"},"content":[{"type":"tool_result","toolUseID":"c1","run":{"output":{"threadID":"T-child123"}}}]},{"role":"assistant","createdAt":"2026-09-25T10:00:03Z","protocolMessageID":"m2","state":{"type":"complete"},"content":[{"type":"text","text":"Ready"}]}]}`)
	lines := ampMessageLines(export)
	if len(lines) != 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	spawn := normalizeAmp(lines[0], time.UTC)[0]
	if spawn.Kind != KindAgentSpawn || spawn.AgentType != "thread" || spawn.AgentDesc != "Research T-child123" {
		t.Fatalf("spawn %+v", spawn)
	}
	last := normalizeAmp(lines[2], time.UTC)[0]
	if !last.Done {
		t.Fatalf("last assistant not done: %+v", last)
	}
}

func TestNormalizeAmpIncompleteAssistantIgnored(t *testing.T) {
	line := ampLine(t, `{"role":"assistant","createdAt":"2026-09-25T10:00:01Z","state":{"type":"streaming"},"content":[{"type":"text","text":"partial"}]}`, false)
	if got := normalizeAmp(line, time.UTC); got != nil {
		t.Fatalf("got %+v", got)
	}
}
