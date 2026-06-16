# entire-tail

Pretty live-view of your current AI coding agent session — Claude Code, Codex
CLI, or Antigravity. Ships as an [`entire`](https://docs.entire.io) plugin,
also runs standalone.

## Why

CLI coding agents render their TUIs directly into the main terminal buffer
with cursor moves (they don't use the alt-screen). The terminal scrollback
captures every partial repaint — so scrolling up to re-read a long
conversation gives you overlapping fragments of UI chrome, not a clean
history. Most agents also have no in-app scroll keybind, no `/history`, no
`/transcript`. Once a message has left the visible viewport, there's no good
way to read it back without exiting.

But the source of truth is on disk: every event in your session is appended
line-by-line to a JSONL file under the agent's data directory.

`entire-tail` discovers that file for the agent you're using, follows it,
formats each event nicely, and renders the markdown bodies **in-process**
with [glamour](https://github.com/charmbracelet/glamour) (the same renderer
[glow](https://github.com/charmbracelet/glow) is built on) using a custom
flush-left style. It's a single self-contained Go binary — no `jq`, `glow`,
`awk`, or other runtime dependencies.

Open it in a separate Zellij pane next to the agent. New messages stream in.
Scroll back through the pane buffer to read clean, full-width markdown of
the session.

## Supported agents

| Agent          | Discovery                                                        |
|----------------|------------------------------------------------------------------|
| Claude Code    | `~/.claude/projects/<encoded-cwd>/*.jsonl`                       |
| Codex CLI      | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` (cwd from `session_meta`) |
| Antigravity    | `~/.gemini/antigravity-cli/brain/<id>/.system_generated/logs/transcript.jsonl` (id looked up from `cache/last_conversations.json`) |

`--agent auto` (the default) picks whichever has the most recently modified
session for `$PWD`. Force a specific agent with `--agent claude|codex|agy`.

## Install

```sh
./install.sh
```

The script does three things in one shot:

1. Builds the Go binary in place (requires the [Go toolchain](https://go.dev/dl/)).
2. Symlinks `entire-tail` into `~/.local/bin/` so the standalone command
   works.
3. Registers it via `entire plugin install` if the [`entire`](https://docs.entire.io)
   CLI is on `$PATH`, so you can also invoke it as `entire tail`.

The binary embeds its themes, so it's self-contained — the symlink works from
anywhere. After editing source or themes, re-run `./install.sh` (or
`go build -o entire-tail .`) to rebuild.

**No runtime dependencies** beyond the binary itself. The interactive session
picker additionally uses `pgrep` + `lsof` when present (both ship with macOS
and most Linux); without them the picker is silently disabled.

## Usage

```sh
entire tail                                # follow the latest session for $PWD
entire-tail                                # same, when called standalone
entire tail --agent codex                  # force a specific agent
entire tail /path/to/session.jsonl         # follow an explicit session
entire tail --theme dracula                # pick a bundled theme (default: tokyo-night)
entire tail -t nord -b 50                  # short flags also work
entire tail --no-backfill                  # skip history, only follow new events
entire tail --tool-style dots              # show tool calls as colored dots
entire tail --no-compact-tools             # show verbose `⚙ Tool  args` per call
entire tail --collapse 10                  # collapse user pastes over 10 lines
entire tail --no-collapse                  # show every user message in full
entire tail --pick                         # choose among live Claude sessions
entire tail --no-pick                      # never prompt; always auto-discover
entire tail --list-themes                  # see what's available
entire tail --help                         # full options
```

All flags also have env-var equivalents (`ENTIRE_TAIL_AGENT`,
`ENTIRE_TAIL_THEME`, `ENTIRE_TAIL_BACKFILL`, `ENTIRE_TAIL_TOOL_STYLE`,
`ENTIRE_TAIL_COLLAPSE`, `ENTIRE_TAIL_PICK`, `GLOW_STYLE`) for shell-rc
convenience — flags override env vars when both are set. The legacy
`CLAUDE_TAIL_*` variants are still honored.

## Live keys

While following on an interactive terminal, single keypresses adjust what new
events show as they stream:

| key            | effect                                                        |
|----------------|---------------------------------------------------------------|
| `t`            | toggle tool-call rendering (hide ⇄ show)                      |
| `c`            | toggle collapsing of long user pastes                         |
| `q` / Ctrl-D / Ctrl-C | quit                                                   |

These declutter the view on the fly without restarting — handy when an agent
goes on a long tool-call spree and you just want to read the prose. They affect
events rendered **from now on**; lines already in your scrollback are left as
they are (this is a streaming view, not an alt-screen TUI, so it never repaints
the screen — your terminal's / Zellij's native scrollback keeps working). To
re-render history with different settings, re-run with `--tool-style` /
`--collapse`. A one-line `keys:` legend prints in the startup banner.

## Picking among live sessions

When you have several agent panes open at once, `entire tail` finds every
working directory with a running agent process and — if more than one session
is live — drops you into a small picker before tailing:

```
Active agent sessions:

   1) claude  entirehq/infra            just now  Let me pull the authoritative `alert_type` list…
   2) claude  entirehq/infra              2m ago  Want me to dig into the permsreconciler alert…
   3) claude  dvydra/agent-planner        3m ago  Let me map the cwd→plan binding subsystem and…

Pick a session [1-3] (Enter=1, q=quit):
```

Each row shows the agent, the directory (last two path components, `(here)` if
it's `$PWD`), how long ago the session was last written, and a one-line preview
of its most recent message so you can tell them apart at a glance. Press the
number, or Enter for the most-recently-active one.

**Multiple panes in the same directory** each get their own row: for every live
cwd, entire-tail lists as many of its newest sessions as there are agent
processes running there (so two `claude` panes in `infra` show as two rows, told
apart by their previews).

By default (`auto`) the picker is **directory-aware**: if exactly one live
session is rooted in `$PWD`, that's unambiguously "the session here" — it tails
silently without a menu, even when other agents are live in other directories
(it prints a one-line note so you know others exist). The menu only appears when
the current directory is genuinely ambiguous — **2+ live sessions in `$PWD`** —
or when `$PWD` has none but **2+ are live elsewhere** and you need to choose
where to attach (and you're on an interactive terminal; a non-interactive run,
e.g. a cron pane, tails silently). Force the menu with `--pick` (useful to
confirm which session you're about to follow, or to attach to one in another
directory), or disable it with `--no-pick`. Set a default via
`ENTIRE_TAIL_PICK=always|never|auto`. The picker is scoped to `--agent`: `auto`
scans Claude, Codex, and Antigravity; forcing `--agent claude`/`codex`/`agy`
scans just that one.

A few caveats from how agents store sessions:

- **Claude** maps cleanly — a live pane always writes its session under
  `~/.claude/projects/<cwd>/`, so every pane shows up at any age (including
  idle-but-open ones).
- **Codex** doesn't encode the cwd in its session path (it's in the rollout's
  `session_meta`), and frequently runs *embedded* (driven by an editor or
  plugin) with no interactive rollout at all. So a running `codex` process only
  appears if it has a matching rollout written in the last ~12h — which filters
  out the embedded/headless ones that aren't tailable.
- **Antigravity (agy)** runs as a process named `agy`, so it's detected like
  the others, but it maps each cwd to a single conversation id (in
  `cache/last_conversations.json`) — so it always contributes **one row per
  cwd**, never per pane. Note the cache only updates once you send a message,
  so a freshly-launched agy session shows its *previous* conversation for that
  directory until you interact with it.

Discovery is best-effort and needs `pgrep` + `lsof` (present on macOS and most
Linux); without them the picker is silently disabled. Pass an explicit
`SESSION_FILE` to tail anything the picker doesn't surface.

## Tool calls

By default, each tool call collapses to a **single colored dot** — a
flurry of reads, greps, and edits between two assistant turns shows up
as a horizontal streak like `..........` instead of one verbose line per
call. The color encodes the kind: blue=read, green=edit, yellow=bash/exec,
magenta=grep, cyan=web, lavender=task, orange=mcp. A legend prints to stderr
at startup as a key. Tool results are dropped — each one is 1:1 with the
preceding tool call, so rendering both would just double-count every action.

Color mapping is agent-agnostic. Codex's `exec_command` and Antigravity's
shell tools get the same yellow as Claude's `Bash`; `apply_patch` gets the
same green as `Edit`/`Write`; etc.

Two other modes:

- `--tool-style none` — drop tool events entirely. Just user + assistant
  text. Useful when re-reading a long session as prose.
- `--tool-style lines` (alias: `--no-compact-tools`) — the original
  verbose `⚙ Tool  input-preview` / `↩ tool_result (×N)` output, one
  line per event.

Override the default via `ENTIRE_TAIL_TOOL_STYLE=none|dots|lines`.

## Collapsing long pastes

When you paste a big blob into the agent — command output, a stack trace, a
log dump — that single user turn can dwarf the rest of the conversation in the
tail. By default, any **user** message longer than **5 lines** is collapsed to
its first 5 lines followed by a marker:

```
… 29 more lines — re-run with --no-collapse to expand
```

- `--collapse N` — change the threshold to N lines (default 5).
- `--no-collapse` — never collapse; show every user message in full.
- `ENTIRE_TAIL_COLLAPSE=N` (or `off`) — env equivalent.

Only user turns collapse — assistant replies and tool calls are never
truncated. The line count is the raw number of lines you pasted, so it matches
what you typed regardless of terminal width (a single very long line that
soft-wraps still counts as one line). If a paste is cut off mid-code-fence, the
preview gets a synthetic closing ``` ``` ``` so the rest of the transcript
still renders cleanly.

This is a **render-time** collapse, not an interactive fold: the tail appends
to the terminal scrollback rather than running an alt-screen TUI. You can press
`c` while following to toggle collapsing for *new* events (see [Live
keys](#live-keys)), but already-printed lines stay as they are — to re-expand
history, re-run with `--no-collapse` (or scroll the agent's own pane).

## Themes

Bundled dark IDE themes (run `entire tail --list-themes` to see them with
descriptions):

- `tokyo-night` (default) — Folke's modern blue/purple palette
- `dracula` — pink keywords, comment-blue dim text
- `nord` — frosted blues and arctic neutrals
- `catppuccin-mocha` — mauve and pastels on dark slate
- `one-dark` — Atom's classic editor palette
- `claude` — the original style (cyan/magenta box headers, gray dim)
- `synthwave` — garish-but-legible neon: hot magenta / electric cyan / lime /
  electric-yellow on deep purple-black

Every theme color-codes structure: each heading level (`#`…`######`) renders in
a distinct palette color, with bold, emphasis, and block quotes tinted too — so
a transcript's shape reads at a glance. Full truecolor depth (the in-process
renderer no longer downsamples to 256 colors).

Each theme is a pair under `themes/`, embedded into the binary at build time:

- `themes/<name>.json` — glamour style (text + chroma syntax highlighting)
- `themes/<name>.sh` — the truecolor ANSI codes for the box headers,
  timestamps, and tool-use one-liners. The binary parses the `THEME_*_ANSI`
  values directly (it doesn't shell out to bash). The first comment line is
  the description shown by `--list-themes`.

To add your own: copy a pair, rename, swap colors, and rebuild — anything
that lands in `themes/<name>.json` + `themes/<name>.sh` is picked up
automatically.

**Full truecolor.** Rendering happens in-process, so each theme's exact hex
colors come through at full 24-bit depth — code-block syntax highlighting,
box headers, and timestamps all in the precise theme palette. (The old bash
version piped through `glow`, which downsampled bodies to 256 colors; that
limitation is gone.)

### Theme Gallery

Original output inside the agent TUI.

![](doc/images/Pasted%20image%2020260518150130.png)
`entire tail --theme=catppuccin-mocha`

![](doc/images/Pasted%20image%2020260518150205.png)
`entire tail --theme=original`

![](doc/images/Pasted%20image%2020260518150227.png)
`entire tail --theme=dracula`

![](doc/images/Pasted%20image%2020260518150250.png)
`entire tail --theme=nord`

![](doc/images/Pasted%20image%2020260518150307.png)
`entire tail --theme=tokyo-night`


## Files

- `*.go` — the source (single `package main`; see Architecture below)
- `themes/<name>.{json,sh}` — bundled themes, embedded at build (see Themes)
- `install.sh` — builds the binary, symlinks it into `~/.local/bin`, and
  registers the entire plugin
- `entire-tail.bash` — the original bash implementation, kept as a reference
  oracle for the equivalence test (`RUN_ORACLE=1 go test`)
- `testdata/` — synthetic session fixtures + golden render output

## Architecture

A single Go package with per-agent **adapters**. Each adapter is a `normalize`
function (`adapter_claude.go`, `adapter_codex.go`, `adapter_agy.go`) that lowers
each jsonl event to a canonical `Record`:

```go
type Record struct {
    Kind    Kind   // USER | CLAUDE | TOOLUSE | TOOLRESULT
    Ts      string // "YYYY-MM-DD HH:MM:SS"  (USER/CLAUDE)
    Body    string // markdown               (USER/CLAUDE)
    Name    string // tool name              (TOOLUSE)
    Summary string // one-line input preview (TOOLUSE)
    N       int    // count                  (TOOLRESULT)
}
```

Everything downstream — turn headers, glamour rendering, tool-dot coloring — is
agent-agnostic and consumes only the `Record`. Discovery (`discovery.go`) and
the live picker (`picker.go`) are likewise per-agent. Adding a new agent means
writing a `normalize` + a discovery function.

The rendering state machine (`render.go`) is one path shared by backfill and
live: it tracks the previous participant (so consecutive same-participant turns
collapse to a dim `⋯ ts` marker) and the dot-streak state, and renders each body
through an in-process glamour renderer.

## Notes

- **One in-process render path.** Both backfill (the whole session by default)
  and live events render each markdown body through the same in-process glamour
  renderer. The bash version needed two separate paths — a batched
  `glow`-subprocess + `awk` pipeline for backfill and a per-event loop for live
  — purely because spawning `glow` per event was slow. In-process rendering is
  fast enough that backfill stays `all` by default with no batching tricks.
- Each turn header gets a dimmed local-time timestamp from the jsonl event
  (`─── ▶ USER ──── 2026-05-18 14:02:33`), stripping the millisecond fraction
  before parsing the ISO-8601 instant.
- Tool uses are dimmed and truncated to ~140 chars (just a marker that a tool
  ran; the args aren't usually what you want to re-read).
- Reasoning / "thinking" blocks are skipped entirely (Claude `thinking`,
  Codex `reasoning`, Antigravity `thinking` field on `PLANNER_RESPONSE`).
- `tool_result` blocks are summarized as `↩ tool_result (×N)` in `lines`
  mode and dropped in `dots` mode (1:1 with the preceding tool_use).
- Word wrap is disabled (glamour `WithWordWrap(0)`). Each markdown paragraph is
  one logical line; your terminal soft-wraps it to whatever pane width you have,
  so resizing re-flows the text naturally on the next render.

## Live following

- **Claude / Codex** append to their jsonl, so the follower resumes from the
  byte offset where backfill ended and emits each new line.
- **Antigravity** rewrites the whole transcript on every step (atomic rename or
  truncate-in-place), so the follower re-reads the file on each change and
  dedups by `step_index` (seeded from the backfill snapshot).

## Tested against the bash original

The port is validated **byte-identical** (ANSI-stripped) to the original bash
implementation across Claude, Codex, and Antigravity in `dots` and `none`
modes. `go test` runs a golden-file suite over synthetic fixtures; `RUN_ORACLE=1
go test` additionally diffs the live Go output against the bash oracle
(`entire-tail.bash`) when `bash`/`jq`/`glow` are present.

Two deliberate improvements over the bash version:

- **Full truecolor** bodies (the bash `glow` pipe capped them at 256 colors).
- **`--tool-style lines`** shows tool-input previews literally; the bash version
  piped them through markdown, which silently ate `*`, `\`, and trailing spaces.

## Caveats / known weirdness

- Chroma (glamour's code-block syntax highlighter) is strict about hex colors —
  all 6-char hex values in each theme JSON are prefixed with `#`. If you fork
  a theme, keep that prefix.
- The binary reads the session file from disk — there's a tiny delay between
  the agent emitting an event and the line appearing here (usually <100ms,
  whatever the OS flushes the append at).
- If the agent is mid-stream on a long assistant message, the partial text
  won't show up until the message completes and gets written as a final
  event. This is a session-log limitation.
- Antigravity tool calls live inside `PLANNER_RESPONSE.tool_calls[]` rather
  than as separate step records — the adapter emits one TOOLUSE per item
  in that array. The matching tool *outputs* arrive as separate events
  named after the tool (`RUN_COMMAND`, `VIEW_FILE`, `LIST_DIRECTORY`,
  `GREP_SEARCH`, `WRITE_TO_FILE`, …) and lower to TOOLRESULT n=1. Unknown
  step types are silently skipped.
