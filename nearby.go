package main

// nearby.go answers "which of these sessions is the agent sitting one pane
// away?" — the question the tree could never answer before.
//
// entire-tail already adopts the claude in your iTerm tab when there's exactly
// one (adopt.go). That covers the common case and skips the tree entirely. But
// it gives up on purpose in two situations — a tab holding two claudes, and
// Ctrl-X, which asks for the tree — and in both of them you landed in a list
// with no indication which row was the agent you were just looking at. On a
// machine with a few dozen sessions across a handful of repos, that's a hunt for
// something that was three feet away.
//
// So the tree learns the same trick adopt.go uses, minus the "exactly one" rule:
// every running claude is placed by its ITERM_SESSION_ID (`wNtNpM:UUID`) as
// sharing our TAB or not, and resolved to the transcript it's writing. Rows get
// a marker, and the cursor opens on one in our tab.
//
// The tab is the whole horizon on purpose. An earlier version also ranked a
// claude in another tab of the same WINDOW, and that made a fresh terminal in a
// folder open with the cursor on whatever happened to be running next door —
// ⏎ then resumed that session instead of starting one here. Nothing in this tab
// means the cursor stays on this folder, where ⏎ is a new session in it.
//
// It is strictly additive, for the same reason applyTapActivity is: not finding
// a session near you is not evidence of anything, so a row is only ever
// promoted, never demoted.

// paneProximity is how close a running claude is to the pane we're in. The
// order matters — the tree opens on the highest one it can find.
type paneProximity int

const (
	paneFar paneProximity = iota // not in this iTerm tab (or not resolvable)
	paneTab                      // a pane of THIS tab: the one you were watching
)

// nearbySessions maps session id → how close its claude is to us. Empty off
// iTerm, or without pgrep/lsof — both are optional everywhere else here, and
// this is a nicety, not a requirement.
//
// Resolution is deliberately cheap. A claude launched by the workspace carries
// its id on the command line (`--session-id` / `--resume`), which costs a string
// scan of argv we already have; only a hand-started one falls back to reading
// the project dir. The tree is meant to open instantly, so nothing here is
// allowed to sample activity over time the way adopt.go may.
func nearbySessions(home string, getenv func(string) string) map[string]paneProximity {
	tab := itermTab(getenv("ITERM_SESSION_ID"))
	if tab == "" || !pickerToolsAvailable() {
		return nil
	}
	out := map[string]paneProximity{}
	for _, p := range claudeProcs() {
		prox := proximityOf(p.itermID, tab)
		if prox == paneFar {
			continue
		}
		path := resolveClaudeSession(home, lsofCwd(p.pid), psCommand(p.pid))
		if path == "" {
			continue
		}
		if id := sessionIDFromPath(path); id != "" && out[id] < prox {
			out[id] = prox
		}
	}
	return out
}

// proximityOf places one claude's ITERM_SESSION_ID relative to ours: in our
// tab, or not. Another tab of the same window is deliberately "not".
func proximityOf(id, ownTab string) paneProximity {
	if ownTab != "" && itermTab(id) == ownTab {
		return paneTab
	}
	return paneFar
}

// applyNearby marks the sessions running near this pane. Strictly additive: a
// session it can't place keeps whatever the crawl already decided about it.
// A nearby session is by definition live, so it's promoted — the folder's Live
// count too, or a group whose only running agent is the one beside you would
// still sort as cold.
func applyNearby(tree *sessionTree, near map[string]paneProximity) {
	if len(near) == 0 {
		return
	}
	for fi := range tree.Folders {
		f := &tree.Folders[fi]
		for si := range f.Sessions {
			s := &f.Sessions[si]
			prox, ok := near[s.ID]
			if !ok || prox <= s.Nearby {
				continue
			}
			s.Nearby = prox
			s.Live = true
			if f.Live == 0 {
				f.Live = 1
			}
		}
	}
}

// nearbyANSI is the marker's colour: a bright cyan for the session in this tab.
const nearbyANSI = "\x1b[38;2;125;215;220m"

// nearbyMark is the two-column cell that flags a session running beside us: an
// arrow back towards the pane it's in, blank when there's nothing to say. A
// fixed cell (like profileMark) so ids stay in one column whether or not
// anything nearby was found.
//
// restore is the row's own colour: styleRow paints the whole line, so a marker
// that ended in a plain reset would leave everything after it uncoloured.
func nearbyMark(prox paneProximity, restore string) string {
	if prox == paneTab {
		return colorize(nearbyANSI, "◀", restore) + " "
	}
	return "  "
}

// nearbyCursor finds the row the tree should open on: the session running in
// our tab. Returns -1 when there's nothing nearby, and the caller keeps its
// usual starting row.
//
// It reports the FOLDER too, because a session row only exists once its folder
// is expanded — pointing the cursor at a row that isn't rendered would put it
// somewhere arbitrary instead.
func nearbyCursor(tree sessionTree) (folder, session int, prox paneProximity) {
	folder, session, prox = -1, -1, paneFar
	for fi := range tree.Folders {
		for si, s := range tree.Folders[fi].Sessions {
			if s.Nearby > prox {
				folder, session, prox = fi, si, s.Nearby
			}
		}
	}
	return folder, session, prox
}
