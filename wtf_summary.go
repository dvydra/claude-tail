package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type wtfSummary struct {
	Summary   string `json:"summary"`
	NeedsUser string `json:"needsUser"`
}

type wtfSummaryCache struct {
	InputHash string
	Value     wtfSummary
}

const wtfSummarySchema = `{
  "title": "WorkSummary",
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "needsUser"],
  "x-order": ["summary", "needsUser"],
  "properties": {
    "summary": {"type": "string", "description": "One concise sentence describing the work and current progress."},
    "needsUser": {"type": "string", "description": "One concise sentence naming a concrete decision or action Daniel owes, or an empty string."}
  }
}`

const wtfSummaryInstructions = "Summarize this coding-agent session. State the concrete work and current progress in one sentence. Set needsUser only for a concrete decision or action Daniel owes. Do not infer repository, branch, trail identity, ownership, or safety."

func wtfSummaryInput(path, home string) string {
	return transcriptText(path, home)
}

func summarizeWTFSession(s wtfSession, home string, cached wtfSummaryCache) (wtfSummary, wtfSummaryCache, error) {
	input := wtfSummaryInput(s.Transcript, home)
	digest := sha256.Sum256([]byte(input))
	inputHash := hex.EncodeToString(digest[:])
	if cached.InputHash == inputHash {
		return overrideWTFNeed(cached.Value, deterministicNeed(home, s)), cached, nil
	}

	fallback := wtfSummary{Summary: fallbackWTFSummary(s)}
	if strings.TrimSpace(input) == "" {
		cache := wtfSummaryCache{InputHash: inputHash, Value: fallback}
		return overrideWTFNeed(fallback, deterministicNeed(home, s)), cache, errNoTranscript
	}

	out, err := fmRunner(wtfSummarySchema, wtfSummaryInstructions, input)
	if err != nil {
		cache := wtfSummaryCache{InputHash: inputHash, Value: fallback}
		return overrideWTFNeed(fallback, deterministicNeed(home, s)), cache, err
	}
	var value wtfSummary
	if !parseSummaryObject(out, &value) || strings.TrimSpace(value.Summary) == "" {
		cache := wtfSummaryCache{InputHash: inputHash, Value: fallback}
		return overrideWTFNeed(fallback, deterministicNeed(home, s)), cache, errUnreadable
	}
	cache := wtfSummaryCache{InputHash: inputHash, Value: value}
	return overrideWTFNeed(value, deterministicNeed(home, s)), cache, nil
}

func overrideWTFNeed(summary wtfSummary, need string) wtfSummary {
	if need != "" {
		summary.NeedsUser = need
	}
	return summary
}

func deterministicNeed(home string, s wtfSession) string {
	if s.Agent != AgentClaude || s.ID == "" {
		return ""
	}
	marker, ok := readPendingMarker(home, s.ID)
	if !ok {
		return ""
	}
	switch marker.Kind {
	case "question":
		questions := claudeParseQuestions(marker.Payload)
		if len(questions) > 0 && strings.TrimSpace(questions[0].Question) != "" {
			return "Waiting for Daniel: " + strings.TrimSpace(questions[0].Question)
		}
	case "permission":
		if summary := strings.TrimSpace(permissionSummary(marker)); summary != "" {
			return "Waiting for Daniel to approve " + summary + "."
		}
	}
	return ""
}

func fallbackWTFSummary(s wtfSession) string {
	if name := strings.TrimSpace(s.Name); name != "" {
		return name
	}
	if id := strings.TrimSpace(s.ID); id != "" {
		return "Session " + id
	}
	return "Untitled session"
}
