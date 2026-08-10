package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// tapdaemon.go — the local reverse proxy Claude Code's API traffic can be
// routed through (ANTHROPIC_BASE_URL), so entire-tail can see the assistant
// stream as it happens rather than when the transcript flushes.
//
// Two things it produces:
//   - per-session sidecars of block-complete events (tap.go), which the tail
//     reads to render a blocked question's preamble
//   - an exact per-session activity table (active.json): the daemon knows
//     which session is mid-request, which no amount of mtime/pgrep guessing can
//     tell you
//
// Deliberate properties:
//   - FIXED default port. A session's ANTHROPIC_BASE_URL is baked in at launch,
//     so a restart on a different port would break every live session. The port
//     is stable and a second instance refuses to start.
//   - Never logs or persists request headers — they carry the auth token. Only
//     method/path/status and the assistant stream (which the transcript already
//     stores in plaintext) are recorded.
//   - Pass-through by default. Only POST /v1/messages* is teed; everything else
//     (oauth refresh, mcp_servers, …) is proxied untouched.
//   - Streaming is transparent: FlushInterval -1 forwards each SSE chunk
//     immediately, and the tee sits in the body's Read path rather than
//     buffering the response.

const (
	// tapDefaultPort is deliberately fixed — see the port note above.
	tapDefaultPort = 47391
	// tapHealthPath is our own endpoint, namespaced so it can never shadow an
	// API route. It reports the pid so a stale state file pointing at someone
	// else's listener is detected instead of trusted.
	tapHealthPath   = "/-/entire-tail-tap/health"
	tapAPIPath      = "/v1/messages"
	tapUpstreamHost = "https://api.anthropic.com"
	// tapInFlightStale bounds a leaked in-flight count: if a request never
	// completed (proxy killed mid-stream), the session stops reading as
	// "generating" after this long.
	tapInFlightStale = 10 * time.Minute
)

// tapState is the daemon's advertised endpoint, written to disk so the tail and
// the launcher can find it.
type tapState struct {
	Pid      int    `json:"pid"`
	Port     int    `json:"port"`
	Upstream string `json:"upstream"`
	Version  string `json:"version"`
	Started  string `json:"started"`
}

func tapStatePath(home string) string { return filepath.Join(tapDir(home), "daemon.json") }

func tapActivePath(home string) string { return filepath.Join(tapDir(home), "active.json") }

// writeJSONAtomic writes via a temp file + rename so a reader never sees a
// half-written file.
func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readTapState(home string) (tapState, bool) {
	b, err := os.ReadFile(tapStatePath(home))
	if err != nil {
		return tapState{}, false
	}
	var st tapState
	if err := json.Unmarshal(b, &st); err != nil || st.Port == 0 {
		return tapState{}, false
	}
	return st, true
}

// tapProbe reports whether a daemon is actually listening on st's port AND is
// the process st names. Checking the pid over HTTP (not just a successful dial)
// means an unrelated program squatting the port is never mistaken for the tap.
func tapProbe(st tapState, timeout time.Duration) bool {
	if st.Port == 0 {
		return false
	}
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d%s", st.Port, tapHealthPath))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		Pid int `json:"pid"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h) != nil {
		return false
	}
	return h.Pid != 0 && (st.Pid == 0 || h.Pid == st.Pid)
}

// tapBaseURL returns the base URL to hand a freshly launched agent, or "" when
// no daemon answers. The empty return is the fail-open contract: a launcher that
// gets "" starts the session with no ANTHROPIC_BASE_URL at all, so a dead or
// missing daemon can never stop a session from working.
func tapBaseURL(home string) string {
	st, ok := readTapState(home)
	if !ok || !tapProbe(st, 300*time.Millisecond) {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", st.Port)
}

// tapUpstream picks what the daemon forwards to: an explicit override, else an
// ANTHROPIC_BASE_URL already in the environment (someone fronting a gateway),
// else the real API. A loopback value is ignored — that's our own address
// leaking in from a shell that already exports it, and honoring it would make
// the daemon proxy to itself.
func tapUpstream(getenv func(string) string) string {
	for _, v := range []string{getenv("ENTIRE_TAIL_TAP_UPSTREAM"), getenv("ANTHROPIC_BASE_URL")} {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Host == "" {
			continue
		}
		if isLoopbackHost(u.Hostname()) {
			continue
		}
		return strings.TrimRight(v, "/")
	}
	return tapUpstreamHost
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ── per-session activity ──────────────────────────────────────────────────────

// tapSessionStatus is what the daemon knows about one session's API activity.
// InFlight > 0 means the model is generating RIGHT NOW — the exact signal the
// picker's freshness heuristics only approximate.
type tapSessionStatus struct {
	InFlight  int   `json:"in_flight"`
	Requests  int   `json:"requests"`
	LastStart int64 `json:"last_start"` // unix millis
	LastEnd   int64 `json:"last_end"`
	LastEvent int64 `json:"last_event"` // last block completed on the wire
}

type tapActive struct {
	Updated  int64                       `json:"updated"`
	Sessions map[string]tapSessionStatus `json:"sessions"`
}

// tapGenerating reports whether a session is mid-request, ignoring a stale
// in-flight count left behind by a proxy killed mid-stream.
func tapGenerating(s tapSessionStatus, nowMs int64) bool {
	return s.InFlight > 0 && nowMs-s.LastStart < tapInFlightStale.Milliseconds()
}

func readTapActive(home string) (tapActive, bool) {
	b, err := os.ReadFile(tapActivePath(home))
	if err != nil {
		return tapActive{}, false
	}
	var a tapActive
	if err := json.Unmarshal(b, &a); err != nil {
		return tapActive{}, false
	}
	if a.Sessions == nil {
		a.Sessions = map[string]tapSessionStatus{}
	}
	return a, true
}

// tapTracker owns the mutable daemon state: the activity table and the sidecar
// writes. Every proxied request touches it from its own goroutine, so all of it
// is mutex-guarded.
type tapTracker struct {
	home string
	now  func() time.Time

	mu       sync.Mutex
	sessions map[string]tapSessionStatus
}

func newTapTracker(home string, now func() time.Time) *tapTracker {
	return &tapTracker{home: home, now: now, sessions: map[string]tapSessionStatus{}}
}

func (t *tapTracker) nowMs() int64 { return t.now().UnixNano() / 1e6 }

func (t *tapTracker) update(session string, f func(*tapSessionStatus)) {
	if session == "" {
		return
	}
	t.mu.Lock()
	s := t.sessions[session]
	f(&s)
	t.sessions[session] = s
	snap := tapActive{Updated: t.nowMs(), Sessions: make(map[string]tapSessionStatus, len(t.sessions))}
	maps.Copy(snap.Sessions, t.sessions)
	t.mu.Unlock()
	_ = writeJSONAtomic(tapActivePath(t.home), snap) // best effort
}

func (t *tapTracker) requestStart(session string) {
	t.update(session, func(s *tapSessionStatus) {
		s.InFlight++
		s.Requests++
		s.LastStart = t.nowMs()
	})
}

func (t *tapTracker) requestEnd(session string) {
	t.update(session, func(s *tapSessionStatus) {
		if s.InFlight > 0 {
			s.InFlight--
		}
		s.LastEnd = t.nowMs()
	})
}

// event records one block-complete event: appended to the session's sidecar and
// stamped on the activity table.
func (t *tapTracker) event(ev tapEvent) {
	t.mu.Lock()
	_ = appendTapEvent(t.home, ev)
	t.mu.Unlock()
	t.update(ev.Session, func(s *tapSessionStatus) { s.LastEvent = t.nowMs() })
}

// ── the tee ───────────────────────────────────────────────────────────────────

// tapTeeBody wraps a streaming response body, feeding every byte to the parser
// on its way to the client. Sitting in the Read path (rather than buffering or
// io.Copy-ing into a pipe) keeps the proxy byte-transparent and adds no latency.
type tapTeeBody struct {
	rc     io.ReadCloser
	parser *tapParser
	onDone func()
	once   sync.Once
}

func (b *tapTeeBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.parser.feed(p[:n])
	}
	if err != nil {
		b.once.Do(b.onDone)
	}
	return n, err
}

func (b *tapTeeBody) Close() error {
	b.once.Do(b.onDone)
	return b.rc.Close()
}

// ── the server ────────────────────────────────────────────────────────────────

// tapSessionOf reads the session id Claude Code stamps on every request. This
// header is why the daemon needs no correlation heuristics at all.
func tapSessionOf(h http.Header) string { return h.Get("x-claude-code-session-id") }

func isTapAPIPath(p string) bool { return strings.HasPrefix(p, tapAPIPath) }

// tapDiagHeaders are the RESPONSE headers worth reporting when upstream refuses a
// request. Deliberately a whitelist: the daemon's rule is that headers are never
// logged, and these are the narrow exceptions — none carry credentials (they're
// upstream's, not the client's) and without them a 429 or 400 is an unexplained
// number. A 429 at session start is normally just a burst being rate-limited and
// the SDK retrying; retry-after and the remaining/reset counters say so outright.
var tapDiagHeaders = []string{
	"retry-after",
	"request-id",
	"anthropic-ratelimit-unified-status",
	"anthropic-ratelimit-requests-remaining",
	"anthropic-ratelimit-requests-reset",
	"anthropic-ratelimit-tokens-remaining",
	"anthropic-ratelimit-tokens-reset",
}

// tapFailureDetail renders the whitelisted diagnostics for a non-2xx response, or
// "" for a normal one.
func tapFailureDetail(resp *http.Response) string {
	if resp.StatusCode < 400 {
		return ""
	}
	var parts []string
	for _, h := range tapDiagHeaders {
		if v := resp.Header.Get(h); v != "" {
			parts = append(parts, h+"="+v)
		}
	}
	if len(parts) == 0 {
		return " (no diagnostic headers)"
	}
	return " [" + strings.Join(parts, " ") + "]"
}

// newTapHandler builds the proxy. logf receives one line per request — method,
// path, status, and for a failure the whitelisted diagnostics above. NEVER
// request headers.
func newTapHandler(upstream *url.URL, tr *tapTracker, logf func(string, ...any)) http.Handler {
	proxy := &httputil.ReverseProxy{
		FlushInterval: -1, // forward each chunk as it arrives; required for SSE
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Host = upstream.Host
			if isTapAPIPath(r.In.URL.Path) {
				// Ask for an uncompressed body so the tee can read the SSE. Set
				// on the OUTBOUND request only; the client asked for whatever it
				// asked for and gets exactly what upstream sends back.
				r.Out.Header.Set("Accept-Encoding", "identity")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.Request == nil || !isTapAPIPath(resp.Request.URL.Path) {
				return nil
			}
			session := tapSessionOf(resp.Request.Header)
			logf("%s %s -> %d%s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode,
				tapFailureDetail(resp))
			if session == "" || !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
				tr.requestEnd(session)
				return nil
			}
			parser := newTapParser(session, func() int64 { return tr.nowMs() }, tr.event)
			resp.Body = &tapTeeBody{rc: resp.Body, parser: parser, onDone: func() { tr.requestEnd(session) }}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// No header/body detail — just the shape of the failure.
			logf("upstream error %s %s: %v", r.Method, r.URL.Path, err)
			tr.requestEnd(tapSessionOf(r.Header))
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(tapHealthPath, func(w http.ResponseWriter, r *http.Request) {
		tr.mu.Lock()
		n := len(tr.sessions)
		tr.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pid": os.Getpid(), "version": version, "upstream": upstream.String(), "sessions": n,
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isTapAPIPath(r.URL.Path) {
			tr.requestStart(tapSessionOf(r.Header))
		}
		proxy.ServeHTTP(w, r)
	})
	return mux
}

// runTapDaemon is `entire-tail tap start`. It binds the fixed port, publishes
// its state, and serves until signalled.
func runTapDaemon(home string, port int, getenv func(string) string, out io.Writer) error {
	if st, ok := readTapState(home); ok && tapProbe(st, 500*time.Millisecond) {
		fmt.Fprintf(out, "entire-tail tap: already running (pid %d, port %d)\n", st.Pid, st.Port)
		return nil
	}

	upstreamStr := tapUpstream(getenv)
	upstream, err := url.Parse(upstreamStr)
	if err != nil {
		return fmt.Errorf("bad upstream %q: %w", upstreamStr, err)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("cannot bind 127.0.0.1:%d (another process holds it?): %w", port, err)
	}
	actual := ln.Addr().(*net.TCPAddr).Port

	tr := newTapTracker(home, time.Now)
	// Start from an empty activity table on disk. The tracker's in-memory map is
	// empty at boot, so leaving the previous daemon's file in place would leave
	// consumers reading activity this daemon never observed — a session could read
	// as "recently active" from a table written before a restart, until the first
	// request happened to overwrite it. If the daemon is up, the table describes
	// only this daemon's lifetime; that's what makes "which session is generating"
	// a fact rather than a leftover.
	_ = writeJSONAtomic(tapActivePath(home), tapActive{
		Updated: time.Now().UnixNano() / 1e6, Sessions: map[string]tapSessionStatus{},
	})
	logf := func(format string, a ...any) {
		fmt.Fprintf(out, "entire-tail tap: "+format+"\n", a...)
	}
	srv := &http.Server{Handler: newTapHandler(upstream, tr, logf)}

	st := tapState{Pid: os.Getpid(), Port: actual, Upstream: upstreamStr, Version: version,
		Started: time.Now().Format(time.RFC3339)}
	if err := writeJSONAtomic(tapStatePath(home), st); err != nil {
		return fmt.Errorf("cannot publish daemon state: %w", err)
	}
	defer os.Remove(tapStatePath(home))

	logf("listening on 127.0.0.1:%d -> %s (pid %d)", actual, upstreamStr, os.Getpid())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logf("shutting down")
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// runTap dispatches `entire-tail tap <sub>`.
func runTap(args []string, home string, getenv func(string) string, out io.Writer) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	port := tapDefaultPort
	binary := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--port" && i+1 < len(args):
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("tap: invalid --port %q", args[i+1])
			}
			port, i = n, i+1
		case strings.HasPrefix(args[i], "--port="):
			n, err := strconv.Atoi(strings.TrimPrefix(args[i], "--port="))
			if err != nil {
				return fmt.Errorf("tap: invalid --port")
			}
			port = n
		case args[i] == "--binary" && i+1 < len(args):
			binary, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--binary="):
			binary = strings.TrimPrefix(args[i], "--binary=")
		}
	}

	switch sub {
	case "start":
		return runTapDaemon(home, port, getenv, out)
	case "status":
		st, ok := readTapState(home)
		if !ok {
			fmt.Fprintln(out, "entire-tail tap: not running (no daemon state)")
			return nil
		}
		alive := tapProbe(st, 500*time.Millisecond)
		fmt.Fprintf(out, "entire-tail tap: pid %d, port %d, upstream %s, started %s — %s\n",
			st.Pid, st.Port, st.Upstream, st.Started, map[bool]string{true: "listening", false: "NOT responding (stale state)"}[alive])
		if a, ok := readTapActive(home); ok {
			nowMs := time.Now().UnixNano() / 1e6
			for id, s := range a.Sessions {
				state := "idle"
				if tapGenerating(s, nowMs) {
					state = "GENERATING"
				}
				fmt.Fprintf(out, "  %s  %s  requests=%d\n", id, state, s.Requests)
			}
		}
		return nil
	case "stop":
		st, ok := readTapState(home)
		if !ok {
			fmt.Fprintln(out, "entire-tail tap: not running")
			return nil
		}
		if st.Pid > 0 {
			if err := syscall.Kill(st.Pid, syscall.SIGTERM); err != nil {
				return fmt.Errorf("tap stop: %w", err)
			}
		}
		fmt.Fprintf(out, "entire-tail tap: stopped (pid %d)\n", st.Pid)
		return nil
	case "install":
		bin, err := tapAgentBinary(binary, exec.LookPath)
		if err != nil {
			return fmt.Errorf("tap install: cannot resolve the binary to run: %w", err)
		}
		path, err := installTapAgent(home, bin, port, out)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "entire-tail tap: LaunchAgent written to %s\n", path)
		fmt.Fprintf(out, "  exec: %s tap start --port %d\n", bin, port)
		// Reload rather than load: an existing agent has to go before the new plist
		// takes effect, and this makes re-running install idempotent.
		_ = tapAgentUnload(path)
		if err := tapAgentLoad(path); err != nil {
			return err
		}
		if st, ok := tapAgentWait(home, 8*time.Second); ok {
			fmt.Fprintf(out, "entire-tail tap: listening on 127.0.0.1:%d (pid %d) — survives crashes and logins\n", st.Port, st.Pid)
			return nil
		}
		return fmt.Errorf("tap install: agent loaded but nothing is listening on 127.0.0.1:%d — see %s", port, tapAgentLogPath(home))
	case "uninstall":
		path := tapAgentPath(home)
		if err := tapAgentUnload(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		// KeepAlive is gone, but a daemon started by hand is still up; stop it too
		// so "uninstall" means the tap is actually off.
		if st, ok := readTapState(home); ok && st.Pid > 0 {
			_ = syscall.Kill(st.Pid, syscall.SIGTERM)
		}
		fmt.Fprintf(out, "entire-tail tap: LaunchAgent removed and stopped (%s)\n", path)
		fmt.Fprintln(out, "  New sessions launch unrouted; entire-tail keeps working without the tap.")
		fmt.Fprintln(out, "  Sessions ALREADY routed through it have its address baked in — restart those.")
		return nil
	}
	return fmt.Errorf("tap: unknown subcommand %q (want start|status|stop|install|uninstall)", sub)
}

const tapAgentLabel = "io.entire.entire-tail.tap"

func tapAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", tapAgentLabel+".plist")
}

func tapAgentLogPath(home string) string { return filepath.Join(tapDir(home), "daemon.log") }

// tapAgentPlist builds the LaunchAgent, pure so its contents are unit-tested.
//
// KeepAlive matters more here than for a normal helper: a routed session has the
// daemon's address baked in at launch, so an unattended death breaks it until the
// daemon returns. ThrottleInterval keeps a crash-loop from hammering — launchd's
// 1s default would spin on, say, a port that never frees.
func tapAgentPlist(bin string, port int, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>tap</string>
    <string>start</string>
    <string>--port</string>
    <string>%d</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, tapAgentLabel, bin, port, logPath, logPath)
}

// looksEphemeralBinary reports whether a path is one a LaunchAgent shouldn't be
// pinned to. A plist outlives the shell that wrote it, so pointing it at a git
// worktree (deleted when the branch is done) or a temp dir gives you a daemon
// that silently stops coming back — and because routing is decided per launch,
// the symptom is "the tap just doesn't work any more" rather than an error.
func looksEphemeralBinary(p string) bool {
	for _, frag := range []string{"/.claude/worktrees/", "/tmp/", "/private/tmp/", "/var/folders/"} {
		if strings.Contains(p, frag) {
			return true
		}
	}
	return false
}

// tapAgentBinary resolves what the plist should exec: an explicit --binary, else
// the installed `entire-tail` on PATH (which survives rebuilds), else this
// executable.
func tapAgentBinary(explicit string, lookPath func(string) (string, error)) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if p, err := lookPath("entire-tail"); err == nil {
		return p, nil
	}
	return os.Executable()
}

// installTapAgent writes the LaunchAgent and loads it, so `tap install` is the
// whole job rather than a file plus a copy-pasted launchctl line.
func installTapAgent(home, bin string, port int, out io.Writer) (string, error) {
	path := tapAgentPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tapDir(home), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tapAgentPlist(bin, port, tapAgentLogPath(home))), 0o644); err != nil {
		return "", err
	}
	if looksEphemeralBinary(bin) {
		fmt.Fprintf(out, "entire-tail tap: WARNING — the agent points at %s\n", bin)
		fmt.Fprintf(out, "  that path looks temporary (worktree or temp dir); when it goes away the\n")
		fmt.Fprintf(out, "  daemon stops coming back. Install entire-tail properly (./install.sh) and\n")
		fmt.Fprintf(out, "  re-run 'entire-tail tap install', or pass --binary <stable path>.\n")
	}
	return path, nil
}

// Indirection for the three side-effecting steps of `tap install`/`uninstall`, so
// tests can exercise the command without bootstrapping a real LaunchAgent into
// the developer's launchd (which, pointed at a test binary under KeepAlive, is a
// respawn loop — learned the hard way).
var (
	tapAgentLoad   = launchctlLoad
	tapAgentUnload = launchctlUnload
	tapAgentWait   = waitForTap
)

// launchctlLoad boots the agent into the user's GUI domain. `bootstrap` is the
// modern verb; `load -w` is kept as the fallback for older systems (and for the
// case where bootstrap rejects an already-loaded label).
func launchctlLoad(path string) error {
	uid := os.Getuid()
	if out, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), path).CombinedOutput(); err == nil {
		return nil
	} else if strings.Contains(string(out), "already") {
		return nil // already bootstrapped — not a failure
	}
	if out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// launchctlUnload removes the agent from the user's GUI domain. Both verbs are
// tried and a "not loaded" outcome is success — disabling must be idempotent.
func launchctlUnload(path string) error {
	uid := os.Getuid()
	if _, err := exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, tapAgentLabel)).CombinedOutput(); err == nil {
		return nil
	}
	if out, err := exec.Command("launchctl", "unload", path).CombinedOutput(); err != nil {
		s := strings.TrimSpace(string(out))
		if strings.Contains(s, "Could not find") || strings.Contains(s, "no such") || s == "" {
			return nil // wasn't loaded
		}
		return fmt.Errorf("launchctl unload failed: %v: %s", err, s)
	}
	return nil
}

// waitForTap polls until the daemon answers, so install/enable can report a fact
// rather than "probably started".
func waitForTap(home string, d time.Duration) (tapState, bool) {
	deadline := time.Now().Add(d)
	for {
		if st, ok := readTapState(home); ok && tapProbe(st, 300*time.Millisecond) {
			return st, true
		}
		if time.Now().After(deadline) {
			return tapState{}, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
