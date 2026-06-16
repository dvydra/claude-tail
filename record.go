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
//	TOOLRESULT    → N
type Record struct {
	Kind    Kind
	Ts      string // "YYYY-MM-DD HH:MM:SS" local time
	Body    string // markdown
	Name    string // tool name
	Summary string // one-line tool input preview
	N       int    // tool_result count
}
