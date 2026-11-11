package main

import "os/exec"

// claudepid.go maps a running `claude` process to the session it is writing,
// from Claude Code's own registry (`<config-dir>/sessions/<pid>.json`, see
// live.go) instead of `lsof`. One `lsof -p` costs about 185k file-system calls
// on macOS, and the link daemon used to run one per claude on every iTerm focus
// change. Amp has no registry, so its paths still use `lsof`.

// claudeSessionOf reads every account's registry once and returns a lookup
// from a claude's pid to its transcript path. A pid with no entry falls back
// to an id on its command line (`--resume` / `--session-id`), read through
// argv. With neither, the answer is "": the newest transcript in the folder
// would be some other session.
func claudeSessionOf(home string, argv func(pid int) string) func(pid int) string {
	reg := map[int]liveSession{}
	for _, s := range collectLiveSessions(liveRoots(home), func(int) bool { return true }) {
		reg[s.PID] = s
	}
	return func(pid int) string {
		if s, ok := reg[pid]; ok {
			return liveTranscriptPath(home, s)
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
// entry behind. Nil without pgrep.
func claudeLiveCwds(home string) map[string]int {
	out, err := exec.Command("pgrep", "-x", "claude").Output()
	if err != nil {
		return nil
	}
	running := map[int]bool{}
	for _, pid := range parsePIDs(out) {
		running[pid] = true
	}
	return liveCwdCounts(collectLiveSessions(liveRoots(home), func(pid int) bool { return running[pid] }))
}
