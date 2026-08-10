package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func tapTestTracker(t *testing.T) (*tapTracker, string) {
	t.Helper()
	home := t.TempDir()
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	return newTapTracker(home, func() time.Time { return now }), home
}

// fakeUpstream stands in for api.anthropic.com: it streams the captured SSE
// frames back with the flush behaviour a real stream has.
func fakeUpstream(t *testing.T, body []byte, contentType string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(200)
		for i := 0; i < len(body); i += 32 {
			_, _ = w.Write(body[i:min(i+32, len(body))])
			w.(http.Flusher).Flush()
		}
	}))
}

func TestTapHandlerTeesStreamAndPreservesBytes(t *testing.T) {
	stream := questionStream()
	up := fakeUpstream(t, stream, "text/event-stream")
	defer up.Close()
	upURL, _ := url.Parse(up.URL)

	tr, home := tapTestTracker(t)
	h := newTapHandler(upURL, tr, func(string, ...any) {})
	front := httptest.NewServer(h)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/v1/messages?beta=true", strings.NewReader(`{"stream":true}`))
	req.Header.Set("x-claude-code-session-id", "sess-tee")
	req.Header.Set("authorization", "Bearer super-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// The client must receive the upstream bytes untouched.
	if !bytes.Equal(got, stream) {
		t.Errorf("proxied body differs from upstream (%d vs %d bytes)", len(got), len(stream))
	}

	// …and the tap must have parsed the same stream into a sidecar.
	b, err := os.ReadFile(tapSidecarPath(home, "sess-tee"))
	if err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	var evs []tapEvent
	for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		var ev tapEvent
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("sidecar line %q: %v", ln, err)
		}
		evs = append(evs, ev)
	}
	if len(evs) != 3 {
		t.Fatalf("want 3 sidecar events, got %d", len(evs))
	}
	if _, ok := tapPending(evs); !ok {
		t.Error("teed events should reconstruct a pending question")
	}

	// The auth token must never reach disk.
	if bytes.Contains(b, []byte("super-secret")) {
		t.Error("sidecar contains the authorization header value")
	}
}

func TestTapHandlerTracksInFlight(t *testing.T) {
	// An upstream that blocks until released, so we can observe mid-request state.
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write(questionStream())
	}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)

	tr, home := tapTestTracker(t)
	front := httptest.NewServer(newTapHandler(upURL, tr, func(string, ...any) {}))
	defer front.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest("POST", front.URL+"/v1/messages", strings.NewReader("{}"))
		req.Header.Set("x-claude-code-session-id", "sess-live")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}()

	// Wait for the daemon to observe the request.
	nowMs := tr.nowMs()
	var gen bool
	for i := 0; i < 100 && !gen; i++ {
		if a, ok := readTapActive(home); ok {
			gen = tapGenerating(a.Sessions["sess-live"], nowMs)
		}
		if !gen {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !gen {
		t.Fatal("session should read as GENERATING while its request is in flight")
	}

	close(release)
	<-done

	// Once the body is drained, in-flight must fall back to zero.
	var idle bool
	for i := 0; i < 100 && !idle; i++ {
		if a, ok := readTapActive(home); ok {
			s := a.Sessions["sess-live"]
			idle = s.InFlight == 0 && s.Requests == 1
		}
		if !idle {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !idle {
		a, _ := readTapActive(home)
		t.Fatalf("in-flight should drop to 0 after the stream ends: %+v", a.Sessions["sess-live"])
	}
}

// Non-API traffic (oauth refresh, mcp_servers) must pass through untouched and
// never produce a sidecar.
func TestTapHandlerPassesThroughNonAPIPaths(t *testing.T) {
	up := fakeUpstream(t, []byte(`{"ok":true}`), "application/json")
	defer up.Close()
	upURL, _ := url.Parse(up.URL)
	tr, home := tapTestTracker(t)
	front := httptest.NewServer(newTapHandler(upURL, tr, func(string, ...any) {}))
	defer front.Close()

	req, _ := http.NewRequest("GET", front.URL+"/v1/mcp_servers?limit=10", nil)
	req.Header.Set("x-claude-code-session-id", "sess-x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != `{"ok":true}` {
		t.Errorf("body = %q", b)
	}
	if _, err := os.Stat(tapSidecarPath(home, "sess-x")); !os.IsNotExist(err) {
		t.Error("non-API traffic must not write a sidecar")
	}
}

// A non-streaming JSON reply on /v1/messages (stream:false) must not be parsed
// as SSE, and must still close out the in-flight count.
func TestTapHandlerNonStreamingAPIReply(t *testing.T) {
	up := fakeUpstream(t, []byte(`{"type":"message","content":[]}`), "application/json")
	defer up.Close()
	upURL, _ := url.Parse(up.URL)
	tr, home := tapTestTracker(t)
	front := httptest.NewServer(newTapHandler(upURL, tr, func(string, ...any) {}))
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-claude-code-session-id", "sess-json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	if _, err := os.Stat(tapSidecarPath(home, "sess-json")); !os.IsNotExist(err) {
		t.Error("a non-SSE reply must not write sidecar events")
	}
	a, ok := readTapActive(home)
	if !ok || a.Sessions["sess-json"].InFlight != 0 {
		t.Errorf("in-flight should be closed out: %+v", a.Sessions["sess-json"])
	}
}

// A 429 through the tap is upstream rate-limiting, not a proxy fault — the log
// line has to say which, and must still never leak request headers.
func TestTapFailureDetailAndRetryPassThrough(t *testing.T) {
	attempts := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("retry-after", "3")
			w.Header().Set("anthropic-ratelimit-requests-remaining", "0")
			w.Header().Set("request-id", "req_abc")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write(questionStream())
	}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)

	tr, _ := tapTestTracker(t)
	var logged []string
	front := httptest.NewServer(newTapHandler(upURL, tr, func(f string, a ...any) {
		logged = append(logged, fmt.Sprintf(f, a...))
	}))
	defer front.Close()

	// First call: the 429 must reach the client verbatim, so the SDK can retry.
	req, _ := http.NewRequest("POST", front.URL+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-claude-code-session-id", "sess-429")
	req.Header.Set("authorization", "Bearer super-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Errorf("status = %d, want the upstream 429 passed through", resp.StatusCode)
	}
	if resp.Header.Get("retry-after") != "3" {
		t.Error("retry-after must reach the client so the SDK can back off correctly")
	}

	all := strings.Join(logged, "\n")
	if !strings.Contains(all, "-> 429") {
		t.Errorf("log should record the status:\n%s", all)
	}
	for _, want := range []string{"retry-after=3", "anthropic-ratelimit-requests-remaining=0", "request-id=req_abc"} {
		if !strings.Contains(all, want) {
			t.Errorf("log should explain the refusal (%s):\n%s", want, all)
		}
	}
	if strings.Contains(all, "super-secret") || strings.Contains(all, "uthorization") {
		t.Errorf("request headers must never be logged:\n%s", all)
	}

	// The retry then succeeds and is teed normally.
	req2, _ := http.NewRequest("POST", front.URL+"/v1/messages", strings.NewReader("{}"))
	req2.Header.Set("x-claude-code-session-id", "sess-429")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	_, _ = io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("retry status = %d", resp2.StatusCode)
	}
	if _, err := os.Stat(tapSidecarPath(tr.home, "sess-429")); err != nil {
		t.Errorf("the successful retry should still be teed: %v", err)
	}
}

func TestTapFailureDetailQuietOnSuccess(t *testing.T) {
	ok := &http.Response{StatusCode: 200, Header: http.Header{"retry-after": {"3"}}}
	if got := tapFailureDetail(ok); got != "" {
		t.Errorf("a 2xx must add nothing to the log line, got %q", got)
	}
	bare := &http.Response{StatusCode: 500, Header: http.Header{}}
	if got := tapFailureDetail(bare); !strings.Contains(got, "no diagnostic headers") {
		t.Errorf("a failure with no headers should say so, got %q", got)
	}
}

func TestTapUpstreamResolution(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", nil, tapUpstreamHost},
		{"explicit override", map[string]string{"ENTIRE_TAIL_TAP_UPSTREAM": "https://gw.example.com/"}, "https://gw.example.com"},
		{"existing gateway", map[string]string{"ANTHROPIC_BASE_URL": "https://litellm.internal:4000"}, "https://litellm.internal:4000"},
		// Our own address leaking in from the shell must not make the daemon
		// proxy to itself.
		{"loopback ignored", map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:47391"}, tapUpstreamHost},
		{"localhost ignored", map[string]string{"ANTHROPIC_BASE_URL": "http://localhost:47391"}, tapUpstreamHost},
		{"override wins", map[string]string{
			"ENTIRE_TAIL_TAP_UPSTREAM": "https://a.example.com",
			"ANTHROPIC_BASE_URL":       "https://b.example.com",
		}, "https://a.example.com"},
	}
	for _, c := range cases {
		got := tapUpstream(func(k string) string { return c.env[k] })
		if got != c.want {
			t.Errorf("%s: tapUpstream = %q, want %q", c.name, got, c.want)
		}
	}
}

// tapBaseURL is the fail-open contract: no daemon -> empty string -> the
// launcher starts the session with no ANTHROPIC_BASE_URL at all.
func TestTapBaseURLFailsOpen(t *testing.T) {
	home := t.TempDir()
	if got := tapBaseURL(home); got != "" {
		t.Errorf("no state file: want \"\", got %q", got)
	}

	// Stale state pointing at a dead port.
	if err := writeJSONAtomic(tapStatePath(home), tapState{Pid: 999999, Port: 1}); err != nil {
		t.Fatal(err)
	}
	if got := tapBaseURL(home); got != "" {
		t.Errorf("stale state: want \"\", got %q", got)
	}

	// A live listener that is NOT our daemon (no/foreign pid) must be rejected.
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"pid":424242}`))
	}))
	defer foreign.Close()
	fu, _ := url.Parse(foreign.URL)
	port := 0
	_, _ = fmt.Sscanf(fu.Port(), "%d", &port)
	if err := writeJSONAtomic(tapStatePath(home), tapState{Pid: 111, Port: port}); err != nil {
		t.Fatal(err)
	}
	if got := tapBaseURL(home); got != "" {
		t.Errorf("foreign listener: want \"\", got %q", got)
	}

	// Matching pid -> usable.
	if err := writeJSONAtomic(tapStatePath(home), tapState{Pid: 424242, Port: port}); err != nil {
		t.Fatal(err)
	}
	if got := tapBaseURL(home); got != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Errorf("live daemon: got %q", got)
	}
}

func TestTapGeneratingIgnoresLeakedInFlight(t *testing.T) {
	nowMs := time.Now().UnixNano() / 1e6
	fresh := tapSessionStatus{InFlight: 1, LastStart: nowMs - 2000}
	if !tapGenerating(fresh, nowMs) {
		t.Error("a request started 2s ago is generating")
	}
	leaked := tapSessionStatus{InFlight: 1, LastStart: nowMs - tapInFlightStale.Milliseconds() - 1}
	if tapGenerating(leaked, nowMs) {
		t.Error("an in-flight count older than the stale bound must not read as generating")
	}
	if tapGenerating(tapSessionStatus{InFlight: 0, LastStart: nowMs}, nowMs) {
		t.Error("no in-flight request is not generating")
	}
}

func TestTapAgentPlist(t *testing.T) {
	p := tapAgentPlist("/usr/local/bin/entire-tail", 47555, "/home/u/.claude/entire-tail/tap/daemon.log")
	for _, want := range []string{
		"<key>Label</key><string>" + tapAgentLabel + "</string>",
		"<string>/usr/local/bin/entire-tail</string>",
		"<string>47555</string>",
		"<key>KeepAlive</key><true/>",
		"<key>RunAtLoad</key><true/>",
		// A crash loop must not hammer launchd's 1s default.
		"<key>ThrottleInterval</key><integer>10</integer>",
		"<string>/home/u/.claude/entire-tail/tap/daemon.log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
}

// A LaunchAgent outlives the shell that wrote it, so pinning it to a path that
// disappears (a git worktree, a temp build) yields a daemon that silently stops
// coming back.
func TestLooksEphemeralBinary(t *testing.T) {
	ephemeral := []string{
		"/Users/d/src/proj/.claude/worktrees/api-tap/entire-tail",
		"/tmp/entire-tail",
		"/private/tmp/build/entire-tail",
		"/var/folders/xf/T/go-build123/entire-tail",
	}
	stable := []string{
		"/Users/d/.local/bin/entire-tail",
		"/usr/local/bin/entire-tail",
		"/opt/homebrew/bin/entire-tail",
		"/Users/d/src/proj/entire-tail",
	}
	for _, p := range ephemeral {
		if !looksEphemeralBinary(p) {
			t.Errorf("%s should be flagged as temporary", p)
		}
	}
	for _, p := range stable {
		if looksEphemeralBinary(p) {
			t.Errorf("%s should NOT be flagged", p)
		}
	}
}

func TestTapAgentBinaryPrefersInstalled(t *testing.T) {
	found := func(string) (string, error) { return "/Users/d/.local/bin/entire-tail", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }

	// An explicit --binary always wins, and is absolutised.
	got, err := tapAgentBinary("/opt/bin/entire-tail", found)
	if err != nil || got != "/opt/bin/entire-tail" {
		t.Errorf("explicit: got %q, %v", got, err)
	}
	// Otherwise the installed copy on PATH, which survives rebuilds of a checkout.
	if got, _ = tapAgentBinary("", found); got != "/Users/d/.local/bin/entire-tail" {
		t.Errorf("PATH: got %q", got)
	}
	// Last resort: this executable (whatever the test binary is).
	got, err = tapAgentBinary("", missing)
	if err != nil || got == "" {
		t.Errorf("fallback: got %q, %v", got, err)
	}
}

func TestInstallTapAgentWarnsOnEphemeralPath(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	worktree := "/Users/d/src/p/.claude/worktrees/x/entire-tail"
	if _, err := installTapAgent(home, worktree, 47391, &out); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(out.String(), "WARNING") || !strings.Contains(out.String(), worktree) {
		t.Errorf("expected a warning naming the path, got:\n%s", out.String())
	}

	out.Reset()
	if _, err := installTapAgent(home, "/usr/local/bin/entire-tail", 47391, &out); err != nil {
		t.Fatalf("install: %v", err)
	}
	if strings.Contains(out.String(), "WARNING") {
		t.Errorf("a stable path must not warn, got:\n%s", out.String())
	}
	// The plist is written either way, pinned to the binary it was told about.
	b, err := os.ReadFile(tapAgentPath(home))
	if err != nil {
		t.Fatalf("plist: %v", err)
	}
	if !strings.Contains(string(b), "/usr/local/bin/entire-tail") {
		t.Errorf("plist should exec the resolved binary:\n%s", b)
	}
}

// stubLaunchd replaces the three side-effecting steps of install/uninstall, so
// the test drives the command without touching the real launchd.
func stubLaunchd(t *testing.T) (loaded *bool, unloads *int) {
	t.Helper()
	l, u := false, 0
	origLoad, origUnload, origWait := tapAgentLoad, tapAgentUnload, tapAgentWait
	tapAgentLoad = func(string) error { l = true; return nil }
	tapAgentUnload = func(string) error { u++; return nil }
	tapAgentWait = func(home string, _ time.Duration) (tapState, bool) {
		return tapState{Pid: 4242, Port: 47555}, true
	}
	t.Cleanup(func() { tapAgentLoad, tapAgentUnload, tapAgentWait = origLoad, origUnload, origWait })
	return &l, &u
}

func TestRunTapStatusAndAgentInstall(t *testing.T) {
	home := t.TempDir()
	loaded, unloads := stubLaunchd(t)
	var out bytes.Buffer
	if err := runTap([]string{"status"}, home, func(string) string { return "" }, &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Errorf("status with no daemon = %q", out.String())
	}

	out.Reset()
	if err := runTap([]string{"install", "--port", "47555", "--binary", "/usr/local/bin/entire-tail"}, home, func(string) string { return "" }, &out); err != nil {
		t.Fatalf("install: %v", err)
	}
	plist, err := os.ReadFile(tapAgentPath(home))
	if err != nil {
		t.Fatalf("plist: %v", err)
	}
	for _, want := range []string{"<key>KeepAlive</key><true/>", "<string>47555</string>", tapAgentLabel, "/usr/local/bin/entire-tail"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist missing %q", want)
		}
	}
	// install must actually load it — writing a file and telling the user to run
	// launchctl themselves was the old, half-done behaviour.
	if !*loaded {
		t.Error("install should load the agent")
	}
	// …and unload first, so re-running install is idempotent rather than "already
	// bootstrapped".
	if *unloads != 1 {
		t.Errorf("install should unload before loading (got %d unloads)", *unloads)
	}
	if !strings.Contains(out.String(), "listening") {
		t.Errorf("install should confirm it's listening:\n%s", out.String())
	}

	out.Reset()
	if err := runTap([]string{"uninstall"}, home, func(string) string { return "" }, &out); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(tapAgentPath(home)); !os.IsNotExist(err) {
		t.Error("uninstall should remove the plist")
	}
	// The fallback promise, in the words the user reads.
	if !strings.Contains(out.String(), "keeps working without the tap") {
		t.Errorf("uninstall should say entire-tail still works:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ALREADY routed") {
		t.Errorf("uninstall should warn about live routed sessions:\n%s", out.String())
	}

	if err := runTap([]string{"bogus"}, home, func(string) string { return "" }, &out); err == nil {
		t.Error("unknown subcommand should error")
	}
}

// install must fail loudly when the agent loads but nothing answers — silently
// reporting success would leave the user believing the tap is on.
func TestRunTapInstallReportsDeadAgent(t *testing.T) {
	home := t.TempDir()
	origLoad, origUnload, origWait := tapAgentLoad, tapAgentUnload, tapAgentWait
	tapAgentLoad = func(string) error { return nil }
	tapAgentUnload = func(string) error { return nil }
	tapAgentWait = func(string, time.Duration) (tapState, bool) { return tapState{}, false }
	t.Cleanup(func() { tapAgentLoad, tapAgentUnload, tapAgentWait = origLoad, origUnload, origWait })

	var out bytes.Buffer
	err := runTap([]string{"install", "--binary", "/usr/local/bin/entire-tail"}, home, func(string) string { return "" }, &out)
	if err == nil {
		t.Fatal("install should error when nothing ends up listening")
	}
	if !strings.Contains(err.Error(), "nothing is listening") {
		t.Errorf("error should name the symptom: %v", err)
	}
}
