package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// trailview.go — the `g` modal: the full state of every trail this tail
// watches, in a scrollable alt-screen panel. The status lines in the tail say
// what changed; this says where everything stands. Pure lines builder +
// thin tty driver, like the settings panel.

const (
	trailGoodANSI = "\x1b[38;2;154;230;0m"
	trailWarnANSI = "\x1b[38;2;255;185;0m"
	trailBadANSI  = "\x1b[38;2;251;44;54m"
)

func trailSevANSI(s trailSev, theme Theme) string {
	switch s {
	case trailSevGood:
		return trailGoodANSI
	case trailSevWarn:
		return trailWarnANSI
	case trailSevBad:
		return trailBadANSI
	}
	return theme.DimANSI
}

func trailGateGlyph(g trailGate) (string, trailSev) {
	switch g.Outcome {
	case "passed":
		return "✔", trailSevGood
	case "failed":
		return "✗", trailSevBad
	}
	return "…", trailSevNeutral
}

func trailHistory(h []trailSev, theme Theme) string {
	if len(h) > 6 {
		h = h[len(h)-6:]
	}
	var b strings.Builder
	for _, s := range h {
		b.WriteString(trailSevANSI(s, theme) + "■" + reset)
	}
	return b.String()
}

var trailSeverityOrder = []string{"high", "medium", "low", ""}

func trailSeverityTitle(s string) string {
	if s == "" {
		return "Unspecified"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// trailModalRow is one selectable change in the panel: its key, its page, and
// the line its header is on.
type trailModalRow struct {
	Key, URL string
	Line     int
}

// trailModalLines lays out every tracked change under its project trail, then
// fetch errors for things with no state, then the status-line history newest
// first. branch names what was checked when nothing is tracked; sel is the
// selected change's key, marked with ▸.
func trailModalLines(states []changeState, notes []trailNoteRec, errs map[string]string, branch string, theme Theme, w int, sel string) (L []string, rows []trailModalRow) {
	dim := func(s string) string { return theme.DimANSI + s + reset }
	bold := func(s string) string { return "\x1b[1m" + s + reset }
	col := func(s trailSev, text string) string { return trailSevANSI(s, theme) + text + reset }

	sorted := append([]changeState(nil), states...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Ref.ProjectFlag() != b.Ref.ProjectFlag() {
			return a.Ref.ProjectFlag() < b.Ref.ProjectFlag()
		}
		if a.ProjectNumber != b.ProjectNumber {
			return a.ProjectNumber < b.ProjectNumber
		}
		return a.Ref.Key() < b.Ref.Key()
	})
	shown := map[string]bool{}
	lastProject := ""
	for _, s := range sorted {
		pk := s.Ref.ProjectFlag() + "#" + strconv.Itoa(s.ProjectNumber)
		if pk != lastProject {
			if lastProject != "" {
				L = append(L, "")
			}
			L = append(L, bold("P"+strconv.Itoa(s.ProjectNumber))+"  "+s.ProjectTitle+"  "+dim(s.Ref.ProjectFlag()))
			lastProject = pk
		}
		shown[s.Ref.Key()] = true
		mark := "  "
		if s.Ref.Key() == sel {
			mark = col(trailSevGood, "▸ ")
		}
		rows = append(rows, trailModalRow{Key: s.Ref.Key(), URL: s.URL, Line: len(L)})
		head := mark + bold(s.Ref.Label()) + "  " + s.Branch
		if s.Base != "" {
			head += " → " + s.Base
		}
		L = append(L, head+"  "+dim(s.Status))
		if e := errs[s.Ref.Key()]; e != "" {
			L = append(L, "    "+col(trailSevBad, "! "+lastLine(e)))
		}
		if len(s.Agents) > 0 {
			L = append(L, "    "+dim("agents"))
			for _, a := range s.Agents {
				v := a.Verdict
				if v == "" {
					v = "–"
				}
				run := ""
				switch a.RunStatus {
				case "failed":
					run = "  " + col(trailSevBad, "run failed")
				case "queued", "pending", "running", "in_progress":
					run = "  " + dim("running")
				}
				L = append(L, fmt.Sprintf("      %-18s %s  %s%s", a.Name, col(a.Sev, fmt.Sprintf("%-5s", v)), trailHistory(a.History, theme), run))
			}
		}
		if len(s.Gates) > 0 {
			L = append(L, "    "+dim("gates"))
			for _, g := range s.Gates {
				glyph, sev := trailGateGlyph(g)
				L = append(L, fmt.Sprintf("      %s %-14s %s", col(sev, glyph), g.Key, dim(g.Detail)))
			}
		}
		m, msev := trailMergeSummary(s)
		L = append(L, "    "+dim("merge ")+col(msev, m))
		if len(s.Findings) > 0 {
			L = append(L, "    "+dim("findings"))
			for _, sevName := range trailSeverityOrder {
				var group []trailFinding
				for _, f := range s.Findings {
					if f.Severity == sevName || (sevName == "" && !inList(f.Severity, trailSeverityOrder[:3])) {
						group = append(group, f)
					}
				}
				if len(group) == 0 {
					continue
				}
				gsev := map[string]trailSev{"high": trailSevBad, "medium": trailSevWarn, "low": trailSevGood}[sevName]
				L = append(L, "      "+col(gsev, trailSeverityTitle(sevName)))
				for _, f := range group {
					where := f.File
					if f.Line > 0 {
						where += ":" + strconv.Itoa(f.Line)
					}
					meta := f.Author
					if where != "" {
						meta += " · " + where
					}
					L = append(L, "        "+dim(meta))
					L = append(L, "        "+truncVisible(f.Body, max(w-14, 20)))
				}
			}
		}
	}

	var orphan []string
	for k, e := range errs {
		if !shown[k] {
			orphan = append(orphan, k+": "+lastLine(e))
		}
	}
	sort.Strings(orphan)
	if len(sorted) == 0 {
		msg := "No trail for this session"
		L = append(L, bold(msg))
		if branch != "" {
			L = append(L, dim("branch "+branch+" checked, and no trail named in the last 24h"))
		}
	}
	if len(orphan) > 0 {
		L = append(L, "", dim("couldn't read"))
		for _, o := range orphan {
			L = append(L, "  "+col(trailSevBad, "! "+o))
		}
	}
	if len(notes) > 0 {
		L = append(L, "", dim("status lines, newest first"))
		for i := len(notes) - 1; i >= 0; i-- {
			n := notes[i]
			L = append(L, "  "+col(n.Sev, trailEscapes.ReplaceAllString(n.Text, "")))
		}
	}
	return L, rows
}

func inList(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type trailModalState struct{ Sel, Top int }

type trailModalAction int

const (
	trailModalNone trailModalAction = iota
	trailModalClose
	trailModalRefresh
	trailModalRemove // x: stop watching the selected change
	trailModalOpen   // ⏎: open the selected change's page
)

// updateTrailModal is the panel's reducer. ↑↓ move between changes and keep
// the selected one in view; past the first or last change they scroll, so the
// findings under the last one are still reachable.
func updateTrailModal(st trailModalState, rows []trailModalRow, nLines, body int, k treeKey, r rune) (trailModalState, trailModalAction) {
	clampTop := func(top int) int { return min(max(top, 0), max(nLines-body, 0)) }
	st.Sel = min(max(st.Sel, 0), max(len(rows)-1, 0))
	switch {
	case k == kEsc || k == kCtrlC || (k == kRune && (r == 'q' || r == 'Q' || r == 'g')):
		return st, trailModalClose
	case k == kDown && st.Sel < len(rows)-1:
		st.Sel++
		st.Top = scrollTo(st.Top, rows[st.Sel].Line, body, nLines)
	case k == kDown:
		st.Top = clampTop(st.Top + 1)
	case k == kUp && st.Sel > 0:
		st.Sel--
		st.Top = scrollTo(st.Top, rows[st.Sel].Line, body, nLines)
	case k == kUp:
		st.Top = clampTop(st.Top - 1)
	case k == kPageUp:
		st.Top = clampTop(st.Top - body)
	case k == kPageDown:
		st.Top = clampTop(st.Top + body)
	case k == kHome:
		st.Top = 0
	case k == kEnd:
		st.Top = clampTop(nLines)
	case k == kRune && (r == 'r' || r == 'R'):
		return st, trailModalRefresh
	case len(rows) == 0:
	case k == kRune && (r == 'x' || r == 'X'):
		return st, trailModalRemove
	case k == kEnter:
		return st, trailModalOpen
	}
	return st, trailModalNone
}

// trailOpenURL opens a page in the browser. A package var so tests never do.
var trailOpenURL = func(url string) error { return exec.Command("open", url).Start() }

// runTrailModal draws the modal until q/Esc. load is called every frame (the
// read is timed, so new state shows up while it is open); refresh is `r`,
// remove is `x` on the selected change.
func runTrailModal(tty *os.File, theme Theme, load func() ([]changeState, []trailNoteRec, map[string]string, string), refresh func(), remove func(key string)) {
	if tty == nil {
		return
	}
	saved, ok := setRawTimed(tty)
	if !ok {
		return
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	st := trailModalState{}
	selKey := ""
	buf := make([]byte, 16)
	for {
		states, notes, errs, branch := load()
		w, h := termSize(tty)
		_, rows := trailModalLines(states, notes, errs, branch, theme, w, "")
		// Keep the selection on the same change when others come and go.
		for i, row := range rows {
			if row.Key == selKey {
				st.Sel = i
			}
		}
		st.Sel = min(max(st.Sel, 0), max(len(rows)-1, 0))
		if len(rows) > 0 {
			selKey = rows[st.Sel].Key
		}
		lines, _ := trailModalLines(states, notes, errs, branch, theme, w, selKey)
		body := helpBodyRows(len(lines), h)
		st.Top = min(max(st.Top, 0), max(len(lines)-body, 0))
		io.WriteString(tty, drawPanel(lines, st.Top, w, h, theme, " ↑↓ select · ⏎ open · x remove · r refresh · q close "))

		// A raw timed read reports a quiet interval as (0, io.EOF): a redraw
		// tick, not the end of input (focus.go's gotcha).
		n, err := tty.Read(buf)
		if err != nil && err != io.EOF {
			return
		}
		if n == 0 {
			continue
		}
		k, r := decodeKey(buf[:n])
		var act trailModalAction
		st, act = updateTrailModal(st, rows, len(lines), body, k, r)
		if len(rows) > 0 {
			selKey = rows[st.Sel].Key
		}
		switch act {
		case trailModalClose:
			return
		case trailModalRefresh:
			refresh()
		case trailModalRemove:
			remove(selKey)
			selKey = ""
		case trailModalOpen:
			if u := rows[st.Sel].URL; u != "" {
				_ = trailOpenURL(u)
			}
		}
	}
}
