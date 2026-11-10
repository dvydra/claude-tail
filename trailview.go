package main

import (
	"fmt"
	"io"
	"os"
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

// trailModalLines lays out every tracked change under its project trail, then
// fetch errors for things with no state, then the status-line history newest
// first. branch names what was checked when nothing is tracked.
func trailModalLines(states []changeState, notes []trailNoteRec, errs map[string]string, branch string, theme Theme, w int) []string {
	dim := func(s string) string { return theme.DimANSI + s + reset }
	bold := func(s string) string { return "\x1b[1m" + s + reset }
	col := func(s trailSev, text string) string { return trailSevANSI(s, theme) + text + reset }
	var L []string

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
		head := "  " + bold(s.Ref.Label()) + "  " + s.Branch
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
	return L
}

func inList(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// runTrailModal draws the modal until q/Esc. load is called every frame (the
// read is timed, so new state shows up while it is open); refresh is `r`.
func runTrailModal(tty *os.File, theme Theme, load func() ([]changeState, []trailNoteRec, map[string]string, string), refresh func()) {
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

	top := 0
	buf := make([]byte, 16)
	for {
		states, notes, errs, branch := load()
		w, h := termSize(tty)
		lines := trailModalLines(states, notes, errs, branch, theme, w)
		body := helpBodyRows(len(lines), h)
		top = max(min(top, len(lines)-body), 0)
		io.WriteString(tty, drawPanel(lines, top, w, h, theme, " ↑↓ scroll · r refresh · q close "))

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
		switch {
		case k == kEsc || k == kCtrlC || (k == kRune && (r == 'q' || r == 'Q' || r == 'g')):
			return
		case k == kUp:
			top--
		case k == kDown:
			top++
		case k == kPageUp:
			top -= body
		case k == kPageDown:
			top += body
		case k == kHome:
			top = 0
		case k == kEnd:
			top = len(lines)
		case k == kRune && (r == 'r' || r == 'R'):
			refresh()
		}
	}
}
