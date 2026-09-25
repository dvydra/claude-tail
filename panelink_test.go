package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// alive stubs the pid liveness check with a fixed set.
func aliveSet(pids ...int) func(int) bool {
	set := map[int]bool{}
	for _, p := range pids {
		set[p] = true
	}
	return func(p int) bool { return set[p] }
}

func TestLinkPartners(t *testing.T) {
	// two tails, two claudes, one session shared by a tail and a claude
	panes := map[string]paneEntry{
		"TAIL-A": {Follow: "sess-1", Pid: 10},
		"TAIL-B": {Follow: "sess-2", Pid: 11},
		"TAIL-C": {Follow: "sess-1", Pid: 12}, // second tail on the same session
	}
	claudes := map[string]string{
		"CLAUDE-1": "sess-1",
		"CLAUDE-2": "sess-2",
	}
	cases := []struct {
		name    string
		focused string
		want    []string
	}{
		{"tail to its claude", "TAIL-A", []string{"CLAUDE-1"}},
		{"claude to both its tails", "CLAUDE-1", []string{"TAIL-A", "TAIL-C"}},
		{"other tail to other claude", "TAIL-B", []string{"CLAUDE-2"}},
		{"claude with one tail", "CLAUDE-2", []string{"TAIL-B"}},
		{"unknown pane", "SOMETHING-ELSE", nil},
		{"empty focus", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := linkPartners(tc.focused, panes, claudes); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("linkPartners(%q) = %v, want %v", tc.focused, got, tc.want)
			}
		})
	}
}

// A tail following a session with no running claude has no partner, and a
// claude whose session nothing follows has none either. Both are silent.
func TestLinkPartnersNoCounterpart(t *testing.T) {
	panes := map[string]paneEntry{"TAIL-A": {Follow: "sess-gone", Pid: 10}}
	claudes := map[string]string{"CLAUDE-9": "sess-unwatched"}
	if got := linkPartners("TAIL-A", panes, claudes); got != nil {
		t.Errorf("tail with dead session: got %v, want nil", got)
	}
	if got := linkPartners("CLAUDE-9", panes, claudes); got != nil {
		t.Errorf("claude with no tail: got %v, want nil", got)
	}
}

// A pane entry outlives the process that wrote it (a SIGKILLed tail leaves its
// file behind), so the pid is the truth — same rule live.go applies to Claude
// Code's own session registry.
func TestPrunePanes(t *testing.T) {
	panes := map[string]paneEntry{
		"TAIL-A": {Follow: "sess-1", Pid: 10},
		"TAIL-B": {Follow: "sess-2", Pid: 11},
	}
	got := prunePanes(panes, aliveSet(10))
	want := map[string]paneEntry{"TAIL-A": {Follow: "sess-1", Pid: 10}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prunePanes = %v, want %v", got, want)
	}
}

func TestPaneRegistryRoundTrip(t *testing.T) {
	home := t.TempDir()
	if err := writePaneEntry(home, "TAIL-A", paneEntry{Follow: "sess-1", Pid: 10}); err != nil {
		t.Fatalf("writePaneEntry: %v", err)
	}
	if err := writePaneEntry(home, "TAIL-B", paneEntry{Follow: "sess-2", Pid: 11}); err != nil {
		t.Fatalf("writePaneEntry: %v", err)
	}
	got := readPaneRegistry(home)
	want := map[string]paneEntry{
		"TAIL-A": {Follow: "sess-1", Pid: 10},
		"TAIL-B": {Follow: "sess-2", Pid: 11},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readPaneRegistry = %v, want %v", got, want)
	}

	// A lineage fork rewrites the entry in place rather than adding one.
	if err := writePaneEntry(home, "TAIL-A", paneEntry{Follow: "sess-forked", Pid: 10}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got := readPaneRegistry(home); got["TAIL-A"].Follow != "sess-forked" || len(got) != 2 {
		t.Errorf("after rewrite = %v, want TAIL-A following sess-forked and 2 entries", got)
	}

	removePaneEntry(home, "TAIL-A")
	if got := readPaneRegistry(home); len(got) != 1 || got["TAIL-B"].Pid != 11 {
		t.Errorf("after remove = %v, want only TAIL-B", got)
	}
}

// A registry dir that doesn't exist, and junk inside one that does, are both
// "no panes" — a viewer must never fail because its own sidecar is nonsense.
func TestPaneRegistryTolerant(t *testing.T) {
	home := t.TempDir()
	if got := readPaneRegistry(home); len(got) != 0 {
		t.Errorf("missing dir = %v, want empty", got)
	}
	dir := paneRegistryDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "BROKEN.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePaneEntry(home, "GOOD", paneEntry{Follow: "s", Pid: 1}); err != nil {
		t.Fatal(err)
	}
	got := readPaneRegistry(home)
	if len(got) != 1 || got["GOOD"].Follow != "s" {
		t.Errorf("readPaneRegistry with junk = %v, want just GOOD", got)
	}
}

func TestLinkScript(t *testing.T) {
	s := linkScript("FOCUS-1", []string{"PARTNER-1", "PARTNER-2"})
	for _, want := range []string{`"FOCUS-1"`, `"PARTNER-1"`, `"PARTNER-2"`, "select", "iTerm2"} {
		if !strings.Contains(s, want) {
			t.Errorf("linkScript missing %q:\n%s", want, s)
		}
	}
	// The same-window skip is what keeps the 3-pane workspace a no-op; without
	// it, selecting the partner's tab would yank you off the tab you just chose.
	if !strings.Contains(s, "focusWinID") {
		t.Errorf("linkScript has no same-window guard:\n%s", s)
	}
}

// Session ids are UUIDs, but the script builder must not be the place that
// assumes it — a quote or a backslash in an id has to come back out escaped.
func TestLinkScriptEscapes(t *testing.T) {
	s := linkScript(`a"b`, []string{`c\d`})
	if strings.Contains(s, `"a"b"`) {
		t.Errorf("unescaped quote reached the script:\n%s", s)
	}
	if !strings.Contains(s, `a\"b`) || !strings.Contains(s, `c\\d`) {
		t.Errorf("escaping wrong:\n%s", s)
	}
}

func TestShouldOfferPaneLink(t *testing.T) {
	ok := paneLinkOfferInputs{isTTY: true, isSupported: true, hasPair: true}
	if !shouldOfferPaneLink(ok) {
		t.Errorf("clean eligible run: want offer")
	}
	cases := []struct {
		name string
		mut  func(*paneLinkOfferInputs)
	}{
		{"not a tty", func(g *paneLinkOfferInputs) { g.isTTY = false }},
		{"unsupported agent", func(g *paneLinkOfferInputs) { g.isSupported = false }},
		{"no linkable pair", func(g *paneLinkOfferInputs) { g.hasPair = false }},
		{"choice recorded", func(g *paneLinkOfferInputs) { g.choiceRecorded = true }},
		{"flag suppressed", func(g *paneLinkOfferInputs) { g.noPaneLink = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := ok
			tc.mut(&g)
			if shouldOfferPaneLink(g) {
				t.Errorf("%s: want no offer", tc.name)
			}
		})
	}
}

// The daemon and the registry only exist once the user has said yes. Off or
// unanswered writes nothing and spawns nothing.
func TestPaneLinkEnabled(t *testing.T) {
	home := t.TempDir()
	if paneLinkEnabled(home) {
		t.Errorf("unanswered: want disabled")
	}
	recordPaneLinkChoice(home, "no")
	if paneLinkEnabled(home) {
		t.Errorf("declined: want disabled")
	}
	recordPaneLinkChoice(home, "yes")
	if !paneLinkEnabled(home) {
		t.Errorf("accepted: want enabled")
	}
}

// A daemon that is up is not a daemon that works: with iTerm's Python API off,
// the watcher restarts a child that can never connect. Reporting that as
// "watching" is the bug this distinction exists to prevent.
func TestPaneLinkLabel(t *testing.T) {
	cases := []struct {
		enabled, running, connected bool
		want                        string
	}{
		{false, false, false, "off"},
		{false, true, true, "off"}, // declined wins over whatever is running
		{true, true, true, "watching"},
		{true, true, false, "not connected — is iTerm's Python API on?"},
		{true, false, false, "enabled, not running"},
	}
	for _, tc := range cases {
		if got := paneLinkLabel(tc.enabled, tc.running, tc.connected); got != tc.want {
			t.Errorf("paneLinkLabel(%v, %v, %v) = %q, want %q",
				tc.enabled, tc.running, tc.connected, got, tc.want)
		}
	}
}

// A version-managed interpreter can vanish under a running daemon — the same
// hazard looksEphemeralBinary guards against in the tap plist.
func TestPickPython(t *testing.T) {
	cases := []struct {
		name  string
		cands []string
		exist map[string]bool
		want  string
	}{
		{
			"prefers homebrew over a mise shim",
			[]string{"/Users/x/.local/share/mise/installs/python/3.12/bin/python3", "/opt/homebrew/bin/python3"},
			map[string]bool{"/Users/x/.local/share/mise/installs/python/3.12/bin/python3": true, "/opt/homebrew/bin/python3": true},
			"/opt/homebrew/bin/python3",
		},
		{
			"falls back to system python",
			[]string{"/usr/bin/python3"},
			map[string]bool{"/usr/bin/python3": true},
			"/usr/bin/python3",
		},
		{
			"a managed python is better than none",
			[]string{"/Users/x/.pyenv/versions/3.12/bin/python3"},
			map[string]bool{"/Users/x/.pyenv/versions/3.12/bin/python3": true},
			"/Users/x/.pyenv/versions/3.12/bin/python3",
		},
		{"nothing usable", []string{"/nope/python3"}, map[string]bool{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickPython(tc.cands, func(p string) bool { return tc.exist[p] })
			if got != tc.want {
				t.Errorf("pickPython = %q, want %q", got, tc.want)
			}
		})
	}
}
