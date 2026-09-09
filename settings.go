package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// settings.go is the `?` screen: a live settings panel over the transcript,
// replacing the read-only help card. Everything the keyboard can change is a row
// you can land on and change from here, with its current value beside it — the
// card used to *describe* the keys and leave you to remember which one you
// wanted.
//
// It reuses the alt-screen box (drawPanel, help.go) and the focus overlay's
// hand-off: the keyboard goroutine parks on resumeCh, so for the panel's
// lifetime this is the sole reader AND writer of the tty. Changes therefore run
// on the render
// goroutine, which is the only one allowed to touch the renderer's non-atomic
// state (the glamour fn, the header strings) — the same reason the display keys
// signal actionCh instead of acting themselves.
//
// Split the usual three ways (as tree.go and handover_picker.go are): pure rows
// + reducer + renderer, and a thin tty driver. The rows are re-read from the
// environment after every change rather than mutated in place, so the screen
// can never disagree with the thing it's controlling.

// settingID names a row's effect. setNone is "not a settings row" — the info,
// key-map and legend lines the cursor skips over.
type settingID int

const (
	setNone settingID = iota
	setTheme
	setTools
	setCollapse
	setBodies
	setWrap
	setStatusBar
	setHooks
	setTap
)

// settingRow is one changeable line.
type settingRow struct {
	ID    settingID
	Label string
	Value string
	// Swatch is a pre-coloured strip drawn before the value, for a row whose
	// value is a look rather than a word (the theme). Carries its own ANSI, so
	// it isn't put through the value colour.
	Swatch string
	// Note is a dim trailing remark, for a row that behaves unlike its
	// neighbours (the one setting that isn't remembered, say).
	Note string
	// Confirm marks a row whose change reaches OUTSIDE this process — it edits
	// ~/.claude/settings.json, or loads a launchd agent. Those don't happen on a
	// stray arrow key: the row arms on ⏎ and acts on a second ⏎.
	Confirm bool
}

// settingsEnv is the screen's window onto the running tail. Rows are a function
// rather than a slice because Apply can change any of them (and Apply is the
// only way they change), so the screen re-reads instead of tracking.
type settingsEnv struct {
	Context []string // read-only session context, shown under the settings
	Keys    []string // the key map, shown under that
	Legend  string   // the dot legend, last
	Rows    func() []settingRow
	// Apply performs a row's change and returns a one-line result for the
	// footer. dir is +1 for → / ⏎ and -1 for ←, so a cycle can go backwards.
	Apply func(id settingID, dir int) string
}

// settingsState is what the reducer moves around.
type settingsState struct {
	cursor  int       // index into the settings rows
	top     int       // first visible display line
	msg     string    // footer result of the last change
	armed   settingID // a Confirm row waiting for its second ⏎
	closing bool
}

// settingsLabelW is the column the values start at. Fixed rather than measured
// so the panel doesn't jump sideways when a value grows.
const settingsLabelW = 14

// settingsLines renders the whole panel body: the settings rows, then the
// session context, the key map and the legend under dividers. Pure — it returns
// the lines and, for each settings row, which line it landed on, which is all
// the scroller needs to keep the cursor in view.
func settingsLines(env settingsEnv, rows []settingRow, cursor int, theme Theme) (lines []string, rowLine []int) {
	dim := func(s string) string { return theme.DimANSI + s + reset }
	val := func(s string) string { return theme.UserANSI + s + reset }

	add := func(s string) { lines = append(lines, s) }
	// A divider has to span the finished panel, whose width is the widest line in
	// it — including lines that come after the divider. So they go in as markers
	// and are drawn to length in a second pass, once that width is known.
	const dividerMark = "\x00divider\x00"
	divider := func(title string) {
		add("")
		add(dividerMark + title)
	}

	add("")
	for i, row := range rows {
		rowLine = append(rowLine, len(lines))
		marker, label := "  ", row.Label
		if i == cursor {
			marker = theme.ClaudeANSI + "▸ " + reset
			label = "\x1b[1m" + row.Label + reset
		}
		// Pad on the visible width: the label may be carrying a bold escape.
		pad := strings.Repeat(" ", max(settingsLabelW-visWidth(label), 1))
		line := marker + label + pad
		// The swatch goes BEFORE the value: it's a fixed width and the name isn't,
		// so this way it stays put as ←→ cycles through names of different lengths.
		if row.Swatch != "" {
			line += row.Swatch + "  "
		}
		line += val(row.Value)
		if row.Note != "" {
			line += dim("  · " + row.Note)
		}
		add(line)
	}

	divider("session")
	for _, l := range env.Context {
		add("  " + l)
	}
	divider("keys")
	for _, l := range env.Keys {
		add("  " + l)
	}
	if env.Legend != "" {
		divider("legend")
		add("  " + env.Legend)
	}

	width := helpMinBox - helpBorders
	for _, l := range lines {
		if !strings.HasPrefix(l, dividerMark) {
			width = max(width, visWidth(l))
		}
	}
	for i, l := range lines {
		title, ok := strings.CutPrefix(l, dividerMark)
		if !ok {
			continue
		}
		head := "── " + title + " "
		lines[i] = dim(head + strings.Repeat("─", max(width-visWidth(head), 0)))
	}
	return lines, rowLine
}

// settingsFooter is the hint in the bottom border: what the cursor's row does,
// or the result of the last change, or the confirmation a Confirm row is
// waiting on.
func settingsFooter(rows []settingRow, st settingsState) string {
	if st.armed != setNone {
		for _, row := range rows {
			if row.ID == st.armed {
				return " ⏎ again to " + strings.ToLower(row.Label) + " · any other key cancels "
			}
		}
	}
	if st.msg != "" {
		return " " + st.msg + " "
	}
	if st.cursor < len(rows) && rows[st.cursor].Confirm {
		return " ↑↓ move · ⏎ change (asks first) · q close "
	}
	return " ↑↓ move · ←→ change · q close "
}

// updateSettings is the reducer: one key in, the next state out, plus the row
// to apply (setNone for a key that only moved the cursor). Pure.
func updateSettings(st settingsState, rows []settingRow, k treeKey, r rune) (settingsState, settingID, int) {
	// Any key that isn't the confirming ⏎ disarms: a pending write must never
	// survive the user navigating away from the row that asked for it.
	armed := st.armed
	st.armed = setNone
	st.msg = ""

	change := func(dir int) (settingsState, settingID, int) {
		if st.cursor >= len(rows) {
			return st, setNone, 0
		}
		row := rows[st.cursor]
		if !row.Confirm {
			return st, row.ID, dir
		}
		if armed == row.ID {
			return st, row.ID, dir // second ⏎: go ahead
		}
		st.armed = row.ID
		return st, setNone, 0
	}

	switch {
	case k == kEsc, k == kCtrlC, k == kRune && (r == 'q' || r == '?'):
		st.closing = true
	case k == kUp, k == kRune && r == 'k':
		st.cursor = max(st.cursor-1, 0)
	case k == kDown, k == kRune && r == 'j':
		st.cursor = min(st.cursor+1, max(len(rows)-1, 0))
	case k == kHome, k == kRune && r == 'g':
		st.cursor = 0
	case k == kEnd, k == kRune && r == 'G':
		st.cursor = max(len(rows)-1, 0)
	case k == kRight, k == kEnter, k == kRune && (r == 'l' || r == ' '):
		return change(+1)
	case k == kLeft, k == kRune && r == 'h':
		return change(-1)
	}
	return st, setNone, 0
}

// scrollTo keeps the cursor's line inside the visible window, moving the view
// by the least it can: a settings panel that recentres on every arrow key is
// harder to read than one that only scrolls at the edges.
func scrollTo(top, line, body, total int) int {
	if line < top {
		top = line
	}
	if line >= top+body {
		top = line - body + 1
	}
	return min(max(top, 0), max(total-body, 0))
}

// runSettings shows the panel on the shared tty (the keyboard goroutine is
// parked, so this is the only reader). A no-op without a tty.
func runSettings(tty *os.File, env settingsEnv, theme Theme) {
	if tty == nil {
		return
	}
	// The tty is in cbreak (the live-tail mode); switch to raw so Esc/Ctrl-C
	// arrive as bytes and the alt-screen is always torn down cleanly.
	saved, ok := setRaw(tty)
	if !ok {
		return
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	st := settingsState{}
	buf := make([]byte, 16)
	for {
		rows := env.Rows()
		st.cursor = min(st.cursor, max(len(rows)-1, 0))
		lines, rowLine := settingsLines(env, rows, st.cursor, theme)
		w, h := termSize(tty)
		body := helpBodyRows(len(lines), h)
		if st.cursor < len(rowLine) {
			st.top = scrollTo(st.top, rowLine[st.cursor], body, len(lines))
		}
		io.WriteString(tty, drawPanel(lines, st.top, w, h, theme, settingsFooter(rows, st)))

		n, err := tty.Read(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		k, r := decodeKey(buf[:n])
		next, id, dir := updateSettings(st, rows, k, r)
		st = next
		if st.closing {
			return
		}
		if id != setNone {
			st.msg = env.Apply(id, dir)
		}
	}
}

// settingsRowsFor builds the rows from live state. The two Confirm rows at the
// bottom are read from disk each time rather than cached: they're global, so
// another entire-tail (or a hand-edited settings.json) can change them under us,
// and a panel that reports a stale answer about a file it's about to rewrite is
// worse than one that costs two stats per keystroke.
func settingsRowsFor(info helpInfo, home string) []settingRow {
	collapse := "off"
	if info.Collapse > 0 {
		collapse = fmt.Sprintf("user pastes > %d lines", info.Collapse)
	}
	bodies := "rendered markdown"
	if info.Mrkdwn {
		bodies = "slack mrkdwn source"
	}
	wrap := "off — paragraphs copy unbroken"
	if info.Wrap > 0 {
		wrap = fmt.Sprintf("on, %d columns", info.Wrap)
	}
	return []settingRow{
		{ID: setTheme, Label: "theme", Value: info.Theme, Swatch: info.ThemeSwatch},
		{ID: setTools, Label: "tools", Value: info.Tools.label()},
		{ID: setCollapse, Label: "collapse", Value: collapse},
		{ID: setWrap, Label: "wrap", Value: wrap},
		{ID: setBodies, Label: "bodies", Value: bodies, Note: "this session only"},
		{ID: setStatusBar, Label: "status bar", Value: onOff(info.StatusBar)},
		{ID: setHooks, Label: "prompt hooks", Value: installedOrNot(hookInstalledFor(home)),
			Note: "writes ~/.claude/settings.json", Confirm: true},
		{ID: setTap, Label: "api tap", Value: runningOrNot(tapBaseURL(home) != ""),
			Note: "launchd agent", Confirm: true},
	}
}

// onOff / installedOrNot / runningOrNot keep the value column reading as plain
// English rather than true/false.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func installedOrNot(b bool) string {
	if b {
		return "installed"
	}
	return "not installed"
}

func runningOrNot(b bool) string {
	if b {
		return "running"
	}
	return "not running"
}

// boolLabel is the footer message for a toggle that just moved.
func boolLabel(on bool, what string) string { return what + ": " + onOff(on) }

// savedNote appends the outcome of persisting a change. Silence on success —
// remembering is the expected behaviour, and a panel that says "saved" after
// every keystroke is noise. A FAILURE has to be said out loud, though: the
// setting did change, it just won't survive the session, and finding that out
// tomorrow is the bad version.
func savedNote(err error) string {
	if err == nil {
		return ""
	}
	return " (not saved: " + err.Error() + ")"
}

// rememberPref reads the saved preferences, applies one change and writes them
// back. Read-modify-write rather than holding the file in memory, so a second
// entire-tail changing a different setting isn't clobbered by this one.
func rememberPref(home string, set func(*savedPrefs)) error {
	p := loadPrefs(home)
	set(&p)
	return savePrefs(home, p)
}

// toggleTapAgent installs or uninstalls the tap's launchd agent, which is the
// durable form of the choice — `tap start` alone dies with the shell that ran
// it, and a session bakes the proxy URL in at launch, so a tap that comes and
// goes is worse than one that was never there. Delegates to runTap so the panel
// and the subcommand can't drift apart, and reports its last line.
func toggleTapAgent(home string) string {
	verb := "install"
	if tapBaseURL(home) != "" {
		verb = "uninstall"
	}
	var out strings.Builder
	if err := runTap([]string{verb}, home, os.Getenv, &out); err != nil {
		return "tap " + verb + " failed: " + err.Error()
	}
	if last := lastLine(out.String()); last != "" {
		return last
	}
	return "tap " + verb + "ed"
}

// lastLine is the final non-blank line of s — a subcommand's outcome, without
// the progress above it.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// settingsContext is the read-only "what am I looking at" block: the startup
// banner's facts that aren't settings.
func settingsContext(info helpInfo) []string {
	return []string{
		fmt.Sprintf("%-9s %s", "agent", info.Agent),
		fmt.Sprintf("%-9s %s", "session", info.Session),
		fmt.Sprintf("%-9s %s (%d..%d of %d)", "backfill", info.Backfill, info.From, info.Total, info.Total),
	}
}

// settingsKeys is the key map. The keys that have a row of their own above are
// left out — the row IS the documentation, and repeating it would make the
// panel look like it has two ways to do everything.
func settingsKeys(treeEnabled bool) []string {
	kv := func(k, v string) string { return fmt.Sprintf("%-7s %s", k, v) }
	L := []string{
		kv("y", "copy the last agent message as Slack mrkdwn"),
		kv("", "(press again within 3s to add the one before it)"),
		kv("r", "re-render the history on demand"),
		kv("→", "focus subagents"),
	}
	if treeEnabled {
		L = append(L, kv("Ctrl-X", "back to the tree picker"))
	}
	return append(L,
		kv("?", "this panel"),
		kv("q", "quit (also Ctrl-D, Ctrl-C)"),
	)
}
