package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// parkKind is one reason a thread can be parked for. Only "not today" changes
// what the tool does (the thread comes back by itself the next working day);
// the rest are there so the parked list says at a glance what each one waits on.
type parkKind struct {
	Key, Label string
	Wakes      bool
	FG, BG     int // 256-colour palette: the label, and the picker's selected row
}

// parkKinds is the picker's order from the BOTTOM up: the default sits next to
// the footer, so `p ⏎` parks as "not today" and the others are a few ↑ away.
var parkKinds = []parkKind{
	{Key: "not_today", Label: "not today", Wakes: true, FG: 117, BG: 25},
	{Key: "review", Label: "waiting on review", FG: 141, BG: 55},
	{Key: "customer", Label: "waiting on customer", FG: 215, BG: 130},
	{Key: "someone", Label: "waiting on someone", FG: 186, BG: 58},
	{Key: "someday", Label: "someday", FG: 248, BG: 240},
}

func parkKindByKey(k string) (parkKind, bool) {
	k = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(k)), "-", "_")
	for _, p := range parkKinds {
		if p.Key == k {
			return p, true
		}
	}
	return parkKind{}, false
}

func parkKindKeys() string {
	var ks []string
	for _, p := range parkKinds {
		ks = append(ks, strings.ReplaceAll(p.Key, "_", "-"))
	}
	return strings.Join(ks, ", ")
}

// parkSummary is the log line's text: "not today", "waiting on customer: Acme
// SSO answer", or a free-text reason on its own.
func parkSummary(label, note string) string {
	switch {
	case label == "":
		return note
	case note == "":
		return label
	}
	return label + ": " + note
}

func clearPark(m *threadMeta) {
	m.ParkKind, m.ParkReason, m.ParkedAt, m.WakesOn = "", "", "", ""
}

// nextWorkday is tomorrow, or Monday from a Friday or the weekend.
func nextWorkday(now time.Time) time.Time {
	d := now.AddDate(0, 0, 1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// dueToWake lists the parked threads whose wakes_on has arrived.
func dueToWake(ts []thread, today string) []string {
	var out []string
	for _, t := range ts {
		if t.Meta.Status == threadParked && t.Meta.WakesOn != "" && t.Meta.WakesOn <= today {
			out = append(out, threadSlugOf(t))
		}
	}
	return out
}

// wakeThreads unparks every thread that is due and reports how many woke. It
// runs on the list paths (the TUI and `threads list`), so a "not today" park
// is back on the open list the next working day without anyone remembering it.
func wakeThreads(root string, now time.Time) int {
	n := 0
	for _, slug := range dueToWake(loadThreads(root), now.Format("2006-01-02")) {
		if moveThread(root, slug, threadOpen, "", "", now) == nil {
			_ = appendThreadLog(filepath.Join(root, slug), now, "woke: the park was for one day")
			n++
		}
	}
	return n
}

func fg256(n int) string { return fmt.Sprintf("\x1b[38;5;%dm", n) }
func bg256(n int) string { return fmt.Sprintf("\x1b[48;5;%dm", n) }

// parkedLabel is a parked row's reason: the kind in its colour, when it wakes,
// and the note.
func parkedLabel(m threadMeta, today string) string {
	pk, ok := parkKindByKey(m.ParkKind)
	if !ok {
		return tvDim + `("` + m.ParkReason + `")` + tvReset
	}
	s := fg256(pk.FG) + pk.Label + tvReset
	if m.WakesOn != "" {
		s += tvDim + " · wakes " + wakeDay(m.WakesOn, today) + tvReset
	}
	if m.ParkReason != "" {
		s += tvDim + ` · "` + m.ParkReason + `"` + tvReset
	}
	return s
}

// wakeDay names a wake date the way you'd say it: "tomorrow", "Mon", "12 Nov".
func wakeDay(day, today string) string {
	d, err1 := time.Parse("2006-01-02", day)
	t, err2 := time.Parse("2006-01-02", today)
	if err1 != nil || err2 != nil {
		return day
	}
	switch n := int(d.Sub(t).Hours() / 24); {
	case n <= 0:
		return "today"
	case n == 1:
		return "tomorrow"
	case n < 7:
		return d.Format("Mon")
	}
	return d.Format("2 Jan")
}

const (
	parkBoxBG = 236
	parkBoxW  = 46
)

// parkBoxLines draws the park picker: a filled box, one row per kind with the
// selected row on the kind's own background, and the note being typed.
func parkBoxLines(name string, pick int, note, today string) []string {
	inner := parkBoxW - 2
	bg := bg256(parkBoxBG)
	edge := bg + fg256(244)
	fill := func(s string, w int) string { return padVisible(truncVisible(s, w), w) }
	title := " park " + `"` + truncVisible(name, inner-12) + `" `
	top := edge + "╭─" + title + strings.Repeat("─", max(0, inner-1-visWidth(title))) + "╮" + tvReset
	out := []string{top}
	row := func(content string) string {
		return edge + "│" + bg + fill(content, inner) + edge + "│" + tvReset
	}
	for i := len(parkKinds) - 1; i >= 0; i-- {
		pk := parkKinds[i]
		hint := ""
		if pk.Wakes {
			hint = "wakes " + wakeDay(nextWorkdayFrom(today), today)
		}
		label := pk.Label
		if i == pick {
			body := fill("▸ "+label+strings.Repeat(" ", max(1, inner-6-len(label)-len(hint)))+hint+" ", inner-2)
			out = append(out, edge+"│"+bg+" "+bg256(pk.BG)+"\x1b[1;97m"+body+tvReset+bg+" "+edge+"│"+tvReset)
			continue
		}
		gap := strings.Repeat(" ", max(1, inner-6-len(label)-len(hint)))
		out = append(out, row("   "+fg256(pk.FG)+label+bg+fg256(242)+gap+hint))
	}
	out = append(out, row(""))
	out = append(out, row(" "+fg256(250)+"note: "+"\x1b[97m"+note+"▏"))
	out = append(out, edge+"╰"+strings.Repeat("─", inner)+"╯"+tvReset)
	return out
}

func nextWorkdayFrom(today string) string {
	t, err := time.Parse("2006-01-02", today)
	if err != nil {
		return ""
	}
	return nextWorkday(t).Format("2006-01-02")
}

// overlayRows lays box over rows with its top-left at (top, col), keeping the
// base text either side of it. Rows past the end are left alone.
func overlayRows(rows []string, box []string, top, col int) {
	for i, b := range box {
		r := top + i
		if r < 0 || r >= len(rows) {
			continue
		}
		rows[r] = spliceVisible(rows[r], b, col)
	}
}

// spliceVisible replaces the visible columns [col, col+width(box)) of base
// with box. The text to the right keeps the colour it had at that column.
func spliceVisible(base, box string, col int) string {
	left := padVisible(truncVisible(base, col), col)
	state, right := dropVisible(base, col+visWidth(box))
	return left + tvReset + box + tvReset + state + right
}

// dropVisible skips n visible columns of s and returns the SGR state in force
// at that point and the rest of s.
func dropVisible(s string, n int) (string, string) {
	rs := []rune(s)
	vis := 0
	active := ""
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b {
			esc, k := readEscape(rs[i:])
			active = applySGR(active, esc)
			i += k - 1
			continue
		}
		if vis == n {
			return active, string(rs[i:])
		}
		vis++
	}
	return active, ""
}

// threadReassessPrompt is the first message of a fresh session opened on a
// thread from `O`: read the thread, check the world, report, then wait.
func threadReassessPrompt(t thread) string {
	var b strings.Builder
	fmt.Fprintf(&b, "We are picking up the thread “%s”. ", threadDisplayName(t))
	fmt.Fprintf(&b, "Read %s", filepath.Join(t.Dir, "thread.md"))
	if _, err := os.Stat(filepath.Join(t.Dir, "knowledge.md")); err == nil {
		fmt.Fprintf(&b, " and %s", filepath.Join(t.Dir, "knowledge.md"))
	}
	b.WriteString(". It was last updated")
	if t.Meta.Updated != "" {
		b.WriteString(" on " + t.Meta.Updated)
	}
	b.WriteString(", so treat it as a claim about the past, not the truth. ")
	b.WriteString("Before doing anything else, reassess the current state: check every worktree, branch, trail and Linear issue it names (git status and log, `entire trail show`, Linear), and anything it says it is waiting on. ")
	b.WriteString("Pick a 1 to 3 word title naming the work: plain words, no codenames, and shorter than the thread name if a shorter name is just as clear. Run `entire-tail threads title <title>` to put it on this iTerm tab; this is allowed before I say go. ")
	b.WriteString("Then give me a short overview, with the title alone on its first line: what this thread is, where it actually is now, what changed since the thread was written, what is in the way, and the next step you propose. ")
	b.WriteString("Apart from the tab title, do not change any code, branch, trail or issue until I say go.")
	return b.String()
}
