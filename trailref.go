package main

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// trailref.go — which trail a piece of transcript text names.
//
// A project trail (`P32`) spans repos; a change (`k3s-config/10`) is one branch
// in one repo. They are numbered separately, so the two kinds never share a key
// and are never passed where the other is expected. See the URL table in
// `entire agent-help trail`.

type trailRefKind int

const (
	trailRefChange trailRefKind = iota
	trailRefProject
)

type trailRef struct {
	Kind           trailRefKind
	Forge, Project string // "et", "dvydra"
	Repo           string // change only
	Number         int
}

// Key is unique across both kinds: a change is `forge/project/repo/n`, a
// project trail `forge/project/-/n`, and `-` is never a repo name.
func (r trailRef) Key() string {
	repo := r.Repo
	if r.Kind == trailRefProject {
		repo = "-"
	}
	return r.Forge + "/" + r.Project + "/" + repo + "/" + strconv.Itoa(r.Number)
}

// Label is what a person reads: `P32` or `k3s-config/10`.
func (r trailRef) Label() string {
	if r.Kind == trailRefProject {
		return "P" + strconv.Itoa(r.Number)
	}
	return r.Repo + "/" + strconv.Itoa(r.Number)
}

// ProjectFlag is the `--project` value: `et/dvydra`.
func (r trailRef) ProjectFlag() string { return r.Forge + "/" + r.Project }

func (r trailRef) zero() bool { return r == trailRef{} }

func trailPositive(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// parseTrailRefURL maps an entire.com/entire.io trail URL to a ref. Extra path
// segments after the trail (a tab like /files) are ignored.
func parseTrailRefURL(raw string) (trailRef, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return trailRef{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host != "entire.com" && host != "entire.io" {
		return trailRef{}, false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 5 {
		return trailRef{}, false
	}
	for i := range p {
		p[i] = strings.ToLower(p[i])
	}
	forge, project := p[0], p[1]
	if (forge != "gh" && forge != "et") || project == "" || project == "-" {
		return trailRef{}, false
	}
	change := func(repo, num string) (trailRef, bool) {
		n, ok := trailPositive(num)
		if !ok || repo == "" || repo == "-" {
			return trailRef{}, false
		}
		return trailRef{trailRefChange, forge, project, repo, n}, true
	}
	switch {
	case p[2] == "-" && p[3] == "trails":
		// <f>/<p>/-/trails/<P>[/changes/<repo>/<n>]
		if len(p) >= 8 && p[5] == "changes" {
			if _, ok := trailPositive(p[4]); !ok {
				return trailRef{}, false
			}
			return change(p[6], p[7])
		}
		n, ok := trailPositive(p[4])
		if !ok {
			return trailRef{}, false
		}
		return trailRef{Kind: trailRefProject, Forge: forge, Project: project, Number: n}, true
	case len(p) >= 8 && p[3] == "-" && p[4] == "trails" && p[6] == "changes":
		// <f>/<p>/<repo>/-/trails/<P>/changes/<n>
		if _, ok := trailPositive(p[5]); !ok {
			return trailRef{}, false
		}
		return change(p[2], p[7])
	case p[3] == "trails":
		// <f>/<p>/<repo>/trails/<n>
		return change(p[2], p[4])
	}
	return trailRef{}, false
}

// trailMention is one place the transcript named a trail. A command that gave a
// bare selector without --project leaves Ref zero and sets Partial; the tracker
// resolves it against Cwd's origin remote (IO, so not here).
type trailMention struct {
	Ref     trailRef
	Partial string
	Cwd     string
	At      time.Time
}

var (
	trailMentionURL = regexp.MustCompile(`https://entire\.(?:com|io)/[^\s"'\\<>)\]]+`)
	// The subcommands that act on an existing trail. `list` and `create` name
	// none.
	trailMentionCmd  = regexp.MustCompile(`entire trail (?:show|checkout|finding|watch|approvals|approve|resume|merge|comment|update|link)\s+([^\s"\\]+)([^"\n]*)`)
	trailMentionProj = regexp.MustCompile(`--project[ =]((?:gh|et)/[A-Za-z0-9_.-]+)`)
	trailMentionTS   = regexp.MustCompile(`"timestamp":"([^"]+)"`)
	trailMentionCwd  = regexp.MustCompile(`"cwd":"([^"]+)"`)
	trailSelChange   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[0-9]+$`)
	trailSelProject  = regexp.MustCompile(`^[0-9]+$`)
)

// trailMentionText is the part of a record that can name a trail: prose, where
// a URL or an `entire trail` command counts, and shell commands, where only an
// `entire trail` command does. A record with tool calls or thinking is narrowed
// to its text blocks and each call's command: a file the agent writes (a test
// fixture, a plan, a heredoc) is full of trail URLs nobody is working on, and
// thinking is never shown. Any other record is all prose.
func trailMentionText(line []byte, s string) (prose, cmds string) {
	if !strings.Contains(s, `"tool_use"`) && !strings.Contains(s, `"thinking"`) {
		return s, ""
	}
	var rec struct {
		Message struct {
			Content []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Input struct {
					Command string `json:"command"`
					Cmd     string `json:"cmd"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return "", ""
	}
	var p, c strings.Builder
	for _, b := range rec.Message.Content {
		switch b.Type {
		case "text":
			p.WriteString(b.Text + "\n")
		case "tool_use":
			c.WriteString(b.Input.Command + "\n" + b.Input.Cmd + "\n")
		}
	}
	return p.String(), c.String()
}

// trailMentionsInLine finds trail references in one raw JSONL line. It runs on
// every line the tail reads, so it uses regexes rather than a JSON decode.
func trailMentionsInLine(line []byte) []trailMention {
	if !strings.Contains(string(line), "entire") {
		return nil
	}
	s := string(line)
	// A tool result is what the session READ (a `trail show --json` lists every
	// change in the project), not a trail it is working on. Claude Code writes
	// each tool result as its own user record, so the whole line is skipped.
	if strings.Contains(s, `"type":"tool_result"`) {
		return nil
	}
	// The timestamp and cwd come from the whole record; the text searched for
	// trails may be narrower (trailMentionText).
	rec := s
	s, cmds := trailMentionText(line, s)
	var at time.Time
	if m := trailMentionTS.FindStringSubmatch(rec); m != nil {
		at, _ = time.Parse(time.RFC3339Nano, m[1])
	}
	cwd := ""
	if m := trailMentionCwd.FindStringSubmatch(rec); m != nil {
		cwd = m[1]
	}
	seen := map[string]bool{}
	var out []trailMention
	add := func(m trailMention) {
		k := m.Partial
		if !m.Ref.zero() {
			k = m.Ref.Key()
		}
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		m.Cwd, m.At = cwd, at
		out = append(out, m)
	}
	for _, u := range trailMentionURL.FindAllString(s, -1) {
		if r, ok := parseTrailRefURL(u); ok {
			add(trailMention{Ref: r})
		}
	}
	for _, m := range trailMentionCmd.FindAllStringSubmatch(s+"\n"+cmds, -1) {
		sel, rest := m[1], m[2]
		var forge, project string
		if p := trailMentionProj.FindStringSubmatch(rest); p != nil {
			fp := strings.SplitN(strings.ToLower(p[1]), "/", 2)
			forge, project = fp[0], fp[1]
		}
		switch {
		case trailSelChange.MatchString(sel):
			i := strings.LastIndexByte(sel, '/')
			n, _ := strconv.Atoi(sel[i+1:])
			if forge == "" {
				add(trailMention{Partial: sel})
			} else if n > 0 {
				add(trailMention{Ref: trailRef{trailRefChange, forge, project, strings.ToLower(sel[:i]), n}})
			}
		case trailSelProject.MatchString(sel):
			n, _ := strconv.Atoi(sel)
			if forge == "" {
				add(trailMention{Partial: sel})
			} else if n > 0 {
				add(trailMention{Ref: trailRef{Kind: trailRefProject, Forge: forge, Project: project, Number: n}})
			}
		}
	}
	return out
}
