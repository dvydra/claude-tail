package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A real registry payload, copied from a running claude (2.1.273).
const liveFixture = `{"pid":86544,"sessionId":"21c1a476-e137-4951-8a9a-1bb471096870","cwd":"/Users/dvydra/src/dvydra/claude-tail","startedAt":1789599145261,"procStart":"Wed Sep 16 22:52:24 2026","version":"2.1.273","peerProtocol":1,"peerFeatures":["notify_idle"],"kind":"interactive","entrypoint":"cli","pidDomain":"darwin","messagingSocketPath":"/tmp/cc-socks/86544.sock","name":"claude-tail-b3","nameSource":"derived","nameSince":1789599145261,"status":"busy","updatedAt":1789599205680,"statusUpdatedAt":1789599205680}`

func TestParseLiveSession(t *testing.T) {
	s, ok := parseLiveSession([]byte(liveFixture))
	if !ok {
		t.Fatal("valid payload rejected")
	}
	if s.PID != 86544 {
		t.Errorf("PID = %d, want 86544", s.PID)
	}
	if s.SessionID != "21c1a476-e137-4951-8a9a-1bb471096870" {
		t.Errorf("SessionID = %q", s.SessionID)
	}
	if s.Cwd != "/Users/dvydra/src/dvydra/claude-tail" {
		t.Errorf("Cwd = %q", s.Cwd)
	}
	if s.Name != "claude-tail-b3" || s.Status != "busy" || s.Version != "2.1.273" {
		t.Errorf("name/status/version = %q/%q/%q", s.Name, s.Status, s.Version)
	}
	if s.Kind != "interactive" || s.Entrypoint != "cli" {
		t.Errorf("kind/entrypoint = %q/%q", s.Kind, s.Entrypoint)
	}
	if s.SocketPath != "/tmp/cc-socks/86544.sock" {
		t.Errorf("SocketPath = %q", s.SocketPath)
	}
	// Raw is kept verbatim for the `j` toggle.
	if s.Raw != liveFixture {
		t.Errorf("Raw not preserved verbatim")
	}
}

func TestParseLiveSessionRejectsIncomplete(t *testing.T) {
	cases := map[string]string{
		"garbage":       `not json at all`,
		"empty":         ``,
		"no pid":        `{"sessionId":"21c1a476-e137-4951-8a9a-1bb471096870","cwd":"/tmp"}`,
		"zero pid":      `{"pid":0,"sessionId":"21c1a476-e137-4951-8a9a-1bb471096870"}`,
		"no session id": `{"pid":123,"cwd":"/tmp"}`,
		// Half-written file: the registry is rewritten in place, so a read can
		// land mid-write. Must be skipped, not rendered as a blank row.
		"truncated": liveFixture[:80],
	}
	for name, payload := range cases {
		if _, ok := parseLiveSession([]byte(payload)); ok {
			t.Errorf("%s: accepted %q", name, payload)
		}
	}
}

// writeRegistry drops <pid>.json files into a temp sessions dir.
func writeRegistry(t *testing.T, dir string, payloads map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range payloads {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func entry(pid int, id, name, status string, statusAt int64) string {
	at := strconv.FormatInt(statusAt, 10)
	return `{"pid":` + strconv.Itoa(pid) + `,"sessionId":"` + id + `","cwd":"/tmp/x","name":"` + name +
		`","status":"` + status + `","version":"2.1.273","kind":"interactive","entrypoint":"cli",` +
		`"startedAt":1000000,"updatedAt":` + at + `,"statusUpdatedAt":` + at + `}`
}

func TestCollectLiveSessionsDropsDeadPIDs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	writeRegistry(t, dir, map[string]string{
		"100.json": entry(100, "aaaaaaaa-0000-0000-0000-000000000000", "alive", "idle", 5000),
		// A SIGKILLed claude leaves its file behind; only the pid check catches it.
		"200.json": entry(200, "bbbbbbbb-0000-0000-0000-000000000000", "ghost", "busy", 9000),
		// Not a registry entry at all (the .key files live here too).
		"100.abc.key": "binary junk",
	})

	got := collectLiveSessions([]liveRoot{{Dir: dir}}, func(pid int) bool { return pid == 100 })
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1: %+v", len(got), got)
	}
	if got[0].Name != "alive" {
		t.Errorf("kept %q, want the live one", got[0].Name)
	}
}

func TestCollectLiveSessionsBusyFirstThenRecent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	writeRegistry(t, dir, map[string]string{
		"1.json": entry(1, "aaaaaaaa-0000-0000-0000-000000000000", "idle-old", "idle", 1000),
		"2.json": entry(2, "bbbbbbbb-0000-0000-0000-000000000000", "idle-new", "idle", 8000),
		"3.json": entry(3, "cccccccc-0000-0000-0000-000000000000", "busy-old", "busy", 2000),
	})
	got := collectLiveSessions([]liveRoot{{Dir: dir}}, func(int) bool { return true })

	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	want := []string{"busy-old", "idle-new", "idle-old"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", names, want)
	}
}

func TestCollectLiveSessionsSpansProfiles(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, ".claude", "sessions")
	personal := filepath.Join(base, ".claude-personal", "sessions")
	writeRegistry(t, work, map[string]string{
		"1.json": entry(1, "aaaaaaaa-0000-0000-0000-000000000000", "work", "idle", 1000),
	})
	writeRegistry(t, personal, map[string]string{
		"2.json": entry(2, "bbbbbbbb-0000-0000-0000-000000000000", "mine", "idle", 2000),
	})

	got := collectLiveSessions([]liveRoot{
		{Dir: work},
		{Dir: personal, Profile: personalProfile},
	}, func(int) bool { return true })

	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	if got[0].Name != "mine" || got[0].Profile != personalProfile {
		t.Errorf("first = %q/%q, want mine/%s", got[0].Name, got[0].Profile, personalProfile)
	}
	if got[1].Profile != "" {
		t.Errorf("work session carries profile %q, want empty", got[1].Profile)
	}
}

func TestCollectLiveSessionsMissingDir(t *testing.T) {
	got := collectLiveSessions([]liveRoot{{Dir: filepath.Join(t.TempDir(), "nope")}}, func(int) bool { return true })
	if got != nil {
		t.Errorf("missing registry dir returned %v, want nil", got)
	}
}

func TestUptime(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "0s"},
		{9, "9s"},
		{59, "59s"},
		{64, "1m 04s"},
		{3599, "59m 59s"},
		{3600, "1h 00m"},
		{58020, "16h 07m"},
		{86400, "1d 00h"},
		{183000, "2d 02h"},
		{-5, "0s"}, // clock skew must not print a negative
	}
	for _, c := range cases {
		if got := uptime(c.secs); got != c.want {
			t.Errorf("uptime(%d) = %q, want %q", c.secs, got, c.want)
		}
	}
}

func TestWorktreeLabel(t *testing.T) {
	cases := []struct {
		cwd, want string
	}{
		{"/Users/d/src/repo/.claude/worktrees/live-view", "live-view"},
		// A session started in a subdirectory of the worktree keeps the subpath —
		// it's where the agent actually is.
		{"/Users/d/src/repo/.claude/worktrees/live-view/sub/dir", "live-view/sub/dir"},
		{"/Users/d/src/repo", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := worktreeLabel(c.cwd); got != c.want {
			t.Errorf("worktreeLabel(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

func TestTailGlanceDropsBlanksAndWindows(t *testing.T) {
	lines := []string{"one", "", "two", "   ", "\x1b[0m", "three", "four"}
	got := tailGlance(lines, 3)
	want := []string{"two", "three", "four"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("tailGlance = %v, want %v", got, want)
	}
	// The renderer's turn seams are chrome; three of them would fill the glance.
	withMarkers := []string{
		"body one",
		"\x1b[2m  ⋯ 2026-09-17 09:30:42\x1b[0m",
		"body two",
		"  ⋯ 2026-09-17 09:30:58",
		"body three",
	}
	got = tailGlance(withMarkers, 6)
	want = []string{"body one", "body two", "body three"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("turn markers survived: %v", got)
	}
	// A real body line that opens with the same rune is content, not a seam.
	long := "⋯ and then the agent kept going for a good while after that point"
	if got := tailGlance([]string{long}, 4); len(got) != 1 {
		t.Errorf("a long ⋯-leading body line was dropped: %v", got)
	}
	if got := tailGlance(lines, 0); got != nil {
		t.Errorf("tailGlance(_, 0) = %v, want nil", got)
	}
	if got := tailGlance([]string{"", "  "}, 4); len(got) != 0 {
		t.Errorf("all-blank input returned %v", got)
	}
}

// liveBlockLines is the whole visual contract; assert it with no theme so the
// output is plain text (every Theme ANSI field is "" in the zero value).
func TestLiveBlockLines(t *testing.T) {
	s, _ := parseLiveSession([]byte(liveFixture))
	// startedAt 1789599145261ms, statusUpdatedAt 1789599205680ms.
	now := int64(1789599265) // 2m00s after start, 60s after the status flip
	got := liveBlockLines(s, []string{"⏺ hello", "⎿ world"}, liveBlockOpts{
		Now: now, Home: "/Users/dvydra", Width: 78,
	})
	joined := strings.Join(got, "\n")

	for _, want := range []string{
		"claude-tail-b3",
		"busy",
		"pid 86544",
		"v2.1.273",
		"21c1a476-e137-4951-8a9a-1bb471096870",
		"~/src/dvydra/claude-tail",
		"up 2m 00s",
		"/tmp/cc-socks/86544.sock",
		"⏺ hello",
		"⎿ world",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("block missing %q:\n%s", want, joined)
		}
	}
	// The cwd is not a worktree, so no worktree line.
	if strings.Contains(joined, "worktree of") {
		t.Errorf("non-worktree cwd produced a worktree line:\n%s", joined)
	}
	for i, l := range got {
		if visWidth(l) > 78 {
			t.Errorf("line %d overflows width 78 (%d): %q", i, visWidth(l), l)
		}
	}
}

// A narrow pane cuts lines mid-escape; every line must still end with the
// colour turned off, or dim bleeds to end-of-line and into the next row.
func TestLiveBlockLinesNarrowWidthClosesColour(t *testing.T) {
	s, _ := parseLiveSession([]byte(liveFixture))
	theme := Theme{DimANSI: "\x1b[2m", ClaudeANSI: "\x1b[38;5;208m"}
	for _, w := range []int{20, 34, 51, 78} {
		got := liveBlockLines(s, []string{"\x1b[2ma dim tail line that is quite long indeed\x1b[0m"},
			liveBlockOpts{Now: 1789599265, Home: "/Users/dvydra", Width: w, Theme: theme, ShowJSON: true})
		for i, l := range got {
			if visWidth(l) > w {
				t.Errorf("width %d line %d overflows (%d)", w, i, visWidth(l))
			}
			if !strings.HasSuffix(l, "\x1b[0m") {
				t.Errorf("width %d line %d does not end reset: %q", w, i, l)
			}
		}
	}
}

func TestLiveBlockLinesShowsWorktree(t *testing.T) {
	s, _ := parseLiveSession([]byte(liveFixture))
	s.Cwd = "/Users/dvydra/src/entirehq/entiredb/.claude/worktrees/ci-push-a-ref"
	s.Branch = "worktree-ci-push-a-ref"
	got := strings.Join(liveBlockLines(s, nil, liveBlockOpts{
		Now: 1789599265, Home: "/Users/dvydra", Width: 100,
	}), "\n")
	// The checkout is named once and the worktree beside it — printing the full
	// worktree path AND a "worktree of <checkout>" line says it twice.
	if !strings.Contains(got, "~/src/entirehq/entiredb  ⑂ ci-push-a-ref  ⎇ worktree-ci-push-a-ref") {
		t.Errorf("folded worktree line missing:\n%s", got)
	}
	if strings.Contains(got, "entiredb/.claude/worktrees") {
		t.Errorf("worktree cwd repeated in full:\n%s", got)
	}
}

func TestLiveBlockLinesRawJSON(t *testing.T) {
	s, _ := parseLiveSession([]byte(liveFixture))
	plain := strings.Join(liveBlockLines(s, nil, liveBlockOpts{Now: 1789599265, Width: 120}), "\n")
	if strings.Contains(plain, "peerProtocol") {
		t.Errorf("raw json leaked without ShowJSON:\n%s", plain)
	}
	withJSON := strings.Join(liveBlockLines(s, nil, liveBlockOpts{Now: 1789599265, Width: 120, ShowJSON: true}), "\n")
	if !strings.Contains(withJSON, "peerProtocol") {
		t.Errorf("ShowJSON did not include the raw payload:\n%s", withJSON)
	}
}

func TestLiveBlockLinesIdleUsesStatusAge(t *testing.T) {
	s, _ := parseLiveSession([]byte(liveFixture))
	s.Status = "idle"
	got := strings.Join(liveBlockLines(s, nil, liveBlockOpts{Now: 1789600105, Width: 100}), "\n")
	if !strings.Contains(got, "idle 15m 00s") {
		t.Errorf("idle age missing:\n%s", got)
	}
}

func TestRenderLiveEmptyStates(t *testing.T) {
	got := renderLive(liveUI{Width: 60, Height: 20})
	if !strings.Contains(got, "no live claude sessions") {
		t.Errorf("empty state missing:\n%s", got)
	}
	got = renderLive(liveUI{Width: 60, Height: 20, NoRegistry: true})
	if !strings.Contains(got, "2.1.273") {
		t.Errorf("no-registry state should name the version requirement:\n%s", got)
	}
}

func TestUpdateLiveMovesCursorAndClamps(t *testing.T) {
	ui := liveUI{Sessions: make([]liveSession, 3), Height: 40}
	ui = updateLive(ui, kDown, 0)
	ui = updateLive(ui, kDown, 0)
	if ui.Cursor != 2 {
		t.Errorf("cursor = %d, want 2", ui.Cursor)
	}
	ui = updateLive(ui, kDown, 0) // past the end
	if ui.Cursor != 2 {
		t.Errorf("cursor ran past the last session: %d", ui.Cursor)
	}
	ui = updateLive(ui, kUp, 0)
	ui = updateLive(ui, kUp, 0)
	ui = updateLive(ui, kUp, 0) // past the start
	if ui.Cursor != 0 {
		t.Errorf("cursor ran past the first session: %d", ui.Cursor)
	}
}

func TestUpdateLiveKeys(t *testing.T) {
	base := liveUI{Sessions: make([]liveSession, 2), TailN: liveTailDefault, Height: 40}

	if ui := updateLive(base, kRune, 'q'); !ui.Quit {
		t.Error("q did not quit")
	}
	if ui := updateLive(base, kEsc, 0); !ui.Quit {
		t.Error("Esc did not quit")
	}
	if ui := updateLive(base, kRune, 'j'); !ui.ShowJSON {
		t.Error("j did not toggle raw json on")
	}
	on := updateLive(base, kRune, 'j')
	if ui := updateLive(on, kRune, 'j'); ui.ShowJSON {
		t.Error("j did not toggle raw json back off")
	}
	if ui := updateLive(base, kRune, 'r'); !ui.Refresh {
		t.Error("r did not request a refresh")
	}
	if ui := updateLive(base, kEnter, 0); ui.Result != treeWorkspace {
		t.Errorf("Enter result = %v, want treeWorkspace", ui.Result)
	}
	if ui := updateLive(base, kRune, 't'); ui.Result != treeChosen {
		t.Errorf("t result = %v, want treeChosen", ui.Result)
	}
	if ui := updateLive(base, kRune, '+'); ui.TailN != liveTailDefault+2 {
		t.Errorf("+ TailN = %d, want %d", ui.TailN, liveTailDefault+2)
	}
	if ui := updateLive(base, kRune, '-'); ui.TailN != liveTailDefault-2 {
		t.Errorf("- TailN = %d, want %d", ui.TailN, liveTailDefault-2)
	}
	// Tail lines never go negative.
	small := liveUI{Sessions: make([]liveSession, 1), TailN: 0, Height: 40}
	if ui := updateLive(small, kRune, '-'); ui.TailN != 0 {
		t.Errorf("- drove TailN to %d, want 0", ui.TailN)
	}
}

// With nothing live there is no session to act on, so the action keys must be
// inert rather than handing the caller an out-of-range cursor.
func TestUpdateLiveNoSessionsIgnoresActions(t *testing.T) {
	ui := updateLive(liveUI{Height: 40}, kEnter, 0)
	if ui.Result != treeNone {
		t.Errorf("Enter with no sessions = %v, want treeNone", ui.Result)
	}
	if ui := updateLive(liveUI{Height: 40}, kRune, 't'); ui.Result != treeNone {
		t.Errorf("t with no sessions = %v, want treeNone", ui.Result)
	}
}

func TestLiveChoiceCarriesSessionIdentity(t *testing.T) {
	s, _ := parseLiveSession([]byte(liveFixture))
	s.Path = "/Users/dvydra/.claude/projects/slug/21c1a476-e137-4951-8a9a-1bb471096870.jsonl"
	s.Profile = personalProfile
	c := liveChoice(liveUI{Sessions: []liveSession{s}, Result: treeWorkspace})
	if c.Result != treeWorkspace {
		t.Errorf("Result = %v", c.Result)
	}
	if c.Path != s.Path || c.ID != s.SessionID || c.Cwd != s.Cwd {
		t.Errorf("choice = %+v, want the session's path/id/cwd", c)
	}
	if c.Account != personalProfile {
		t.Errorf("Account = %q, want %q", c.Account, personalProfile)
	}
}

// A timed read reports its 0-byte timeout as (0, io.EOF), so the only thing
// separating an idle terminal from a closed one is how long the read took.
func TestLiveReadStall(t *testing.T) {
	// An idle terminal: every empty read costs the full stty TIME, so the
	// counter must stay flat forever rather than creeping toward the limit.
	spins := 0
	for range liveMaxSpins * 3 {
		var dead bool
		spins, dead = liveReadStall(600*time.Millisecond, spins)
		if dead {
			t.Fatal("an idle terminal was mistaken for a dead one")
		}
		if spins != 0 {
			t.Fatalf("idle read incremented the counter to %d", spins)
		}
	}
	// A dead fd returns instantly: give up, but only after enough of them that
	// no real terminal could produce the run.
	spins = 0
	dead := false
	for i := 1; !dead && i <= liveMaxSpins*2; i++ {
		spins, dead = liveReadStall(0, spins)
		if dead && i <= liveMaxSpins {
			t.Fatalf("gave up after only %d fast empty reads", i)
		}
	}
	if !dead {
		t.Error("a dead fd never ended the loop")
	}
	// One real keystroke in the middle resets the run.
	spins, _ = liveReadStall(0, 0)
	if got, _ := liveReadStall(600*time.Millisecond, spins); got != 0 {
		t.Errorf("a slow read left the counter at %d, want 0", got)
	}
}

func TestLiveWindowAlwaysHoldsTheCursor(t *testing.T) {
	heights := []int{5, 5, 5, 5, 5, 5} // 6 blocks, +1 blank row between each

	// Everything fits: show it all, from the top.
	if s, e := liveWindow(heights, 3, 100); s != 0 || e != 6 {
		t.Errorf("roomy window = %d..%d, want 0..6", s, e)
	}
	// Budget for two blocks. The cursor must be inside the window wherever it is
	// — the bug this replaced left it below the fold, invisible.
	for cursor := range heights {
		s, e := liveWindow(heights, cursor, 11)
		if cursor < s || cursor >= e {
			t.Errorf("cursor %d fell outside window %d..%d", cursor, s, e)
		}
		if e-s != 2 {
			t.Errorf("cursor %d: window %d..%d holds %d blocks, want 2", cursor, s, e, e-s)
		}
	}
	// A budget too small for even one block still draws the cursor's block
	// rather than nothing at all.
	if s, e := liveWindow(heights, 4, 1); s != 4 || e != 5 {
		t.Errorf("tiny window = %d..%d, want 4..5", s, e)
	}
	if s, e := liveWindow(nil, 0, 50); s != 0 || e != 0 {
		t.Errorf("empty window = %d..%d, want 0..0", s, e)
	}
	// An out-of-range cursor is clamped, not panicked on.
	if s, e := liveWindow(heights, 99, 11); e <= s || e > 6 {
		t.Errorf("clamped window = %d..%d", s, e)
	}
}

func TestRenderLiveScrollsToCursorAndCounts(t *testing.T) {
	a, _ := parseLiveSession([]byte(liveFixture))
	var sessions []liveSession
	for i := range 5 {
		s := a
		s.PID = 100 + i
		s.Name = "sess-" + strconv.Itoa(i)
		s.SessionID = strconv.Itoa(i) + "1c1a476-e137-4951-8a9a-1bb471096870"
		sessions = append(sessions, s)
	}
	// Height fits roughly one block, so the last session is only reachable by
	// the window following the cursor.
	ui := liveUI{Sessions: sessions, Width: 78, Height: 6, Now: 1789599265, Cursor: 4}
	out := renderLive(ui)
	if !strings.Contains(out, "sess-4") {
		t.Errorf("cursor's block not drawn:\n%s", out)
	}
	if strings.Contains(out, "sess-0") {
		t.Errorf("window did not scroll away from the top:\n%s", out)
	}
	if !strings.Contains(out, "of 5)") {
		t.Errorf("clipped list did not say how many were hidden:\n%s", out)
	}
	// When everything fits there is no counter to show.
	if out := renderLive(liveUI{Sessions: sessions, Width: 78, Height: 200, Now: 1789599265}); strings.Contains(out, "of 5)") {
		t.Errorf("counter shown for an unclipped list:\n%s", out)
	}
}

func TestRenderLiveMarksCursor(t *testing.T) {
	a, _ := parseLiveSession([]byte(liveFixture))
	b := a
	b.PID, b.SessionID, b.Name = 41763, "eab90abf-1b59-4244-ac0d-9803fc257e94", "entiredb-16"
	ui := liveUI{Sessions: []liveSession{a, b}, Width: 78, Height: 40, Now: 1789599265, TailN: liveTailDefault}
	out := renderLive(ui)
	if !strings.Contains(out, liveCursorMark+" ") {
		t.Errorf("cursor mark missing:\n%s", out)
	}
	if strings.Count(out, liveCursorMark) != 1 {
		t.Errorf("want exactly one cursor mark, got %d:\n%s", strings.Count(out, liveCursorMark), out)
	}
}
