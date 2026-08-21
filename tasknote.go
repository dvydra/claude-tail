package main

import (
	"regexp"
	"strings"
)

// Background-task plumbing, rendered as a marker instead of a USER turn.
//
// Claude Code injects a background task's progress (Monitor ticks, a task
// finishing) into the transcript as a `type:"user"` record whose promptSource is
// "system" — a message the user never typed. Rendered as a USER turn it is
// actively misleading: a full box header attributing to the human a payload of
// CI statuses, a task id, a temp output-file path, and an instruction addressed
// to the agent ("send a PushNotification"). Worse, the `<task-notification>`
// wrapper is eaten as an HTML tag by glamour, so the guts spill out bare.
//
// So we classify it and render one dim line. The payload's own summary is the
// only part written for a human ("Monitor "CI on #3304" stream ended"); the
// event body carries the signal worth keeping (which checks passed). Everything
// else — ids, paths, the agent-directed instruction — is dropped.

// taskNoteTag is the wrapper Claude Code writes around a background-task
// notification. Matched as a fallback for transcripts predating the `origin`
// field, where the payload is the only evidence.
const taskNoteTag = "<task-notification>"

// taskNoteMaxRunes caps the rendered marker. A settled CI run reports every
// check in one blob, which is a paragraph of text with no business occupying a
// marker line — the agent's own next turn says what it means. 120 matches the
// cap `full` mode puts on a flattened Bash command.
const taskNoteMaxRunes = 120

// taskNoteSummaryMaxRunes caps the summary HALF of the marker, so the event body
// is guaranteed room. The summary is the task's title, repeated verbatim on every
// tick of the same Monitor; the event is what changed. Budgeting the whole line
// as one string spends it on the title and elides the news, which was the case
// on the session that prompted this (a title long enough to leave one check
// visible before the ellipsis).
const taskNoteSummaryMaxRunes = 60

var (
	taskNoteSummaryRe = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	taskNoteEventRe   = regexp.MustCompile(`(?s)<event>(.*?)</event>`)
)

// isTaskNote reports whether a user record is a background-task notification
// rather than something the human said. origin.kind is the authoritative signal;
// the payload tag covers transcripts written before that field existed.
func isTaskNote(originKind, promptSource, body string) bool {
	if originKind == "task-notification" {
		return true
	}
	return promptSource == "system" && strings.Contains(body, taskNoteTag)
}

// taskNoteLine reduces a task-notification payload to one line: its summary,
// then the event body flattened behind a separator. Returns "" when there is
// nothing human-readable to show, which the caller renders as nothing at all.
func taskNoteLine(body string) string {
	summary := flattenTaskNote(firstGroup(taskNoteSummaryRe, body))
	event := flattenTaskNote(firstGroup(taskNoteEventRe, body))

	// Only trim the title when there's an event competing for the line; a
	// completion notice is nothing but its summary and can have the full width.
	if event != "" {
		summary = truncRunes(summary, taskNoteSummaryMaxRunes)
	}

	line := summary
	switch {
	case line == "":
		line = event
	case event != "":
		line += " · " + event
	}
	return truncRunes(line, taskNoteMaxRunes)
}

// firstGroup returns the first capture group of re in s, or "".
func firstGroup(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// flattenTaskNote folds a multi-line payload field onto one line: each line
// trimmed, empties dropped, joined with ", ". A Monitor event is a list of
// "check: status" lines, which reads fine inline.
func flattenTaskNote(s string) string {
	var parts []string
	for ln := range strings.SplitSeq(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			parts = append(parts, ln)
		}
	}
	return strings.Join(parts, ", ")
}

// truncRunes cuts s to at most n runes, marking the cut with an ellipsis.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n]), " ,") + "…"
}
