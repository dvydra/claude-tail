package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveTapActivityWiring is a diagnostic, not a unit test: it runs the REAL
// tree build against the REAL ~/.claude and reports whether the tap's activity
// table actually reaches a rendered row. It exists because this feature's bugs
// have all been wiring (a watcher that was never armed, a gate that was always
// false) rather than logic, and unit tests with fixture homes can't see that.
//
// Skips unless RUN_LIVE_TAP=1, so `go test ./...` stays hermetic.
func TestLiveTapActivityWiring(t *testing.T) {
	if os.Getenv("RUN_LIVE_TAP") != "1" {
		t.Skip("set RUN_LIVE_TAP=1 to check the tap→tree wiring against the real ~/.claude")
	}
	home := os.Getenv("HOME")
	act, ok := readTapActive(home)
	if !ok || len(act.Sessions) == 0 {
		t.Skip("no tap activity table yet — start the daemon and run a routed session")
	}
	t.Logf("tap knows %d session(s)", len(act.Sessions))
	nowMs := time.Now().UnixNano() / 1e6
	for id, s := range act.Sessions {
		last := max(s.LastEvent, s.LastEnd, s.LastStart)
		t.Logf("  %s in_flight=%d last=%dms ago (live window %ds) → wantLive=%v",
			shortID(id), s.InFlight, nowMs-last, recentLiveWindow,
			nowMs-last < int64(recentLiveWindow)*1000)
	}

	tree := buildSessionTree(home, mustGetwd(), 7, time.Now().Unix(), false, false)
	matched, generating := 0, 0
	for _, f := range tree.Folders {
		for _, s := range f.Sessions {
			if _, in := act.Sessions[s.ID]; !in {
				continue
			}
			matched++
			row := composeSessionRow(s, tree.Now)
			t.Logf("  %s live=%v generating=%v row=%q", shortID(s.ID), s.Live, s.Generating, strings.TrimSpace(row))
			if s.Generating {
				generating++
				if !strings.Contains(row, "◉") {
					t.Errorf("a generating session must render the ◉ glyph: %q", row)
				}
			}
		}
	}
	if matched == 0 {
		t.Errorf("the tap knows %d session(s) but NONE matched a tree row — "+
			"the activity overlay is not reaching the picker", len(act.Sessions))
	}
	t.Logf("matched %d tree row(s), %d generating right now", matched, generating)
}
