package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const ampCommandTimeout = 15 * time.Second
const ampFocusRefreshTimeout = time.Second
const ampLocalLogPollInterval = 50 * time.Millisecond
const ampLocalSnapshotFallback = 10 * time.Second

type ampThread struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Updated      string `json:"updated"`
	UpdatedAt    string `json:"updatedAt"`
	Tree         string `json:"tree"`
	MessageCount int    `json:"messageCount"`
}

func (t ampThread) updatedUnix() int64 {
	v, _ := time.Parse(time.RFC3339Nano, firstNonEmpty(t.Updated, t.UpdatedAt))
	return v.Unix()
}

func (t ampThread) cwd() string { return ampFilePath(t.Tree) }

type ampExport struct {
	V        int64         `json:"v"`
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Created  int64         `json:"created"`
	Env      ampExportEnv  `json:"env"`
	Meta     ampExportMeta `json:"meta"`
	Messages []ampMessage  `json:"messages"`
}

type ampExportEnv struct {
	Initial struct {
		WorkingDirectory string `json:"workingDirectory"`
		RunnerID         any    `json:"runnerID"`
		Trees            []struct {
			URI         string `json:"uri"`
			DisplayName string `json:"displayName"`
		} `json:"trees"`
	} `json:"initial"`
}

type ampExportMeta struct {
	ExecutorType        string `json:"executorType"`
	AgentMode           string `json:"agentMode"`
	LastKnownAgentState struct {
		State     string `json:"state"`
		MessageID string `json:"messageID"`
		UpdatedAt string `json:"updatedAt"`
	} `json:"lastKnownAgentState"`
}

func (e ampExport) cwd() string {
	if p := ampFilePath(e.Env.Initial.WorkingDirectory); p != "" {
		return p
	}
	for _, tree := range e.Env.Initial.Trees {
		if p := ampFilePath(tree.URI); p != "" {
			return p
		}
	}
	return ""
}

var ampRun = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "amp", args...)
	b, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("amp %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, err
	}
	return b, nil
}

type ampActiveThread struct {
	ID                string `json:"id"`
	ThreadID          string `json:"threadID"`
	ThreadId          string `json:"threadId"`
	Title             string `json:"title"`
	State             string `json:"state"`
	AgentState        string `json:"agentState"`
	Status            string `json:"status"`
	ExecutorType      string `json:"executorType"`
	Executor          string `json:"executor"`
	Tree              string `json:"tree"`
	TreeURI           string `json:"treeURI"`
	WorkingDirectory  string `json:"workingDirectory"`
	UpdatedAt         string `json:"updatedAt"`
	Working           *bool  `json:"working"`
	ExecutorConnected *bool  `json:"executorConnected"`
}

func (t ampActiveThread) id() string { return firstNonEmpty(t.ID, t.ThreadID, t.ThreadId) }

func (t ampActiveThread) cwd() string {
	return ampFilePath(firstNonEmpty(t.Tree, t.TreeURI, t.WorkingDirectory))
}

func (t ampActiveThread) status() string {
	if t.Working != nil && *t.Working {
		return "busy"
	}
	if t.ExecutorConnected != nil && *t.ExecutorConnected {
		return "idle"
	}
	s := firstNonEmpty(t.State, t.AgentState, t.Status)
	if s == "streaming" || s == "running" || s == "working" {
		return "busy"
	}
	if s == "" {
		return "live"
	}
	return s
}

func (t ampActiveThread) active() bool {
	if t.Working != nil || t.ExecutorConnected != nil {
		return t.Working != nil && *t.Working || t.ExecutorConnected != nil && *t.ExecutorConnected
	}
	return true // older amp top schemas reported only active rows
}

type ampTopEvent struct {
	UpdatedAt    string            `json:"updatedAt"`
	Threads      []ampActiveThread `json:"threads"`
	Reconnecting bool              `json:"reconnecting"`
}

func parseAmpTopEvent(line []byte) ([]ampActiveThread, bool) {
	var event ampTopEvent
	if json.Unmarshal(line, &event) != nil {
		return nil, false
	}
	if event.Reconnecting {
		return nil, false // retain the last authoritative set while disconnected
	}
	out := make([]ampActiveThread, 0, len(event.Threads))
	for _, thread := range event.Threads {
		if validAmpThreadID(thread.id()) && thread.active() {
			out = append(out, thread)
		}
	}
	return out, true
}

var ampTopCommand = func(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "amp", "top", "--stream-jsonl")
}

type ampTopWatcher struct {
	mu      sync.RWMutex
	threads []ampActiveThread
	ready   chan struct{}
	stop    context.CancelFunc
	once    sync.Once
}

func startAmpTop() *ampTopWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	w := &ampTopWatcher{ready: make(chan struct{}), stop: cancel}
	go w.run(ctx)
	return w
}

func (w *ampTopWatcher) run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		cmd := ampTopCommand(ctx)
		stdout, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err == nil {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if threads, ok := parseAmpTopEvent(scanner.Bytes()); ok {
					w.mu.Lock()
					w.threads = append(w.threads[:0], threads...)
					w.mu.Unlock()
					w.once.Do(func() { close(w.ready) })
					backoff = time.Second
				}
			}
			_ = cmd.Wait()
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

func (w *ampTopWatcher) snapshot() ([]ampActiveThread, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	ready := false
	select {
	case <-w.ready:
		ready = true
	default:
	}
	return append([]ampActiveThread(nil), w.threads...), ready
}

func (w *ampTopWatcher) wait(timeout time.Duration) ([]ampActiveThread, bool) {
	// amp top emits an initial empty snapshot before its populated snapshot. Give
	// the stream the full startup window rather than returning on that first row.
	<-time.After(timeout)
	return w.snapshot()
}

func (w *ampTopWatcher) close() { w.stop() }

func ampList(home string, cacheOnly bool) ([]ampThread, error) {
	return ampListWithin(home, cacheOnly, ampCommandTimeout)
}

func ampListWithin(home string, cacheOnly bool, timeout time.Duration) ([]ampThread, error) {
	path := filepath.Join(ampCacheDir(home), "threads.json")
	if cacheOnly {
		return readAmpList(path)
	}
	b, err := runAmpWithin(timeout, "threads", "list", "--json", "--include-archived")
	if err == nil {
		var threads []ampThread
		if json.Unmarshal(b, &threads) == nil {
			threads = validAmpThreads(threads)
			_ = writeAmpCache(path, b)
			return threads, nil
		}
		err = errors.New("amp returned malformed thread inventory")
	}
	if cached, cacheErr := readAmpList(path); cacheErr == nil {
		return cached, err
	}
	return nil, err
}

func checkAmpInventory(home string, cacheOnly bool) error {
	threads, err := ampList(home, cacheOnly)
	if err == nil || len(threads) > 0 {
		return nil
	}
	if cacheOnly {
		return fmt.Errorf("no cached Amp thread inventory: %w", err)
	}
	return fmt.Errorf("cannot list Amp threads: %w", err)
}

func ampExportThread(home, id string, cacheOnly bool) (ampExport, error) {
	return ampExportThreadWithin(home, id, cacheOnly, ampCommandTimeout)
}

func ampExportThreadWithin(home, id string, cacheOnly bool, timeout time.Duration) (ampExport, error) {
	var out ampExport
	if !validAmpThreadID(id) {
		return out, fmt.Errorf("invalid Amp thread id: %s", id)
	}
	path := filepath.Join(ampCacheDir(home), "exports", id+".json")
	if cacheOnly {
		return readAmpExport(path)
	}
	b, err := runAmpWithin(timeout, "threads", "export", id)
	if err == nil {
		if json.Unmarshal(b, &out) == nil && out.ID == id {
			_ = writeAmpCache(path, b)
			return out, nil
		}
		err = errors.New("amp returned a malformed thread export")
	}
	if cached, cacheErr := readAmpExport(path); cacheErr == nil {
		return cached, err
	}
	return out, err
}

func ampSearch(query string) ([]ampThread, error) {
	b, err := runAmp("threads", "search", "--json", query)
	if err != nil {
		return nil, err
	}
	var threads []ampThread
	if err := json.Unmarshal(b, &threads); err != nil {
		return nil, fmt.Errorf("malformed Amp search response: %w", err)
	}
	return validAmpThreads(threads), nil
}

func findAmpThread(home, pwd string, cacheOnly bool) (ampThread, bool) {
	threads, _ := ampList(home, cacheOnly)
	return newestAmpThread(threads, pwd, true)
}

func findAmpThreadExact(home, pwd string, cacheOnly bool) (ampThread, bool) {
	threads, _ := ampList(home, cacheOnly)
	return newestAmpThread(threads, pwd, false)
}

func newestAmpThread(threads []ampThread, pwd string, fallback bool) (ampThread, bool) {
	var newest, exact ampThread
	for _, thread := range threads {
		if newest.ID == "" || thread.updatedUnix() > newest.updatedUnix() {
			newest = thread
		}
		if filepath.Clean(thread.cwd()) == filepath.Clean(pwd) && (exact.ID == "" || thread.updatedUnix() > exact.updatedUnix()) {
			exact = thread
		}
	}
	if exact.ID != "" {
		return exact, true
	}
	if !fallback {
		return ampThread{}, false
	}
	return newest, newest.ID != ""
}

type ampSnapshotChange int

const (
	ampSnapshotUnchanged ampSnapshotChange = iota
	ampSnapshotAppend
	ampSnapshotRewrite
)

func classifyAmpSnapshot(prior, current []byte) ampSnapshotChange {
	if bytes.Equal(prior, current) {
		return ampSnapshotUnchanged
	}
	if bytes.HasPrefix(current, prior) {
		return ampSnapshotAppend
	}
	return ampSnapshotRewrite
}

func runAmp(args ...string) ([]byte, error) {
	return runAmpWithin(ampCommandTimeout, args...)
}

func runAmpWithin(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	b, err := ampRun(ctx, args...)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("amp command timed out after %s", timeout)
	}
	return b, err
}

func validAmpThreads(in []ampThread) []ampThread {
	out := make([]ampThread, 0, len(in))
	for _, t := range in {
		if validAmpThreadID(t.ID) && firstNonEmpty(t.Updated, t.UpdatedAt) != "" {
			out = append(out, t)
		}
	}
	return out
}

func validAmpThreadID(id string) bool {
	return strings.HasPrefix(id, "T-") && len(id) > 2 && !strings.ContainsAny(id, "/\\ \t\r\n")
}

func ampFilePath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return filepath.Clean(u.Path)
}

func ampCacheDir(home string) string { return filepath.Join(home, ".cache", "entire-tail", "amp") }

func ampSnapshotPath(home, id string) string {
	return filepath.Join(ampCacheDir(home), "render", id+".jsonl")
}

func ampThreadLogPath(home, id string) string {
	return filepath.Join(home, ".cache", "amp", "logs", "threads", id+".log")
}

func readAmpList(path string) ([]ampThread, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []ampThread
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return validAmpThreads(out), nil
}

func readAmpExport(path string) (ampExport, error) {
	var out ampExport
	b, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil || !validAmpThreadID(out.ID) {
		return out, errors.New("invalid cached Amp export")
	}
	return out, nil
}

func writeAmpCache(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func ampLogNeedsSnapshot(line []byte) bool {
	var event struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &event) != nil {
		return false
	}
	return event.Type == "message_added" || event.Type == "agent_state"
}

func watchAmpThreadLog(home, id string, stop <-chan struct{}) (<-chan struct{}, bool) {
	path := ampThreadLogPath(home, id)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	offset := fi.Size()
	changed := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(ampLocalLogPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fi, err := os.Stat(path)
				if err != nil {
					continue
				}
				if fi.Size() < offset {
					offset = 0
				}
				if fi.Size() == offset {
					continue
				}
				f, err := os.Open(path)
				if err != nil {
					continue
				}
				_, err = f.Seek(offset, io.SeekStart)
				if err != nil {
					_ = f.Close()
					continue
				}
				r := bufio.NewReader(f)
				for {
					line, readErr := r.ReadBytes('\n')
					if len(line) > 0 && line[len(line)-1] == '\n' {
						offset += int64(len(line))
						if ampLogNeedsSnapshot(line) {
							select {
							case changed <- struct{}{}:
							default:
							}
						}
					}
					if readErr != nil {
						break
					}
				}
				_ = f.Close()
			}
		}
	}()
	return changed, true
}

// startAmpSnapshot writes a line-oriented rendering source for the existing tail
// loop and keeps it current from Amp's immutable export snapshots. The caller
// closes stop when it leaves the session.
func startAmpSnapshot(home, id string, cacheOnly bool) (string, chan struct{}, <-chan error, error) {
	path := ampSnapshotPath(home, id)
	ex, err := ampExportThread(home, id, cacheOnly)
	if err != nil && ex.ID == "" {
		return "", nil, nil, err
	}
	data := ampExportLines(ex)
	if err := writeAmpCache(path, data); err != nil {
		return "", nil, nil, err
	}
	stop := make(chan struct{})
	var sourceErrors chan error
	if !cacheOnly {
		sourceErrors = make(chan error, 1)
		logChanged, local := watchAmpThreadLog(home, id, stop)
		lastError := ""
		if err != nil {
			lastError = err.Error()
		}
		go func(version int64, prior []byte, logChanged <-chan struct{}, local bool, lastError string) {
			defer close(sourceErrors)
			interval := time.Second
			if local {
				interval = ampLocalSnapshotFallback
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-logChanged:
				case <-ticker.C:
				}
				next, fetchErr := ampExportThread(home, id, false)
				if fetchErr != nil {
					if fetchErr.Error() != lastError {
						select {
						case sourceErrors <- fetchErr:
						default:
						}
						lastError = fetchErr.Error()
					}
					continue
				}
				lastError = ""
				if next.V == version {
					continue
				}
				current := ampExportLines(next)
				if bytes.Equal(current, prior) {
					version = next.V
					continue
				}
				if err := writeAmpCache(path, current); err == nil {
					version, prior = next.V, current
				}
			}
		}(ex.V, data, logChanged, local, lastError)
		if err != nil {
			sourceErrors <- err
		}
	}
	return path, stop, sourceErrors, err
}

func ampExportLines(ex ampExport) []byte {
	b, _ := json.Marshal(ex)
	lines := ampMessageLines(b)
	if len(lines) == 0 {
		return nil
	}
	return append(bytes.Join(lines, []byte("\n")), '\n')
}

func materializeAmpExportFile(home, source string) (string, ampExport, error) {
	var ex ampExport
	b, err := os.ReadFile(source)
	if err != nil {
		return "", ex, err
	}
	if err := json.Unmarshal(b, &ex); err != nil || !validAmpThreadID(ex.ID) {
		return "", ex, errors.New("invalid Amp export file")
	}
	dir := filepath.Join(ampCacheDir(home), "render")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", ex, err
	}
	f, err := os.CreateTemp(dir, "import-*.jsonl")
	if err != nil {
		return "", ex, err
	}
	path := f.Name()
	if err := f.Chmod(0o600); err == nil {
		_, err = f.Write(ampExportLines(ex))
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", ex, err
	}
	return path, ex, nil
}

func ampChildChannels(home string, parent ampExport, cacheOnly bool) []subagentChannel {
	childIDs := map[string]string{}
	for _, message := range parent.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_result" {
				if id := ampChildID(block.Run); id != "" {
					childIDs[block.ToolUseID] = id
				}
			}
		}
	}
	var channels []subagentChannel
	for _, message := range parent.Messages {
		for _, block := range message.Content {
			id := childIDs[block.ID]
			if block.Type != "tool_use" || block.Name != "create_thread" || id == "" {
				continue
			}
			timeout := ampCommandTimeout
			if !cacheOnly {
				timeout = ampFocusRefreshTimeout
			}
			ex, err := ampExportThreadWithin(home, id, cacheOnly, timeout)
			if err != nil && ex.ID == "" {
				continue
			}
			path := filepath.Join(ampCacheDir(home), "render", id+".jsonl")
			if err := writeAmpCache(path, ampExportLines(ex)); err != nil {
				continue
			}
			created, _ := time.Parse(time.RFC3339Nano, message.CreatedAt)
			spawnTs := int64(0)
			if !created.IsZero() {
				spawnTs = created.Unix()
			}
			channels = append(channels, subagentChannel{
				Agent: AgentAmp, AgentID: id, ThreadID: id, Path: path,
				Description: firstNonEmpty(ampThreadTitle(block.Input, ""), ex.Title, "thread "+shortID(id)),
				AgentType:   "thread", SpawnTs: spawnTs, State: ex.Meta.LastKnownAgentState.State,
			})
		}
	}
	return channels
}

func refreshAmpChannel(home string, ch *subagentChannel) {
	if ch == nil || ch.Agent != AgentAmp || !validAmpThreadID(ch.ThreadID) {
		return
	}
	ex, err := ampExportThreadWithin(home, ch.ThreadID, false, ampFocusRefreshTimeout)
	if err != nil && ex.ID == "" {
		return
	}
	data := ampExportLines(ex)
	prior, _ := os.ReadFile(ch.Path)
	if !bytes.Equal(prior, data) {
		_ = writeAmpCache(ch.Path, data)
	}
	ch.State = ex.Meta.LastKnownAgentState.State
}
