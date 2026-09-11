package main

import (
	"regexp"
	"strings"
)

// synthetic.go — telling the human's turns apart from the records Claude Code
// injects on their behalf.
//
// A transcript's `user` records are not all the user. Skill bodies, slash-command
// envelopes, task notifications and local-command caveats all arrive with role
// "user", and the injected ones are often far longer than anything a human types
// — a skill body alone runs to several KB. Anything that reads the transcript to
// infer what the human WANTED (the drift check, the summary card) is swamped by
// them unless they're dropped first.
//
// Provenance travels on the record, not in the body: promptSource is "typed" or
// "queued" for a human and "system"/"sdk" for an injection, origin.kind is
// "human" or the injector's name, and isMeta marks scaffolding outright. So the
// test is exact rather than a guess at the text.

var syntheticOrigins = map[string]bool{
	"task-notification": true,
	"auto-continuation": true,
}

var syntheticSources = map[string]bool{
	"system": true,
	"sdk":    true,
}

// isSyntheticUser reports whether a `user` record was injected rather than typed.
// Absent provenance (older transcripts wrote neither field) means human: the
// fields appeared later, and treating their absence as synthetic would blank out
// every pre-existing session.
func isSyntheticUser(originKind, promptSource string, isMeta bool) bool {
	return isMeta || syntheticOrigins[originKind] || syntheticSources[promptSource]
}

var (
	cmdNameRe = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)
	cmdArgsRe = regexp.MustCompile(`<command-args>([^<]*)</command-args>`)
)

// unwrapCommand reduces a slash-command envelope to the line the human typed.
// The envelope IS their intent — `/fix-alert triage item 4` is a real turn — so
// it's kept and stripped of chrome rather than dropped. Anything that isn't an
// envelope passes through untouched.
func unwrapCommand(s string) string {
	m := cmdNameRe.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	out := strings.TrimSpace(m[1])
	if a := cmdArgsRe.FindStringSubmatch(s); a != nil {
		if args := strings.TrimSpace(a[1]); args != "" {
			out += " " + args
		}
	}
	return out
}
