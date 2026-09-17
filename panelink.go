package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// panelink.go — the pure half of the pane link: which two iTerm sessions belong
// together, and the AppleScript that acts on that.
//
// The problem it solves is a layout the workspace launcher doesn't produce. When
// a claude and the entire-tail watching it are panes of ONE tab (`⏎`/`n`,
// iterm.go), both are on screen and there is nothing to do. When they are tabs
// of two DIFFERENT windows — claude sessions in one window, their tails in
// another — selecting a claude leaves the other window showing whichever tail
// happened to be selected last, and the pair has to be re-aligned by hand every
// time attention moves.
//
// So: when either side is selected, switch the other window to the tab holding
// its partner. Measured on iTerm2 3.6.11 — selecting a tab in a window that is
// NOT the current one leaves `current window` and `current session` untouched.
// That is the whole reason this is safe to do automatically: no focus is taken,
// so there is no way for the two sides to bounce focus off each other, and no
// suppression window is needed. Nothing here ever activates an app, raises a
// window, or moves focus.
//
// Named panelink rather than focus because focus.go is the subagent overlay.

// paneEntry is one running entire-tail's registration: the iTerm session it
// occupies (the file name) and what it is following right now.
//
// Follow is rewritten whenever the tail moves — a worktree fork, a /clear, a
// relocation (lineage.go) — because a tail's CURRENT target is the one thing
// nobody else can work out. It is not in its argv: a tail launched with
// `--follow-session X` may well be streaming X's grandchild by now.
type paneEntry struct {
	Follow string `json:"follow_session"`
	Pid    int    `json:"pid"`
}

func paneLinkDir(home string) string {
	return filepath.Join(home, ".claude", "entire-tail", "link")
}

func paneRegistryDir(home string) string { return filepath.Join(paneLinkDir(home), "panes") }

func paneEntryPath(home, uuid string) string {
	return filepath.Join(paneRegistryDir(home), uuid+".json")
}

// writePaneEntry publishes (or rewrites) this tail's registration.
func writePaneEntry(home, uuid string, e paneEntry) error {
	if uuid == "" {
		return nil
	}
	return writeJSONAtomic(paneEntryPath(home, uuid), e)
}

// removePaneEntry drops this tail's registration on a clean exit. Best-effort:
// an entry left behind is pruned by its pid anyway (see prunePanes).
func removePaneEntry(home, uuid string) {
	if uuid != "" {
		_ = os.Remove(paneEntryPath(home, uuid))
	}
}

// readPaneRegistry loads every registration. A missing directory, unreadable
// file, or non-JSON junk inside it all read as "no panes" — a viewer must never
// fail because its own sidecar is nonsense.
func readPaneRegistry(home string) map[string]paneEntry {
	out := map[string]paneEntry{}
	ents, err := os.ReadDir(paneRegistryDir(home))
	if err != nil {
		return out
	}
	for _, de := range ents {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(paneRegistryDir(home), name))
		if err != nil {
			continue
		}
		var e paneEntry
		if json.Unmarshal(b, &e) != nil || e.Follow == "" {
			continue
		}
		out[strings.TrimSuffix(name, ".json")] = e
	}
	return out
}

// prunePanes drops entries whose process is gone. The file outlives a SIGKILLed
// tail, so the pid is the truth and its presence is not — the same rule live.go
// applies to Claude Code's own session registry, learned the same way.
func prunePanes(panes map[string]paneEntry, alive func(int) bool) map[string]paneEntry {
	out := map[string]paneEntry{}
	for uuid, e := range panes {
		if alive(e.Pid) {
			out[uuid] = e
		}
	}
	return out
}

// linkPartners returns the iTerm session ids that should follow the one just
// selected, in sorted order so a run is reproducible.
//
// panes is the tail registry (iTerm session id → what it follows); claudes maps
// a running claude's iTerm session id → the transcript it is writing, resolved
// by the daemon rather than registered by anyone, so a claude restarted in a new
// tab re-pairs on its own.
//
// Both directions are the same lookup from opposite ends. Anything unknown —
// a shell, an editor, a claude nothing is watching — has no partner and produces
// no action at all, not even an osascript call.
func linkPartners(focused string, panes map[string]paneEntry, claudes map[string]string) []string {
	if focused == "" {
		return nil
	}
	var out []string
	if e, ok := panes[focused]; ok {
		for uuid, sess := range claudes {
			if sess == e.Follow {
				out = append(out, uuid)
			}
		}
	} else if sess, ok := claudes[focused]; ok {
		// Two tails in two windows may watch one session; switch both.
		for uuid, e := range panes {
			if e.Follow == sess {
				out = append(out, uuid)
			}
		}
	}
	sort.Strings(out)
	return out
}

// switchEchoWindowMS is how long after selecting a partner's tab an event
// naming that partner is treated as our own echo rather than a click. Short,
// because it must not swallow a real return to the pane you came from —
// iTerm reports the change within a few milliseconds, a human takes hundreds.
const switchEchoWindowMS = 400

// switchGuard is the second line of defence against the feedback loop this
// feature shipped with: selecting a partner's tab is itself an iTerm event, so
// a watcher that reports it hands the daemon its own action, which pairs it and
// switches the other side, forever. Two linked pairs flip tabs until something
// is killed.
//
// The real fix is in panelink.py — the key window's current session cannot echo,
// because our tab switch never moves focus. This exists because the cost of
// being wrong again is a UI that thrashes in the user's hands, and because the
// evidence for the fix (`active_session_changed` naming an unfocused window's
// session) is the sort of behaviour that can differ across iTerm versions.
type switchGuard struct {
	selected map[string]int64 // partner session id → when we selected it (ms)
}

func newSwitchGuard() *switchGuard { return &switchGuard{selected: map[string]int64{}} }

// remember records the partners we just acted on.
func (g *switchGuard) remember(partners []string, nowMS int64) {
	for _, p := range partners {
		g.selected[p] = nowMS
	}
}

// isEcho reports whether this event is one we caused. It also drops entries
// that have aged out, so the map stays the size of a pairing rather than the
// size of a session's history.
func (g *switchGuard) isEcho(uuid string, nowMS int64) bool {
	for id, at := range g.selected {
		if nowMS-at >= switchEchoWindowMS {
			delete(g.selected, id)
		}
	}
	at, ok := g.selected[uuid]
	return ok && nowMS-at < switchEchoWindowMS
}

// linkScript builds the one AppleScript pass that checks the event is still
// current, works out which window must not be touched, and selects the
// partners' tabs everywhere else.
//
// It is one pass on purpose, and both guards are inside it because both facts
// go stale in the milliseconds it takes to get here:
//
//   - **The event may already be old.** A focus event names a session, but by
//     the time the daemon has read the registry and resolved a partner, the user
//     may have moved on. Acting on it then switched a tab in the window they had
//     just moved TO — yanking them off the tab they chose, and changing the key
//     session, which reported straight back to us and bounced the pair between
//     two windows. So the script re-reads the focused session and does nothing
//     unless it is still the one the event named.
//   - **The window to leave alone is the CURRENT one**, asked of iTerm here
//     rather than derived from the (possibly stale) event's session. Two panes of
//     one window cannot both be shown, so selecting a partner there would drag
//     the user off their own tab — and it is what makes the 3-pane workspace a
//     no-op rather than a special case.
//
// A tab is addressed by index and indexes shift as tabs open, close and move, so
// finding and selecting in one script also removes that gap. A select that fails
// — the tab closed a moment ago — is swallowed, so one dead partner cannot stop
// the others being switched.
func linkScript(focused string, partners []string) string {
	quoted := make([]string, 0, len(partners))
	for _, p := range partners {
		quoted = append(quoted, `"`+asEscape(p)+`"`)
	}
	var b strings.Builder
	b.WriteString("tell application \"iTerm2\"\n")
	b.WriteString("\tset focusedID to \"" + asEscape(focused) + "\"\n")
	b.WriteString("\tset wantIDs to {" + strings.Join(quoted, ", ") + "}\n")
	b.WriteString("\tif (count of windows) is 0 then return \"no-window\"\n")
	b.WriteString("\tset curWin to current window\n")
	b.WriteString("\tset curSess to id of current session of current tab of curWin\n")
	b.WriteString("\tif curSess is not focusedID then return \"stale\"\n")
	b.WriteString("\tset focusWinID to (id of curWin as text)\n")
	b.WriteString("\tset hits to {}\n")
	b.WriteString("\trepeat with w in windows\n")
	b.WriteString("\t\tif (id of w as text) is not focusWinID then\n")
	b.WriteString("\t\t\trepeat with t in tabs of w\n")
	b.WriteString("\t\t\t\trepeat with s in sessions of t\n")
	b.WriteString("\t\t\t\t\tif wantIDs contains (id of s) then set end of hits to t\n")
	b.WriteString("\t\t\t\tend repeat\n")
	b.WriteString("\t\t\tend repeat\n")
	b.WriteString("\t\tend if\n")
	b.WriteString("\tend repeat\n")
	b.WriteString("\trepeat with t in hits\n")
	b.WriteString("\t\ttry\n")
	b.WriteString("\t\t\tselect t\n")
	b.WriteString("\t\tend try\n")
	b.WriteString("\tend repeat\n")
	b.WriteString("\treturn (count of hits) as text\n")
	b.WriteString("end tell\n")
	return b.String()
}

// ownPaneUUID is this process's iTerm session id — the part of ITERM_SESSION_ID
// after the colon ("w0t2p0:UUID"). Empty off iTerm, which disables the whole
// feature for this tail.
//
// Only the UUID is used. The wNtN prefix is NOT a placement: a running process's
// environment cannot be rewritten, so it still names the window and tab the
// session was BORN in, and goes stale the moment a tab is dragged elsewhere.
// Every window/tab question here is asked of the live tree instead.
func ownPaneUUID(getenv func(string) string) string {
	return paneUUIDFromSessionID(getenv("ITERM_SESSION_ID"))
}

// paneUUIDFromSessionID takes the UUID out of an ITERM_SESSION_ID value. It is
// the same id AppleScript reports as `id of session`, verified against a live
// window: a shell reporting w0t2p0:D2AAFDDB-… was found at that UUID.
func paneUUIDFromSessionID(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[i+1:]
	}
	return ""
}

// ── the opt-in gate ──

func paneLinkChoicePath(home string) string {
	return filepath.Join(home, ".claude", "entire-tail", "link-choice")
}

func recordPaneLinkChoice(home, choice string) {
	p := paneLinkChoicePath(home)
	if os.MkdirAll(filepath.Dir(p), 0o700) == nil {
		_ = os.WriteFile(p, []byte(choice+"\n"), 0o600)
	}
}

func paneLinkChoiceRecorded(home string) bool {
	_, err := os.Stat(paneLinkChoicePath(home))
	return err == nil
}

// paneLinkEnabled reports whether the user has said yes. Everything else in this
// file is gated on it: with the feature off or simply unanswered, no pane file is
// written and no daemon is spawned, so an untouched machine runs exactly as it
// did before the feature existed.
func paneLinkEnabled(home string) bool {
	b, err := os.ReadFile(paneLinkChoicePath(home))
	return err == nil && strings.TrimSpace(string(b)) == "yes"
}

// paneLinkOfferInputs are the pure inputs to the one-time offer decision.
type paneLinkOfferInputs struct {
	isTTY          bool
	isClaude       bool
	hasPair        bool // a registered-or-registerable tail and its claude, in different windows
	choiceRecorded bool
	noPaneLink     bool
}

// shouldOfferPaneLink reports whether to ask, once, about setting the link up.
//
// hasPair is the load it carries: the question is only worth asking when there
// is actually something to link right now, which is also what keeps it away from
// the 3-pane workspace (both sides in one tab is not a pair). Unlike the hook
// offer this does NOT bail on --follow-session, because a tail that knows what
// it follows is exactly the case the feature exists for.
//
// The check runs once before backfill and never mid-stream: a streaming viewer
// must not stop to ask a question.
func shouldOfferPaneLink(g paneLinkOfferInputs) bool {
	if !g.isTTY || !g.isClaude {
		return false
	}
	if !g.hasPair {
		return false // nothing to link — say nothing, ask on a later run
	}
	return !g.choiceRecorded && !g.noPaneLink
}

// pickPython chooses the interpreter the venv is built from. Preference order is
// the caller's, with one override: an interpreter under a version manager is
// taken only when nothing else exists.
//
// A mise/pyenv/asdf path names one installed VERSION, and that directory goes
// away when the user upgrades or prunes — taking a long-running daemon's
// interpreter with it. Same hazard looksEphemeralBinary guards against for the
// tap's LaunchAgent binary, and on this machine `python3` resolves to a mise
// 3.12 first, so it is the default case rather than an edge one.
func pickPython(candidates []string, exists func(string) bool) string {
	var managed string
	for _, c := range candidates {
		if !exists(c) {
			continue
		}
		if isManagedPython(c) {
			if managed == "" {
				managed = c
			}
			continue
		}
		return c
	}
	return managed
}

func isManagedPython(p string) bool {
	for _, marker := range []string{"/mise/", "/.pyenv/", "/.asdf/", "/shims/", "/.rtx/"} {
		if strings.Contains(p, marker) {
			return true
		}
	}
	return false
}
