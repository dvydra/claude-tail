package main

import "time"

// Agent identifies which coding-agent log format a session is in.
type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
	AgentAgy    Agent = "agy"
)

// normalize lowers one raw jsonl line to zero or more Records for the given
// agent. A malformed or uninteresting line yields nil.
func normalize(agent Agent, line []byte, loc *time.Location) []Record {
	switch agent {
	case AgentClaude:
		return normalizeClaude(line, loc)
	case AgentCodex:
		return normalizeCodex(line, loc)
	case AgentAgy:
		return normalizeAgy(line, loc)
	}
	return nil
}
