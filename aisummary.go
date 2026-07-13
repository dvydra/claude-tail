package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"
)

// aisummary.go generates the `i` card's summary via Apple's built-in Foundation
// Models CLI (`fm`, /usr/bin/fm on macOS 26+), using guided generation
// (`fm respond --schema`) for structured output on the on-device model. All
// best-effort: no fm / unavailable model → the card is metadata-only.

type aiSummary struct {
	Headline  string   `json:"headline"`
	Summary   string   `json:"summary"`
	KeyPoints []string `json:"keyPoints"`
	Outcome   string   `json:"outcome"`
}

const fmInstructions = "Summarize this AI coding-agent session transcript accurately and concisely."

// fmSummarySchema is fm's generation schema (as emitted by `fm schema object …`)
// that forces the model to fill exactly these fields. fm's decoder requires the
// "title" and "x-order" keys — a bare JSON Schema without them is rejected with
// "The data couldn't be read because it is missing."
const fmSummarySchema = `{
  "title": "SessionSummary",
  "type": "object",
  "additionalProperties": false,
  "required": ["headline", "summary", "keyPoints", "outcome"],
  "x-order": ["headline", "summary", "keyPoints", "outcome"],
  "properties": {
    "headline":  {"type": "string", "description": "6-10 word title"},
    "summary":   {"type": "string", "description": "2-3 sentence summary of what the user wanted and what happened"},
    "keyPoints": {"type": "array", "items": {"type": "string"}, "description": "3-5 key actions or decisions"},
    "outcome":   {"type": "string", "description": "one-sentence outcome or status"}
  }
}`

func fmAvailable() bool {
	_, err := exec.LookPath("fm")
	return err == nil
}

// aiSummarize summarizes transcript text via fm's on-device model. ok=false when
// fm is missing, the model is unavailable, or the output can't be parsed.
func aiSummarize(text string) (aiSummary, bool) {
	if !fmAvailable() || strings.TrimSpace(text) == "" {
		return aiSummary{}, false
	}
	schema, err := os.CreateTemp("", "entire-tail-*.schema.json")
	if err != nil {
		return aiSummary{}, false
	}
	defer os.Remove(schema.Name())
	schema.WriteString(fmSummarySchema)
	schema.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "fm", "respond",
		"--model", "system", "--no-stream", "--schema", schema.Name(), "-i", fmInstructions)
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil {
		return aiSummary{}, false
	}
	return parseSummaryJSON(out)
}

// parseSummaryJSON pulls the JSON object out of fm's output (ignoring any spinner
// or ANSI chrome) and decodes it.
func parseSummaryJSON(out []byte) (aiSummary, bool) {
	s := string(out)
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return aiSummary{}, false
	}
	var sum aiSummary
	if json.Unmarshal([]byte(s[i:j+1]), &sum) != nil || strings.TrimSpace(sum.Summary) == "" {
		return aiSummary{}, false
	}
	return sum, true
}

// summaryBudget caps the characters fed to the model (~4k-token context for the
// on-device model); a longer session is sampled head + tail.
const summaryBudget = 10000

// transcriptText extracts a session's user/assistant turns as plain
// "User:/Assistant:" text (system/tool noise dropped), then samples it to fit the
// model: short sessions in full; long ones as the opening turns (the goal) plus
// the recent turns (the work/outcome), with the middle elided — so the summary
// reflects the whole session, not just its tail.
func transcriptText(path, home string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	agent := detectAgentForFile(home, path)
	var turns []string
	for _, l := range splitLines(data) {
		for _, rec := range normalize(agent, l, time.Local) {
			switch rec.Kind {
			case KindUser:
				turns = append(turns, "User: "+rec.Body)
			case KindAssistant:
				turns = append(turns, "Assistant: "+rec.Body)
			}
		}
	}
	return sampleTurns(turns, summaryBudget)
}

// sampleTurns joins turns whole when they fit in budget, else takes ~40% from the
// front and ~60% from the back (weighting the recent end), eliding the middle.
func sampleTurns(turns []string, budget int) string {
	full := strings.Join(turns, "\n")
	if len(full) <= budget {
		return full
	}
	headCap, tailCap := budget*2/5, budget*3/5
	var head []string
	used, hi := 0, 0
	for ; hi < len(turns); hi++ {
		if used+len(turns[hi]) > headCap {
			break
		}
		head = append(head, turns[hi])
		used += len(turns[hi])
	}
	var tail []string
	used = 0
	for ti := len(turns) - 1; ti >= hi; ti-- {
		if used+len(turns[ti]) > tailCap {
			break
		}
		tail = append([]string{turns[ti]}, tail...)
		used += len(turns[ti])
	}
	if len(head) == 0 && len(tail) == 0 { // one giant turn — take the recent budget of it
		return full[len(full)-budget:]
	}
	return strings.Join(head, "\n") + "\n\n…[middle of a long session elided]…\n\n" + strings.Join(tail, "\n")
}
