package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// liveSession is one row in the picker: a session file belonging to a cwd that
// currently has a running agent process.
type liveSession struct {
	Mtime int64
	Agent Agent
	Path  string
	Cwd   string
}

func agentProcname(a Agent) string {
	switch a {
	case AgentClaude:
		return "claude"
	case AgentCodex:
		return "codex"
	case AgentAgy:
		return "agy"
	}
	return ""
}

// agentMaxAgeSecs caps how stale a matched session may be and still count as
// live. Codex often runs headless with no tailable rollout, so requiring a
// recent one avoids surfacing a stale leftover; claude/agy map reliably so any
// age is kept.
func agentMaxAgeSecs(a Agent) int64 {
	if a == AgentCodex {
		return 43200 // 12h
	}
	return 0
}

// relAge renders a relative age like "just now" / "3m ago" / "2h ago" /
// "4d ago" from epoch seconds.
func relAge(t, now int64) string {
	delta := max(now-t, 0)
	switch {
	case delta < 45:
		return "just now"
	case delta < 5400:
		return strconv.FormatInt((delta+30)/60, 10) + "m ago"
	case delta < 129600:
		return strconv.FormatInt((delta+1800)/3600, 10) + "h ago"
	default:
		return strconv.FormatInt((delta+43200)/86400, 10) + "d ago"
	}
}

func fileMtime(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.ModTime().Unix()
}

// parsePIDs extracts PIDs from `pgrep -x` output.
func parsePIDs(out []byte) []int {
	var pids []int
	for line := range strings.FieldsSeq(string(out)) {
		if n, err := strconv.Atoi(line); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// parseLsofCwd pulls the cwd from `lsof -Fn` output: the first line starting
// with 'n' (minus the 'n' prefix).
func parseLsofCwd(out []byte) string {
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return line[1:]
		}
	}
	return ""
}

type cwdCount struct {
	Cwd   string
	Count int
}

// pickerToolsAvailable reports whether pgrep and lsof are both on PATH; the
// picker is silently disabled without them (matching the bash version).
func pickerToolsAvailable() bool {
	for _, t := range []string{"pgrep", "lsof"} {
		if _, err := exec.LookPath(t); err != nil {
			return false
		}
	}
	return true
}

// activeCwdCounts returns each distinct cwd with a running process named
// procname, and how many such processes run there.
func activeCwdCounts(procname string) []cwdCount {
	out, err := exec.Command("pgrep", "-x", procname).Output()
	if err != nil {
		return nil
	}
	counts := map[string]int{}
	var order []string
	for _, pid := range parsePIDs(out) {
		lo, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
		if err != nil {
			continue
		}
		cwd := parseLsofCwd(lo)
		if cwd == "" {
			continue
		}
		if _, seen := counts[cwd]; !seen {
			order = append(order, cwd)
		}
		counts[cwd]++
	}
	sort.Strings(order)
	out2 := make([]cwdCount, 0, len(order))
	for _, c := range order {
		out2 = append(out2, cwdCount{Cwd: c, Count: counts[c]})
	}
	return out2
}

// sessionsForCwdAgent returns up to n newest session files for the given agent
// rooted in cwd.
func sessionsForCwdAgent(agent Agent, home, cwd string, n int, scanner *codexScanner) []string {
	switch agent {
	case AgentClaude:
		all := newestGlobAll(filepath.Join(home, ".claude", "projects", claudeSlug(cwd), "*.jsonl"))
		if len(all) > n {
			all = all[:n]
		}
		return all
	case AgentCodex:
		return scanner.sessionsForCwd(cwd, n)
	case AgentAgy:
		root := agyRoot(home)
		id := agyConversationID(root, cwd)
		if id == "" {
			return nil
		}
		if t := agyTranscriptPath(root, id); isFile(t) {
			return []string{t}
		}
	}
	return nil
}

// findActiveSessions returns one row per live session across the given agents,
// newest-first.
func findActiveSessions(agents []Agent, home string, now int64, scanner *codexScanner) []liveSession {
	if !pickerToolsAvailable() {
		return nil
	}
	var sessions []liveSession
	for _, agent := range agents {
		procname := agentProcname(agent)
		if procname == "" {
			continue
		}
		maxage := agentMaxAgeSecs(agent)
		for _, cc := range activeCwdCounts(procname) {
			for _, f := range sessionsForCwdAgent(agent, home, cc.Cwd, cc.Count, scanner) {
				if !isFile(f) {
					continue
				}
				mtime := fileMtime(f)
				if maxage > 0 && now-mtime > maxage {
					continue
				}
				sessions = append(sessions, liveSession{Mtime: mtime, Agent: agent, Path: f, Cwd: cc.Cwd})
			}
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Mtime > sessions[j].Mtime })
	return sessions
}

type pickAction int

const (
	actNone pickAction = iota // no pick → caller falls back to discovery
	actPick                   // auto-resolved to a single session
	actMenu                   // show the interactive menu
)

type pickDecision struct {
	Action pickAction
	Index  int
	Note   string // optional stderr note for actPick
}

// decidePick reproduces the auto/always picker policy as a pure function.
//
//	auto:   one live session in pwd → tail it (note if others exist); 2+ here, or
//	        none here but 2+ elsewhere → menu if a tty is usable, else fall back.
//	always: one session → use it; otherwise menu (when a tty is usable).
func decidePick(sessions []liveSession, pick string, ttyOK bool, pwd string) pickDecision {
	n := len(sessions)
	if n == 0 {
		return pickDecision{Action: actNone}
	}
	localN, localIdx := 0, -1
	for i, s := range sessions {
		if s.Cwd == pwd {
			localN++
			if localIdx < 0 {
				localIdx = i
			}
		}
	}
	if pick == "auto" {
		if localN == 1 {
			note := ""
			if n > 1 {
				note = fmt.Sprintf("tailing the live session here (%d other live; --pick to choose).", n-1)
			}
			return pickDecision{Action: actPick, Index: localIdx, Note: note}
		}
		if !(ttyOK && (localN >= 2 || n >= 2)) {
			return pickDecision{Action: actNone}
		}
	} else { // always
		if n == 1 || !ttyOK {
			return pickDecision{Action: actPick, Index: 0}
		}
	}
	return pickDecision{Action: actMenu}
}

// cwShort renders a cwd as its last two path components ("parent/base").
func cwShort(cwd string) string {
	parts := strings.Split(cwd, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return parts[len(parts)-1]
}

// runPicker resolves a session for the given agents. For Claude/auto on a tty it
// opens the interactive session tree (the leveled-up picker): `--pick` opens it
// unconditionally (browsing past sessions is the point), while `auto` keeps the
// zero-friction shortcut of tailing the single live session in $PWD and only
// opens the tree when the live set is ambiguous. On a pick it returns
// (path, agent, true); otherwise ok=false so the caller falls back to discovery.
// Quitting the tree exits the process (like the old menu's `q`).
func runPicker(agents []Agent, home, pwd, pick string, days int, theme Theme, scanner *codexScanner, stderr io.Writer) (string, Agent, bool) {
	treeCapable := ttyUsable() && slices.Contains(agents, AgentClaude)

	if pick == "always" && treeCapable {
		if p, ok := resolveTreeChoice(runClaudeTree(home, pwd, days, theme)); ok {
			return p, AgentClaude, true
		}
		return "", "", false // empty tree → discovery
	}

	// auto: the live-session shortcut still decides whether to prompt at all.
	sessions := findActiveSessions(agents, home, time.Now().Unix(), scanner)
	switch dec := decidePick(sessions, pick, ttyUsable(), pwd); dec.Action {
	case actNone:
		return "", "", false
	case actPick:
		if dec.Note != "" {
			fmt.Fprintln(stderr, "entire-tail: "+dec.Note)
		}
		s := sessions[dec.Index]
		return s.Path, s.Agent, true
	case actMenu:
		if treeCapable {
			if p, ok := resolveTreeChoice(runClaudeTree(home, pwd, days, theme)); ok {
				return p, AgentClaude, true
			}
		}
		// Non-Claude ambiguity (codex/agy) or an empty tree → tail the newest live.
		if len(sessions) > 0 {
			return sessions[0].Path, sessions[0].Agent, true
		}
		return "", "", false
	}
	return "", "", false
}

// resolveTreeChoice acts on a tree selection. It returns (path, true) when the
// caller should tail that session in-place. A resume selection launches the
// iTerm split and exits; a quit exits; anything else returns ok=false so the
// caller falls back to discovery. When iTerm isn't available a resume degrades
// to tailing in-place.
func resolveTreeChoice(c treeChoice) (string, bool) {
	switch c.Result {
	case treeChosen:
		return c.Path, true
	case treeResume:
		if itermAvailable() {
			if err := launchResumePair(c.Cwd, c.Path, c.ID); err != nil {
				fmt.Fprintln(os.Stderr, "entire-tail: "+err.Error())
			}
			os.Exit(0)
		}
		return c.Path, true // no iTerm → just tail it here
	case treeQuit:
		os.Exit(0)
	}
	return "", false
}

func ttyUsable() bool {
	if !isCharDevice(os.Stdout) {
		return false
	}
	if f, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0); err == nil {
		f.Close()
		return true
	}
	return false
}

// ── preview extraction ──────────────────────────────────────────────────────

var (
	agyPrevLeadRe  = regexp.MustCompile(`(?s)^.*<USER_REQUEST>\s*`)
	agyPrevTrailRe = regexp.MustCompile(`(?s)\s*</USER_REQUEST>.*$`)
	prevSpaceRe    = regexp.MustCompile(` +`)
)

// previewCandidate extracts a session's human-readable text from a single event
// line, per agent. It underpins the preview/snippet tests and is reused where a
// last-message preview is wanted.
func previewCandidate(agent Agent, line []byte) string {
	switch agent {
	case AgentClaude:
		var ev claudeEvent
		if json.Unmarshal(line, &ev) != nil || ev.Message == nil {
			return ""
		}
		switch ev.Type {
		case "user":
			var s string
			if json.Unmarshal(ev.Message.Content, &s) == nil {
				return s
			}
			return joinClaudeText(ev.Message.Content)
		case "assistant":
			return joinClaudeText(ev.Message.Content)
		}
	case AgentCodex:
		var ev codexEvent
		if json.Unmarshal(line, &ev) != nil || ev.Payload == nil {
			return ""
		}
		if ev.Type == "event_msg" && ev.Payload.Type == "agent_message" {
			return ev.Payload.Message
		}
		if ev.Type == "response_item" && ev.Payload.Type == "message" {
			var texts []string
			for _, c := range ev.Payload.Content {
				texts = append(texts, c.Text)
			}
			return strings.Join(texts, " ")
		}
	case AgentAgy:
		var ev agyEvent
		if json.Unmarshal(line, &ev) != nil {
			return ""
		}
		content := rawAsString(ev.Content)
		switch ev.Type {
		case "USER_INPUT":
			s := agyPrevLeadRe.ReplaceAllString(content, "")
			return agyPrevTrailRe.ReplaceAllString(s, "")
		case "PLANNER_RESPONSE":
			return content
		}
	}
	return ""
}

func joinClaudeText(content json.RawMessage) string {
	var blocks []claudeBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, " ")
}

// collapsePreview squeezes whitespace and truncates to 60 runes (57 + ellipsis).
func collapsePreview(text string) string {
	text = strings.NewReplacer("\n", " ", "\t", " ").Replace(text)
	text = prevSpaceRe.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > 60 {
		return string(r[:57]) + "…"
	}
	return text
}

// tailLines returns up to the last n lines of a file, reading only the tail.
func tailLines(path string, n int) [][]byte {
	const maxTail = 512 * 1024
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if fi.Size() > maxTail {
		start = fi.Size() - maxTail
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var lines [][]byte
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	// Drop a possibly-partial first line when we didn't start at BOF.
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
