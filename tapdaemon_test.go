package main

import (
	"bytes"
	"encoding/json"
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

func TestRunTapStatusAndAgentInstall(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	if err := runTap([]string{"status"}, home, func(string) string { return "" }, &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Errorf("status with no daemon = %q", out.String())
	}

	out.Reset()
	if err := runTap([]string{"install", "--port", "47555"}, home, func(string) string { return "" }, &out); err != nil {
		t.Fatalf("install: %v", err)
	}
	plist, err := os.ReadFile(tapAgentPath(home))
	if err != nil {
		t.Fatalf("plist: %v", err)
	}
	for _, want := range []string{"<key>KeepAlive</key><true/>", "<string>47555</string>", tapAgentLabel} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist missing %q", want)
		}
	}

	out.Reset()
	if err := runTap([]string{"uninstall"}, home, func(string) string { return "" }, &out); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(tapAgentPath(home)); !os.IsNotExist(err) {
		t.Error("uninstall should remove the plist")
	}

	if err := runTap([]string{"bogus"}, home, func(string) string { return "" }, &out); err == nil {
		t.Error("unknown subcommand should error")
	}
}
