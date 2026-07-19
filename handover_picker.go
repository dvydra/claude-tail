package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// handover_picker.go is the grouping-picker: a flat list of today's sessions the
// user tags into handover-doc groups before the docs are written. Independent
// (its own doc) is the default; digits 1-9 merge sessions into one doc; '-'
// excludes. The reducer + render are pure (unit-tested without a tty); the driver
// owns the tty, mirroring tree.go's split.

type handoverTag int

const (
	tagExcluded    handoverTag = -1
	tagIndependent handoverTag = 0 // 1..9 = user group number
)

type handoverPickUI struct {
	Items      []handoverItem
	Tags       []handoverTag // parallel to Items
	Cursor     int
	Top        int
	Width      int
	Height     int
	Now        int64
	Done       bool // Enter → proceed with the current assignment
	Quit       bool // q/Esc → abort
	PreviewReq bool // i/p → show the highlighted session's info view
}

// handoverGroup is one output doc: the sessions the user grouped together.
type handoverGroup struct {
	GroupID  string
	Sessions []handoverItem
}

func allIndependent(n int) []handoverTag { return make([]handoverTag, n) }

// updateHandoverPick maps a key (plus a rune for kRune) to the next picker state.
// It never touches the terminal — the driver renders the returned state.
func updateHandoverPick(ui handoverPickUI, k treeKey, r rune) handoverPickUI {
	switch k {
	case kUp:
		ui.Cursor--
	case kDown:
		ui.Cursor++
	case kHome:
		ui.Cursor = 0
	case kEnd:
		ui.Cursor = len(ui.Items) - 1
	case kEnter:
		ui.Done = true
	case kEsc, kCtrlC:
		ui.Quit = true
	case kRune:
		switch {
		case r >= '1' && r <= '9':
			ui.setTag(handoverTag(r - '0'))
		case r == 'x' || r == 'X' || r == ' ':
			ui.setTag(tagIndependent)
		case r == '-':
			ui.setTag(tagExcluded)
		case r == 'i' || r == 'I' || r == 'p' || r == 'P':
			ui.PreviewReq = true
		case r == 'j':
			ui.Cursor++
		case r == 'k':
			ui.Cursor--
		case r == 'q' || r == 'Q':
			ui.Quit = true
		}
	}
	ui.clampPick()
	return ui
}

func (ui *handoverPickUI) setTag(tag handoverTag) {
	if ui.Cursor >= 0 && ui.Cursor < len(ui.Tags) {
		ui.Tags[ui.Cursor] = tag
	}
}

func (ui *handoverPickUI) clampPick() {
	if ui.Cursor >= len(ui.Items) {
		ui.Cursor = len(ui.Items) - 1
	}
	if ui.Cursor < 0 {
		ui.Cursor = 0
	}
	if ui.Height <= 0 {
		return
	}
	if ui.Cursor < ui.Top {
		ui.Top = ui.Cursor
	}
	if ui.Cursor >= ui.Top+ui.Height {
		ui.Top = ui.Cursor - ui.Height + 1
	}
	if ui.Top < 0 {
		ui.Top = 0
	}
}

// buildGroups collapses the picker assignment into output groups, in item order:
// each independent session is its own solo-N group; sessions sharing a digit merge
// into a g<digit> group (created on first touch); excluded sessions are dropped.
func buildGroups(items []handoverItem, tags []handoverTag) []handoverGroup {
	var groups []handoverGroup
	byDigit := map[handoverTag]int{} // digit → index into groups
	solo := 0
	for i, it := range items {
		tag := tagIndependent
		if i < len(tags) {
			tag = tags[i]
		}
		switch {
		case tag == tagExcluded:
			continue
		case tag == tagIndependent:
			solo++
			groups = append(groups, handoverGroup{GroupID: fmt.Sprintf("solo-%d", solo), Sessions: []handoverItem{it}})
		default:
			if gi, ok := byDigit[tag]; ok {
				groups[gi].Sessions = append(groups[gi].Sessions, it)
			} else {
				byDigit[tag] = len(groups)
				groups = append(groups, handoverGroup{GroupID: fmt.Sprintf("g%d", int(tag)), Sessions: []handoverItem{it}})
			}
		}
	}
	return groups
}

// ── rendering (pure) ─────────────────────────────────────────────────────────

func rowTag(tag handoverTag) string {
	switch {
	case tag == tagExcluded:
		return "[ ]"
	case tag == tagIndependent:
		return "[x]"
	default:
		return fmt.Sprintf("[%d]", int(tag))
	}
}

func handoverHeader() string {
	return "  HANDOVER — today   1-9 group · x separate · - skip · i preview · ⏎ write · q abort"
}

// composeHandoverRow is the per-session row: group tag, live/ended marker,
// short id, repo, age, tokens, branch, and the cleaned title (placeholder when
// there's no real prompt to show — press i to preview).
func composeHandoverRow(it handoverItem, tag handoverTag, now int64) string {
	mark := "○"
	if it.Live {
		mark = "●"
	}
	branch := ""
	if it.Branch != "" {
		branch = "[" + it.Branch + "] "
	}
	title := it.Title
	if title == "" {
		title = "(no prompt — i to preview)"
	}
	return fmt.Sprintf("%s %s %-8s %-24s %-7s %6s  %s%s",
		rowTag(tag), mark, shortID(it.SessionID), it.Repo, relAge(it.LastActivity, now), formatTokens(it.Tokens), branch, title)
}

func renderHandoverPick(ui handoverPickUI) string {
	var b strings.Builder
	b.WriteString("\x1b[H")
	b.WriteString("\x1b[1m" + truncateRunes(handoverHeader(), ui.Width) + reset + "\x1b[K\n")
	end := min(ui.Top+ui.Height, len(ui.Items))
	shown := 0
	for i := ui.Top; i < end; i++ {
		it := ui.Items[i]
		tag := tagIndependent
		if i < len(ui.Tags) {
			tag = ui.Tags[i]
		}
		tier := classifyTier(it.LastActivity, ui.Now, it.Live)
		b.WriteString(styleRow(composeHandoverRow(it, tag, ui.Now), tier, i == ui.Cursor, ui.Width) + "\x1b[K\n")
		shown++
	}
	for ; shown < ui.Height; shown++ {
		b.WriteString("\x1b[K\n")
	}
	b.WriteString("\x1b[J")
	return b.String()
}

// ── driver (imperative tty; not unit-tested) ─────────────────────────────────

// runHandoverPicker runs the interactive grouping picker over items and returns
// the collapsed groups, or ok=false if the user aborted / no tty was available.
// Live sessions start selected (their own doc); ended ones start excluded.
func runHandoverPicker(items []handoverItem, home string, theme Theme, now int64) ([]handoverGroup, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, false
	}
	defer tty.Close()
	saved, ok := setRaw(tty)
	if !ok {
		return nil, false
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return nil, false
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	ui := handoverPickUI{Items: items, Tags: defaultTags(items), Now: now}
	buf := make([]byte, 16)
	for {
		w, h := termSize(tty)
		ui.Width, ui.Height = w, h-1
		if ui.Height < 1 {
			ui.Height = 1
		}
		ui.clampPick()
		io.WriteString(tty, renderHandoverPick(ui))
		n, err := tty.Read(buf)
		if err != nil || n == 0 {
			return nil, false
		}
		k, r := decodeKey(buf[:n])
		ui = updateHandoverPick(ui, k, r)
		if ui.Quit {
			return nil, false
		}
		if ui.PreviewReq {
			ui.PreviewReq = false
			if ui.Cursor >= 0 && ui.Cursor < len(ui.Items) {
				it := ui.Items[ui.Cursor]
				showInfo(tty, treeSession{
					Path: it.Path, ID: it.SessionID, Snippet: it.Title,
					Repo: it.Repo, Branch: it.Branch, Mtime: it.LastActivity,
					Tokens: it.Tokens, Live: it.Live,
				}, home, theme)
			}
			continue
		}
		if ui.Done {
			return buildGroups(ui.Items, ui.Tags), true
		}
	}
}
