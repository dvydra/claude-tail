package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// reconstruct.go recovers a cloud-only session's transcript — one tracked by
// entire but no longer under ~/.claude on this machine — from its repo's local
// git checkpoint refs (refs/entire/checkpoints/**), so the search tree can still
// tail it. Only works when the repo is checked out locally.

var (
	repoDirsOnce sync.Once
	repoDirsMap  map[string]string
)

// localRepoDirs maps a repo (indexed both as "owner/repo" and bare "repo") to a
// local checkout's git top-level, discovered from the cwds recorded in ~/.claude
// sessions. Computed once per process (a stat/read + git call per project dir).
func localRepoDirs(home string) map[string]string {
	repoDirsOnce.Do(func() {
		repoDirsMap = map[string]string{}
		cache := map[string]string{}
		dirs, _ := filepath.Glob(filepath.Join(claudeProjectsDir(home), "*"))
		for _, d := range dirs {
			f := newestGlob(filepath.Join(d, "*.jsonl"))
			if f == "" {
				continue
			}
			_, _, _, cwd, _, _ := loadClaudeMeta(f)
			if cwd == "" {
				continue
			}
			repo := repoForCwd(cwd, home, cache)
			// Skip non-git dirs (repoForCwd falls back to a ~/path there).
			if repo == "" || strings.HasPrefix(repo, "~") || strings.HasPrefix(repo, "/") {
				continue
			}
			top := gitToplevel(cwd)
			if top == "" {
				continue
			}
			repoDirsMap[repo] = top
			if i := strings.LastIndex(repo, "/"); i >= 0 {
				repoDirsMap[repo[i+1:]] = top
			}
		}
	})
	return repoDirsMap
}

func gitToplevel(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// reconstructTranscript writes a cloud-only session's transcript (recovered from
// its repo's git checkpoint refs) to a temp file and returns the path. ok=false
// when the repo isn't checked out locally or the transcript can't be found.
func reconstructTranscript(home, sessionID, repo string) (string, bool) {
	dirs := localRepoDirs(home)
	dir := dirs[repo]
	if dir == "" {
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			dir = dirs[repo[i+1:]]
		}
	}
	if dir == "" {
		return "", false
	}
	commit, path := gitFindTranscript(dir, sessionID)
	if commit == "" {
		return "", false
	}
	data, err := exec.Command("git", "-C", dir, "show", commit+":"+path).Output()
	if err != nil || len(data) == 0 {
		return "", false
	}
	tmp := filepath.Join(os.TempDir(), "entire-tail-"+shortID(sessionID)+".jsonl")
	if os.WriteFile(tmp, data, 0o600) != nil {
		return "", false
	}
	return tmp, true
}

// gitFindTranscript locates the largest transcript.jsonl blob referencing
// sessionID across the repo's entire checkpoint refs (largest = most complete
// snapshot), returning its commit + in-tree path.
func gitFindTranscript(dir, sessionID string) (commit, path string) {
	refsOut, err := exec.Command("git", "-C", dir, "for-each-ref",
		"--format=%(objectname)", "refs/entire/checkpoints/").Output()
	if err != nil {
		return "", ""
	}
	var refs []string
	for r := range strings.SplitSeq(strings.TrimSpace(string(refsOut)), "\n") {
		if r != "" {
			refs = append(refs, r)
		}
	}
	if len(refs) == 0 {
		return "", ""
	}
	// One `git grep` over all refs → "<commit>:<path>" lines.
	args := append([]string{"-C", dir, "grep", "-l", "-F", sessionID}, refs...)
	out, _ := exec.Command("git", args...).Output()
	var best int64 = -1
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		c, p, ok := strings.Cut(line, ":")
		if !ok || !strings.HasSuffix(p, "transcript.jsonl") {
			continue
		}
		if sz := gitBlobSize(dir, c, p); sz > best {
			best, commit, path = sz, c, p
		}
	}
	return commit, path
}

func gitBlobSize(dir, commit, path string) int64 {
	out, err := exec.Command("git", "-C", dir, "cat-file", "-s", commit+":"+path).Output()
	if err != nil {
		return -1
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}
