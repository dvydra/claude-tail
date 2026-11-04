package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	threadsExitMiss      = 1  // lookup miss, enabled false, other errors
	threadsExitDuplicate = 2  // new: slug already exists
	threadsExitStale     = 3  // commit: base hash is out of date
	threadsExitBadFM     = 4  // commit: frontmatter does not parse
	threadsExitUsage     = 64 // unknown subcommand or bad arguments
)

// runThreads is the `threads` entry point. With arguments it runs the
// agent-facing command and exits with its code; with none it lists threads.
func runThreads(cfg Config) {
	root := threadsRoot(os.Getenv)
	args := cfg.ThreadsArgs
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
		if len(rest) < 1 {
			return threadsUsage(errOut, "new <name>")
		}
		slug, err := createThread(root, strings.Join(rest, " "), now)
		if err != nil {
			if strings.Contains(err.Error(), "already exists") {
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
		if err := registerSession(root, rest[0], rest[1], rest[2], now); err != nil {
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
		if m.Slug == "" {
			m.Slug = filepath.Base(t.Dir)
		}
		r := threadListRow{Slug: m.Slug, Name: m.Name, Status: m.Status, WaitingOn: m.WaitingOn,
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

type threadBegin struct {
	Base     string `json:"base"`
	Dir      string `json:"dir"`
	ThreadMD string `json:"thread_md"`
}

func readBegin(dir string) (threadBegin, error) {
	data, err := os.ReadFile(filepath.Join(dir, "thread.md"))
	if err != nil {
		return threadBegin{}, err
	}
	sum := sha256.Sum256(data)
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return threadBegin{Base: hex.EncodeToString(sum[:]), Dir: abs, ThreadMD: string(data)}, nil
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

// threadsCommit replaces thread.md with an agent-edited copy, but only if the
// file is unchanged since `begin`. Status fields are forced back to disk
// values: agents change status through park/unpark/close, never here.
func threadsCommit(args []string, root string, now time.Time, out, errOut io.Writer) int {
	var slug, base, file string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--base" && i+1 < len(args):
			base = args[i+1]
			i++
		case args[i] == "--file" && i+1 < len(args):
			file = args[i+1]
			i++
		case slug == "" && !strings.HasPrefix(args[i], "--"):
			slug = args[i]
		default:
			return threadsUsage(errOut, "commit <slug> --base <hash> --file <path>")
		}
	}
	if slug == "" || base == "" || file == "" {
		return threadsUsage(errOut, "commit <slug> --base <hash> --file <path>")
	}
	newData, err := os.ReadFile(file)
	if err != nil {
		return threadsFail(errOut, err)
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
	if cur.Base != base {
		threadsJSON(out, errOut, cur)
		return threadsExitStale
	}
	disk, _, perr := parseThreadMD([]byte(cur.ThreadMD))
	if perr != nil {
		fmt.Fprintf(errOut, "entire-tail threads: %s: %v\n", filepath.Join(dir, "thread.md"), perr)
		return threadsExitBadFM
	}
	meta, secs, perr := parseThreadMD(newData)
	if perr != nil {
		fmt.Fprintf(errOut, "entire-tail threads: %s: %v\n", file, perr)
		return threadsExitBadFM
	}
	meta.Status, meta.ParkReason, meta.ParkedAt = disk.Status, disk.ParkReason, disk.ParkedAt
	meta.Updated = now.Format("2006-01-02")
	if err := atomicWrite(filepath.Join(dir, "thread.md"), formatThreadMD(meta, secs)); err != nil {
		return threadsFail(errOut, err)
	}
	return 0
}
