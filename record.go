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
)

// Record is one renderable unit. Which fields are populated depends on Kind:
//
//	USER/CLAUDE   → Ts, Body
//	TOOLUSE       → Name, Summary
//	TOOLRESULT    → N, and (full mode, when available) Result
type Record struct {
	Kind    Kind
	Ts      string      // "YYYY-MM-DD HH:MM:SS" local time
	Body    string      // markdown
	Name    string      // tool name
	Summary string      // one-line tool input preview
	N       int         // tool_result count
	Result  *ToolResult // rich result detail for full mode (nil if unavailable)
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
