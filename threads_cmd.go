package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// threadsSubcommands is the accepted set of `entire-tail threads` subcommands.
var threadsSubcommands = []string{"list", "lookup", "new", "register", "park", "unpark", "close", "reopen", "begin", "commit", "enabled", "path"}

// Exit codes of the agent-facing subcommands.
const (
	threadsExitMiss       = 1  // lookup miss, enabled false, other errors
	threadsExitDuplicate  = 2  // new: slug already exists
	threadsExitStale      = 3  // commit: base hash is out of date
	threadsExitBadFM      = 4  // commit: the new file's frontmatter does not parse
	threadsExitDiskBroken = 5  // commit: the thread.md on disk does not parse
	threadsExitUsage      = 64 // unknown subcommand or bad arguments
)

// runThreads is the `threads` entry point. With arguments it runs the
// agent-facing command and exits with its code; with none it lists threads.
func runThreads(cfg Config) {
	root := threadsRoot(os.Getenv)
	args := cfg.ThreadsArgs
	if len(args) == 0 && isCharDevice(os.Stdout) {
		if err := runThreadsTUI(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "entire-tail threads:", err)
			os.Exit(1)
		}
		return
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	os.Exit(runThreadsCommand(args, root, time.Now(), os.Stdout, os.Stderr))
}

func threadsUsage(errOut io.Writer, msg string) int {
	if msg != "" {
		fmt.Fprintln(errOut, "entire-tail threads:", msg)
	}
	fmt.Fprintln(errOut, "usage: entire-tail threads <"+strings.Join(threadsSubcommands, "|")+"> [args]")
	return threadsExitUsage
}

func threadsFail(errOut io.Writer, err error) int {
	fmt.Fprintln(errOut, "entire-tail threads:", err)
	return threadsExitMiss
}

// runThreadsCommand runs one subcommand and returns its exit code.
func runThreadsCommand(args []string, root string, now time.Time, out, errOut io.Writer) int {
	if len(args) == 0 {
		return threadsUsage(errOut, "")
	}
	known := false
	for _, s := range threadsSubcommands {
		if s == args[0] {
			known = true
		}
	}
	if !known {
		return threadsUsage(errOut, fmt.Sprintf("unknown subcommand %q", args[0]))
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list":
		return threadsList(rest, root, out, errOut)
	case "lookup":
		if len(rest) != 2 {
			return threadsUsage(errOut, "lookup <agent> <session-id>")
		}
		slug, ok := lookupSession(root, rest[0], rest[1])
		if !ok {
			return threadsExitMiss
		}
		return threadsJSON(out, errOut, map[string]string{"slug": slug})
	case "new":
		// --created backdates a thread (the bootstrap); commit never changes it.
		day := now
		if len(rest) >= 2 && rest[0] == "--created" {
			d, err := time.ParseInLocation("2006-01-02", rest[1], now.Location())
			if err != nil {
				return threadsUsage(errOut, "new [--created YYYY-MM-DD] <name>")
			}
			day, rest = d, rest[2:]
		}
		if len(rest) < 1 {
			return threadsUsage(errOut, "new [--created YYYY-MM-DD] <name>")
		}
		slug, err := createThread(root, strings.Join(rest, " "), day)
		if err != nil {
			if errors.Is(err, errThreadExists) {
				fmt.Fprintln(errOut, "entire-tail threads:", err)
				return threadsExitDuplicate
			}
			return threadsFail(errOut, err)
		}
		fmt.Fprintln(out, slug)
		return 0
	case "register":
		if len(rest) != 3 {
			return threadsUsage(errOut, "register <slug> <agent> <session-id>")
		}
		if err := registerSession(root, rest[0], rest[1], rest[2]); err != nil {
			return threadsFail(errOut, err)
		}
		return 0
	case "park":
		if len(rest) < 2 {
			return threadsUsage(errOut, "park <slug> <reason...>")
		}
		return threadsStatus(root, rest[0], threadParked, strings.Join(rest[1:], " "), now, errOut)
	case "unpark", "reopen":
		if len(rest) != 1 {
			return threadsUsage(errOut, cmd+" <slug>")
		}
		return threadsStatus(root, rest[0], threadOpen, "", now, errOut)
	case "close":
		if len(rest) != 1 {
			return threadsUsage(errOut, "close <slug>")
		}
		return threadsStatus(root, rest[0], threadClosed, "", now, errOut)
	case "begin":
		if len(rest) != 1 {
			return threadsUsage(errOut, "begin <slug>")
		}
		return threadsBegin(root, rest[0], out, errOut)
	case "commit":
		return threadsCommit(rest, root, now, out, errOut)
	case "enabled":
		if _, err := os.Stat(filepath.Join(root, ".enabled")); err != nil {
			return threadsExitMiss
		}
		return 0
	case "path":
		fmt.Fprintln(out, root)
		return 0
	}
	return threadsUsage(errOut, "")
}

func threadsStatus(root, slug string, to threadStatus, reason string, now time.Time, errOut io.Writer) int {
	if err := setThreadStatus(root, slug, to, reason, now); err != nil {
		return threadsFail(errOut, err)
	}
	return 0
}

func threadsJSON(out, errOut io.Writer, v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return threadsFail(errOut, err)
	}
	fmt.Fprintln(out, string(b))
	return 0
}

type threadListRow struct {
	Slug      string        `json:"slug"`
	Name      string        `json:"name"`
	Status    threadStatus  `json:"status"`
	WaitingOn string        `json:"waiting_on"`
	NeedsYou  bool          `json:"needs_you"`
	Updated   string        `json:"updated"`
	Trails    []string      `json:"trails"`
	Linear    []string      `json:"linear"`
	Claims    []threadClaim `json:"claims"`
	Customer  string        `json:"customer"`
}

func threadsList(args []string, root string, out, errOut io.Writer) int {
	all := false
	for _, a := range args {
		if a != "--all" {
			return threadsUsage(errOut, "list [--all]")
		}
		all = true
	}
	ts := loadThreads(root)
	if all {
		ts = append(ts, loadThreads(filepath.Join(root, threadsClosedDir))...)
	}
	rows := make([]threadListRow, 0, len(ts))
	for _, t := range ts {
		m := t.Meta
		r := threadListRow{Slug: m.Slug, Name: threadDisplayName(t), Status: m.Status, WaitingOn: m.WaitingOn,
			NeedsYou: m.NeedsYou, Updated: m.Updated, Trails: m.Trails, Linear: m.Linear,
			Claims: m.Claims, Customer: m.Customer}
		if r.Trails == nil {
			r.Trails = []string{}
		}
		if r.Linear == nil {
			r.Linear = []string{}
		}
		if r.Claims == nil {
			r.Claims = []threadClaim{}
		}
		rows = append(rows, r)
	}
	return threadsJSON(out, errOut, rows)
}

// threadDir finds a thread's directory, live or closed.
func threadDir(root, slug string) (string, error) {
	if err := validThreadSlug(slug); err != nil {
		return "", err
	}
	for _, d := range []string{filepath.Join(root, slug), filepath.Join(root, threadsClosedDir, slug)} {
		if _, err := os.Stat(filepath.Join(d, "thread.md")); err == nil {
			return d, nil
		}
	}
	return "", fmt.Errorf("thread %q not found", slug)
}

// threadBegin is what `begin` prints and what a stale `commit` prints back:
// the current thread.md and knowledge.md with a hash of each to commit against.
// KnowledgeBase is "" when knowledge.md does not exist.
type threadBegin struct {
	Base          string `json:"base"`
	Dir           string `json:"dir"`
	ThreadMD      string `json:"thread_md"`
	KnowledgeBase string `json:"knowledge_base"`
	KnowledgeMD   string `json:"knowledge_md"`
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readBegin(dir string) (threadBegin, error) {
	data, err := os.ReadFile(filepath.Join(dir, "thread.md"))
	if err != nil {
		return threadBegin{}, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	b := threadBegin{Base: sha256Hex(data), Dir: abs, ThreadMD: string(data)}
	k, err := os.ReadFile(filepath.Join(dir, "knowledge.md"))
	switch {
	case err == nil:
		b.KnowledgeBase, b.KnowledgeMD = sha256Hex(k), string(k)
	case !errors.Is(err, os.ErrNotExist):
		return threadBegin{}, err
	}
	return b, nil
}

func threadsBegin(root, slug string, out, errOut io.Writer) int {
	dir, err := threadDir(root, slug)
	if err != nil {
		return threadsFail(errOut, err)
	}
	b, err := readBegin(dir)
	if err != nil {
		return threadsFail(errOut, err)
	}
	return threadsJSON(out, errOut, b)
}

const threadsCommitUsage = "commit <slug> --base <hash> --file <path> [--knowledge <path> --knowledge-base <hash>] [--log [YYYY-MM-DD=]<path>]..."

// threadLogPart is one --log: text appended to log/<Date>.md.
type threadLogPart struct {
	Date string
	Text []byte
}

// parseLogArg reads `--log [YYYY-MM-DD=]<path>`; without a date it is today's log.
func parseLogArg(arg string, now time.Time) (string, string, error) {
	if len(arg) > 11 && arg[10] == '=' {
		if _, err := time.Parse("2006-01-02", arg[:10]); err == nil {
			return arg[:10], arg[11:], nil
		}
	}
	return now.Format("2006-01-02"), arg, nil
}

// appendLogText adds text to log/<date>.md, creating it if missing and
// keeping a blank line between what was there and what is added.
func appendLogText(dir, date string, text []byte) error {
	p := filepath.Join(dir, "log", date+".md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var add []byte
	if len(old) > 0 {
		if old[len(old)-1] != '\n' {
			add = append(add, '\n')
		}
		add = append(add, '\n')
	}
	add = append(add, text...)
	if len(add) > 0 && add[len(add)-1] != '\n' {
		add = append(add, '\n')
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(add)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// threadsCommit replaces thread.md with an agent-edited copy, and optionally
// knowledge.md and log entries, in one locked step. Each replaced file must be
// unchanged since `begin`; if any check fails nothing is written. Status
// fields are forced back to disk values: agents change status through
// park/unpark/close, never here.
func threadsCommit(args []string, root string, now time.Time, out, errOut io.Writer) int {
	var slug, base, file, kfile, kbase string
	kbaseSet := false
	var logArgs []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--base" && i+1 < len(args):
			base = args[i+1]
			i++
		case args[i] == "--file" && i+1 < len(args):
			file = args[i+1]
			i++
		case args[i] == "--knowledge" && i+1 < len(args):
			kfile = args[i+1]
			i++
		case args[i] == "--knowledge-base" && i+1 < len(args):
			kbase, kbaseSet = args[i+1], true
			i++
		case args[i] == "--log" && i+1 < len(args):
			logArgs = append(logArgs, args[i+1])
			i++
		case slug == "" && !strings.HasPrefix(args[i], "--"):
			slug = args[i]
		default:
			return threadsUsage(errOut, threadsCommitUsage)
		}
	}
	if slug == "" || base == "" || file == "" || (kfile != "") != kbaseSet {
		return threadsUsage(errOut, threadsCommitUsage)
	}
	newData, err := os.ReadFile(file)
	if err != nil {
		return threadsFail(errOut, err)
	}
	var newKnowledge []byte
	if kfile != "" {
		if newKnowledge, err = os.ReadFile(kfile); err != nil {
			return threadsFail(errOut, err)
		}
	}
	var logs []threadLogPart
	for _, a := range logArgs {
		date, p, _ := parseLogArg(a, now)
		text, err := os.ReadFile(p)
		if err != nil {
			return threadsFail(errOut, err)
		}
		logs = append(logs, threadLogPart{Date: date, Text: text})
	}
	if err := validThreadSlug(slug); err != nil {
		return threadsFail(errOut, err)
	}
	release, err := lockThreads(root)
	if err != nil {
		return threadsFail(errOut, err)
	}
	defer release()

	dir, err := threadDir(root, slug)
	if err != nil {
		return threadsFail(errOut, err)
	}
	cur, err := readBegin(dir)
	if err != nil {
		return threadsFail(errOut, err)
	}
	if cur.Base != base || (kfile != "" && cur.KnowledgeBase != kbase) {
		threadsJSON(out, errOut, cur)
		return threadsExitStale
	}
	disk, _, perr := parseThreadMD([]byte(cur.ThreadMD))
	if perr != nil {
		fmt.Fprintf(errOut, "entire-tail threads: %s: %v\n", filepath.Join(dir, "thread.md"), perr)
		fmt.Fprintln(errOut, "entire-tail threads: thread.md on disk is broken; tell Daniel")
		return threadsExitDiskBroken
	}
	meta, secs, perr := parseThreadMD(newData)
	if perr != nil {
		fmt.Fprintf(errOut, "entire-tail threads: %s: %v\n", file, perr)
		return threadsExitBadFM
	}
	// Status moves through park/unpark/close; sessions through register; the
	// creation date never moves. An agent can't drop or rewrite any of them.
	meta.Status, meta.ParkReason, meta.ParkedAt = disk.Status, disk.ParkReason, disk.ParkedAt
	meta.Sessions, meta.Created = disk.Sessions, disk.Created
	meta.Slug = filepath.Base(dir) // identity is the directory's, not the agent's
	meta.Updated = now.Format("2006-01-02")
	if err := atomicWrite(filepath.Join(dir, "thread.md"), formatThreadMD(meta, secs)); err != nil {
		return threadsFail(errOut, err)
	}
	if kfile != "" {
		if err := atomicWrite(filepath.Join(dir, "knowledge.md"), newKnowledge); err != nil {
			return threadsFail(errOut, err)
		}
	}
	for _, l := range logs {
		if err := appendLogText(dir, l.Date, l.Text); err != nil {
			return threadsFail(errOut, err)
		}
	}
	return 0
}
