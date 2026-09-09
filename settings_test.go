package main

import (
	"errors"
	"strings"
	"testing"
)

func settingsTestEnv(t *testing.T) settingsEnv {
	t.Helper()
	return settingsEnv{
		Context: settingsContext(testHelpInfo()),
		Keys:    settingsKeys(true),
		Legend:  "● read  ● edit",
	}
}

// The panel's whole reason to exist: every changeable thing shows its CURRENT
// value beside it. The old card described the keys and left you to remember
// which one you wanted.
func TestSettingsRowsShowLiveValues(t *testing.T) {
	rows := settingsRowsFor(testHelpInfo(), t.TempDir())
	got := map[settingID]string{}
	for _, r := range rows {
		got[r.ID] = r.Value
	}
	for id, want := range map[settingID]string{
		setTheme:     "tokyo-night",
		setTools:     "dots",
		setCollapse:  "user pastes > 5 lines",
		setWrap:      "on, 119 columns",
		setBodies:    "rendered markdown",
		setStatusBar: "on",
	} {
		if got[id] != want {
			t.Errorf("row %d value = %q, want %q", id, got[id], want)
		}
	}
}

// Wrap suspended by `w`, and collapse turned off, have to read as such —
// otherwise prose running off the right edge looks like a bug rather than a mode
// you chose.
func TestSettingsRowsReflectTheOffStates(t *testing.T) {
	info := testHelpInfo()
	info.Wrap, info.Collapse, info.Tools, info.StatusBar = 0, 0, toolNone, false
	info.Mrkdwn = true
	rows := settingsRowsFor(info, t.TempDir())
	joined := ""
	for _, r := range rows {
		joined += r.Label + "=" + r.Value + "\n"
	}
	for _, want := range []string{"wrap=off", "collapse=off", "tools=hidden", "status bar=off", "bodies=slack mrkdwn source"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q:\n%s", want, joined)
		}
	}
}

// The two rows that reach outside this process are marked Confirm; nothing else
// is. A stray → must never edit ~/.claude/settings.json or load a launchd agent.
func TestSettingsConfirmRowsAreTheGlobalOnes(t *testing.T) {
	for _, r := range settingsRowsFor(testHelpInfo(), t.TempDir()) {
		wantConfirm := r.ID == setHooks || r.ID == setTap
		if r.Confirm != wantConfirm {
			t.Errorf("row %q Confirm = %v, want %v", r.Label, r.Confirm, wantConfirm)
		}
	}
}

// A Confirm row arms on the first ⏎ and only acts on the second, and any other
// key in between cancels it — a pending write must not survive navigating away.
func TestSettingsConfirmNeedsTwoPresses(t *testing.T) {
	rows := settingsRowsFor(testHelpInfo(), t.TempDir())
	hooks := -1
	for i, r := range rows {
		if r.ID == setHooks {
			hooks = i
		}
	}
	if hooks < 0 {
		t.Fatal("no hooks row")
	}
	st := settingsState{cursor: hooks}

	st, id, _ := updateSettings(st, rows, kEnter, 0)
	if id != setNone {
		t.Fatalf("first ⏎ applied %d, want nothing", id)
	}
	if st.armed != setHooks {
		t.Fatalf("first ⏎ did not arm the row (armed=%d)", st.armed)
	}
	after, id, _ := updateSettings(st, rows, kEnter, 0)
	if id != setHooks {
		t.Errorf("second ⏎ applied %d, want setHooks", id)
	}
	if after.armed != setNone {
		t.Errorf("row stayed armed after acting")
	}

	// …and an intervening keypress disarms.
	armed := settingsState{cursor: hooks}
	armed, _, _ = updateSettings(armed, rows, kEnter, 0)
	moved, _, _ := updateSettings(armed, rows, kUp, 0)
	if moved.armed != setNone {
		t.Errorf("moving off the row left it armed")
	}
	if _, id, _ := updateSettings(moved, rows, kEnter, 0); id == setHooks {
		t.Errorf("⏎ after cancelling still fired the write")
	}
}

// An ordinary row changes on a single ←/→, and the direction is passed through
// so a cycle can be walked backwards.
func TestSettingsArrowsChangeAndCarryDirection(t *testing.T) {
	rows := settingsRowsFor(testHelpInfo(), t.TempDir())
	st := settingsState{cursor: 0} // theme
	if _, id, dir := updateSettings(st, rows, kRight, 0); id != setTheme || dir != +1 {
		t.Errorf("→ gave (%d, %d), want (setTheme, +1)", id, dir)
	}
	if _, id, dir := updateSettings(st, rows, kLeft, 0); id != setTheme || dir != -1 {
		t.Errorf("← gave (%d, %d), want (setTheme, -1)", id, dir)
	}
}

// The cursor moves over settings rows only — it never lands in the read-only
// context, the key map or the legend below them.
func TestSettingsCursorStaysOnRows(t *testing.T) {
	rows := settingsRowsFor(testHelpInfo(), t.TempDir())
	st := settingsState{}
	for range len(rows) + 5 {
		st, _, _ = updateSettings(st, rows, kDown, 0)
	}
	if st.cursor != len(rows)-1 {
		t.Errorf("cursor ran to %d, want it pinned at %d", st.cursor, len(rows)-1)
	}
	for range len(rows) + 5 {
		st, _, _ = updateSettings(st, rows, kUp, 0)
	}
	if st.cursor != 0 {
		t.Errorf("cursor ran to %d, want it pinned at 0", st.cursor)
	}
}

func TestSettingsClosingKeys(t *testing.T) {
	rows := settingsRowsFor(testHelpInfo(), t.TempDir())
	for _, c := range []struct {
		name string
		k    treeKey
		r    rune
	}{
		{"q", kRune, 'q'},
		{"?", kRune, '?'},
		{"esc", kEsc, 0},
		{"ctrl-c", kCtrlC, 0},
	} {
		if st, _, _ := updateSettings(settingsState{}, rows, c.k, c.r); !st.closing {
			t.Errorf("%s did not close the panel", c.name)
		}
	}
}

// The cursor's row has to stay visible when the panel is taller than the
// terminal, and the view should move by the least it can — a list that recentres
// on every arrow key is harder to read than one that scrolls at the edges.
func TestScrollTo(t *testing.T) {
	if got := scrollTo(0, 3, 6, 20); got != 0 {
		t.Errorf("a visible line scrolled the view to %d, want 0", got)
	}
	if got := scrollTo(0, 9, 6, 20); got != 4 {
		t.Errorf("scrolling down to line 9 gave top=%d, want 4", got)
	}
	if got := scrollTo(8, 3, 6, 20); got != 3 {
		t.Errorf("scrolling up to line 3 gave top=%d, want 3", got)
	}
	if got := scrollTo(0, 19, 6, 20); got != 14 {
		t.Errorf("the last line gave top=%d, want 14", got)
	}
	if got := scrollTo(5, 0, 30, 20); got != 0 {
		t.Errorf("a list shorter than the window gave top=%d, want 0", got)
	}
}

// The panel's body carries the settings AND the context/keys/legend the old help
// card showed, so nothing was lost when it replaced that card.
func TestSettingsLinesKeepTheHelpContent(t *testing.T) {
	env := settingsTestEnv(t)
	lines, rowLine := settingsLines(env, settingsRowsFor(testHelpInfo(), t.TempDir()), 1, helpTestTheme())
	got := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{
		"theme", "tokyo-night", "tools", "dots",
		"session", "claude", "~/.claude/projects/-repo/abc.jsonl", "all (1..3 of 3)",
		"keys", "copy the last agent message as Slack mrkdwn", "Ctrl-X", "quit",
		"legend", "● read",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("panel content missing %q:\n%s", want, got)
		}
	}
	if len(rowLine) == 0 {
		t.Fatal("no row line mapping")
	}
	// The cursor marker sits on the row it names, and only there.
	if n := strings.Count(stripANSI(lines[rowLine[1]]), "▸"); n != 1 {
		t.Errorf("cursor row %q has %d markers, want 1", lines[rowLine[1]], n)
	}
	if strings.Contains(stripANSI(lines[rowLine[0]]), "▸") {
		t.Errorf("marker on a row that isn't the cursor: %q", lines[rowLine[0]])
	}
}

// Ctrl-X is Claude-only (the tree has nothing to go back to elsewhere), so the
// panel must not advertise it for codex/agy.
func TestSettingsKeysHidesTreeKeyWhenDisabled(t *testing.T) {
	if got := strings.Join(settingsKeys(false), "\n"); strings.Contains(got, "Ctrl-X") {
		t.Errorf("Ctrl-X shown for a non-Claude session:\n%s", got)
	}
}

// The footer says what the cursor's row will do, and a Confirm row says it asks
// first — so the second ⏎ isn't a surprise.
func TestSettingsFooter(t *testing.T) {
	rows := settingsRowsFor(testHelpInfo(), t.TempDir())
	if got := settingsFooter(rows, settingsState{cursor: 0}); !strings.Contains(got, "←→ change") {
		t.Errorf("ordinary row footer = %q", got)
	}
	hooks := 0
	for i, r := range rows {
		if r.ID == setHooks {
			hooks = i
		}
	}
	if got := settingsFooter(rows, settingsState{cursor: hooks}); !strings.Contains(got, "asks first") {
		t.Errorf("confirm row footer = %q", got)
	}
	if got := settingsFooter(rows, settingsState{cursor: hooks, armed: setHooks}); !strings.Contains(got, "⏎ again") {
		t.Errorf("armed footer = %q", got)
	}
	if got := settingsFooter(rows, settingsState{msg: "theme: nord"}); !strings.Contains(got, "theme: nord") {
		t.Errorf("result footer = %q", got)
	}
}

// savedNote is silent on success (remembering is the expected behaviour) and
// loud on failure (the setting changed but won't survive the session).
func TestSavedNote(t *testing.T) {
	if got := savedNote(nil); got != "" {
		t.Errorf("savedNote(nil) = %q, want empty", got)
	}
	if got := savedNote(errors.New("disk full")); !strings.Contains(got, "not saved") {
		t.Errorf("savedNote(err) = %q", got)
	}
}

func TestLastLine(t *testing.T) {
	if got := lastLine("starting\ndone\n\n"); got != "done" {
		t.Errorf("lastLine = %q, want %q", got, "done")
	}
	if got := lastLine("   \n"); got != "" {
		t.Errorf("lastLine of blank output = %q, want empty", got)
	}
}

// ← has to walk the cycles backwards, not three-quarters of the way round. Both
// cycles wrap, so stepping back from the first lands on the last.
func TestStepTheme(t *testing.T) {
	first, err := stepTheme("", +1)
	if err != nil {
		t.Fatalf("stepTheme: %v", err)
	}
	back, err := stepTheme(first.Name, -1)
	if err != nil {
		t.Fatalf("stepTheme back: %v", err)
	}
	fwd, err := stepTheme(back.Name, +1)
	if err != nil {
		t.Fatalf("stepTheme fwd: %v", err)
	}
	if fwd.Name != first.Name {
		t.Errorf("← then → landed on %q, want %q", fwd.Name, first.Name)
	}
	if back.Name == first.Name {
		t.Errorf("← from the first theme stayed put (%q)", back.Name)
	}
}

func TestStepTools(t *testing.T) {
	r := newRendererWith(nil, Theme{}, "dots", 0, nil)
	start := toolStyleKind(r.toolStyle.Load())
	r.stepTools(+1)
	if toolStyleKind(r.toolStyle.Load()) == start {
		t.Fatal("→ did not move the tool style")
	}
	r.stepTools(-1)
	if got := toolStyleKind(r.toolStyle.Load()); got != start {
		t.Errorf("→ then ← gave %v, want %v", got, start)
	}
	// And it wraps rather than sticking at the end.
	for range len(toolCycle) {
		r.stepTools(-1)
	}
	if got := toolStyleKind(r.toolStyle.Load()); got != start {
		t.Errorf("a full backwards lap gave %v, want %v", got, start)
	}
}

// The theme row carries the theme's colour strip, so ←→ previews a palette
// before the transcript re-renders in it. Only that row — the others have no
// colours to show.
func TestSettingsThemeRowShowsSwatch(t *testing.T) {
	info := testHelpInfo()
	info.ThemeSwatch = "\x1b[38;2;1;2;3m██" + reset
	rows := settingsRowsFor(info, t.TempDir())
	lines, rowLine := settingsLines(settingsTestEnv(t), rows, 1, helpTestTheme())
	for i, r := range rows {
		line := lines[rowLine[i]]
		has := strings.Contains(line, info.ThemeSwatch)
		if r.ID == setTheme && !has {
			t.Errorf("theme row lacks its swatch: %q", line)
		}
		if r.ID != setTheme && has {
			t.Errorf("%s row carries the swatch: %q", r.Label, line)
		}
	}
	theme := lines[rowLine[0]]
	if !strings.Contains(theme, "tokyo-night"+reset+"  "+info.ThemeSwatch) {
		t.Errorf("swatch should follow the name after two spaces: %q", theme)
	}
	if w := visWidth(theme); w != visWidth(strings.Replace(theme, info.ThemeSwatch, "", 1))+2 {
		t.Errorf("swatch should add 2 visible cells, line = %q", theme)
	}
}
