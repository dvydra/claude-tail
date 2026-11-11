package main

import "os/exec"

// claudepid.go maps a running `claude` process to the session it is writing,
// from Claude Code's own registry (`<config-dir>/sessions/<pid>.json`, see
// live.go) instead of `lsof`. One `lsof -p` costs about 185k file-system calls
// on macOS, and the link daemon used to run one per claude on every iTerm focus
// change. Amp has no registry, so its paths still use `lsof`.

// claudeProcCwd reads a process's cwd. Only used when the registry is empty.
var claudeProcCwd = lsofCwd

// claudeSessionOf reads every account's registry once and returns a lookup
// from a claude's pid to its transcript path. Claude Code writes the entry
// before the first turn and rewrites sessionId on /clear and /resume.
//
// A pid with no entry falls back to an id on its command line (`--resume` /
// `--session-id`), read through argv. With neither, the answer is "": the
// newest transcript in the folder would be some other session.
//
// The registry is an undocumented Claude Code file. If it has no entries at
// all, assume it moved and go back to the old way: the process cwd from lsof,
// then the newest transcript in that folder.
func claudeSessionOf(home string, argv func(pid int) string) func(pid int) string {
	reg := map[int]liveSession{}
	for _, s := range collectLiveSessions(liveRoots(home), func(int) bool { return true }) {
		reg[s.PID] = s
	}
	return func(pid int) string {
		if s, ok := reg[pid]; ok {
			return liveTranscriptPath(home, s)
		}
		if len(reg) == 0 {
			return resolveClaudeSession(home, claudeProcCwd(pid), argv(pid))
		}
		// With no cwd, resolveClaudeSession only follows an id on argv.
		return resolveClaudeSession(home, "", argv(pid))
	}
}

// liveCwdCounts maps each running session's cwd to how many sessions run there.
func liveCwdCounts(sessions []liveSession) map[string]int {
	m := map[string]int{}
	for _, s := range sessions {
		if s.Cwd != "" {
			m[s.Cwd]++
		}
	}
	return m
}

// claudeLiveCwds returns each cwd with a running `claude` session mapped to its
// session count, from Claude Code's registry (live.go). A pid counts only while
// pgrep still finds a `claude` under it, since a SIGKILLed session leaves its
// entry behind. Nil without pgrep. Running claudes with no entries at all fall
// back to lsof, as in claudeSessionOf.
func claudeLiveCwds(home string) map[string]int {
	out, err := exec.Command("pgrep", "-x", "claude").Output()
	if err != nil {
		return nil
	}
	pids := parsePIDs(out)
	running := map[int]bool{}
	for _, pid := range pids {
		running[pid] = true
	}
	sessions := collectLiveSessions(liveRoots(home), func(pid int) bool { return running[pid] })
	if len(sessions) == 0 {
		for _, pid := range pids {
			sessions = append(sessions, liveSession{PID: pid, Cwd: claudeProcCwd(pid)})
		}
	}
	return liveCwdCounts(sessions)
}
