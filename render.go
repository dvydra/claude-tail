package main

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/glamour"
	"github.com/muesli/termenv"
)

// Box-header content (between the color prefix and the reset). Copied verbatim
// from the bash version — the dash counts are load-bearing for output parity.
const (
	userHdrBody   = "─── ▶ USER ───────────────────────────────"
	claudeHdrBody = "─── ◀ AGENT ──────────────────────────────"
	reset         = "\x1b[0m"
)

// toolStyleKind is how tool-use events render. Stored as an atomic int32 on the
// Renderer so the keyboard goroutine can flip it live without racing the render
// goroutine.
type toolStyleKind int32

const (
	toolNone  toolStyleKind = iota // "hidden": drop tool events
	toolDots                       // "dots":   one colored dot per call
	toolLines                      // "full":   verbose "⚙ name  input" line
)

// toolCycle is the order the `t` key steps through: full → dots → hidden → …
var toolCycle = []toolStyleKind{toolLines, toolDots, toolNone}

// parseToolStyle maps a --tool-style value to a kind. full/dots/hidden are the
// canonical names; none (=hidden) and lines (=full) are accepted as aliases.
func parseToolStyle(s string) toolStyleKind {
	switch s {
	case "none", "hidden":
		return toolNone
	case "lines", "full":
		return toolLines
	default:
		return toolDots
	}
}

// label is the user-facing name (full / dots / hidden).
func (k toolStyleKind) label() string {
	switch k {
	case toolNone:
		return "hidden"
	case toolLines:
		return "full"
	default:
		return "dots"
	}
}

// Renderer turns a stream of Records into the styled transcript, maintaining
// the cross-event state the layout depends on: which participant spoke last
// (so consecutive same-participant turns collapse to a dim "⋯ ts" marker) and
// whether we're mid dot-streak (so the next header/body/line breaks out of it).
//
// In-process glamour rendering lets backfill and live share this single path —
// the bash version needed two (a batched glow+awk pipeline for backfill, a
// per-event loop for live) only because spawning glow per event was slow.
type Renderer struct {
	w      io.Writer
	render func(string) (string, error) // markdown → styled string (glamour)
	theme  Theme

	// Live-mutable display settings (atomic: the keyboard goroutine flips them
	// while the render goroutine reads them in emit).
	toolStyle atomic.Int32 // current toolStyleKind
	collapse  atomic.Int32 // current paste-collapse threshold (0 = off)
	// Immutable threshold to restore when re-enabling collapse.
	collapseDefault int32

	userHdr   string // full USER box header line (color + body + reset)
	claudeHdr string // full AGENT box header line

	lastKind    Kind
	inDotStreak bool
	live        bool // set true for the follow phase → ring the bell on assistant turns
}

func newRenderer(w io.Writer, theme Theme, toolStyle string, collapse int) (*Renderer, error) {
	md, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(theme.StyleJSON),
		glamour.WithWordWrap(0),
		// Force truecolor in-process. The bash version piped glow and relied on
		// CLICOLOR_FORCE, which capped rendering at 256 colors; rendering in
		// our own process lets us emit the theme's exact hex colors.
		glamour.WithColorProfile(termenv.TrueColor),
		// glamour defaults code-block syntax highlighting to chroma's
		// "terminal256"; use the 24-bit formatter so code blocks are truecolor
		// too (otherwise the markdown is truecolor but the code isn't).
		glamour.WithChromaFormatter("terminal16m"),
	)
	if err != nil {
		return nil, err
	}
	return newRendererWith(w, theme, toolStyle, collapse, md.Render), nil
}

// newRendererWith builds a Renderer around an arbitrary markdown render
// function, so the layout state machine can be tested without glamour's
// color-dependent output.
func newRendererWith(w io.Writer, theme Theme, toolStyle string, collapse int, render func(string) (string, error)) *Renderer {
	collapseDefault := int32(collapse)
	if collapseDefault == 0 { // started off → "enable" uses the standard threshold
		collapseDefault = 5
	}
	r := &Renderer{
		w:               w,
		render:          render,
		theme:           theme,
		collapseDefault: collapseDefault,
		userHdr:         theme.UserANSI + userHdrBody + reset,
		claudeHdr:       theme.ClaudeANSI + claudeHdrBody + reset,
	}
	r.toolStyle.Store(int32(parseToolStyle(toolStyle)))
	r.collapse.Store(int32(collapse))
	return r
}

// cycleTools advances tool-call rendering to the next state (full → dots →
// hidden → full), for future events only. Returns a short status for the user.
func (r *Renderer) cycleTools() string {
	cur := toolStyleKind(r.toolStyle.Load())
	i := 0
	for j, k := range toolCycle {
		if k == cur {
			i = j
			break
		}
	}
	next := toolCycle[(i+1)%len(toolCycle)]
	r.toolStyle.Store(int32(next))
	return "tool calls " + next.label()
}

// toggleCollapse flips long-user-paste collapsing on/off (future events only).
func (r *Renderer) toggleCollapse() string {
	if r.collapse.Load() > 0 {
		r.collapse.Store(0)
		return "collapse off (full user pastes)"
	}
	r.collapse.Store(r.collapseDefault)
	return "collapse on (user pastes > " + strconv.Itoa(int(r.collapseDefault)) + " lines)"
}

// emit renders one record, advancing the layout state.
func (r *Renderer) emit(rec Record) {
	switch rec.Kind {
	case KindUser:
		r.header(KindUser, rec.Ts)
		r.body(collapseBody(rec.Body, int(r.collapse.Load())))
	case KindAssistant:
		if r.live {
			// BEL on each live assistant turn — lets the user wander off and
			// get pinged when the agent responds. Backfill replays bypass this.
			io.WriteString(r.w, "\a")
		}
		r.header(KindAssistant, rec.Ts)
		r.body(rec.Body)
	case KindToolUse:
		r.toolUse(rec.Name, rec.Summary)
	case KindToolResult:
		r.toolResult(rec.N)
	}
}

func (r *Renderer) header(kind Kind, ts string) {
	if r.inDotStreak {
		io.WriteString(r.w, "\n")
	}
	if kind == r.lastKind {
		// Same participant as the previous turn → dim continuation marker.
		io.WriteString(r.w, r.theme.DimANSI+"  ⋯ "+ts+reset+"\n")
	} else {
		hdr := r.claudeHdr
		if kind == KindUser {
			hdr = r.userHdr
		}
		io.WriteString(r.w, hdr+" "+r.theme.DimANSI+ts+reset+"\n")
	}
	r.inDotStreak = false
	r.lastKind = kind
}

// body renders markdown through glamour and prints it flush — leading and
// trailing blank lines stripped — so the header above and next event below hug
// it directly.
func (r *Renderer) body(mdText string) {
	out, err := r.render(mdText)
	if err != nil {
		out = mdText
	}
	out = stripTerminalNoise(out)

	lines := strings.Split(out, "\n")
	start, end := 0, len(lines)-1
	for start <= end && isBlank(lines[start]) {
		start++
	}
	for end >= start && isBlank(lines[end]) {
		end--
	}
	// Squeeze runs of blank lines to a single blank, discarding whitespace on
	// blank lines — matching the bash backfill awk's held_blank behavior, and
	// keeping backfill and live consistent (the bash live path did not squeeze).
	prevBlank := false
	for i := start; i <= end; i++ {
		if isBlank(lines[i]) {
			if prevBlank {
				continue
			}
			io.WriteString(r.w, "\n")
			prevBlank = true
			continue
		}
		io.WriteString(r.w, lines[i])
		io.WriteString(r.w, "\n")
		prevBlank = false
	}
}

func (r *Renderer) toolUse(name, summary string) {
	switch toolStyleKind(r.toolStyle.Load()) {
	case toolNone:
		return
	case toolDots:
		io.WriteString(r.w, dotColor(name)+"."+reset)
		r.inDotStreak = true
	default: // toolLines
		r.breakDotStreak()
		io.WriteString(r.w, r.theme.DimANSI+"  ⚙ "+name+"  "+truncateRunes(summary, 140)+reset+"\n")
	}
}

func (r *Renderer) toolResult(n int) {
	// Only the verbose "lines" style shows results; in dots mode a result dot
	// would just double-count the tool_use it pairs with.
	if toolStyleKind(r.toolStyle.Load()) != toolLines {
		return
	}
	r.breakDotStreak()
	io.WriteString(r.w, r.theme.DimANSI+"  ↩ tool_result (×"+strconv.Itoa(n)+")"+reset+"\n")
}

func (r *Renderer) breakDotStreak() {
	if r.inDotStreak {
		io.WriteString(r.w, "\n")
		r.inDotStreak = false
	}
}

func isBlank(s string) bool {
	return strings.TrimSpace(s) == ""
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

var (
	oscNoiseRe = regexp.MustCompile("\x1b\\]1[01];[^\x1b\x07]*(?:\x1b\\\\|\x07)")
	csiNoiseRe = regexp.MustCompile(`\x1b\[[0-9]+;[0-9]+R`)
)

// stripTerminalNoise removes OSC 10/11 color queries/responses and CSI
// cursor-position reports that can leak into a rendered stream (tmux/zellij
// heartbeats, shell prompt probes) and otherwise show up as literal text.
func stripTerminalNoise(s string) string {
	s = oscNoiseRe.ReplaceAllString(s, "")
	s = csiNoiseRe.ReplaceAllString(s, "")
	return s
}

// dotColor maps a tool name to its truecolor SGR prefix. Kept in sync with the
// startup legend. Knows Claude, Codex, and agy tool names.
func dotColor(name string) string {
	switch name {
	case "Read", "NotebookRead", "view_file", "view_code_item":
		return "\x1b[38;2;125;185;235m"
	case "LS", "Glob", "list_dir", "list_permissions":
		return "\x1b[38;2;160;200;240m"
	case "Grep", "zoekt", "grep_search":
		return "\x1b[38;2;220;140;230m"
	case "Write", "Edit", "MultiEdit", "NotebookEdit",
		"apply_patch", "write_to_file", "replace_file_content", "edit_file":
		return "\x1b[38;2;125;215;145m"
	case "Bash", "exec_command", "shell", "local_shell_call", "run_command":
		return "\x1b[38;2;240;200;110m"
	case "WebFetch", "WebSearch", "search_web", "read_url_content", "view_image":
		return "\x1b[38;2;110;220;220m"
	case "Task", "Agent":
		return "\x1b[38;2;205;155;255m"
	case "TodoWrite", "update_plan":
		return "\x1b[38;2;150;160;180m"
	}
	if strings.HasPrefix(name, "mcp__") {
		return "\x1b[38;2;255;180;130m"
	}
	return "\x1b[38;2;200;200;200m"
}
