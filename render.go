package main

import (
	"fmt"
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
	// lineOpen means an unterminated line is pending output — a body whose final
	// newline was deferred (so a dot streak can ride the end of the agent turn) or
	// an in-progress dot streak. endLine() emits the owed newline before the next
	// block. inDotStreak implies lineOpen.
	lineOpen bool
	live     bool // set true for the follow phase → ring the bell on assistant turns

	// seenQuestions dedups the question bell so a card that's re-rendered (reload,
	// or a poll that re-reads the same line) rings at most once per question id.
	seenQuestions map[string]bool
}

func newRenderer(w io.Writer, theme Theme, toolStyle string, collapse int) (*Renderer, error) {
	render, err := newGlamour(theme.StyleJSON)
	if err != nil {
		return nil, err
	}
	return newRendererWith(w, theme, toolStyle, collapse, render), nil
}

// newGlamour builds the markdown→styled-string render function for a theme's
// style JSON. Split out of newRenderer so a live theme swap (the `T` key) can
// rebuild just the render function without a whole new Renderer.
func newGlamour(styleJSON []byte) (func(string) (string, error), error) {
	md, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(styleJSON),
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
	return md.Render, nil
}

// applyTheme swaps the renderer to a new theme in place: it rebuilds the glamour
// render function from the new style JSON and recomputes the box-header strings.
// Called ONLY on the render goroutine (the `T`-key path in the live loop), so the
// non-atomic theme fields (render/theme/userHdr/claudeHdr) it mutates are never
// read concurrently — unlike the atomic t/c toggles, which the keyboard goroutine
// flips directly. A rebuild failure leaves the current theme untouched.
func (r *Renderer) applyTheme(t Theme) error {
	render, err := newGlamour(t.StyleJSON)
	if err != nil {
		return err
	}
	r.render = render
	r.theme = t
	r.userHdr = t.UserANSI + userHdrBody + reset
	r.claudeHdr = t.ClaudeANSI + claudeHdrBody + reset
	return nil
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
		seenQuestions:   map[string]bool{},
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

// reset clears the cross-event layout state so a fresh full re-render (the `r`
// reload) starts with a clean box header rather than a continuation marker.
func (r *Renderer) reset() {
	r.lastKind = ""
	r.inDotStreak = false
	r.lineOpen = false
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
		r.toolResult(rec.Result)
	case KindAgentSpawn:
		r.agentSpawn(rec.AgentDesc, rec.AgentType)
	case KindQuestion:
		r.question(rec)
	}
}

// agentSpawn renders a subagent launch as a distinct marker, shown in every tool
// style (it's orchestration, not a routine tool call). Purple to match the
// Task/Agent dot color.
func (r *Renderer) agentSpawn(desc, atype string) {
	r.endLine()
	agentColor := dotColor("Agent")
	line := agentColor + "⏺" + reset + " " + agentColor + "▸ agent:" + reset + " " + desc
	if atype != "" {
		line += " " + r.theme.DimANSI + "(" + atype + ")" + reset
	}
	io.WriteString(r.w, line+"\n")
}

// question renders a pending AskUserQuestion as a bold bordered card, and — live
// only, once per question id — rings the terminal bell so a waiting prompt is
// noticed even when the user isn't looking at the pane.
func (r *Renderer) question(rec Record) {
	r.endLine()
	if r.live && rec.QID != "" && !r.seenQuestions[rec.QID] {
		io.WriteString(r.w, "\a")
	}
	if rec.QID != "" {
		r.seenQuestions[rec.QID] = true
	}
	io.WriteString(r.w, questionCard(rec.Questions))
}

func (r *Renderer) header(kind Kind, ts string) {
	r.endLine() // flush any open body/dots line before the box header or marker
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
	var kept []string
	prevBlank := false
	for i := start; i <= end; i++ {
		if isBlank(lines[i]) {
			if prevBlank {
				continue
			}
			kept = append(kept, "")
			prevBlank = true
			continue
		}
		kept = append(kept, lines[i])
		prevBlank = false
	}
	if len(kept) == 0 {
		return
	}
	// Write the body joined by newlines but DEFER the final newline, leaving the
	// last line open so a following dot streak rides the end of the agent turn
	// (dots mode). endLine() emits the owed newline before the next header/block.
	io.WriteString(r.w, strings.Join(kept, "\n"))
	r.lineOpen = true
	r.inDotStreak = false
}

func (r *Renderer) toolUse(name, summary string) {
	switch toolStyleKind(r.toolStyle.Load()) {
	case toolNone:
		return
	case toolDots:
		// A tool streak renders as a bracketed group riding the end of the agent
		// turn: [.] growing to [.....]. Open the bracket on the first dot; endLine()
		// writes the closing ']' when the streak ends. Only ride an *assistant*
		// line (space-joined) — tools belong to the agent, so a streak after a user
		// turn (a text-less assistant turn emits no body) starts on its own line
		// instead of corrupting the user's text.
		if !r.inDotStreak {
			if r.lineOpen && r.lastKind == KindAssistant {
				io.WriteString(r.w, " ")
			} else {
				r.endLine() // flush a user (or empty) line; start the streak fresh
			}
			io.WriteString(r.w, r.theme.DimANSI+"["+reset)
			r.lineOpen = true
			r.inDotStreak = true
		}
		io.WriteString(r.w, dotColor(name)+"."+reset)
	default: // full → Claude-style "⏺ Label(arg)"
		r.endLine()
		label, arg := toolLabelArg(name, summary)
		io.WriteString(r.w, dotColor(name)+"⏺"+reset+" "+label+"("+truncateRunes(arg, 120)+")\n")
	}
}

// Question colors (fixed amber, prominent on dark themes).
const (
	qTitleANSI = "\x1b[1m\x1b[38;2;240;190;90m"
	qTitle     = "⁉ WAITING FOR YOUR ANSWER"
)

// questionCard renders one or more pending questions as a prominent — but
// un-boxed, un-truncated — block: an amber-bold title, each question's (amber-bold)
// head, then its numbered options in full. No borders and no width math, so
// nothing is ever clipped; long lines just soft-wrap in the terminal like any
// other body.
func questionCard(qs []QuestionItem) string {
	var b strings.Builder
	b.WriteString(qTitleANSI + qTitle + reset + "\n")
	for i, q := range qs {
		if i > 0 {
			b.WriteString("\n") // blank line between questions
		}
		head := q.Question
		if q.Header != "" {
			head = q.Header + ": " + q.Question
		}
		b.WriteString(qTitleANSI + head + reset + "\n")
		for j, o := range q.Options {
			fmt.Fprintf(&b, "  %d. %s\n", j+1, o)
		}
	}
	return b.String()
}

// Diff colors (fixed, readable on dark themes): green add, red remove.
const (
	diffAddANSI = "\x1b[38;2;126;211;134m"
	diffDelANSI = "\x1b[38;2;224;108;117m"
)

func (r *Renderer) toolResult(res *ToolResult) {
	// Only full mode shows results; in dots mode a result dot would just
	// double-count the tool_use it pairs with. No rich detail (codex/agy, or an
	// unrecognized result shape) → omit the ⎿ line entirely.
	if toolStyleKind(r.toolStyle.Load()) != toolLines || res == nil {
		return
	}
	dim := r.theme.DimANSI

	// The ⎿ headline is the summary, or (for output-only results) the first
	// output line. The whole result block is dim except diff +/- lines.
	head, rest := res.Summary, res.Output
	if head == "" && len(rest) > 0 {
		head, rest = rest[0], rest[1:]
	}
	if head == "" && len(res.Diff) == 0 {
		return // nothing to show
	}
	r.endLine()
	io.WriteString(r.w, dim+"  ⎿  "+head+reset+"\n")
	for _, d := range res.Diff {
		r.writeDiffLine(d)
	}
	for _, l := range rest {
		io.WriteString(r.w, dim+"     "+l+reset+"\n")
	}
}

// writeDiffLine renders one diff line: dim line number, then the content tinted
// green (added) / red (removed) / dim (context).
func (r *Renderer) writeDiffLine(d DiffLine) {
	dim := r.theme.DimANSI
	num := padNum(d.Num)
	switch d.Sign {
	case '+':
		io.WriteString(r.w, dim+"     "+num+reset+" "+diffAddANSI+"+ "+d.Text+reset+"\n")
	case '-':
		io.WriteString(r.w, dim+"     "+num+reset+" "+diffDelANSI+"- "+d.Text+reset+"\n")
	default:
		io.WriteString(r.w, dim+"     "+num+"   "+d.Text+reset+"\n")
	}
}

// padNum right-aligns a line number in a 4-wide column.
func padNum(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 4 {
		s = " " + s
	}
	return s
}

// toolLabelArg maps a tool name + input-summary to Claude's display form,
// e.g. ("Edit","/a/b/main.go") → ("Update","main.go").
func toolLabelArg(name, summary string) (label, arg string) {
	label = claudeToolLabel(name)
	switch name {
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "NotebookRead":
		arg = baseName(summary) // summary is the file path
	default:
		arg = summary
	}
	return label, arg
}

func claudeToolLabel(name string) string {
	switch name {
	case "Edit", "MultiEdit", "NotebookEdit":
		return "Update"
	case "Write":
		return "Write"
	case "Read", "NotebookRead":
		return "Read"
	case "Grep", "Glob":
		return "Search"
	case "LS":
		return "List"
	case "WebFetch":
		return "Fetch"
	case "WebSearch":
		return "Web Search"
	case "TodoWrite":
		return "Update Todos"
	}
	return name // Bash/Task and codex/agy/mcp tools keep their own name
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}

// endLine terminates any open line — a body whose final newline was deferred (so
// dots can ride it) or an in-progress dot streak — so the next header/marker/block
// starts at column 0. Closes the dot-streak bracket first. A no-op when nothing is
// pending.
func (r *Renderer) endLine() {
	if r.lineOpen {
		if r.inDotStreak {
			io.WriteString(r.w, r.theme.DimANSI+"]"+reset)
		}
		io.WriteString(r.w, "\n")
		r.lineOpen = false
		r.inDotStreak = false
	}
}

// abortLine closes an open dots bracket but DEFERS the trailing newline — used
// when handing the primary screen to the tree picker's alt-screen on Ctrl-X.
// endLine's newline would scroll a blank line into the primary buffer just before
// the swap (visible when the picker restores it); the owed newline is written
// after the picker returns instead, ahead of the next session's banner.
func (r *Renderer) abortLine() {
	if r.lineOpen && r.inDotStreak {
		io.WriteString(r.w, r.theme.DimANSI+"]"+reset)
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
