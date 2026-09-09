package main

import (
	"strings"
	"testing"
)

func TestItermWindow(t *testing.T) {
	cases := map[string]string{
		"w0t1p0:3894364C-898B-41C4-8C51-4174812F38F2": "w0",
		"w2t10p3:ABC": "w2",
		"w0t0p0":      "w0",
		"":            "",
		"garbage":     "",
	}
	for in, want := range cases {
		if got := itermWindow(in); got != want {
			t.Errorf("itermWindow(%q) = %q, want %q", in, got, want)
		}
	}
}

// A pane of our tab beats a pane elsewhere in the window, which beats anything
// outside it — and the order matters, because the tree opens on the highest.
func TestProximityOf(t *testing.T) {
	const ownTab, ownWin = "w0t1", "w0"
	cases := []struct {
		name string
		id   string
		want paneProximity
	}{
		{"same tab, other pane", "w0t1p2:X", paneTab},
		{"same tab, same pane", "w0t1p0:X", paneTab},
		{"other tab, same window", "w0t3p0:X", paneWindow},
		{"other window", "w1t1p0:X", paneFar},
		{"no iterm id", "", paneFar},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := proximityOf(c.id, ownTab, ownWin); got != c.want {
				t.Errorf("proximityOf(%q) = %d, want %d", c.id, got, c.want)
			}
		})
	}
	// Off iTerm entirely there is nothing to be near.
	if got := proximityOf("w0t1p0:X", "", ""); got != paneFar {
		t.Errorf("with no own id, got %d, want paneFar", got)
	}
}

func nearbyTestTree() sessionTree {
	return sessionTree{
		Folders: []treeFolder{
			{Cwd: "o/cold", Sessions: []treeSession{{ID: "aaa"}, {ID: "bbb"}}},
			{Cwd: "o/warm", Sessions: []treeSession{{ID: "ccc"}, {ID: "ddd"}}},
		},
	}
}

// The overlay only ever promotes. A session it can't place must keep whatever
// the crawl decided — "no claude found near you" is not evidence that a session
// isn't running.
func TestApplyNearbyIsAdditive(t *testing.T) {
	tree := nearbyTestTree()
	tree.Folders[0].Sessions[0].Live = true // the crawl already believed this one
	applyNearby(&tree, map[string]paneProximity{"ddd": paneTab})

	if s := tree.Folders[0].Sessions[0]; !s.Live || s.Nearby != paneFar {
		t.Errorf("an unrelated live session was disturbed: %+v", s)
	}
	d := tree.Folders[1].Sessions[1]
	if d.Nearby != paneTab {
		t.Errorf("nearby session not marked: %+v", d)
	}
	// A session running beside you is running, so the row and its group have to
	// read as live — otherwise the group with the agent you're using sorts cold.
	if !d.Live {
		t.Error("a nearby session was not promoted to live")
	}
	if tree.Folders[1].Live == 0 {
		t.Error("the folder holding a nearby session was left with no live count")
	}
	// And an empty map changes nothing at all.
	before := nearbyTestTree()
	after := nearbyTestTree()
	applyNearby(&after, nil)
	if after.Folders[1].Sessions[1].Live != before.Folders[1].Sessions[1].Live {
		t.Error("an empty overlay changed the tree")
	}
}

// The tab wins over the window: two agents nearby, and the cursor goes to the
// one in the pane next door.
func TestNearbyCursorPrefersTheTab(t *testing.T) {
	tree := nearbyTestTree()
	applyNearby(&tree, map[string]paneProximity{"bbb": paneWindow, "ccc": paneTab})
	fi, si, prox := nearbyCursor(tree)
	if prox != paneTab || fi != 1 || si != 0 {
		t.Errorf("nearbyCursor = (%d, %d, %d), want (1, 0, paneTab)", fi, si, prox)
	}
	// Nothing nearby → nothing to say, and the caller keeps its usual start row.
	if _, _, p := nearbyCursor(nearbyTestTree()); p != paneFar {
		t.Errorf("an empty tree reported proximity %d", p)
	}
}

// The marker is a fixed two-column cell (like the account `@`), so ids stay in
// one column whether or not anything nearby was found — and it hands the row's
// colour back, because styleRow paints the whole line after it.
func TestNearbyMark(t *testing.T) {
	const restore = "\x1b[38;2;1;2;3m"
	for _, c := range []struct {
		prox paneProximity
		want string
	}{
		{paneFar, "  "},
		{paneTab, "◀ "},
		{paneWindow, "◀ "},
	} {
		got := nearbyMark(c.prox, restore)
		if plain := stripANSI(got); plain != c.want {
			t.Errorf("nearbyMark(%d) = %q (plain %q), want %q", c.prox, got, plain, c.want)
		}
		if c.prox != paneFar && !strings.HasSuffix(strings.TrimSuffix(got, " "), restore) {
			t.Errorf("nearbyMark(%d) = %q, did not restore the row colour", c.prox, got)
		}
	}
	// The two live marks are visibly the same glyph in different colours — the
	// confident one and the muted one, as the account marker does.
	tab, win := nearbyMark(paneTab, restore), nearbyMark(paneWindow, restore)
	if tab == win {
		t.Error("this-tab and this-window render identically")
	}
	if stripANSI(tab) != stripANSI(win) {
		t.Error("the two marks should differ only in colour")
	}
}

// The whole point: the tree opens on the agent one pane away, not on whatever
// the current directory happened to be.
func TestInitialCursorPrefersTheNearbySession(t *testing.T) {
	tree := nearbyTestTree()
	tree.CurrentGroup = "o/cold"
	tree.Folders[0].Expanded = true
	tree.Folders[1].Expanded = true
	applyNearby(&tree, map[string]paneProximity{"ddd": paneTab})

	ui := treeUI{Tree: tree}
	ui.Rows = flattenRows(ui.Tree, "")
	got := initialCursor(ui)
	row := ui.Rows[got]
	if row.Session == -1 || tree.Folders[row.Folder].Sessions[row.Session].ID != "ddd" {
		t.Errorf("cursor landed on row %d (%+v), want the session ddd", got, row)
	}

	// With nothing nearby it falls back to the current group's folder row, as
	// before — this must not change the no-iTerm behaviour.
	plain := nearbyTestTree()
	plain.CurrentGroup = "o/cold"
	plain.Folders[0].Expanded = true
	ui2 := treeUI{Tree: plain}
	ui2.Rows = flattenRows(ui2.Tree, "")
	r2 := ui2.Rows[initialCursor(ui2)]
	if r2.Session != -1 || plain.Folders[r2.Folder].Cwd != "o/cold" {
		t.Errorf("fallback cursor landed on %+v, want the o/cold folder row", r2)
	}
}

// The footer only explains the glyph when one is on screen: a legend for a
// marker that isn't there is noise on every other run.
func TestFooterExplainsTheMarkOnlyWhenShown(t *testing.T) {
	plain := treeUI{Tree: nearbyTestTree()}
	if got := composeFooter(plain); strings.Contains(got, "◀") {
		t.Errorf("footer explained a marker that isn't shown: %q", got)
	}
	tree := nearbyTestTree()
	applyNearby(&tree, map[string]paneProximity{"ccc": paneTab})
	if got := composeFooter(treeUI{Tree: tree}); !strings.Contains(got, "◀ runs in this tab") {
		t.Errorf("footer = %q, want it to name the marker", got)
	}
	win := nearbyTestTree()
	applyNearby(&win, map[string]paneProximity{"ccc": paneWindow})
	if got := composeFooter(treeUI{Tree: win}); !strings.Contains(got, "◀ runs in this window") {
		t.Errorf("footer = %q, want the window wording", got)
	}
	// With both on screen the footer has to name both, or the dim mark beside a
	// session in the next tab sits under a line calling it this one.
	both := nearbyTestTree()
	applyNearby(&both, map[string]paneProximity{"ccc": paneTab, "bbb": paneWindow})
	if got := composeFooter(treeUI{Tree: both}); !strings.Contains(got, "dim: this window") {
		t.Errorf("footer = %q, want it to explain both marks", got)
	}
}

// Off iTerm (or without pgrep/lsof) there's nothing to place anything against,
// and the tree has to behave exactly as it did before.
func TestNearbySessionsNeedsAnItermID(t *testing.T) {
	if got := nearbySessions(t.TempDir(), func(string) string { return "" }); got != nil {
		t.Errorf("got %v with no ITERM_SESSION_ID, want nil", got)
	}
}
