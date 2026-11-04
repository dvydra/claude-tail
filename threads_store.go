package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const threadsClosedDir = "_closed"

// errThreadExists is returned by createThread when the slug is taken.
var errThreadExists = errors.New("already exists")

// lockThreads takes the exclusive, blocking lock that serialises every thread
// mutation. The lock file is created on demand and never unlinked.
func lockThreads(root string) (func(), error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }) }, nil
}

// atomicWrite writes data to path through a temp file and a rename.
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// threadRegistry maps "<agent>:<id>" to the slug of the thread the session works on.
type threadRegistry struct {
	Sessions map[string]string `json:"sessions"`
}

func registryPath(root string) string { return filepath.Join(root, ".registry.json") }

func loadRegistry(root string) (threadRegistry, error) {
	r := threadRegistry{Sessions: map[string]string{}}
	data, err := os.ReadFile(registryPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return threadRegistry{Sessions: map[string]string{}}, err
	}
	if r.Sessions == nil {
		r.Sessions = map[string]string{}
	}
	return r, nil
}

func saveRegistry(root string, r threadRegistry) error {
	if r.Sessions == nil {
		r.Sessions = map[string]string{}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(registryPath(root), append(data, '\n'))
}

func registryKey(agent, id string) string { return agent + ":" + id }

func lookupSession(root, agent, id string) (string, bool) {
	r, err := loadRegistry(root)
	if err != nil {
		return "", false
	}
	slug, ok := r.Sessions[registryKey(agent, id)]
	return slug, ok
}

// validThreadSlug rejects slugs that could escape the threads root or collide
// with the reserved dot and underscore directories.
func validThreadSlug(slug string) error {
	if slug == "" || strings.Contains(slug, "/") || strings.HasPrefix(slug, ".") || strings.HasPrefix(slug, "_") {
		return fmt.Errorf("invalid thread slug %q", slug)
	}
	return nil
}

// readThreadFile loads a thread's thread.md for mutation. A parse error is
// returned as is: the meta may be partial and must never be written back.
func readThreadFile(dir string) (threadMeta, map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "thread.md"))
	if err != nil {
		return threadMeta{}, nil, err
	}
	meta, secs, perr := parseThreadMD(data)
	if perr != nil {
		return threadMeta{}, nil, fmt.Errorf("%s: %w", filepath.Join(dir, "thread.md"), perr)
	}
	return meta, secs, nil
}

// registerSession records that agent session id works on the thread: a
// registry entry plus a newest-first Sessions entry, both idempotent.
func registerSession(root, slug, agent, id string, now time.Time) error {
	if err := validThreadSlug(slug); err != nil {
		return err
	}
	if agent == "" || id == "" {
		return errors.New("session agent and id are required")
	}
	release, err := lockThreads(root)
	if err != nil {
		return err
	}
	defer release()
	dir := filepath.Join(root, slug)
	meta, secs, err := readThreadFile(dir)
	if err != nil {
		return err
	}
	reg, err := loadRegistry(root)
	if err != nil {
		return err
	}
	ref := threadSessionRef{Agent: agent, ID: id}
	rest := make([]threadSessionRef, 0, len(meta.Sessions)+1)
	for _, s := range meta.Sessions {
		if s != ref {
			rest = append(rest, s)
		}
	}
	meta.Sessions = append([]threadSessionRef{ref}, rest...)
	meta.Updated = now.Format("2006-01-02")
	if err := atomicWrite(filepath.Join(dir, "thread.md"), formatThreadMD(meta, secs)); err != nil {
		return err
	}
	reg.Sessions[registryKey(agent, id)] = slug
	return saveRegistry(root, reg)
}

// createThread makes a new open thread and returns its slug.
func createThread(root, name string, now time.Time) (string, error) {
	slug := threadSlug(name)
	if err := validThreadSlug(slug); err != nil {
		return "", fmt.Errorf("thread name %q gives no usable slug", name)
	}
	release, err := lockThreads(root)
	if err != nil {
		return "", err
	}
	defer release()
	for _, d := range []string{filepath.Join(root, slug), filepath.Join(root, threadsClosedDir, slug)} {
		if _, err := os.Stat(d); err == nil {
			return "", fmt.Errorf("thread %q: %w", slug, errThreadExists)
		}
	}
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(filepath.Join(dir, "log"), 0o755); err != nil {
		return "", err
	}
	day := now.Format("2006-01-02")
	meta := threadMeta{Name: name, Status: threadOpen, Created: day, Updated: day}
	if err := atomicWrite(filepath.Join(dir, "thread.md"), formatThreadMD(meta, nil)); err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(dir, "knowledge.md"), nil); err != nil {
		return "", err
	}
	return slug, nil
}

func appendThreadLog(dir string, now time.Time, line string) error {
	p := filepath.Join(dir, "log", now.Format("2006-01-02")+".md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "- %s %s\n", now.Format("15:04"), line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// setThreadStatus moves a thread between open, parked and closed. Closing
// moves its directory to _closed; any other status moves it back to root.
func setThreadStatus(root, slug string, to threadStatus, reason string, now time.Time) error {
	if err := validThreadSlug(slug); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	switch to {
	case threadOpen, threadClosed:
	case threadParked:
		if reason == "" {
			return errors.New("parking a thread requires a reason")
		}
	default:
		return fmt.Errorf("unknown thread status %q", to)
	}
	release, err := lockThreads(root)
	if err != nil {
		return err
	}
	defer release()

	live := filepath.Join(root, slug)
	closed := filepath.Join(root, threadsClosedDir, slug)
	dir, wasClosed := live, false
	if _, err := os.Stat(live); err != nil {
		if _, cerr := os.Stat(closed); cerr != nil {
			return fmt.Errorf("thread %q not found", slug)
		}
		dir, wasClosed = closed, true
	}
	meta, secs, err := readThreadFile(dir)
	if err != nil {
		return err
	}
	from := meta.Status
	if wasClosed {
		from = threadClosed
	}
	if from == to {
		return nil
	}

	var logLine string
	switch {
	case to == threadParked:
		logLine = "parked: " + reason
		meta.ParkReason, meta.ParkedAt = reason, now.Format("2006-01-02")
	case to == threadClosed:
		logLine = "closed"
		meta.ParkReason, meta.ParkedAt = "", ""
	case from == threadParked:
		logLine = "unparked"
		meta.ParkReason, meta.ParkedAt = "", ""
	default:
		logLine = "reopened"
		meta.ParkReason, meta.ParkedAt = "", ""
	}
	meta.Status = to
	meta.Updated = now.Format("2006-01-02")

	var dest string
	switch {
	case to == threadClosed && !wasClosed:
		dest = closed
	case to != threadClosed && wasClosed:
		dest = live
	}
	if dest != "" {
		if _, err := os.Stat(dest); err == nil {
			return fmt.Errorf("cannot move thread %q: %s already exists", slug, dest)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
	}
	if err := atomicWrite(filepath.Join(dir, "thread.md"), formatThreadMD(meta, secs)); err != nil {
		return err
	}
	if err := appendThreadLog(dir, now, logLine); err != nil {
		return err
	}
	if dest != "" {
		return os.Rename(dir, dest)
	}
	return nil
}
