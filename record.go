package main

// Kind is the normalized event class shared by every agent adapter. Each agent
// lowers its raw jsonl events to a stream of Records, so all downstream
// formatting only needs to know about this shape, not per-agent event types.
type Kind string

const (
	KindUser       Kind = "USER"
	KindAssistant  Kind = "CLAUDE" // historical name; rendered as "AGENT"
	KindToolUse    Kind = "TOOLUSE"
	KindToolResult Kind = "TOOLRESULT"
	// KindAgentSpawn is a subagent (Agent/Task tool_use) launch — rendered as a
	// distinct marker in the main stream regardless of tool style, so the
	// orchestration is always visible.
	KindAgentSpawn Kind = "AGENTSPAWN"
	// KindQuestion is an AskUserQuestion tool_use — rendered as a prominent card
	// (and, live, rings the bell once) so a waiting prompt is noticed.
	KindQuestion Kind = "QUESTION"
)

// Record is one renderable unit. Which fields are populated depends on Kind:
//
//	USER/CLAUDE   → Ts, Body
//	TOOLUSE       → Name, Summary
//	TOOLRESULT    → N, and (full mode, when available) Result
//	AGENTSPAWN    → Ts, AgentDesc, AgentType
//	QUESTION      → Ts, QID, Questions
type Record struct {
	Kind    Kind
	Ts      string      // "YYYY-MM-DD HH:MM:SS" local time
	Body    string      // markdown
	Name    string      // tool name
	Summary string      // one-line tool input preview
	N       int         // tool_result count
	Result  *ToolResult // rich result detail for full mode (nil if unavailable)

	AgentDesc string // subagent task description (AGENTSPAWN)
	AgentType string // subagent type, e.g. "general-purpose" (AGENTSPAWN)

	QID       string         // AskUserQuestion tool_use id — bell dedup (QUESTION)
	Questions []QuestionItem // the pending question(s) (QUESTION)
}

// QuestionItem is one question within an AskUserQuestion call.
type QuestionItem struct {
	Header   string   // short tag, e.g. "Scope"
	Question string   // the question text
	Options  []string // option labels (description folded in when short)
}

// ToolResult is the detail rendered under the "⎿" in full mode. Populated from
// Claude's toolUseResult (structuredPatch / stdout / file …); nil for agents or
// tools without such data.
type ToolResult struct {
	Summary string     // one-liner, e.g. "Added 1 line, removed 1 line" / "Read 1304 lines"
	Diff    []DiffLine // edit/write hunks (empty otherwise)
	Output  []string   // a few output/preview lines (already trimmed); "" → none
}

// DiffLine is one line of a rendered diff hunk.
type DiffLine struct {
	Sign byte   // ' ' context | '-' removed | '+' added
	Num  int    // line number to display
	Text string // line content (without the leading sign)
}
