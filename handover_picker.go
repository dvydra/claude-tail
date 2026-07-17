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
	Items  []handoverItem
	Tags   []handoverTag // parallel to Items
	Cursor int
	Top    int
	Width  int
	Height int
	Done   bool // Enter → proceed with the current assignment
	Quit   bool // q/Esc → abort
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
	return "  HANDOVER — today's sessions   1-9 group · x separate · - skip · ⏎ write · q abort"
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
		text := fmt.Sprintf("%s %-8s %-24s %6s  %s",
			rowTag(tag), shortID(it.SessionID), it.Repo, formatTokens(it.Tokens), it.Title)
		prefix := "  "
		if i == ui.Cursor {
			prefix = "❯ "
		}
		line := truncateRunes(prefix+text, ui.Width)
		if i == ui.Cursor {
			b.WriteString("\x1b[7m" + line + reset + "\x1b[K\n")
		} else {
			b.WriteString(line + "\x1b[K\n")
		}
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
func runHandoverPicker(items []handoverItem) ([]handoverGroup, bool) {
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

	ui := handoverPickUI{Items: items, Tags: allIndependent(len(items))}
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
		if ui.Done {
			return buildGroups(ui.Items, ui.Tags), true
		}
	}
}
