package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// threadsUI is the triage screen's state. Mode is "list", "park", "close",
// "openall", "details", "knowledge" or "filter" (typing into Filter).
// ParkPick indexes parkKinds while Mode is "park"; Input is the note.
type threadsUI struct {
	Threads        []thread
	Facts          map[string]threadFacts
	Cursor, Scroll int
	Mode           string
	Input          string
	ParkPick       int
	Filter         string
	Width, Height  int
	Today          string
	Note           string
}

// threadsAction is what the driver must do after a key. Kind is "" for none.
type threadsAction struct {
	Kind         string // ""|"open"|"openall"|"park"|"unpark"|"close"|"quit"
	Slug, Reason string
	ParkKind     string   // park: a parkKinds key
	Slugs        []string // openall: the open threads in view, in list order
}

const threadsFooter = "⏎ open   O open all…   p park…   u unpark   x close…   d details   k knowledge   / filter   q quit"

func threadSlugOf(t thread) string { return filepath.Base(t.Dir) }

func threadMatches(t thread, q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	if strings.Contains(strings.ToLower(threadDisplayName(t)), q) {
		return true
	}
	for _, s := range t.Sections {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// visibleThreads is the cursor's order: open threads, then parked ones.
// Closed threads and filter misses are left out.
func visibleThreads(ui threadsUI) (open, parked []thread) {
	for _, t := range ui.Threads {
		if !threadMatches(t, ui.Filter) {
			continue
		}
		switch t.Meta.Status {
		case threadParked:
			parked = append(parked, t)
		case threadClosed:
		default:
			open = append(open, t)
		}
	}
	return
}

func threadsAt(ui threadsUI) []thread {
	o, p := visibleThreads(ui)
	return append(o, p...)
}

func updateThreads(ui threadsUI, k treeKey, r rune) (threadsUI, threadsAction) {
	var a threadsAction
	if ui.Mode == "" {
		ui.Mode = "list"
	}
	if k == kCtrlC {
		return ui, threadsAction{Kind: "quit"}
	}
	ts := threadsAt(ui)
	var cur *thread
	if len(ts) > 0 {
		ui.Cursor = max(0, min(ui.Cursor, len(ts)-1))
		cur = &ts[ui.Cursor]
	}
	ui.Note = ""
	switch ui.Mode {
	case "park":
		switch k {
		case kEsc:
			ui.Mode, ui.Input = "list", ""
		case kUp:
			ui.ParkPick = min(len(parkKinds)-1, ui.ParkPick+1)
		case kDown:
			ui.ParkPick = max(0, ui.ParkPick-1)
		case kBackspace:
			if rs := []rune(ui.Input); len(rs) > 0 {
				ui.Input = string(rs[:len(rs)-1])
			}
		case kRune:
			if !unicode.IsControl(r) {
				ui.Input += string(r)
			}
		case kEnter:
			if cur != nil {
				pk := parkKinds[max(0, min(ui.ParkPick, len(parkKinds)-1))]
				a = threadsAction{Kind: "park", Slug: threadSlugOf(*cur), ParkKind: pk.Key, Reason: strings.TrimSpace(ui.Input)}
			}
			ui.Mode, ui.Input = "list", ""
		}
		return ui, a
	case "openall":
		if k == kRune && r == 'y' {
			open, _ := visibleThreads(ui)
			a.Kind = "openall"
			for _, t := range open {
				a.Slugs = append(a.Slugs, threadSlugOf(t))
			}
		}
		ui.Mode = "list"
		return ui, a
	case "close":
		if k == kRune && r == 'y' && cur != nil {
			a = threadsAction{Kind: "close", Slug: threadSlugOf(*cur)}
		}
		ui.Mode = "list"
		return ui, a
	case "filter":
		switch k {
		case kEsc:
			ui.Mode, ui.Filter = "list", ""
		case kEnter:
			ui.Mode = "list"
		case kBackspace:
			if rs := []rune(ui.Filter); len(rs) > 0 {
				ui.Filter = string(rs[:len(rs)-1])
			}
		case kRune:
			if !unicode.IsControl(r) {
				ui.Filter += string(r)
			}
		}
		ui.Cursor, ui.Scroll = 0, 0
		return ui, a
	case "details", "knowledge":
		dpage := max(1, ui.Height/2-1)
		switch {
		case k == kEsc, k == kRune && r == 'q':
			ui.Mode, ui.Scroll = "list", 0
		case k == kUp:
			ui.Scroll--
		case k == kDown:
			ui.Scroll++
		case k == kPageDown, k == kRune && r == ' ':
			ui.Scroll += dpage
		case k == kPageUp:
			ui.Scroll -= dpage
		}
		ui.Scroll = max(0, ui.Scroll)
		return ui, a
	}
	page := max(1, ui.Height/2-1)
	switch {
	case k == kUp:
		ui.Cursor--
		ui.Scroll = 0
	case k == kDown:
		ui.Cursor++
		ui.Scroll = 0
	case k == kHome:
		ui.Cursor, ui.Scroll = 0, 0
	case k == kEnd:
		ui.Cursor, ui.Scroll = len(ts)-1, 0
	case k == kPageDown, k == kRune && r == ' ':
		ui.Scroll += page
	case k == kPageUp:
		ui.Scroll -= page
	case k == kEsc:
		if ui.Filter != "" {
			ui.Filter = ""
			ui.Cursor, ui.Scroll = 0, 0
		} else {
			a.Kind = "quit"
		}
	case k == kEnter:
		if cur != nil {
			a = threadsAction{Kind: "open", Slug: threadSlugOf(*cur)}
		}
	case k == kRune:
		switch r {
		case 'q':
			a.Kind = "quit"
		case '/':
			ui.Mode = "filter"
		case 'p':
			if cur != nil {
				ui.Mode, ui.Input, ui.ParkPick = "park", "", 0
			}
		case 'O':
			if open, _ := visibleThreads(ui); len(open) > 0 {
				ui.Mode = "openall"
			} else {
				ui.Note = "no open threads in view"
			}
		case 'u':
			if cur != nil && cur.Meta.Status == threadParked {
				a = threadsAction{Kind: "unpark", Slug: threadSlugOf(*cur)}
			}
		case 'x':
			if cur != nil {
				ui.Mode = "close"
			}
		case 'd':
			if cur != nil {
				ui.Mode, ui.Scroll = "details", 0
			}
		case 'k':
			if cur != nil {
				ui.Mode, ui.Scroll = "knowledge", 0
			}
		}
	}
	if len(ts) > 0 {
		ui.Cursor = max(0, min(ui.Cursor, len(ts)-1))
	} else {
		ui.Cursor = 0
	}
	// The upper bound on Scroll depends on rendered output; the driver applies
	// clampThreadsScroll after each update.
	ui.Scroll = max(0, ui.Scroll)
	return ui, a
}

// threadsLayout returns the list window height, the number of list display
// rows, and the detail pane height. Rows: header, list, rule, detail, footer.
func threadsLayout(ui threadsUI) (listH, listRows, detailH int) {
	o, p := visibleThreads(ui)
	listRows = len(o)
	if len(p) > 0 {
		listRows += 1 + len(p)
	}
	if listRows == 0 {
		listRows = 1
	}
	avail := max(3, ui.Height) - 3
	listH = max(1, min(listRows, avail/2))
	detailH = max(1, avail-listH)
	return
}

const (
	tvDim   = "\x1b[2m"
	tvBold  = "\x1b[1m"
	tvRed   = "\x1b[31m"
	tvYel   = "\x1b[33m"
	tvReset = "\x1b[0m"
)

func threadDays(updated, today string) (int, bool) {
	u, err1 := time.Parse("2006-01-02", updated)
	t, err2 := time.Parse("2006-01-02", today)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return int(t.Sub(u).Hours() / 24), true
}

func threadAge(updated, today string) string {
	n, ok := threadDays(updated, today)
	switch {
	case !ok:
		return ""
	case n <= 0:
		return "today"
	case n == 1:
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}

func threadToneColour(tone string) string {
	switch tone {
	case "stale":
		return tvRed
	case "risk":
		return tvYel
	case "you":
		return tvBold + tvYel
	}
	return ""
}

func renderMD(render func(string) (string, error), s string) string {
	if render == nil {
		return s
	}
	out, err := render(s)
	if err != nil {
		return s
	}
	return out
}

func dimLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.ReplaceAll(l, "\x1b[0m", tvReset+tvDim)
		l = strings.ReplaceAll(l, "\x1b[m", tvReset+tvDim)
		out = append(out, tvDim+l+tvReset)
	}
	return out
}

func physRows(line string, w int) int {
	return max(1, (visWidth(line)+w-1)/w)
}

// detailWindow takes lines from sc while their physical rows (a long line the
// terminal soft-wraps costs several) fit in h. A first line bigger than h is
// hard-truncated to h rows. Returns the lines and the rows they use.
func detailWindow(lines []string, sc, h, w int) ([]string, int) {
	var out []string
	used := 0
	for i := max(0, sc); i < len(lines); i++ {
		n := physRows(lines[i], w)
		if len(out) == 0 && n > h {
			return []string{truncVisible(lines[i], h*w)}, h
		}
		if used+n > h {
			break
		}
		out = append(out, lines[i])
		used += n
	}
	return out, used
}

// maxThreadsScroll is the largest scroll that still shows the last line.
func maxThreadsScroll(lines []string, h, w int) int {
	used := 0
	for i := len(lines) - 1; i >= 0; i-- {
		used += physRows(lines[i], w)
		if used > h {
			return min(i+1, len(lines)-1)
		}
	}
	return 0
}

// clampThreadsScroll bounds Scroll with the same lines the renderer windows.
// The driver calls it after every update, since render cannot mutate ui.
func clampThreadsScroll(ui threadsUI, render func(string) (string, error), knowledge func(string) string) threadsUI {
	if ui.Height < 6 {
		ui.Height = 6
	}
	_, _, detailH := threadsLayout(ui)
	lines := threadDetailLines(ui, render, knowledge)
	ui.Scroll = max(0, min(ui.Scroll, maxThreadsScroll(lines, detailH, max(20, ui.Width))))
	return ui
}

// threadDetailLines is the whole detail pane as rows, wrapped to the width and
// never truncated; the caller windows it by Scroll.
func threadDetailLines(ui threadsUI, render func(string) (string, error), knowledge func(string) string) []string {
	ts := threadsAt(ui)
	if len(ts) == 0 {
		return nil
	}
	t := ts[max(0, min(ui.Cursor, len(ts)-1))]
	w := max(20, ui.Width)
	var out []string
	add := func(s string) {
		out = append(out, strings.Split(strings.TrimRight(wrapANSI(s, w), "\n"), "\n")...)
	}
	out = append(out, tvBold+threadDisplayName(t)+tvReset, "")
	switch ui.Mode {
	case "details":
		f := ui.Facts[threadSlugOf(t)]
		add("CLAIMS")
		for _, c := range t.Meta.Claims {
			add("  " + describeClaim(c))
		}
		add("")
		add("SESSIONS")
		for _, s := range t.Meta.Sessions {
			add("  " + s.Agent + " " + s.ID)
		}
		add("")
		add("CHECKS")
		for _, d := range f.Details {
			add("  " + d)
		}
		return out
	case "knowledge":
		if knowledge != nil {
			add(renderMD(render, knowledge(threadSlugOf(t))))
		}
		return out
	}
	for _, h := range threadSections {
		body := strings.TrimSpace(t.Sections[h])
		if body == "" {
			continue
		}
		label := strings.ToUpper(h)
		stale := false
		if d := t.Meta.AsOf[h]; d != "" {
			if n, ok := threadDays(d, ui.Today); ok && n > 7 {
				stale = true
				if ad, err := time.Parse("2006-01-02", d); err == nil {
					label += " · as of " + ad.Format("2 Jan")
				}
			}
		}
		out = append(out, tvDim+label+tvReset)
		text := strings.TrimRight(wrapANSI(renderMD(render, body), w), "\n")
		if stale {
			out = append(out, dimLines(text)...)
		} else {
			out = append(out, strings.Split(text, "\n")...)
		}
		out = append(out, "")
	}
	return out
}

func describeClaim(c threadClaim) string {
	switch c.Kind {
	case "worktree":
		return fmt.Sprintf("worktree %s (%s) head %s", c.Path, c.Branch, firstNonEmpty(c.Head, "unpushed"))
	case "trail":
		return fmt.Sprintf("trail %s expect %s", c.Trail, c.Expect)
	case "linear":
		return fmt.Sprintf("linear %s expect %s", c.Linear, c.Expect)
	}
	return c.Kind
}

func renderThreads(ui threadsUI, render func(string) (string, error), knowledge func(slug string) string) string {
	w := max(20, ui.Width)
	if ui.Mode == "" {
		ui.Mode = "list"
	}
	if ui.Height < 6 {
		ui.Height = 6
	}
	var rows []string
	row := func(s string) { rows = append(rows, truncVisible(s, w)) }

	open, parked := visibleThreads(ui)
	hdr := "threads"
	if d, err := time.Parse("2006-01-02", ui.Today); err == nil {
		hdr += "  " + d.Format("Monday 2 Jan")
	}
	hdr += fmt.Sprintf(" · %d open · %d parked", len(open), len(parked))
	if ui.Filter != "" || ui.Mode == "filter" {
		hdr += "  /" + ui.Filter
	}
	row(tvBold + hdr + tvReset)

	if len(open)+len(parked) == 0 {
		msg := "no threads yet: run /handover in a session to start one"
		if ui.Filter != "" {
			msg = "no threads match /" + ui.Filter
		}
		row("")
		row(msg)
		for len(rows) < ui.Height-1 {
			rows = append(rows, "")
		}
		rows = append(rows, truncVisible(footerFor(ui), w))
		return strings.Join(rows, "\n")
	}

	cur := max(0, min(ui.Cursor, len(open)+len(parked)-1))
	nameW := 8
	for _, t := range append(append([]thread{}, open...), parked...) {
		nameW = max(nameW, min(28, visWidth(threadDisplayName(t))))
	}
	var list []string
	curRow := 0
	mark := func(i int) string {
		if i == cur {
			return "▸ "
		}
		return "  "
	}
	for i, t := range open {
		f := ui.Facts[threadSlugOf(t)]
		text, tone := threadListStatus(t, f)
		line := mark(i) + padVisible(truncVisible(threadDisplayName(t), nameW), nameW) + "  " + threadToneColour(tone) + text + tvReset
		if age := threadAge(t.Meta.Updated, ui.Today); age != "" {
			line += "  " + tvDim + age + tvReset
		}
		if i == cur {
			curRow = len(list)
			line = tvBold + line + tvReset
		}
		list = append(list, truncVisible(line, w))
	}
	if len(parked) > 0 {
		list = append(list, tvDim+"── parked ──"+tvReset)
		for j, t := range parked {
			i := len(open) + j
			line := tvDim + mark(i) + padVisible(truncVisible(threadDisplayName(t), nameW), nameW) + tvReset + "  " + parkedLabel(t.Meta, ui.Today)
			if i == cur {
				curRow = len(list)
			}
			list = append(list, truncVisible(line, w))
		}
	}

	listH, _, detailH := threadsLayout(ui)
	top := 0
	if len(list) > listH {
		top = max(0, min(curRow-listH+1, len(list)-listH))
	}
	rows = append(rows, list[top:min(len(list), top+listH)]...)
	for len(rows) < 1+listH {
		rows = append(rows, "")
	}
	rows = append(rows, tvDim+strings.Repeat("─", w)+tvReset)

	lines := threadDetailLines(ui, render, knowledge)
	sc := max(0, min(ui.Scroll, maxThreadsScroll(lines, detailH, w)))
	win, used := detailWindow(lines, sc, detailH, w)
	rows = append(rows, win...)
	for ; used < detailH; used++ {
		rows = append(rows, "")
	}
	rows = append(rows, truncVisible(footerFor(ui), w))
	if ui.Mode == "park" && len(threadsAt(ui)) > 0 {
		box := parkBoxLines(threadDisplayName(threadsAt(ui)[cur]), ui.ParkPick, ui.Input, ui.Today)
		top := max(1, len(rows)-1-len(box))
		overlayRows(rows, box, top, max(0, (w-parkBoxW)/2))
	}
	return strings.Join(rows, "\n")
}

func footerFor(ui threadsUI) string {
	foot := threadsFooter
	switch ui.Mode {
	case "park":
		foot = "↑↓ pick a reason · type to add a note · ⏎ park · Esc cancel"
	case "openall":
		open, _ := visibleThreads(ui)
		foot = "open " + plural(len(open), "thread") + " in new tabs here, each a fresh Claude that reassesses first? y to confirm, any other key cancels"
	case "close":
		foot = "close this thread? y to confirm, any other key cancels"
	case "details", "knowledge":
		foot = "↑↓ PgUp PgDn scroll   q back"
	case "filter":
		foot = "filter: " + ui.Filter + "▏  (⏎ keep, Esc clear)"
	}
	if ui.Note != "" {
		foot = tvYel + ui.Note + tvReset + "   " + foot
	}
	return foot
}
