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

// newTapHandler builds the proxy. logf receives one line per request — method,
// path, status only, NEVER headers.
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
			logf("%s %s -> %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode)
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
		path, err := installTapAgent(home, port)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "entire-tail tap: LaunchAgent written to %s\n", path)
		fmt.Fprintf(out, "  load it with:  launchctl load -w %s\n", path)
		return nil
	case "uninstall":
		path := tapAgentPath(home)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintf(out, "entire-tail tap: LaunchAgent removed (%s)\n", path)
		fmt.Fprintf(out, "  unload it with:  launchctl unload %s\n", path)
		return nil
	}
	return fmt.Errorf("tap: unknown subcommand %q (want start|status|stop|install|uninstall)", sub)
}

const tapAgentLabel = "io.entire.entire-tail.tap"

func tapAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", tapAgentLabel+".plist")
}

// installTapAgent writes a KeepAlive LaunchAgent so the daemon comes back if it
// crashes. KeepAlive matters more here than for a normal helper: sessions
// launched through the tap have its address baked in, so an unattended death
// would break them until it returns.
func installTapAgent(home string, port int) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := tapAgentPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, tapAgentLabel, exe, port, filepath.Join(tapDir(home), "daemon.log"), filepath.Join(tapDir(home), "daemon.log"))

	if err := os.MkdirAll(tapDir(home), 0o700); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(plist), 0o644)
}
