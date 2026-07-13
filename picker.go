package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

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

// cwShort renders a cwd as its last two path components ("parent/base").
func cwShort(cwd string) string {
	parts := strings.Split(cwd, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return parts[len(parts)-1]
}

// runPicker opens the interactive session tree (the default entry point) and
// acts on the selection. It returns (path, claude, true) when the caller should
// tail that session in the current pane; it returns ok=false when there's no tty
// or no Claude agent in scope, so the caller falls back to auto-discovery. A
// workspace selection launches the iTerm layout and exits; quitting exits.
func runPicker(agents []Agent, home, pwd string, days int, local, cloud bool, theme Theme) (string, Agent, bool) {
	if !ttyUsable() || !slices.Contains(agents, AgentClaude) {
		return "", "", false
	}
	if p, ok := resolveTreeChoice(home, runClaudeTree(home, pwd, days, local, cloud, theme)); ok {
		return p, AgentClaude, true
	}
	return "", "", false
}

// resolveTreeChoice acts on a tree selection. treeChosen → tail that session
// in-place (return path, true). treeWorkspace → open the iTerm workspace and
// exit, but only in a single-pane iTerm window; otherwise (already split, or no
// iTerm) just tail in-place, leaving any existing layout alone. treeQuit → exit.
// treeNone (empty tree / no tty) → ok=false so the caller auto-discovers.
//
// A cloud-only session (no local jsonl) is reconstructed from its repo's git
// checkpoint refs when that repo is checked out locally; otherwise it exits with
// a note rather than tailing something unrelated.
func resolveTreeChoice(home string, c treeChoice) (string, bool) {
	switch c.Result {
	case treeNewWorkspace:
		if itermAvailable() {
			if err := launchNewWorkspace(c.Cwd); err != nil {
				fmt.Fprintln(os.Stderr, "entire-tail: "+err.Error())
			}
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "entire-tail: a new-session workspace needs iTerm2 on macOS.")
		os.Exit(0)
	case treeChosen, treeWorkspace:
		if c.Path == "" {
			if tmp, ok := reconstructTranscript(home, c.ID, c.Repo); ok {
				return tmp, true // reconstructed from git; tail it in place
			}
			// Not local and not reconstructable — exit cleanly (not an error)
			// rather than falling back to tailing an unrelated $PWD session.
			fmt.Fprintln(os.Stderr, "entire-tail: session "+shortID(c.ID)+" isn't on this machine and its repo isn't checked out here — nothing to tail.")
			os.Exit(0)
		}
		if c.Result == treeWorkspace && itermAvailable() && itermSinglePane() {
			if err := launchWorkspace(sessionCwd(c.Path), c.ID, c.Path); err != nil {
				fmt.Fprintln(os.Stderr, "entire-tail: "+err.Error())
				return c.Path, true // launch failed → tail in-place instead
			}
			os.Exit(0)
		}
		return c.Path, true // tail in-place (already split / no iTerm / t key)
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
