package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
)

// agentpane.go finds the iTerm pane of the agent beside this tail, for Ctrl-T
// send mode (sendmode.go), and reads the cwd a session is writing from now,
// for the workspace launcher (workspaceCwd).

// agentPaneFor resolves the agent pane to type into, or "" when there isn't one
// we can name with confidence. pgrep/lsof stay optional here as they are
// everywhere else.
func agentPaneFor(home string, agent Agent, threadID, cur string) string {
	if !pickerToolsAvailable() {
		return ""
	}
	tab := itermTab(os.Getenv("ITERM_SESSION_ID"))
	if agent != AgentAmp {
		return tabAgentPane(tab, cur, claudeProcs(), func(p claudeProc) string {
			return resolveClaudeSession(home, lsofCwd(p.pid), psCommand(p.pid))
		})
	}
	return tabAgentPane(tab, threadID, ampProcs(), func(p claudeProc) string {
		files, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(p.pid), "-Fn").Output()
		if err != nil {
			return ""
		}
		return ampThreadIDFromLsof(files)
	})
}

// tabAgentPane picks the iTerm session to type into: an agent in OUR tab, and
// preferably the one writing the session we are tailing.
//
// The tab is the whole horizon, for the reason nearby.go spells out: an agent
// in another tab is someone else's work. Within the tab, matching the tailed
// session comes first because a tab can hold two agents; falling back to "the
// only one here" is adopt.go's exactly-one rule, and anything still ambiguous
// gives back "".
//
// sessionOf is injected so the placement logic is testable without processes.
func tabAgentPane(ownTab, cur string, procs []claudeProc, sessionOf func(claudeProc) string) string {
	if ownTab == "" {
		return ""
	}
	var inTab []claudeProc
	for _, p := range procs {
		if itermTab(p.itermID) == ownTab {
			inTab = append(inTab, p)
		}
	}
	// One agent in the tab is the answer without asking which session it
	// writes, and that question costs an lsof and a ps per process.
	if len(inTab) == 1 {
		return paneUUIDFromSessionID(inTab[0].itermID)
	}
	for _, p := range inTab {
		if cur != "" && sessionOf(p) == cur {
			return paneUUIDFromSessionID(p.itermID)
		}
	}
	return ""
}

// sessionCwdNow reads a transcript's bounded tail window and returns the cwd the
// session is writing from now. Empty when the file can't be read or holds no
// cwd at all.
//
// Distinct from entire.go's `sessionCwd`, which answers the same question from
// the transcript's HEAD: where the session STARTED.
func sessionCwdNow(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return tailCwd(splitLines(tailWindow(f)))
}

// tailCwd walks a tail window backward for the newest user/assistant cwd.
//
// Backward, and from the tail, for the reason tailMeta gives for the branch: a
// session can hop directories mid-flight, so the head's cwd names where it
// STARTED. The `"cwd"` pre-filter keeps the tail's bulk from being JSON-parsed,
// and the window's partial first line simply fails to parse and is skipped.
func tailCwd(lines [][]byte) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || !bytes.Contains(line, []byte(`"cwd"`)) {
			continue
		}
		var ev claudeMetaEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if (ev.Type == "user" || ev.Type == "assistant") && ev.Cwd != "" {
			return ev.Cwd
		}
	}
	return ""
}
