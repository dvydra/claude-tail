package main

import (
	"encoding/json"
	"reflect"
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
