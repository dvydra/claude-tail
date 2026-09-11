package main

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// drift.go — the `d` card: has this session wandered off what was actually asked?
//
// Where the `i` card summarises a whole session, this compares two slices of it
// and judges the distance between them. Two things make that work where a plain
// summary doesn't:
//
//   - Only the HUMAN's turns are fed to the model. Intent lives in what the user
//     typed; the assistant's turns ARE the drift, and their code blocks dominate
//     the model's attention if left in (measured: a report that described the
//     steps of a shell snippet as the user's goal).
//   - The two slices are labelled. sampleTurns blurs head and tail into one
//     blob, which is right for "summarise this" and useless for "compare these".

var errDriftTooShort = errors.New("not enough of this session yet to judge drift")

const (
	driftOpenMarker = "=== OPENING ASK ==="
	driftNowMarker  = "=== WHAT'S HAPPENING NOW ==="

	driftOpenTurns = 3    // how many turns establish the original goal
	driftNowTurns  = 5    // how many establish the current one
	driftTurnCap   = 1000 // per-turn cap; humans front-load intent, so keep the head
	driftMinTurns  = 3    // below this there's no "then" and "now" to compare

	driftMaxHops = 4
)

const (
	verdictOnTrack  = "ON TRACK"
	verdictAdjacent = "ADJACENT"
	verdictDrifted  = "DRIFTED"
)

var driftVerdicts = map[string]bool{
	verdictOnTrack:  true,
	verdictAdjacent: true,
	verdictDrifted:  true,
}

type driftReport struct {
	Verdict string   `json:"verdict"`
	Asked   string   `json:"asked"`
	Doing   string   `json:"doing"`
	Hops    []string `json:"hops"`
}

// driftInstructions spells the verdicts out. Without explicit criteria the model
// answers DRIFTED for everything — measured across a corpus run, 10 sessions out
// of 10, including one whose opening and current work were the same word.
const driftInstructions = "Compare the OPENING ASK against WHAT'S HAPPENING NOW. " +
	"Both are the user's own messages, in order.\n" +
	"Choose the verdict by these rules, in order:\n" +
	"ON TRACK — the current work is the original goal, or a step needed to reach it. " +
	"If the two describe the same subject, this is always the answer.\n" +
	"ADJACENT — a different subject, but one that serves the original goal " +
	"(a blocker found on the way, a tool the goal needs).\n" +
	"DRIFTED — a different subject that does not serve the original goal at all.\n" +
	"Then name what was asked and what is happening, each in one line, using the " +
	"actual subjects — never 'the task' or 'the project'. Do not quote the section " +
	"headings back. Be blunt."

// driftSchema mirrors fmSummarySchema's shape: fm's decoder requires "title" and
// "x-order" alongside the JSON Schema keys. The verdict enum is honoured by the
// decoder, so the model cannot invent a fourth verdict.
const driftSchema = `{
  "title": "DriftReport",
  "type": "object",
  "additionalProperties": false,
  "required": ["verdict", "asked", "doing", "hops"],
  "x-order": ["verdict", "asked", "doing", "hops"],
  "properties": {
    "verdict": {"type": "string", "enum": ["ON TRACK", "ADJACENT", "DRIFTED"], "description": "how far the current work sits from the opening ask"},
    "asked":   {"type": "string", "description": "one line, at most 12 words: what the user originally asked for"},
    "doing":   {"type": "string", "description": "one line, at most 12 words: what is being worked on now"},
    "hops":    {"type": "array", "items": {"type": "string"}, "description": "3-4 short steps showing how the opening ask turned into the current work"}
  }
}`

var fenceRe = regexp.MustCompile("(?s)\n?```.*?```\n?")

// stripCodeFences removes fenced blocks, and anything after an unterminated
// fence — a half-pasted file shouldn't become the model's idea of the goal.
// Inline backticks are left alone; they read as prose.
func stripCodeFences(s string) string {
	s = fenceRe.ReplaceAllString(s, "\n")
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.Trim(s, "\n")
}

// driftSample renders the human turns as two labelled sections. Returns "" when
// the session is too short for the question to mean anything.
func driftSample(turns []turn) string {
	var human []string
	for _, t := range turns {
		if !t.user {
			continue
		}
		b := strings.TrimSpace(stripCodeFences(t.body))
		if b == "" {
			continue
		}
		if len(b) > driftTurnCap {
			b = b[:driftTurnCap]
		}
		human = append(human, b)
	}
	if len(human) < driftMinTurns {
		return ""
	}

	split := driftOpenTurns
	if len(human) <= driftOpenTurns+driftNowTurns {
		// Short session: every turn appears exactly once, on one side or the other.
		if split > len(human)-1 {
			split = len(human) - 1
		}
	}
	opening, now := human[:split], human[split:]
	if len(now) > driftNowTurns {
		now = now[len(now)-driftNowTurns:]
	}

	var b strings.Builder
	b.WriteString(driftOpenMarker + "\n")
	for _, t := range opening {
		b.WriteString("User: " + t + "\n")
	}
	b.WriteString("\n" + driftNowMarker + "\n")
	for _, t := range now {
		b.WriteString("User: " + t + "\n")
	}
	return b.String()
}

// goalNormRe keeps letters, digits and single spaces — everything a difference
// in wording would survive, and nothing a difference in typography would.
var goalNormRe = regexp.MustCompile(`[^a-z0-9]+`)

// sameGoal reports whether two goal lines say the same thing once case, spacing
// and punctuation are set aside. Deliberately exact rather than fuzzy: a loose
// match here would forgive real drift, which is the one mistake this feature
// cannot afford.
func sameGoal(asked, doing string) bool {
	norm := func(s string) string {
		return strings.Trim(goalNormRe.ReplaceAllString(strings.ToLower(s), " "), " ")
	}
	a, d := norm(asked), norm(doing)
	return a != "" && a == d
}

// parseDriftJSON pulls the report object out of fm's output, ignoring spinner and
// ANSI chrome. A report missing the two lines the card is built from is a
// failure, not a blank card.
func parseDriftJSON(out []byte) (driftReport, bool) {
	s := string(out)
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return driftReport{}, false
	}
	var r driftReport
	if json.Unmarshal([]byte(s[i:j+1]), &r) != nil {
		return driftReport{}, false
	}
	if strings.TrimSpace(r.Asked) == "" || strings.TrimSpace(r.Doing) == "" {
		return driftReport{}, false
	}
	if !driftVerdicts[r.Verdict] {
		r.Verdict = verdictAdjacent
	}
	// The model will call a session DRIFTED while describing its opening and its
	// current work in identical words (measured on the corpus). Telling it not to
	// doesn't hold; this does.
	if sameGoal(r.Asked, r.Doing) {
		r.Verdict = verdictOnTrack
	}
	if len(r.Hops) > driftMaxHops {
		r.Hops = r.Hops[:driftMaxHops]
	}
	return r, true
}

// driftCheck runs the comparison on-device. The error is for showing, not just
// for branching: a card that says why it is empty beats one that is silently
// blank. errDriftTooShort means the session has no "then" and "now" yet.
func driftCheck(path, home string) (driftReport, error) {
	text := driftSample(sessionTurns(path, home))
	if text == "" {
		return driftReport{}, errDriftTooShort
	}
	out, err := fmRunner(driftSchema, driftInstructions, text)
	if err != nil {
		return driftReport{}, err
	}
	r, ok := parseDriftJSON(out)
	if !ok {
		return driftReport{}, errUnreadable
	}
	return r, nil
}
