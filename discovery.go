package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// codexScanCap mirrors the bash `head -200`: only the 200 newest codex rollouts
// are content-scanned to recover their cwd.
const codexScanCap = 200

// claudeSlugRe matches every rune Claude Code rewrites to '-' when it names a
// project folder: anything outside [A-Za-z0-9].
var claudeSlugRe = regexp.MustCompile(`[^A-Za-z0-9]`)

// claudeSlug encodes an absolute cwd the way Claude Code names its project
// folder under ~/.claude/projects: every non-alphanumeric rune (slash, dot,
// underscore, space, …) becomes '-'. The bash original — and the first Go
// port — replaced only '/', so any cwd containing a '.' (e.g. "…/entire.io")
// resolved to a folder name that never existed and silently fell back to the
// global-newest session.
func claudeSlug(path string) string {
	return claudeSlugRe.ReplaceAllString(path, "-")
}

func claudeProjectsDir(home string) string {
	return filepath.Join(home, ".claude", "projects")
}

// findSessionClaude resolves the newest Claude session for pwd. It prefers an
// exact-cwd match (lossless), then the nearest session in the same directory
// tree (a parent or child of pwd — see claudeTreeDir), and finally the global
// newest across all projects.
func findSessionClaude(home, pwd string) string {
	root := claudeProjectsDir(home)
	if f := newestGlob(filepath.Join(root, claudeSlug(pwd), "*.jsonl")); f != "" {
		return f
	}
	if d := claudeTreeDir(root, home, pwd); d != "" {
		if f := newestGlob(filepath.Join(d, "*.jsonl")); f != "" {
			return f
		}
	}
	return newestGlob(filepath.Join(root, "*", "*.jsonl"))
}

// claudeAncestors yields pwd's ancestor directories from nearest (its parent)
// up to — but excluding — home. The home bound keeps a stray session in ~ or
// /Users from being treated as part of pwd's project tree; broad ancestors like
// ~/src remain eligible only as a last resort because nearer ones are tried
// first.
func claudeAncestors(home, pwd string) []string {
	bound := filepath.Clean(home) + string(os.PathSeparator)
	var out []string
	for dir := filepath.Dir(pwd); strings.HasPrefix(dir, bound); dir = filepath.Dir(dir) {
		out = append(out, dir)
	}
	return out
}

// claudeTreeDir returns the project folder of the nearest cwd in pwd's tree
// that has sessions: the deepest ancestor of pwd with a folder, else the
// shallowest descendant. Ancestors win over descendants because they're matched
// losslessly from pwd's real path, whereas descendants are only inferred from a
// slug prefix. Returns "" when nothing in the tree has a session.
func claudeTreeDir(root, home, pwd string) string {
	for _, anc := range claudeAncestors(home, pwd) {
		if d := filepath.Join(root, claudeSlug(anc)); dirHasSessions(d) {
			return d
		}
	}
	// Descendants: a deeper cwd's folder is slug(pwd) extended by a path
	// segment, i.e. its base name starts with slug(pwd)+"-". Nearest ≈ shortest
	// base name; the newest jsonl breaks ties.
	prefix := claudeSlug(pwd) + "-"
	best, bestMtime := "", int64(-1)
	matches, _ := filepath.Glob(filepath.Join(root, "*"))
	for _, d := range matches {
		base := filepath.Base(d)
		if !strings.HasPrefix(base, prefix) {
			continue
		}
		f := newestGlob(filepath.Join(d, "*.jsonl"))
		if f == "" {
			continue
		}
		mt := fileMtimeNano(f)
		switch {
		case best == "", len(base) < len(filepath.Base(best)):
			best, bestMtime = d, mt
		case len(base) == len(filepath.Base(best)) && mt > bestMtime:
			best, bestMtime = d, mt
		}
	}
	return best
}

// claudeAncestorDir returns the real ancestor directory of pwd whose project
// folder is base, or "" if base isn't an ancestor's folder. Used to explain a
// same-tree fallback in the cwd-mismatch note.
func claudeAncestorDir(home, pwd, base string) string {
	for _, anc := range claudeAncestors(home, pwd) {
		if claudeSlug(anc) == base {
			return anc
		}
	}
	return ""
}

func dirHasSessions(dir string) bool {
	return newestGlob(filepath.Join(dir, "*.jsonl")) != ""
}

// findSessionAgy resolves the agy transcript for pwd via the cwd→id cache,
// falling back to the global newest transcript.
func findSessionAgy(home, pwd string) string {
	root := agyRoot(home)
	if !isDir(filepath.Join(root, "brain")) {
		return ""
	}
	if id := agyConversationID(root, pwd); id != "" {
		if t := agyTranscriptPath(root, id); isFile(t) {
			return t
		}
	}
	return newestGlob(agyTranscriptPath(root, "*"))
}

func agyRoot(home string) string { return filepath.Join(home, ".gemini", "antigravity-cli") }

// agyTranscriptPath is the transcript location for a conversation id under root
// (id may be "*" to form a glob).
func agyTranscriptPath(root, id string) string {
	return filepath.Join(root, "brain", id, ".system_generated", "logs", "transcript.jsonl")
}

// agyConversationID looks up the conversation id mapped to cwd in the agy cache.
func agyConversationID(root, cwd string) string {
	b, err := os.ReadFile(filepath.Join(root, "cache", "last_conversations.json"))
	if err != nil {
		return ""
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	return m[cwd]
}

// codexScanner reads codex rollouts' cwd lazily and once. Codex paths don't
// encode the cwd; the first line (session_meta) carries payload.cwd. The bash
// version re-walked the rollouts per live cwd and again in the fallback; here a
// single memoized scan serves discovery and the picker.
type codexScanner struct {
	home     string
	rollouts []string // newest-first, all
	cwdCache map[string]string
}

func newCodexScanner(home string) *codexScanner {
	return &codexScanner{home: home, cwdCache: map[string]string{}}
}

func (s *codexScanner) list() []string {
	if s.rollouts == nil {
		s.rollouts = newestGlobAll(filepath.Join(s.home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl"))
		if s.rollouts == nil {
			s.rollouts = []string{} // mark scanned-but-empty
		}
	}
	return s.rollouts
}

func (s *codexScanner) cwdOf(path string) string {
	if c, ok := s.cwdCache[path]; ok {
		return c
	}
	var ev codexEvent
	_ = json.Unmarshal(firstLine(path), &ev)
	c := ""
	if ev.Payload != nil {
		c = ev.Payload.Cwd
	}
	s.cwdCache[path] = c
	return c
}

// sessionsForCwd returns up to n newest rollouts whose cwd matches, scanning at
// most codexScanCap files.
func (s *codexScanner) sessionsForCwd(cwd string, n int) []string {
	var out []string
	for i, f := range s.list() {
		if i >= codexScanCap {
			break
		}
		if s.cwdOf(f) == cwd {
			out = append(out, f)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

// findForCwd returns the newest rollout matching cwd, or the global newest as a
// fallback.
func (s *codexScanner) findForCwd(cwd string) string {
	if m := s.sessionsForCwd(cwd, 1); len(m) > 0 {
		return m[0]
	}
	if list := s.list(); len(list) > 0 {
		return list[0]
	}
	return ""
}

var upperEnumRe = regexp.MustCompile(`^[A-Z_]+$`)

// detectAgentForFile identifies which agent owns a session path: by location
// first, then by sniffing the first line's shape.
func detectAgentForFile(home, path string) Agent {
	switch {
	case strings.HasPrefix(path, filepath.Join(home, ".claude", "projects")+string(os.PathSeparator)):
		return AgentClaude
	case strings.HasPrefix(path, filepath.Join(home, ".codex", "sessions")+string(os.PathSeparator)):
		return AgentCodex
	case strings.HasPrefix(path, filepath.Join(home, ".gemini", "antigravity-cli")+string(os.PathSeparator)):
		return AgentAgy
	}
	return sniffAgent(firstLine(path))
}

// sniffAgent classifies a session by its first event:
//
//	codex:  .payload.type set, or .type == "session_meta"
//	agy:    .type is an uppercase enum like "USER_INPUT"
//	claude: anything else (.type is "user"/"assistant", lowercase)
func sniffAgent(first []byte) Agent {
	var top map[string]json.RawMessage
	if json.Unmarshal(first, &top) != nil {
		return AgentClaude
	}
	if pl, ok := top["payload"]; ok {
		var plm map[string]json.RawMessage
		if json.Unmarshal(pl, &plm) == nil {
			if t, ok := plm["type"]; ok && isTruthy(t) {
				return AgentCodex
			}
		}
	}
	if trimmed(top["type"]) == `"session_meta"` {
		return AgentCodex
	}
	if upperEnumRe.MatchString(jqToStringRaw(top["type"])) {
		return AgentAgy
	}
	// entire's own transcript format: top-level content + ts, no nested message.
	if _, hasMsg := top["message"]; !hasMsg {
		_, hasContent := top["content"]
		_, hasTs := top["ts"]
		if hasContent && hasTs {
			return AgentEntire
		}
	}
	return AgentClaude
}

// isTruthy mirrors jq truthiness: everything except null and false is truthy.
func isTruthy(raw json.RawMessage) bool {
	s := trimmed(raw)
	return s != "" && s != "null" && s != "false"
}

// cwdMismatchNote returns a stderr note when the resolved session doesn't belong
// to pwd (so the user knows they're looking at a global-latest fallback), or ""
// when it matches.
func cwdMismatchNote(agent Agent, session, home, pwd string, scanner *codexScanner) string {
	switch agent {
	case AgentClaude:
		base := filepath.Base(filepath.Dir(session))
		switch {
		case base == claudeSlug(pwd):
			return "" // exact match — the common case.
		case claudeAncestorDir(home, pwd, base) != "":
			return "no Claude session for " + pwd + " — following same-tree dir " + claudeAncestorDir(home, pwd, base) + "."
		case strings.HasPrefix(base, claudeSlug(pwd)+"-"):
			return "no Claude session for " + pwd + " — following same-tree subdirectory session."
		default:
			return "no Claude session for " + pwd + " — using global latest."
		}
	case AgentCodex:
		sessCwd := scanner.cwdOf(session)
		if sessCwd != "" && sessCwd != pwd {
			return "no Codex session for " + pwd + " — using global latest (cwd=" + sessCwd + ")."
		}
	case AgentAgy:
		// Conversation id is the brain/<id> dir, three levels above the
		// transcript file. The note fires unless the cwd's expected id matches
		// (an empty expected never equals a real id, so this one test covers
		// both "no mapping" and "mismatched mapping").
		sessID := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(session))))
		if agyConversationID(agyRoot(home), pwd) != sessID {
			return "no Antigravity session for " + pwd + " — using global latest."
		}
	}
	return ""
}

// newestGlob returns the most-recently-modified file matching pattern, or "".
func newestGlob(pattern string) string {
	all := newestGlobAll(pattern)
	if len(all) == 0 {
		return ""
	}
	return all[0]
}

// newestGlobAll returns files matching pattern sorted newest-first (by mtime,
// ties broken by path descending — a stable stand-in for `ls -t`).
func newestGlobAll(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	type entry struct {
		path  string
		mtime int64
	}
	entries := make([]entry, 0, len(matches))
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		entries = append(entries, entry{m, fi.ModTime().UnixNano()})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].mtime != entries[j].mtime {
			return entries[i].mtime > entries[j].mtime
		}
		return entries[i].path > entries[j].path
	})
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.path
	}
	return out
}

// firstLine reads the first line of a file (without the trailing newline), or
// nil. The buffer is grown to handle very long session_meta lines.
func firstLine(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if sc.Scan() {
		return append([]byte(nil), sc.Bytes()...)
	}
	return nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
