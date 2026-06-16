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
		slug := strings.ReplaceAll(cwd, "/", "-")
		all := newestGlobAll(filepath.Join(home, ".claude", "projects", slug, "*.jsonl"))
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

// menuLine formats one picker row, matching the bash printf layout.
func menuLine(num int, s liveSession, pwd string, now int64) string {
	here := ""
	if s.Cwd == pwd {
		here = " (here)"
	}
	age := relAge(s.Mtime, now)
	prev := sessionPreview(s.Agent, s.Path)
	return fmt.Sprintf("  %2d) %-7s %-24s %9s  %s", num, s.Agent, cwShort(s.Cwd)+here, age, prev)
}

// parseChoice interprets a menu input line. quit signals q/Q; retry carries a
// validation message to reprint; otherwise choice is the 1-based selection.
func parseChoice(input string, n int) (choice int, quit bool, retry string) {
	switch {
	case input == "":
		return 1, false, ""
	case input == "q" || input == "Q":
		return 0, true, ""
	case strings.ContainsFunc(input, func(r rune) bool { return r < '0' || r > '9' }):
		return 0, false, "  not a number"
	}
	c, err := strconv.Atoi(input)
	if err != nil || c < 1 || c > n {
		return 0, false, "  out of range"
	}
	return c, false, ""
}

// runPicker resolves a live session for the given agents. On a pick it returns
// (path, agent, true); on no pick it returns ok=false so the caller falls back
// to normal discovery. Quitting the menu exits the process (like the bash `q`).
func runPicker(agents []Agent, home, pwd string, pick string, scanner *codexScanner, stderr io.Writer) (string, Agent, bool) {
	sessions := findActiveSessions(agents, home, time.Now().Unix(), scanner)
	ttyOK := ttyUsable()
	dec := decidePick(sessions, pick, ttyOK, pwd)

	switch dec.Action {
	case actNone:
		return "", "", false
	case actPick:
		if dec.Note != "" {
			fmt.Fprintln(stderr, "entire-tail: "+dec.Note)
		}
		s := sessions[dec.Index]
		return s.Path, s.Agent, true
	}

	// actMenu: draw the menu on the tty and read a choice.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		// No tty after all → fall back rather than block.
		return "", "", false
	}
	defer tty.Close()

	now := time.Now().Unix()
	fmt.Fprintln(tty)
	fmt.Fprintln(tty, "Active agent sessions:")
	fmt.Fprintln(tty)
	for i, s := range sessions {
		fmt.Fprintln(tty, menuLine(i+1, s, pwd, now))
	}
	fmt.Fprintln(tty)

	n := len(sessions)
	reader := bufio.NewReader(tty)
	for {
		fmt.Fprintf(tty, "Pick a session [1-%d] (Enter=1, q=quit): ", n)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(tty)
			os.Exit(0)
		}
		choice, quit, retry := parseChoice(strings.TrimRight(line, "\n"), n)
		if quit {
			fmt.Fprintln(tty)
			os.Exit(0)
		}
		if retry != "" {
			fmt.Fprintln(tty, retry)
			continue
		}
		s := sessions[choice-1]
		return s.Path, s.Agent, true
	}
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

// sessionPreview returns a one-line preview of a session's last human-readable
// message, whitespace-collapsed and truncated. It reads only the file tail.
func sessionPreview(agent Agent, path string) string {
	last := ""
	for _, line := range tailLines(path, 100) {
		if cand := previewCandidate(agent, line); strings.TrimSpace(cand) != "" {
			last = cand
		}
	}
	return collapsePreview(last)
}

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
