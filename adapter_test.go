package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

var utc = time.UTC

func TestNormalizeClaudeUserString(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2026-06-15T05:00:00Z","message":{"content":"hello there"}}`)
	got := normalize(AgentClaude, line, utc)
	want := []Record{{Kind: KindUser, Ts: "2026-06-15 05:00:00", Body: "hello there"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestNormalizeClaudeAssistantTextAndTool(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-06-15T05:00:00Z","message":{"content":[
		{"type":"text","text":"thinking out loud"},
		{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}},
		{"type":"tool_use","name":"Read","input":{"file_path":"/x/y.go"}}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	want := []Record{
		{Kind: KindAssistant, Ts: "2026-06-15 05:00:00", Body: "thinking out loud"},
		{Kind: KindToolUse, Name: "Bash", Summary: "ls -la"},
		{Kind: KindToolUse, Name: "Read", Summary: "/x/y.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestNormalizeClaudeToolResultCount(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2026-06-15T05:00:00Z","message":{"content":[
		{"type":"tool_result","content":"output a"},
		{"type":"tool_result","content":"output b"}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	want := []Record{{Kind: KindToolResult, N: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestNormalizeClaudeAskQuestion(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-06-15T05:00:00Z","message":{"content":[
		{"type":"tool_use","id":"toolu_q1","name":"AskUserQuestion","input":{"questions":[
			{"question":"Pick one","header":"Scope","options":[
				{"label":"A","description":"first"},
				{"label":"B"}
			]}
		]}}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	if len(got) != 1 || got[0].Kind != KindQuestion || got[0].QID != "toolu_q1" {
		t.Fatalf("got %+v, want one KindQuestion with QID toolu_q1", got)
	}
	q := got[0].Questions
	if len(q) != 1 || q[0].Header != "Scope" || q[0].Question != "Pick one" {
		t.Fatalf("question = %+v", q)
	}
	if len(q[0].Options) != 2 || q[0].Options[0] != "A — first" || q[0].Options[1] != "B" {
		t.Errorf("options = %+v", q[0].Options)
	}
}

func TestNormalizeClaudeAgentSpawn(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-06-15T05:00:00Z","message":{"content":[
		{"type":"tool_use","name":"Agent","input":{"description":"Map the data model","subagent_type":"general-purpose","prompt":"go do it"}}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	want := []Record{{Kind: KindAgentSpawn, Ts: "2026-06-15 05:00:00", AgentDesc: "Map the data model", AgentType: "general-purpose"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestNormalizeClaudeAnswerUnwrap(t *testing.T) {
	content := "User has answered your questions: chose A. You can now continue with the user's answers in mind."
	line := []byte(`{"type":"user","timestamp":"2026-06-15T05:00:00Z","message":{"content":[
		{"type":"tool_result","content":` + jsonString(content) + `}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	want := []Record{{Kind: KindUser, Ts: "2026-06-15 05:00:00", Body: "chose A"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestClaudeToolSummary(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"Bash", `{"command":"echo hi"}`, "echo hi"},
		{"Grep", `{"pattern":"foo","path":"/src"}`, "foo in /src"},
		{"Grep", `{"pattern":"foo"}`, "foo"},
		{"TodoWrite", `{"todos":[{},{},{}]}`, "3 todos"},
		{"Task", `{"description":"do a thing"}`, "do a thing"},
		{"Task", `{"subagent_type":"general"}`, "general"},
		{"WebSearch", `{"query":"golang"}`, "golang"},
		{"Bash", `{"command":"a\nb"}`, "a b"},         // newlines → spaces
		{"Unknown", `{"z":1,"a":2}`, `{"z":1,"a":2}`}, // else → input tostring, order preserved
	}
	for _, c := range cases {
		if got := claudeToolSummary(c.name, []byte(c.input)); got != c.want {
			t.Errorf("claudeToolSummary(%q,%s) = %q, want %q", c.name, c.input, got, c.want)
		}
	}
}

func TestNormalizeCodex(t *testing.T) {
	user := []byte(`{"type":"response_item","timestamp":"2026-06-15T05:00:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`)
	if got := normalize(AgentCodex, user, utc); len(got) != 1 || got[0].Kind != KindUser || got[0].Body != "hi" {
		t.Errorf("user: got %+v", got)
	}
	asst := []byte(`{"type":"response_item","timestamp":"2026-06-15T05:00:00Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"yo"}]}}`)
	if got := normalize(AgentCodex, asst, utc); len(got) != 1 || got[0].Kind != KindAssistant || got[0].Body != "yo" {
		t.Errorf("asst: got %+v", got)
	}
	dev := []byte(`{"type":"response_item","timestamp":"2026-06-15T05:00:00Z","payload":{"type":"message","role":"developer","content":[{"type":"text","text":"prefill"}]}}`)
	if got := normalize(AgentCodex, dev, utc); got != nil {
		t.Errorf("developer prefill should be dropped, got %+v", got)
	}
	out := []byte(`{"type":"response_item","timestamp":"2026-06-15T05:00:00Z","payload":{"type":"function_call_output"}}`)
	if got := normalize(AgentCodex, out, utc); len(got) != 1 || got[0].Kind != KindToolResult || got[0].N != 1 {
		t.Errorf("output: got %+v", got)
	}
	em := []byte(`{"type":"event_msg","timestamp":"2026-06-15T05:00:00Z","payload":{"type":"task_complete"}}`)
	if got := normalize(AgentCodex, em, utc); got != nil {
		t.Errorf("event_msg should be dropped, got %+v", got)
	}
}

func TestCodexToolSummary(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"exec_command", `{"cmd":"ls -la"}`, "ls -la"},
		{"apply_patch", `{"input":"\n\nfirst real line\nsecond"}`, "first real line"},
		{"update_plan", `{"plan":[{},{},{}]}`, "3 steps"},
		{"view_image", `{"path":"/x.png"}`, "/x.png"},
		{"shell", `{"command":["bash","-c","echo hi"]}`, "bash -c echo hi"},
		{"other", `{"k":"v","a":1}`, `{"k":"v","a":1}`}, // else → args tostring, order preserved
		{"exec_command", `not json`, ""},                // unparseable args → {}
	}
	for _, c := range cases {
		p := &codexPayload{Name: c.name, Arguments: c.args}
		if got := codexToolSummary(p); got != c.want {
			t.Errorf("codexToolSummary(%q,%s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

func TestNormalizeAgyUserEnvelope(t *testing.T) {
	line := []byte(`{"type":"USER_INPUT","created_at":"2026-06-15T05:00:00Z","content":"<ADDITIONAL_METADATA>junk</ADDITIONAL_METADATA><USER_REQUEST>\n  the real ask  \n</USER_REQUEST>"}`)
	got := normalize(AgentAgy, line, utc)
	want := []Record{{Kind: KindUser, Ts: "2026-06-15 05:00:00", Body: "the real ask"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestNormalizeAgyUserNoEnvelopeDropped(t *testing.T) {
	line := []byte(`{"type":"USER_INPUT","created_at":"2026-06-15T05:00:00Z","content":"no envelope here"}`)
	if got := normalize(AgentAgy, line, utc); got != nil {
		t.Errorf("USER_INPUT without <USER_REQUEST> must be dropped, got %+v", got)
	}
}

func TestNormalizeAgyMultipleEnvelopes(t *testing.T) {
	line := []byte(`{"type":"USER_INPUT","created_at":"2026-06-15T05:00:00Z","content":"<USER_REQUEST>one</USER_REQUEST> mid <USER_REQUEST>two</USER_REQUEST>"}`)
	got := normalize(AgentAgy, line, utc)
	if len(got) != 2 || got[0].Body != "one" || got[1].Body != "two" {
		t.Errorf("expected two USER records, got %+v", got)
	}
}

func TestNormalizeAgyPlannerResponse(t *testing.T) {
	line := []byte(`{"type":"PLANNER_RESPONSE","created_at":"2026-06-15T05:00:00Z","content":"doing the thing","tool_calls":[
		{"name":"run_command","args":{"Command":"ls"}},
		{"name":"view_file","args":{"AbsolutePath":"/a/b"}}
	]}`)
	got := normalize(AgentAgy, line, utc)
	want := []Record{
		{Kind: KindAssistant, Ts: "2026-06-15 05:00:00", Body: "doing the thing"},
		{Kind: KindToolUse, Name: "run_command", Summary: "ls"},
		{Kind: KindToolUse, Name: "view_file", Summary: "/a/b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestNormalizeAgyPlannerResponseNoContent(t *testing.T) {
	// Empty content → no CLAUDE record, only the tool call.
	line := []byte(`{"type":"PLANNER_RESPONSE","created_at":"2026-06-15T05:00:00Z","content":"","tool_calls":[{"name":"run_command","args":{"Command":"ls"}}]}`)
	got := normalize(AgentAgy, line, utc)
	want := []Record{{Kind: KindToolUse, Name: "run_command", Summary: "ls"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestNormalizeAgyResultTypes(t *testing.T) {
	for _, typ := range agyResultTypes {
		line := []byte(`{"type":"` + typ + `","created_at":"2026-06-15T05:00:00Z"}`)
		got := normalize(AgentAgy, line, utc)
		if len(got) != 1 || got[0].Kind != KindToolResult || got[0].N != 1 {
			t.Errorf("type %s: got %+v", typ, got)
		}
	}
}

func TestAgyCallSummaryUnwrapsDoubleEncoded(t *testing.T) {
	// agy args arrive pre-stringified; unq unwraps one level.
	tc := agyToolCall{Name: "run_command", Args: []byte(`{"Command":"\"git status\""}`)}
	if got := agyCallSummary(tc); got != "git status" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeMalformedLine(t *testing.T) {
	for _, agent := range []Agent{AgentClaude, AgentCodex, AgentAgy} {
		if got := normalize(agent, []byte(`{not json`), utc); got != nil {
			t.Errorf("%s: malformed line should yield nil, got %+v", agent, got)
		}
	}
}

// jsonString quotes a Go string as a JSON string literal for embedding in test
// fixtures.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Claude's "I'm done" signal: message.stop_reason. "end_turn" ends the turn and
// hands control back; "tool_use" means the agent is still working.
func TestNormalizeClaudeDoneOnEndTurn(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-06-15T05:00:00Z","message":{"id":"msg_1","stop_reason":"end_turn","content":[
		{"type":"text","text":"all set"}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	want := []Record{{Kind: KindAssistant, Ts: "2026-06-15 05:00:00", Body: "all set", Done: true, MsgID: "msg_1"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestNormalizeClaudeNotDoneOnToolUse(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-06-15T05:00:00Z","message":{"id":"msg_2","stop_reason":"tool_use","content":[
		{"type":"text","text":"looking"}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	if len(got) != 1 || got[0].Done {
		t.Errorf("tool_use must not be a done signal: %+v", got)
	}
}

// A subagent's end_turn is the SUBAGENT finishing, not the main agent.
func TestNormalizeClaudeSidechainEndTurnNotDone(t *testing.T) {
	line := []byte(`{"type":"assistant","isSidechain":true,"timestamp":"2026-06-15T05:00:00Z","message":{"id":"msg_3","stop_reason":"end_turn","content":[
		{"type":"text","text":"subagent report"}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	if len(got) != 1 || got[0].Done {
		t.Errorf("sidechain end_turn must not be a done signal: %+v", got)
	}
}

// One message, several text blocks → at most one banner.
func TestNormalizeClaudeDoneOnlyFirstTextBlock(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-06-15T05:00:00Z","message":{"id":"msg_4","stop_reason":"end_turn","content":[
		{"type":"text","text":"first"},
		{"type":"text","text":"second"}
	]}}`)
	got := normalize(AgentClaude, line, utc)
	if len(got) != 2 || !got[0].Done || got[1].Done {
		t.Errorf("only the first text block should carry Done: %+v", got)
	}
}

// Transcripts predating stop_reason (and other agents) keep rendering as before.
func TestNormalizeClaudeNoStopReasonNotDone(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-06-15T05:00:00Z","message":{"content":[{"type":"text","text":"hi"}]}}`)
	got := normalize(AgentClaude, line, utc)
	if len(got) != 1 || got[0].Done {
		t.Errorf("missing stop_reason must not be a done signal: %+v", got)
	}
}

func TestClaudeTurnDone(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   bool
	}{
		{"end_turn", true}, {"stop_sequence", true}, {"max_tokens", true}, {"refusal", true},
		{"tool_use", false}, {"", false}, {"something_new", false},
	} {
		if got := claudeTurnDone(tc.reason); got != tc.want {
			t.Errorf("claudeTurnDone(%q) = %v want %v", tc.reason, got, tc.want)
		}
	}
}
