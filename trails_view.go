package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

type trailsUI struct {
	Catalog                              trailsCatalog
	Query, SelectedKey, Error, Collector string
	Filtering                            bool
	Width, Height, Top                   int
	SessionKeys                          []string
	SessionCursor                        int
}
type trailsAction struct {
	OpenURL, SessionKey string
	Refresh, Quit       bool
}

func updateTrails(ui trailsUI, key treeKey, r rune, now time.Time) (trailsUI, trailsAction) {
	a := trailsAction{}
	if key == kCtrlC {
		a.Quit = true
		return ui, a
	}
	if len(ui.SessionKeys) > 0 {
		switch key {
		case kUp:
			ui.SessionCursor--
		case kDown:
			ui.SessionCursor++
		case kEsc:
			ui.SessionKeys = nil
		case kRune:
			if r == 'q' {
				ui.SessionKeys = nil
			}
		case kEnter:
			a.SessionKey = ui.SessionKeys[ui.SessionCursor]
			ui.SessionKeys = nil
		}
		ui.SessionCursor = max(0, min(ui.SessionCursor, len(ui.SessionKeys)-1))
		return ui, a
	}
	if ui.Filtering {
		switch key {
		case kEsc:
			ui.Filtering = false
			ui.Query = ""
		case kEnter:
			ui.Filtering = false
		case kBackspace:
			rs := []rune(ui.Query)
			if len(rs) > 0 {
				ui.Query = string(rs[:len(rs)-1])
			}
		case kRune:
			if !unicode.IsControl(r) {
				ui.Query += string(r)
			}
		}
		key = kNone
	}
	rows := selectTrails(ui.Catalog, now, ui.Query)
	idx := 0
	for i, row := range rows {
		if row.Key == ui.SelectedKey {
			idx = i
			break
		}
	}
	switch key {
	case kUp:
		idx--
	case kDown:
		idx++
	case kHome:
		idx = 0
	case kEnd:
		idx = len(rows) - 1
	case kPageUp:
		idx -= max(1, (ui.Height-8)/3)
	case kPageDown:
		idx += max(1, (ui.Height-8)/3)
	case kEsc:
		if ui.Query != "" {
			ui.Query = ""
		} else {
			a.Quit = true
		}
	case kRune:
		switch r {
		case 'q':
			a.Quit = true
		case '/':
			ui.Filtering = true
		case 'r':
			a.Refresh = true
		}
	}
	if len(rows) > 0 {
		idx = max(0, min(idx, len(rows)-1))
		ui.SelectedKey = rows[idx].Key
		if key == kEnter {
			a.OpenURL = rows[idx].Trail.URL
		}
		if key == kRune && r == 's' {
			if len(rows[idx].SessionKeys) == 1 {
				a.SessionKey = rows[idx].SessionKeys[0]
			} else {
				ui.SessionKeys = rows[idx].SessionKeys
				ui.SessionCursor = 0
			}
		}
		budget := max(1, (ui.Height-8)/3)
		ui.Top = max(0, min(ui.Top, idx))
		if idx >= ui.Top+budget {
			ui.Top = idx - budget + 1
		}
	}
	return ui, a
}

func trailsSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
}

func renderTrails(ui trailsUI, now time.Time) string {
	w, h := max(1, ui.Width), max(1, ui.Height)
	lines := []string{"\x1b[1;36m TRAILS\x1b[0m  " + now.Format("Mon 2 Jan") + "  ·  Right now / Today", ""}
	if ui.Filtering || ui.Query != "" {
		lines = append(lines, " / "+trailsSafe(ui.Query)+"▏")
	} else {
		lines = append(lines, " / search trails, repos, sessions")
	}
	lines = append(lines, "")
	rows := selectTrails(ui.Catalog, now, ui.Query)
	if len(ui.SessionKeys) > 0 {
		lines = append(lines, "\x1b[1m Choose a session\x1b[0m")
		start := max(0, ui.SessionCursor-max(1, h-9)+1)
		for i := start; i < len(ui.SessionKeys) && len(lines) < h-3; i++ {
			s := ui.Catalog.Sessions[ui.SessionKeys[i]]
			prefix := "   "
			if i == ui.SessionCursor {
				prefix = " ▸ "
			}
			lines = append(lines, prefix+trailsSafe(string(s.Agent)+" · "+firstNonEmpty(s.Name, s.ID)+" · "+firstNonEmpty(s.State, "stopped")))
		}
	} else if len(rows) == 0 {
		if ui.Query != "" {
			lines = append(lines, " No matches. Esc clears the search.")
		} else {
			lines = append(lines, " No trails found today.", " Links and session branches are collected automatically.")
		}
	} else {
		section := ""
		for i := min(ui.Top, len(rows)-1); i < len(rows); i++ {
			row := rows[i]
			label := "EARLIER TODAY"
			if row.Active {
				label = "RIGHT NOW"
			}
			needed := 2
			if label != section {
				needed++
			}
			if len(lines)+needed > h-3 {
				break
			}
			if label != section {
				lines = append(lines, "\x1b[1;36m "+label+"\x1b[0m")
				section = label
			}
			_, _, number, _ := parseTrailsURL(row.Trail.URL)
			prefix := "   "
			color := ""
			if row.Key == ui.SelectedKey {
				prefix = " ▸ "
				color = "\x1b[7m"
			}
			status := firstNonEmpty(row.Trail.Status, "unknown")
			if row.Trail.MetadataError != "" {
				status += " · stale"
			}
			title := trailsSafe(row.Trail.Repo + " #" + number + "  " + firstNonEmpty(row.Trail.Title, "(loading title)") + "  [" + status + "]")
			lines = append(lines, color+prefix+title+"\x1b[0m")
			var sessionLabels []string
			for _, id := range row.SessionKeys {
				s := ui.Catalog.Sessions[id]
				a := row.Trail.Associations[id]
				state := "stopped"
				if s.Active {
					state = firstNonEmpty(s.State, "active")
				}
				if !a.BranchMatched {
					state = "mentioned"
				}
				sessionLabels = append(sessionLabels, string(s.Agent)+" "+firstNonEmpty(s.Name, shortID(s.ID))+" ("+state+")")
			}
			lines = append(lines, "     "+time.Unix(row.LastAt, 0).In(now.Location()).Format("15:04")+" · "+trailsSafe(strings.Join(sessionLabels, " · ")))
		}
	}
	for len(lines) < h-3 {
		lines = append(lines, "")
	}
	footer := firstNonEmpty(ui.Collector, "background off") + " · updated " + trailsScanTime(ui.Catalog.UpdatedAt)
	if ui.Error != "" {
		footer += " · " + ui.Error
	} else if len(ui.Catalog.Errors) > 0 {
		footer += fmt.Sprintf(" · %d degraded sources · %s", len(ui.Catalog.Errors), ui.Catalog.Errors[0])
	}
	hints := " / search  Enter open  s session  r refresh  q quit"
	if w < visWidth(hints) {
		hints = " /find  ⏎open  s tail  r scan  q quit"
	}
	if len(ui.SessionKeys) > 0 {
		hints = " ↑↓ select  Enter tail  Esc back"
	}
	lines = append(lines, "\x1b[2m "+trailsSafe(footer)+"\x1b[0m", hints)
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = truncVisible(lines[i], w)
	}
	return strings.Join(lines, "\n")
}

var trailsChildRun = func(bin string, args []string, tty *os.File) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	return cmd.Run()
}
var trailsOpenURL = func(raw string) error {
	u, _, _, ok := parseTrailsURL(raw)
	if !ok {
		return fmt.Errorf("invalid trail URL")
	}
	return exec.Command("open", u).Run()
}

func trailsTailArgs(s wtfSession) ([]string, error) {
	args := []string{"--no-pick", "--no-hook-install", "--no-pane-link"}
	if s.Agent == AgentAmp {
		if !validAmpThreadID(s.ID) {
			return nil, fmt.Errorf("session unavailable: invalid Amp thread ID")
		}
		return append(args, "--agent", "amp", "--follow-session", s.ID), nil
	}
	if s.Agent != AgentClaude || !isFile(s.Transcript) {
		return nil, fmt.Errorf("session unavailable: transcript no longer exists")
	}
	return append(args, "--agent", "claude", s.Transcript), nil
}

func runTrails(cfg Config) error {
	home := firstNonEmpty(os.Getenv("HOME"), mustHome())
	if len(cfg.TrailsArgs) > 0 {
		return runTrailsCommand(cfg.TrailsArgs, home, os.Stdout)
	}
	c, err := loadTrails(home)
	if err != nil {
		return fmt.Errorf("catalog preserved: %w", err)
	}
	if !isCharDevice(os.Stdout) {
		release, ok, e := acquireTrailsLock(home)
		if e != nil {
			return e
		}
		if ok {
			defer release()
			d, closeDeps := newTrailsScanDeps(home)
			defer closeDeps()
			c, err = collectTrailsOnce(context.Background(), home, c, d, "foreground")
			if err != nil {
				return err
			}
		}
		fmt.Fprintln(os.Stdout, stripANSI(renderTrails(trailsUI{Catalog: c, Width: 120, Height: max(12, len(c.Trails)*3+8)}, time.Now())))
		return nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	saved, ok := setRawTimed(tty)
	if !ok {
		return fmt.Errorf("cannot set terminal mode")
	}
	defer restoreCbreak(tty, saved)
	io.WriteString(tty, "\x1b[?1049h\x1b[?25l")
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := make(chan error, 1)
	start := func() { go func() { worker <- runTrailsCollector(ctx, home, trailsScanDeps{}, "foreground") }() }
	start()
	_, _ = requestTrailsScan(home)
	ui := trailsUI{Catalog: c}
	var retry time.Time
	buf := make([]byte, 128)
	for {
		select {
		case e := <-worker:
			if e != nil && !errors.Is(e, errTrailsBusy) {
				ui.Error = e.Error()
			}
			retry = time.Now().Add(2 * time.Second)
		default:
		}
		if !retry.IsZero() && time.Now().After(retry) {
			retry = time.Time{}
			start()
		}
		if next, e := loadTrails(home); e == nil {
			ui.Catalog = next
		} else {
			ui.Error = e.Error()
		}
		h := readTrailsHealth(home)
		ui.Collector = trailsHealthLabel(h)
		if h.Mode != "daemon" {
			ui.Collector += " · background off"
		}
		if h.Error != "" {
			ui.Error = h.Error
		}
		ui.Width, ui.Height = termSize(tty)
		ui, _ = updateTrails(ui, kNone, 0, time.Now())
		io.WriteString(tty, "\x1b[H\x1b[2J"+strings.ReplaceAll(renderTrails(ui, time.Now()), "\n", "\r\n"))
		ui.Error = ""
		n, e := tty.Read(buf)
		if e != nil && !(n == 0 && errors.Is(e, io.EOF)) {
			return e
		}
		if n == 0 {
			continue
		}
		var chunks [][]byte
		if buf[0] == 0x1b {
			chunks = append(chunks, append([]byte(nil), buf[:n]...))
		} else {
			for _, r := range string(buf[:n]) {
				chunks = append(chunks, []byte(string(r)))
			}
		}
		for _, chunk := range chunks {
			key, r := decodeKey(chunk)
			var a trailsAction
			ui, a = updateTrails(ui, key, r, time.Now())
			if a.Quit {
				return nil
			}
			if a.Refresh {
				ui.Error = ""
				if _, e := requestTrailsScan(home); e != nil {
					ui.Error = e.Error()
				}
			}
			if a.OpenURL != "" {
				if e := trailsOpenURL(a.OpenURL); e != nil {
					ui.Error = e.Error()
				}
			}
			if a.SessionKey != "" {
				args, e := trailsTailArgs(ui.Catalog.Sessions[a.SessionKey])
				if e != nil {
					ui.Error = e.Error()
					continue
				}
				bin, e := os.Executable()
				if e != nil {
					return e
				}
				io.WriteString(tty, "\x1b[?25h\x1b[?1049l")
				restoreCbreak(tty, saved)
				e = trailsChildRun(bin, args, tty)
				if _, ok := setRawTimed(tty); !ok {
					return fmt.Errorf("cannot restore trails terminal")
				}
				io.WriteString(tty, "\x1b[?1049h\x1b[?25l")
				if e != nil {
					ui.Error = e.Error()
				}
			}
		}
	}
}
