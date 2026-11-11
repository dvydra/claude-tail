package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// trailstate.go — one change's state as entire.com shows it on the change page,
// read through the `entire` CLI: `trail show` for the project trail and the
// change's repository id, the change endpoint for agents and gates (its
// `mergeability.gates` is the same list the gates endpoint returns), and the
// reviews endpoint for findings. Shapes are recorded in the design doc
// (docs/superpowers/specs/2026-11-11-trail-status-design.md).

type trailSev int

// Ordered so the worst of several is their max.
const (
	trailSevNeutral trailSev = iota
	trailSevGood
	trailSevWarn
	trailSevBad
)

type trailGate struct {
	Key, Type string
	Outcome   string // "passed", "failed", or "" while not finished
	Last      string // the last finished outcome, carried across a rerun (settleGates)
	Detail    string
}

type trailAgent struct {
	Name, Verdict string // Verdict: Pass/Fail, Low/Med/High, N%, or "" before a reading
	RunStatus     string // latest_run.status
	Sev           trailSev
	History       []trailSev // oldest first
}

type trailFinding struct {
	ID, Severity, Author, File string
	Line                       int
	Body                       string // first paragraph
}

type changeState struct {
	Ref                trailRef
	ProjectNumber      int
	ProjectTitle       string
	ProjectURL         string
	URL, Title, Branch string
	Base, Status       string
	Agents             []trailAgent
	Gates              []trailGate // enabled gates only
	Findings           []trailFinding
	Mergeable          bool
	BehindBy           int
	Conflict           string
	FetchedAt          time.Time
}

// Running reports whether an agent run or a gate is still working, which is
// what moves polling to the fast cadence. A gate waiting on a person
// (approvals) or on findings is not "running": it can wait for days.
func (s changeState) Running() bool {
	for _, a := range s.Agents {
		switch a.RunStatus {
		case "queued", "running", "pending", "in_progress":
			return true
		}
	}
	for _, g := range s.Gates {
		if g.Outcome == "" && g.Key != "approvals" && g.Key != "findings" {
			return true
		}
	}
	return false
}

func (s changeState) Gate(key string) (trailGate, bool) {
	for _, g := range s.Gates {
		if g.Key == key {
			return g, true
		}
	}
	return trailGate{}, false
}

// ── JSON shapes (only the fields read) ──

type trailShowJSON struct {
	Project struct {
		Jurisdiction string `json:"jurisdiction"`
		Reference    struct {
			Forge   string `json:"forge"`
			Project string `json:"project"`
		} `json:"reference"`
	} `json:"project"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Changes []struct {
		RepositoryID string `json:"repositoryId"`
		Repository   string `json:"repository"`
		Branch       string `json:"branch"`
		Number       int    `json:"number"`
		Status       string `json:"status"`
		URL          string `json:"url"`
		Current      bool   `json:"current"`
	} `json:"changes"`
}

type trailReading struct {
	Bool *bool    `json:"boolean_value"`
	Pct  *float64 `json:"percent_value"`
	Size string   `json:"size_value"`
}

type trailChangeJSON struct {
	Title  string `json:"title"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Status string `json:"status"`
	Agents []struct {
		Agent struct {
			Name string `json:"name"`
		} `json:"agent"`
		Signal struct {
			ValueType string `json:"value_type"`
			Polarity  string `json:"polarity"`
		} `json:"signal"`
		Reading   *trailReading `json:"reading"`
		LatestRun *struct {
			Status string `json:"status"`
		} `json:"latest_run"`
		History []trailReading `json:"history"`
	} `json:"agents"`
	Mergeability struct {
		Mergeable bool   `json:"mergeable"`
		BehindBy  int    `json:"behind_by"`
		Conflict  string `json:"conflict_status"`
		Gates     []struct {
			Key     string          `json:"gate_key"`
			Type    string          `json:"gate_type"`
			State   string          `json:"state"`
			Status  string          `json:"status"`
			Outcome *string         `json:"outcome"`
			Value   json.RawMessage `json:"value"`
		} `json:"gates"`
	} `json:"mergeability"`
}

type trailCommentsJSON struct {
	Comments []struct {
		ID       string  `json:"id"`
		Author   string  `json:"actor_display_name"`
		Body     string  `json:"body"`
		Severity *string `json:"severity"`
		Status   string  `json:"status"`
		Location *struct {
			File string `json:"file_path"`
			Line *int   `json:"start_line"`
		} `json:"location"`
	} `json:"comments"`
}

// agentVerdict rates a reading the way entire.com does: yes/no is good when it
// matches the polarity; percent is good from 67 and a warning from 34
// (inverted for lower-is-better); size small/medium/large ranks the same way.
func agentVerdict(valueType, polarity string, r trailReading) (string, trailSev) {
	lower := polarity == "lower_is_better"
	switch valueType {
	case "boolean":
		if r.Bool == nil {
			return "", trailSevNeutral
		}
		if *r.Bool != lower {
			return "Pass", trailSevGood
		}
		return "Fail", trailSevBad
	case "percent":
		if r.Pct == nil {
			return "", trailSevNeutral
		}
		v := *r.Pct
		label := strconv.Itoa(int(v+0.5)) + "%"
		if lower {
			v = 100 - v
		}
		switch {
		case v >= 67:
			return label, trailSevGood
		case v >= 34:
			return label, trailSevWarn
		}
		return label, trailSevBad
	case "size":
		rank := map[string]int{"small": 0, "medium": 1, "large": 2}
		n, ok := rank[r.Size]
		if !ok {
			return "", trailSevNeutral
		}
		label := []string{"Low", "Med", "High"}[n]
		if !lower {
			n = 2 - n
		}
		return label, []trailSev{trailSevGood, trailSevWarn, trailSevBad}[n]
	}
	return "", trailSevNeutral
}

func firstParagraph(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// gateDetail is the short text under a gate's name in the modal.
func gateDetail(key string, outcome string, value json.RawMessage) string {
	var v struct {
		BehindBy *int   `json:"behind_by"`
		Approved *int   `json:"approved"`
		Required *int   `json:"required"`
		Branch   string `json:"branch"`
		Checks   []struct {
			Conclusion *string `json:"conclusion"`
		} `json:"checks"`
	}
	_ = json.Unmarshal(value, &v)
	switch {
	case key == "up_to_date" && v.BehindBy != nil:
		if *v.BehindBy == 0 {
			return "up to date"
		}
		return strconv.Itoa(*v.BehindBy) + " behind"
	case key == "approvals" && v.Approved != nil && v.Required != nil:
		return fmt.Sprintf("%d of %d approvals", *v.Approved, *v.Required)
	case len(v.Checks) > 0:
		ok := 0
		for _, c := range v.Checks {
			if c.Conclusion != nil && (*c.Conclusion == "success" || *c.Conclusion == "skipped" || *c.Conclusion == "neutral") {
				ok++
			}
		}
		d := fmt.Sprintf("%d of %d passed", ok, len(v.Checks))
		if v.Branch != "" {
			d = v.Branch + ": " + d
		}
		return d
	}
	if outcome == "" {
		return "running"
	}
	return outcome
}

// parseChangeState builds a change's state from the three responses, and
// refuses one whose show response doesn't contain the change asked for.
func parseChangeState(ref trailRef, show, change, comments []byte) (changeState, error) {
	var sh trailShowJSON
	if err := json.Unmarshal(show, &sh); err != nil {
		return changeState{}, err
	}
	// Every string here is other people's (or agents') text, written to the tty
	// raw: control bytes would be executed by the terminal, and a newline would
	// break the modal's box. trailsSafe turns each into a space.
	safe := trailsSafe
	s := changeState{Ref: ref, ProjectNumber: sh.Number, ProjectTitle: safe(sh.Title), ProjectURL: sh.URL}
	found := false
	for _, c := range sh.Changes {
		if strings.EqualFold(c.Repository, ref.Repo) && c.Number == ref.Number {
			s.URL, s.Branch, s.Status = c.URL, safe(c.Branch), safe(c.Status)
			found = true
			break
		}
	}
	if !found {
		return changeState{}, fmt.Errorf("trail show did not return %s", ref.Label())
	}
	var ch trailChangeJSON
	if err := json.Unmarshal(change, &ch); err != nil {
		return changeState{}, err
	}
	s.Title, s.Base = safe(ch.Title), safe(ch.Base)
	if ch.Branch != "" {
		s.Branch = safe(ch.Branch)
	}
	if ch.Status != "" {
		s.Status = safe(ch.Status)
	}
	for _, a := range ch.Agents {
		ag := trailAgent{Name: safe(a.Agent.Name)}
		if a.Reading != nil {
			ag.Verdict, ag.Sev = agentVerdict(a.Signal.ValueType, a.Signal.Polarity, *a.Reading)
		}
		if a.LatestRun != nil {
			ag.RunStatus = a.LatestRun.Status
		}
		for _, h := range a.History {
			_, sev := agentVerdict(a.Signal.ValueType, a.Signal.Polarity, h)
			ag.History = append(ag.History, sev)
		}
		s.Agents = append(s.Agents, ag)
	}
	m := ch.Mergeability
	s.Mergeable, s.BehindBy, s.Conflict = m.Mergeable, m.BehindBy, m.Conflict
	for _, g := range m.Gates {
		if g.State == "disabled" {
			continue
		}
		out := ""
		if g.Outcome != nil {
			out = *g.Outcome
		}
		s.Gates = append(s.Gates, trailGate{Key: safe(g.Key), Type: g.Type, Outcome: out, Detail: safe(gateDetail(g.Key, out, g.Value))})
	}
	var cm trailCommentsJSON
	if len(comments) > 0 {
		if err := json.Unmarshal(comments, &cm); err != nil {
			return changeState{}, err
		}
	}
	for _, c := range cm.Comments {
		if c.Status == "resolved" || c.Status == "dismissed" {
			continue
		}
		f := trailFinding{ID: c.ID, Author: safe(c.Author), Body: safe(firstParagraph(c.Body))}
		if c.Severity != nil {
			f.Severity = safe(*c.Severity)
		}
		if c.Location != nil {
			f.File = safe(c.Location.File)
			if c.Location.Line != nil {
				f.Line = *c.Location.Line
			}
		}
		s.Findings = append(s.Findings, f)
	}
	return s, nil
}

const (
	trailCallTimeout = 8 * time.Second
	trailFastPoll    = 10 * time.Second
	trailSlowPoll    = 60 * time.Second
)

func trailCall(ctx context.Context, run wtfCommandRunner, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, trailCallTimeout)
	defer cancel()
	return run(ctx, dir, name, args...)
}

// trailErrText is an error's message plus a failed command's stderr, which is
// where the CLI says why.
func trailErrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return err.Error()
}

func trailCachePath(home string, ref trailRef) string {
	return filepath.Join(home, ".claude", "entire-tail", "trailstatus", strings.ReplaceAll(ref.Key(), "/", "_")+".json")
}

// fetchChangeState reads a change, through a disk cache shared by every tail
// on the machine: a state younger than its poll interval is reused, so several
// tails watching one trail cost about one set of calls.
func fetchChangeState(ctx context.Context, run wtfCommandRunner, ref trailRef, home string, now time.Time) (changeState, error) {
	path := trailCachePath(home, ref)
	if b, err := os.ReadFile(path); err == nil {
		var c changeState
		if json.Unmarshal(b, &c) == nil && c.Ref == ref {
			ttl := trailSlowPoll
			if c.Running() {
				ttl = trailFastPoll
			}
			if age := now.Sub(c.FetchedAt); age >= 0 && age < ttl {
				return c, nil
			}
		}
	}
	show, err := trailCall(ctx, run, "", "entire", "trail", "show", ref.Label(), "--project", ref.ProjectFlag(), "--json")
	if err != nil {
		return changeState{}, errors.New(trailErrText(err))
	}
	var sh trailShowJSON
	if err := json.Unmarshal(show, &sh); err != nil {
		return changeState{}, err
	}
	repoID := ""
	for _, c := range sh.Changes {
		if strings.EqualFold(c.Repository, ref.Repo) && c.Number == ref.Number {
			repoID = c.RepositoryID
		}
	}
	if repoID == "" {
		return changeState{}, fmt.Errorf("trail show did not return %s", ref.Label())
	}
	j := sh.Project.Jurisdiction
	n := strconv.Itoa(ref.Number)
	change, err := trailCall(ctx, run, "", "entire", "api", "-j", j, "/api/v1/repos/"+repoID+"/trails/"+n)
	if err != nil {
		return changeState{}, errors.New(trailErrText(err))
	}
	comments, err := trailCall(ctx, run, "", "entire", "api", "-j", j, "/api/v1/trails/"+ref.Forge+"/"+ref.Project+"/"+ref.Repo+"/"+n+"/reviews/comments")
	if err != nil {
		return changeState{}, errors.New(trailErrText(err))
	}
	s, err := parseChangeState(ref, show, change, comments)
	if err != nil {
		return changeState{}, err
	}
	s.FetchedAt = now
	if b, err := json.Marshal(s); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		tmp := path + ".tmp" + strconv.Itoa(os.Getpid())
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, path)
		}
	}
	return s, nil
}

// fetchProjectChanges lists a project trail's changes that are still open.
func fetchProjectChanges(ctx context.Context, run wtfCommandRunner, ref trailRef) ([]trailRef, error) {
	b, err := trailCall(ctx, run, "", "entire", "trail", "show", strconv.Itoa(ref.Number), "--project", ref.ProjectFlag(), "--json")
	if err != nil {
		return nil, errors.New(trailErrText(err))
	}
	var sh trailShowJSON
	if err := json.Unmarshal(b, &sh); err != nil {
		return nil, err
	}
	if sh.Number != ref.Number {
		return nil, fmt.Errorf("trail show returned P%d for P%d", sh.Number, ref.Number)
	}
	var out []trailRef
	for _, c := range sh.Changes {
		if c.Status == "merged" || c.Status == "closed" {
			continue
		}
		out = append(out, trailRef{trailRefChange, ref.Forge, ref.Project, strings.ToLower(c.Repository), c.Number})
	}
	return out, nil
}

// fetchBranchChange finds the change for a repo branch. repo is
// forge/owner/repo. A branch with no trail is (zero, false, nil).
func fetchBranchChange(ctx context.Context, run wtfCommandRunner, repo, branch string) (trailRef, bool, error) {
	b, err := trailCall(ctx, run, "", "entire", "trail", "show", "--branch", branch, "--repo", repo, "--json")
	if err != nil {
		if strings.Contains(trailErrText(err), "no discoverable project trail") {
			return trailRef{}, false, nil
		}
		return trailRef{}, false, errors.New(trailErrText(err))
	}
	var sh trailShowJSON
	if err := json.Unmarshal(b, &sh); err != nil {
		return trailRef{}, false, err
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 3 {
		return trailRef{}, false, fmt.Errorf("bad repo %q", repo)
	}
	for _, c := range sh.Changes {
		if c.Current && strings.EqualFold(c.Repository, parts[2]) && (c.Branch == "" || c.Branch == branch) {
			forge, project := sh.Project.Reference.Forge, sh.Project.Reference.Project
			if forge == "" {
				forge, project = parts[0], parts[1]
			}
			return trailRef{trailRefChange, strings.ToLower(forge), strings.ToLower(project), strings.ToLower(c.Repository), c.Number}, true, nil
		}
	}
	return trailRef{}, false, nil
}
