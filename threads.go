package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// readFile is a seam so tests can stub file reads.
var readFile = os.ReadFile

type threadStatus string

const (
	threadOpen   threadStatus = "open"
	threadParked threadStatus = "parked"
	threadClosed threadStatus = "closed"
)

// threadClaim is one checkable statement a thread makes about the world.
type threadClaim struct {
	Kind   string `yaml:"kind" json:"kind"` // "worktree" | "trail" | "linear"
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	Head   string `yaml:"head,omitempty" json:"head,omitempty"`   // worktree; "" means unpushed
	Trail  string `yaml:"trail,omitempty" json:"trail,omitempty"` // "gh/owner/repo/123" or "et/..."
	Linear string `yaml:"linear,omitempty" json:"linear,omitempty"`
	Expect string `yaml:"expect,omitempty" json:"expect,omitempty"` // trail/linear state
}

// threadSessionRef names an agent session that worked on a thread, newest first.
type threadSessionRef struct {
	Agent string `yaml:"agent"`
	ID    string `yaml:"id"`
}

type threadMeta struct {
	Name       string             `yaml:"name"`
	Slug       string             `yaml:"slug,omitempty"`
	Status     threadStatus       `yaml:"status"`
	ParkKind   string             `yaml:"park_kind,omitempty"`   // a parkKinds key; "" for a free-text park
	ParkReason string             `yaml:"park_reason,omitempty"` // the note; required only without a kind
	ParkedAt   string             `yaml:"parked_at,omitempty"`
	WakesOn    string             `yaml:"wakes_on,omitempty"` // YYYY-MM-DD the park ends by itself
	WaitingOn  string             `yaml:"waiting_on,omitempty"`
	NeedsYou   bool               `yaml:"needs_you,omitempty"`
	Customer   string             `yaml:"customer,omitempty"`
	Linear     []string           `yaml:"linear,omitempty"`
	Trails     []string           `yaml:"trails,omitempty"`
	Claims     []threadClaim      `yaml:"claims,omitempty"`
	Sessions   []threadSessionRef `yaml:"sessions,omitempty"`
	Created    string             `yaml:"created,omitempty"`
	Updated    string             `yaml:"updated,omitempty"`
	AsOf       map[string]string  `yaml:"as_of,omitempty"` // section heading -> YYYY-MM-DD
	// Extra holds frontmatter keys this struct does not name (Obsidian's tags,
	// aliases, cssclasses, anything hand-added). Kept as nodes so a Go write
	// gives them back in their own style: a date stays a date, a flow list
	// stays a flow list, a trailing comment stays.
	Extra map[string]yaml.Node `yaml:",inline"`
	// Preamble is the body text before the first known section, e.g. a
	// `# Title` and an intro paragraph. Not frontmatter.
	Preamble string `yaml:"-"`
}

type thread struct {
	Meta     threadMeta
	Sections map[string]string
	Dir      string
	ParseErr error
	// ClosedInRoot is set when thread.md says closed but the folder is not in
	// _closed. Such a thread is shown as open with a risk, so it is not lost.
	ClosedInRoot bool
}

// threadDisplayName is the thread's name, or its slug when the name is
// missing (an unreadable or hand-made thread.md).
func threadDisplayName(t thread) string {
	return firstNonEmpty(t.Meta.Name, t.Meta.Slug, filepath.Base(t.Dir))
}

var threadSections = []string{"What it is", "Where it got to", "What's in the way", "People", "Recent"}

// threadsRoot is where thread directories live, under the handover vault.
func threadsRoot(getenv func(string) string) string {
	return filepath.Join(firstNonEmpty(getenv("ENTIRE_TAIL_HANDOVER_VAULT"), defaultHandoverVault), "Entire", "Threads")
}

// threadSlug lowercases name and collapses every run of non-alphanumerics to one dash.
func threadSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
	}
	return b.String()
}

func isThreadSection(h string) bool {
	for _, s := range threadSections {
		if s == h {
			return true
		}
	}
	return false
}

// parseThreadMD splits thread.md into frontmatter and sections. A bad
// frontmatter returns an error but the sections are still parsed. On error the
// returned meta may be partially filled and must never be written back.
// CRLF line endings are accepted.
func parseThreadMD(data []byte) (threadMeta, map[string]string, error) {
	var meta threadMeta
	var perr error
	body := strings.ReplaceAll(string(data), "\r\n", "\n")
	if rest, ok := strings.CutPrefix(body, "---\n"); ok {
		var fm string
		switch {
		case strings.HasPrefix(rest, "---\n"):
			fm, body = "", rest[4:]
		case strings.HasSuffix(rest, "\n---"):
			fm, body = rest[:len(rest)-4], ""
		default:
			if i := strings.Index(rest, "\n---\n"); i >= 0 {
				fm, body = rest[:i], rest[i+5:]
			} else {
				perr = errors.New("thread.md: unterminated frontmatter")
				fm, body = "", rest
			}
		}
		if perr == nil {
			if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
				perr = err
			}
		}
	} else {
		perr = errors.New("thread.md: missing frontmatter")
	}

	secs := map[string]string{}
	cur := ""
	var lines []string
	flush := func() {
		text := strings.Trim(strings.Join(lines, "\n"), "\n")
		if cur != "" {
			secs[cur] = text
		} else {
			meta.Preamble = text
		}
		lines = nil
	}
	for _, ln := range strings.Split(body, "\n") {
		if h, ok := strings.CutPrefix(ln, "## "); ok && isThreadSection(strings.TrimSpace(h)) {
			flush()
			cur = strings.TrimSpace(h)
			continue
		}
		lines = append(lines, ln)
	}
	flush()
	return meta, secs, perr
}

// formatThreadMD renders meta, the preamble and the five sections; absent
// sections get an empty heading. Two-space indent, the same as the skill writes.
func formatThreadMD(meta threadMeta, sections map[string]string) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(meta); err != nil {
		panic("formatThreadMD: cannot marshal thread frontmatter: " + err.Error())
	}
	if err := enc.Close(); err != nil {
		panic("formatThreadMD: cannot marshal thread frontmatter: " + err.Error())
	}
	buf.WriteString("---\n")
	if meta.Preamble != "" {
		buf.WriteString("\n" + meta.Preamble + "\n")
	}
	for _, h := range threadSections {
		buf.WriteString("\n## " + h + "\n")
		if s := sections[h]; s != "" {
			buf.WriteString(s + "\n")
		}
	}
	return buf.Bytes()
}

// loadThreads reads every thread directly under root (the threads root, or
// its _closed folder). Unreadable ones are kept with ParseErr set so they stay
// visible. The folder decides closed: under _closed a thread is closed, and
// one in root whose file says closed is shown as open with ClosedInRoot set.
func loadThreads(root string) []thread {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	inClosed := filepath.Base(root) == threadsClosedDir
	var out []thread
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		dir := filepath.Join(root, name)
		data, err := readFile(filepath.Join(dir, "thread.md"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var meta threadMeta
		var secs map[string]string
		perr := err
		if err == nil {
			meta, secs, perr = parseThreadMD(data)
		}
		meta.Slug = name // the directory name is the slug, whatever the frontmatter says
		t := thread{Meta: meta, Sections: secs, Dir: dir, ParseErr: perr}
		switch {
		case inClosed:
			t.Meta.Status = threadClosed
		case meta.Status == threadClosed:
			t.Meta.Status, t.ClosedInRoot = threadOpen, true
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Meta, out[j].Meta
		if a.NeedsYou != b.NeedsYou {
			return a.NeedsYou
		}
		return a.Updated > b.Updated
	})
	return out
}
