package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

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

	// wrap is the column limit glamour breaks bodies at (0 = don't wrap, let the
	// terminal soft-wrap). Held on the Renderer because both rebuild paths need
	// it: applyTheme (`T`) rebuilds glamour from a new style at the SAME width,
	// and setWrap (SIGWINCH) rebuilds it from the SAME style at a new width.
	wrap int

	// Live-mutable display settings (atomic: the keyboard goroutine flips them
	// while the render goroutine reads them in emit).
	toolStyle atomic.Int32 // current toolStyleKind
	collapse  atomic.Int32 // current paste-collapse threshold (0 = off)
	mrkdwn    atomic.Bool  // `m`: render agent bodies as Slack mrkdwn, not glamour
	// Immutable threshold to restore when re-enabling collapse.
	collapseDefault int32

	// The `y` yank buffer: the raw markdown of recent agent messages, so a copy
	// is made from the source text rather than from glamour's wrapped, colored
	// rendering of it. Touched only on the render goroutine — the keyboard
	// signals yankCh and never reads this.
	yankMsgs   []yankMsg
	yankN      int       // messages the last `y` copied (a repeat extends it)
	lastYankAt time.Time // when that press was, for the extend window

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

	// pendingShown holds content keys of questions already rendered from a live
	// marker (the pre-answer alert). The eventual JSONL card for the same
	// question is suppressed so the user sees exactly one card. Cleared by
	// reset() so a full re-render (r / rollover) shows JSONL cards normally.
	pendingShown map[string]bool

	// pendingAt records turnsRendered at the moment each marker card was drawn,
	// and turnsRendered counts the user/assistant bodies actually printed. Their
	// difference answers the one question the suppression needs: did a turn body
	// land BETWEEN the alert card and the transcript record for the same
	// question? If it did, the card is redrawn after it (see question()).
	pendingAt     map[string]int
	turnsRendered int

	// earlyShown holds keys of assistant text already rendered from the API tap
	// (render.go earlyTextKey), so the transcript record that lands later — after
	// the user answers the question the text preceded — is suppressed instead of
	// printed a second time. Keyed by provider message id + exact text, both of
	// which appear identically in the wire stream and the JSONL, so the match is
	// exact rather than a content-similarity guess. Cleared by reset().
	earlyShown map[string]bool

	// lastDoneMsgID is the message id whose done banner we last printed. One
	// assistant message occasionally spans two jsonl text records (both carrying
	// the same terminal stop_reason); this keeps the banner to one per message.
	lastDoneMsgID string
}

func newRenderer(w io.Writer, theme Theme, toolStyle string, collapse, wrap int) (*Renderer, error) {
	render, err := newGlamour(theme.StyleJSON, wrap)
	if err != nil {
		return nil, err
	}
	r := newRendererWith(w, theme, toolStyle, collapse, render)
	r.wrap = wrap
	return r, nil
}

// newGlamour builds the markdown→styled-string render function for a theme's
// style JSON, wrapping bodies at `wrap` columns (0 = no wrap). Split out of
// newRenderer so a live theme swap (the `T` key) and a resize (setWrap) can
// rebuild just the render function without a whole new Renderer.
func newGlamour(styleJSON []byte, wrap int) (func(string) (string, error), error) {
	md, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(styleJSON),
		glamour.WithWordWrap(wrap),
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
	if wrap == 0 {
		return md.Render, nil // no padding to undo; the golden path, byte for byte
	}
	return func(s string) (string, error) {
		out, err := md.Render(s)
		if err != nil {
			return out, err
		}
		return trimWrapPad(out), nil
	}, nil
}

// trailPadRe matches the trailing run of padding at the end of a line: spaces
// and the escape sequences interleaved with them. Glamour doesn't append plain
// spaces — it emits each pad column as its own styled cell
// ("\x1b[38;5;252m \x1b[0m" over and over), so a plain TrimRight sees an escape,
// not a space, and takes nothing off.
var trailPadRe = regexp.MustCompile(`(?:\x1b\[[0-9;]*[A-Za-z]|[ \t])+$`)

// escRe matches a single ANSI escape sequence.
var escRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// resetRe matches an SGR that resets all attributes.
var resetRe = regexp.MustCompile(`^\x1b\[0?m$`)

// sgrRe matches one SGR (colour/attribute) sequence and captures its parameters.
var sgrRe = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// setsBackground reports whether any SGR in the line sets a background colour:
// 40–47 (basic), 100–107 (bright), or 48 followed by a 5;n / 2;r;g;b payload.
//
// The parameters are walked rather than pattern-matched because 38 and 48 both
// swallow the parameters that follow them: a foreground RGB of `38;2;48;10;20`
// contains a literal "48" that a regex would read as a background.
func setsBackground(line string) bool {
	for _, m := range sgrRe.FindAllStringSubmatch(line, -1) {
		params := strings.Split(m[1], ";")
		for i := 0; i < len(params); i++ {
			n, err := strconv.Atoi(params[i])
			if err != nil {
				continue
			}
			switch {
			case n == 48:
				return true
			case n == 38:
				// Extended foreground: skip its payload so an RGB component
				// that happens to equal 41 isn't read as a background.
				if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				} else if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				}
			case n >= 40 && n <= 47, n >= 100 && n <= 107:
				return true
			}
		}
	}
	return false
}

// trimWrapPad removes the padding glamour adds once a wrap width is set: it
// right-pads EVERY line out to that width. On prose that padding is pure cost —
// it spends the last column, it lands in the clipboard on a mouse drag-select,
// and it pushes the dot streak that rides the end of an agent turn past the
// terminal edge, so a two-dot streak soft-wraps onto a row of its own (see "Dots
// ride the agent turn").
//
// A line that sets a BACKGROUND colour is left exactly as glamour produced it,
// because there the padding is what makes a block a rectangle rather than a
// ragged edge. That guard is currently inert: `WithChromaFormatter("terminal16m")`
// emits foreground colours only, so no bundled theme produces a background-styled
// body line and code blocks get trimmed like prose. It's kept because the day a
// theme or formatter does emit one, trimming it would visibly shred the block.
//
// Trailing whitespace inside a code block goes with the padding — the two are
// indistinguishable by the time glamour is done — which is the right trade: it's
// almost always incidental, and nobody wants it in a pasted command.
//
// Only ever called when wrap > 0. At wrap 0 glamour pads nothing, and the
// unwrapped path stays byte-identical to what the goldens pin.
func trimWrapPad(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if setsBackground(line) {
			continue
		}
		pad := trailPadRe.FindString(line)
		if !strings.ContainsAny(pad, " \t") {
			continue // a bare closing escape, nothing padded — leave it alone
		}
		// Drop the pad's spaces but keep its escapes, so the line's SGR state
		// still closes: trimming them off would leave a colour open to EOL.
		escapes := escRe.FindAllString(pad, -1)
		tail := strings.Join(escapes, "")
		// Those escapes are colour-set/reset pairs around each pad column, so
		// when the run ends in a reset the whole pile collapses to that reset.
		if n := len(escapes); n > 0 && resetRe.MatchString(escapes[n-1]) {
			tail = escapes[n-1]
		}
		lines[i] = line[:len(line)-len(pad)] + tail
	}
	return strings.Join(lines, "\n")
}

// applyTheme swaps the renderer to a new theme in place: it rebuilds the glamour
// render function from the new style JSON and recomputes the box-header strings.
// Called ONLY on the render goroutine (the `T`-key path in the live loop), so the
// non-atomic theme fields (render/theme/userHdr/claudeHdr) it mutates are never
// read concurrently — unlike the atomic t/c toggles, which the keyboard goroutine
// flips directly. A rebuild failure leaves the current theme untouched.
func (r *Renderer) applyTheme(t Theme) error {
	render, err := newGlamour(t.StyleJSON, r.wrap)
	if err != nil {
		return err
	}
	r.render = render
	r.theme = t
	r.userHdr = t.UserANSI + userHdrBody + reset
	r.claudeHdr = t.ClaudeANSI + claudeHdrBody + reset
	return nil
}

// minWrapWidth is the narrowest terminal we'll wrap in. Below it, glamour's
// indents (lists, block quotes, code blocks) eat so much of the line that
// wrapping produces worse output than leaving the terminal to soft-wrap.
const minWrapWidth = 20

// wrapWidthFor turns a measured terminal width into the column limit handed to
// glamour. One column short of the terminal on purpose: a line that fills the
// final column makes the terminal wrap the cursor by itself, which shows up as a
// phantom blank line after the paragraph.
func wrapWidthFor(cols int, tty bool) int {
	if !tty || cols < minWrapWidth {
		return 0
	}
	return cols - 1
}

// setWrap rebuilds glamour at a new column limit — the SIGWINCH path. It only
// changes the width bodies rendered from NOW on are wrapped at; lines already in
// scrollback can't be rewrapped in place, so the caller re-renders (see the
// winch handling in main.go). Reports whether the width actually changed, so a
// SIGWINCH that only altered the height (or a themeless test renderer) doesn't
// trigger a pointless re-render. A rebuild failure leaves the current width
// untouched: a resize must never be able to break rendering.
func (r *Renderer) setWrap(wrap int) bool {
	if wrap == r.wrap {
		return false
	}
	render, err := newGlamour(r.theme.StyleJSON, wrap)
	if err != nil {
		return false
	}
	r.render = render
	r.wrap = wrap
	return true
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
		pendingShown:    map[string]bool{},
		pendingAt:       map[string]int{},
		earlyShown:      map[string]bool{},
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
	r.lastDoneMsgID = ""
	clear(r.pendingShown)
	clear(r.pendingAt)
	clear(r.earlyShown)
	r.turnsRendered = 0
	// A reset always precedes a full re-emit of the transcript (reload, theme
	// swap, rollover), which would otherwise append every message to the yank
	// buffer a second time.
	r.yankMsgs = nil
}

// toggleMrkdwn flips agent bodies between glamour and Slack mrkdwn source
// (future events only). Returns a short status for the user.
func (r *Renderer) toggleMrkdwn() string {
	on := !r.mrkdwn.Load()
	r.mrkdwn.Store(on)
	if on {
		return "agent text as slack mrkdwn (select with the mouse to copy it)"
	}
	return "agent text rendered normally"
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
		r.turnsRendered++
		r.header(KindUser, rec.Ts)
		r.body(collapseBody(rec.Body, int(r.collapse.Load())))
	case KindAssistant:
		if r.consumeEarlyText(rec) {
			return // already shown from the tap, before the question it preceded
		}
		r.turnsRendered++
		if r.live {
			// BEL on each live assistant turn — lets the user wander off and
			// get pinged when the agent responds. Backfill replays bypass this.
			io.WriteString(r.w, "\a")
		}
		r.recordYank(rec.Body, rec.MsgID)
		r.header(KindAssistant, rec.Ts)
		r.doneBanner(rec)
		r.body(rec.Body)
	case KindToolUse:
		r.toolUse(rec.Name, rec.Summary)
	case KindToolResult:
		r.toolResult(rec.Result)
	case KindAgentSpawn:
		r.agentSpawn(rec.AgentDesc, rec.AgentType)
	case KindQuestion:
		r.question(rec)
	case KindTaskNote:
		r.taskNote(rec.Body)
	}
}

// taskNote renders a background-task notification as one dim line. Shown in
// every tool style: it isn't a tool call, it's the reason the agent woke up —
// and one dim line is cheap enough that hiding it would only cost context. The
// glyph matches the "waiting on something else" sense of a pending task.
func (r *Renderer) taskNote(body string) {
	r.endLine()
	io.WriteString(r.w, r.theme.DimANSI+"  ⧗ "+body+reset+"\n")
}

// Done-banner colors (fixed bright green, prominent on light and dark themes).
const (
	doneANSI = "\x1b[1m\x1b[38;2;60;235;120m"
	doneMark = "✔ DONE — over to you"
)

// doneBanner prints the bright-green "the agent finished its turn" line at the
// TOP of the closing message, so a turn that hands control back is obvious at a
// glance without reading the text. Only Claude reports this explicitly
// (message.stop_reason); other agents never set Done, so nothing is printed.
// Deduped by message id — one message can span two text records.
func (r *Renderer) doneBanner(rec Record) {
	if !rec.Done {
		return
	}
	if rec.MsgID != "" && rec.MsgID == r.lastDoneMsgID {
		return
	}
	r.lastDoneMsgID = rec.MsgID
	io.WriteString(r.w, doneANSI+doneMark+reset+"\n")
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

// questionsContentKey is a stable hash of a question set's rendered content, so
// a pre-answer marker card and the eventual JSONL card dedup against each other
// regardless of tool_use ids.
func questionsContentKey(qs []QuestionItem) string {
	h := sha256.New()
	for _, q := range qs {
		io.WriteString(h, q.Header+"\x00"+q.Question+"\x00")
		for _, o := range q.Options {
			io.WriteString(h, o+"\x1f")
		}
		io.WriteString(h, "\x1e")
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// earlyTextKey identifies one assistant text block by the ids both the wire and
// the transcript carry: the provider message id plus the block's exact text.
// Byte-identical on both sides (the tap accumulates the same deltas the
// transcript record is built from), so suppression can be exact — no hashing of
// near-equal content, and two identical short texts in different messages never
// collide.
func earlyTextKey(msgID, body string) string { return "text:" + msgID + "\x00" + body }

// tapPreamble renders a blocked question straight from the API tap: the text
// blocks that led up to it, then the question card.
//
// This is the one place the tap renders anything. Claude Code withholds the
// WHOLE message containing an AskUserQuestion — preamble text included — until
// the user answers, so without this the card appears (from the hook marker) with
// no sign of the reasoning that produced it, and the text only lands afterwards,
// below the card, reading backwards. Rendering from the wire puts it in the
// right order at the right time; each block is remembered in earlyShown so the
// transcript twin is suppressed when it eventually arrives.
func (r *Renderer) tapPreamble(p tapPendingPrompt, ts string) {
	for _, body := range p.Preamble {
		r.header(KindAssistant, ts)
		r.body(body)
		if p.MsgID != "" {
			r.earlyShown[earlyTextKey(p.MsgID, body)] = true
		}
	}
	r.pendingQuestion(p.Questions)
	if p.QID != "" {
		// The card is on screen and the bell has rung; record the id so the JSONL
		// record doesn't ring again for an already-seen prompt.
		r.seenQuestions[p.QID] = true
	}
}

// consumeEarlyText reports whether this record was already rendered from the tap,
// consuming the key one-shot so a later full re-render (r / T, which clears
// earlyShown anyway) still shows it normally.
func (r *Renderer) consumeEarlyText(rec Record) bool {
	if rec.MsgID == "" || len(r.earlyShown) == 0 {
		return false
	}
	key := earlyTextKey(rec.MsgID, rec.Body)
	if !r.earlyShown[key] {
		return false
	}
	delete(r.earlyShown, key)
	return true
}

// pendingQuestion renders a question card from a live marker (before the JSONL
// flush), always ringing the bell, and records its content key so the eventual
// JSONL card is suppressed. Runs on the render goroutine like every other emit.
func (r *Renderer) pendingQuestion(qs []QuestionItem) {
	key := questionsContentKey(qs)
	// There are now TWO early paths to the same card — the hook marker and the API
	// tap — and a session with both live hits both: the tap sees the question at
	// message_stop on the wire, then the hook fires when Claude Code dispatches the
	// tool a moment later. Whichever arrives first owns the card; the second is a
	// no-op. (Observed live as the card printed twice.)
	if r.pendingShown[key] {
		return
	}
	r.endLine()
	io.WriteString(r.w, "\a")
	io.WriteString(r.w, questionCard(qs))
	r.pendingShown[key] = true
	r.pendingAt[key] = r.turnsRendered
}

// pendingPermission renders a one-line "waiting on a permission prompt" notice
// from a live marker, with the bell. There's no JSONL counterpart to dedup — a
// granted permission just becomes a normal tool render later.
func (r *Renderer) pendingPermission(summary string) {
	r.endLine()
	io.WriteString(r.w, "\a")
	io.WriteString(r.w, r.theme.DimANSI+"⏳ waiting: "+summary+reset+"\n")
}

// question renders a pending AskUserQuestion as a bold bordered card, and — live
// only, once per question id — rings the terminal bell so a waiting prompt is
// noticed even when the user isn't looking at the pane. If a live marker
// already rendered this exact question (pendingQuestion), the JSONL card is
// suppressed — the user already saw it — and the key is consumed one-shot so a
// later full re-render (reload) shows the card normally again.
//
// The exception is when a turn body landed in between. Claude Code withholds the
// whole message an AskUserQuestion belongs to — its preamble text included —
// until the user answers, so on a session with no API tap the preamble arrives
// AFTER the alert card and the pane reads backwards: question first, then the
// reasoning that led to it. When that happens the card is redrawn below its
// preamble, so the transcript ends up in wire order (text → card → answer). The
// bell never rings again; only the card is repeated.
func (r *Renderer) question(rec Record) {
	key := questionsContentKey(rec.Questions)
	if r.pendingShown[key] {
		at, tracked := r.pendingAt[key]
		delete(r.pendingShown, key)
		delete(r.pendingAt, key)
		// A live marker already showed this card AND rang the bell. Record the
		// QID so a later full re-render (r / T, which clears pendingShown but not
		// seenQuestions and replays with live=true) redraws the card without
		// re-ringing the bell for an already-answered prompt.
		if rec.QID != "" {
			r.seenQuestions[rec.QID] = true
		}
		if !tracked || at == r.turnsRendered {
			return // nothing came between the alert and this record — one card is right
		}
		// A deferred preamble (or another turn) printed in between: fall through
		// and redraw the card so it follows the text it belongs to.
	}
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
	var out string
	// `m` mode: agent bodies print as Slack mrkdwn source instead of glamour, so
	// a mouse drag-select copies mrkdwn straight out of the terminal. User turns
	// keep rendering normally — it's the agent's text that gets pasted onward.
	if r.mrkdwn.Load() && r.lastKind == KindAssistant {
		out = toSlackMrkdwn(mdText)
	} else {
		rendered, err := r.render(mdText)
		if err != nil {
			rendered = mdText
		}
		out = stripTerminalNoise(rendered)
	}

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
