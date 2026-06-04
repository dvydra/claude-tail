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
formats each event nicely, and renders the markdown bodies through
[glow](https://github.com/charmbracelet/glow) using a custom flush-left
style.

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

The script does both things in one shot:

1. Symlinks `entire-tail` into `~/.local/bin/` so the standalone command
   works.
2. Registers it via `entire plugin install` if the [`entire`](https://docs.entire.io)
   CLI is on `$PATH`, so you can also invoke it as `entire tail`.

Editing files in this directory takes effect immediately for both invocation
paths — both routes resolve through the symlink.

Requires `jq`, `glow`, `tail`, `base64`, `awk` on `$PATH`. On macOS:

```sh
brew install glow jq
```

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
to the terminal scrollback rather than running an alt-screen TUI, so there's no
in-window key to expand a block after it scrolls past. To see the full text,
re-run with `--no-collapse` (or scroll the agent's own pane). See
[Architecture](#architecture) for why the streaming design rules out a live
toggle.

## Themes

Bundled dark IDE themes (run `entire tail --list-themes` to see them with
descriptions):

- `tokyo-night` (default) — Folke's modern blue/purple palette
- `dracula` — pink keywords, comment-blue dim text
- `nord` — frosted blues and arctic neutrals
- `catppuccin-mocha` — mauve and pastels on dark slate
- `one-dark` — Atom's classic editor palette
- `claude` — the original style (cyan/magenta box headers, gray dim)

Each theme is a pair under `themes/`:

- `themes/<name>.json` — glow style (text + chroma syntax highlighting)
- `themes/<name>.sh` — sourced for the truecolor ANSI codes used outside glow
  (turn headers, timestamps, tool-use one-liners). The first comment line is
  the description shown by `--list-themes`.

To add your own: copy a pair, rename, swap colors. Anything that lands in
`themes/<name>.json` + `themes/<name>.sh` is picked up automatically.

**Color depth caveat.** Glow's stdout in this pipeline (`glow | awk`) is
never a TTY, so glow downsamples each theme's hex colors to their closest
ANSI 16 match. Themes still look visibly different from each other (Tokyo
Night → bright blues, Dracula → pink/red, Nord → frost cyan, etc.), just not
pixel-perfect to the real IDE. Box headers and timestamps go straight to
your terminal via `printf`, so those render in full truecolor and pop in the
exact theme palette. The downsample is the cost of the 12× backfill speedup
that the awk pass enables.

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

- `entire-tail` — the script
- `themes/<name>.{json,sh}` — bundled themes (see Themes section above)
- `install.sh` — installs `entire-tail` as an entire plugin and as
  `~/.local/bin/entire-tail`

## Architecture

A single bash script with per-agent **adapters**. Each adapter is two
pieces:

1. A shell function that discovers the session file for `$PWD`
   (`find_session_<agent>`).
2. A jq filter (`JQ_CLAUDE`, `JQ_CODEX`, `JQ_AGY`) with a `normalize:`
   function that lowers each jsonl event to a canonical record:
   ```
   {kind: "USER"|"CLAUDE"|"TOOLUSE"|"TOOLRESULT",
    ts:   "YYYY-MM-DD HH:MM:SS",  (USER/CLAUDE only)
    body: <markdown string>,       (USER/CLAUDE only)
    name: <tool name>,             (TOOLUSE only)
    summary: <one-line input>,     (TOOLUSE only)
    n: <count>}                    (TOOLRESULT only)
   ```

Everything downstream — turn headers, glow rendering, tool-dot coloring,
the backfill/live split — is agent-agnostic and consumes only the
normalized record. Adding a new agent is a matter of writing a new pair
(`find_session_<agent>` + `JQ_<AGENT>`).

## Notes

- **Two-phase rendering.** The backfill (the whole session by default) is
  concatenated into one big markdown document and piped through a **single**
  glow invocation, so the Go binary startup + chroma syntax-highlighting cost
  is paid once instead of per-message. On a 1500-event / 3.5MB session this
  drops backfill render time from ~1.5s to ~0.13s — fast enough that there's
  no reason to truncate the history, hence the `all` default. Once we're
  caught up, live events render per-message (one glow call each) at the
  natural pace of the conversation.
- Each turn header gets a dimmed local-time timestamp from the jsonl event
  (`─── ▶ USER ──── 2026-05-18 14:02:33`). Timestamps go through jq's
  `strflocaltime`, after stripping the millisecond fraction so
  `fromdateiso8601` will accept them.
- Backfill uses on-line sentinels (`[[CTAIL_U]]…`, `[[CTAIL_C]]…`, etc.)
  inside the markdown stream so glow can render the whole session in one
  pass; an `awk` post-processor rewrites those sentinels into the same ANSI
  box headers live mode emits directly. End result: identical look for
  backfilled and live turns.
- Tool uses are dimmed and truncated to ~140 chars (just a marker that a tool
  ran; the args aren't usually what you want to re-read).
- Reasoning / "thinking" blocks are skipped entirely (Claude `thinking`,
  Codex `reasoning`, Antigravity `thinking` field on `PLANNER_RESPONSE`).
- `tool_result` blocks are summarized as `↩ tool_result (×N)` in `lines`
  mode and dropped in `dots` mode (1:1 with the preceding tool_use).
- `glow -w 0` disables glow's own word wrap. Each markdown paragraph is one
  logical line in glow's output; your terminal soft-wraps it to whatever pane
  width you have, which means resizing the pane re-flows the text naturally
  on the next render.

## Format pipeline

```
backfill: sed -n M,Np session.jsonl | jq (per-agent normalize) → concatenated
            markdown | glow (one call) | awk (rewrites sentinels → headers, dots)
live:     tail -F -n +N+1 session.jsonl | jq (per-agent normalize) → one
            base64 line per event | bash loop → ANSI header + glow render
            per message
```

Base64 in the live-mode middle stage keeps multi-line markdown bodies intact
across the shell pipeline without escaping/quoting headaches.

## Caveats / known weirdness

- Chroma (glow's code-block syntax highlighter) is strict about hex colors —
  all 6-char hex values in each theme JSON are prefixed with `#`. If you fork
  a theme, keep that prefix or glow will panic on the first code block.
- The script reads the session file from disk — there's a tiny delay between
  the agent emitting an event and the line appearing here (usually <100ms,
  whatever the OS flushes the append at).
- If the agent is mid-stream on a long assistant message, the partial text
  won't show up until the message completes and gets written as a final
  event. This is a session-log limitation, not something the script can work
  around.
- Antigravity tool calls live inside `PLANNER_RESPONSE.tool_calls[]` rather
  than as separate step records — the adapter emits one TOOLUSE per item
  in that array. The matching tool *outputs* arrive as separate events
  named after the tool (`RUN_COMMAND`, `VIEW_FILE`, `LIST_DIRECTORY`,
  `GREP_SEARCH`, `WRITE_TO_FILE`, …) and lower to TOOLRESULT n=1. Unknown
  step types are silently skipped.
