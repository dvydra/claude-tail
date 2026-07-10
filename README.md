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

**No runtime dependencies** beyond the binary itself. The session tree
additionally uses `pgrep` + `lsof` when present (both ship with macOS and most
Linux) to mark which sessions are live; without them the tree still works, just
without live markers.

## Usage

```sh
entire tail                                # open the session tree picker (default)
entire-tail                                # same, when called standalone
entire tail --no-pick                      # skip the picker: auto-detect + tail $PWD
entire tail --agent codex                  # force a specific agent (tails directly)
entire tail /path/to/session.jsonl         # follow an explicit session
entire tail --theme dracula                # pick a bundled theme (default: tokyo-night)
entire tail -t nord -b 50                  # short flags also work
entire tail --no-backfill                  # skip history, only follow new events
entire tail --tool-style dots              # show tool calls as colored dots
entire tail --tool-style full              # Claude-style: ⏺ Update(main.go) + ⎿ diff
entire tail --collapse 10                  # collapse user pastes over 10 lines
entire tail --no-collapse                  # show every user message in full
entire tail --list                         # static ls-style dump of every session
entire tail --list --days 3                # ...only sessions from the last 3 days
entire tail --list-themes                  # see what's available
entire tail --help                         # full options
```

All flags also have env-var equivalents (`ENTIRE_TAIL_AGENT`,
`ENTIRE_TAIL_THEME`, `ENTIRE_TAIL_BACKFILL`, `ENTIRE_TAIL_TOOL_STYLE`,
`ENTIRE_TAIL_COLLAPSE`, `ENTIRE_TAIL_PICK`, `ENTIRE_TAIL_DAYS`, `GLOW_STYLE`) for shell-rc
convenience — flags override env vars when both are set. The legacy
`CLAUDE_TAIL_*` variants are still honored.

## Live keys

While following on an interactive terminal, single keypresses adjust what new
events show as they stream:

| key            | effect                                                        |
|----------------|---------------------------------------------------------------|
| `t`            | cycle tool-call rendering: **full → dots → hidden**           |
| `c`            | toggle collapsing of long user pastes                         |
| `r`            | reload — re-render the whole transcript with current settings |
| `q` / Ctrl-D / Ctrl-C | quit                                                   |

`t`/`c` declutter the view on the fly — handy when an agent goes on a long
tool-call spree and you just want the prose. They affect events rendered **from
now on** (this is a streaming view, not an alt-screen TUI, so it never repaints
in place — your terminal's / Zellij's native scrollback keeps working). To apply
them to the **history**, press **`r`**: it re-renders the whole current
transcript with the live settings, appending a fresh copy to the scrollback. So
the usual flow is "cycle to full with `t`, then `r` to redraw everything as
rich diffs." A one-line `keys:` legend prints in the startup banner.

## The session tree (default)

Can't remember which session that was? Just run `entire tail` — with no session
to tail, it opens an interactive tree of your sessions, grouped by **repo**:

```
  CLAUDE SESSIONS   ↑↓ move · → expand · ⏎ workspace↗ · t tail · / filter · q quit

▾ entirehq/infra  (4)  20h ago
    ○ 8babea4d  20h ago  Monitor Kubernetes Node Disk Usage
    ○ 3f23dd13  4d ago   Investigate ENT-977 telemetry regression
    ○ 9de20fff  4d ago   Fix AWS Server Nodegroup Telemetry Installer
▸ entirehq/entiredb  (7)  27h ago
▸ entirehq/entire.io  (4)  2d ago

  4 repos · 16 sessions
```

### Where the sessions come from

Three layers, tuned so the default is **instant and fully local**:

1. **Base — every local session** from a `~/.claude` crawl (nothing omitted),
   **grouped by repo** via each session's `cwd` git `origin` remote (for
   [`entire`](https://docs.entire.io)-enabled repos that's `entire://…/owner/repo`,
   so it lands on the same `owner/repo` the cloud uses; non-git dirs fall back to
   the folder path). No network — a few hundred milliseconds.
2. **Titles** — each row's label is the session's own summary / first prompt.
3. **Cloud (opt-in) — `--cloud`** enriches with `entire`'s generated titles and
   appends sessions tracked on **other machines** (listed, not tailable here).
   The fetch takes a few seconds and is **cached ~10 min**, so ordinary runs
   afterward stay instant *and* keep the nicer titles.

`--local` is the pure `~/.claude` crawl grouped by **folder** (no git remote
lookups, no cloud) — fastest / fully offline — with `● live` markers:

```
▾ ~/src/entirehq/entiredb  (3)  3m ago  ● live
    ● 99044a91  3m ago   [main] Lightweight HubSpot product properties sync
    ○ 6e18caf2  18h ago  [main] User self deletion and erasure epic
```

Each session row shows its id (the session uuid), age, and a one-line title;
the local view adds the git branch and live markers.

**Navigation:** arrow keys or `hjkl` move; `→` expands a group and `←`
collapses; `/` filters by name/title/id as you type (`Esc` clears); `q`/`Esc`
quits. The most recent group starts expanded. On a session:

- **`Enter`** → open the **iTerm workspace** for it (see below).
- **`t`** → just tail the session in the current pane.

**Recency at a glance** — rows are colored on a four-step scale by last activity:

| color        | meaning                                             |
|--------------|-----------------------------------------------------|
| bright green | **live now** — active in the last ~2 min (or, in `--local`, a running `claude` process) |
| muted green  | **recently active** — last 15 min                   |
| white        | **recent** — today                                  |
| grey         | **older**                                           |

**Scope.** The tree covers the last **`--days`** days (default **7**); widen with
`--days 30`, or `--days all` for everything. The `--local` crawl stays fast on
huge histories via a cheap directory-mtime gate that skips cold folders without
opening their session files; the `entire`-sourced view gets a bounded set from
the cloud and reads no session files for the listing at all.

**Skipping the picker.** `--no-pick` (or a non-interactive/piped run) goes
straight to auto-discovering `$PWD`'s most recent session and tailing it in
place. An explicit `SESSION_FILE` argument tails that file directly. Set a
default via `ENTIRE_TAIL_PICK=always|never`.

`-L`/`--list` prints the same tree as a **static, greppable `ls`-style dump** and
exits — handy for `grep`/`fzf` or just a full inventory. It's uncapped by default
(narrow it with `--days`) and only colorizes when writing to a terminal:

```sh
entire tail --list | grep -i erasure     # find that session about account erasure
entire tail --list --days 1              # what did I work on today?
```

**Tailing** prefers the session's local `~/.claude` jsonl. If it's not there — a
**cloud-only** session pruned locally or created on another machine — entire-tail
**reconstructs the transcript from the repo's local git checkpoint refs**
(`refs/entire/checkpoints/**`, where entire stores each session's transcript) as
long as that repo is checked out here, and tails that. Only if the repo isn't
cloned locally does it report the session can't be opened. Codex/Antigravity
aren't tailable through the tree yet — use `--agent codex`/`agy` or an explicit
`SESSION_FILE`.

## The iTerm2 workspace (macOS)

Pressing **`Enter`** on a session (on macOS + iTerm2) turns the **current**
window into a 3-pane workspace for it, via AppleScript — no extra deps,
`osascript` ships with the OS. The pane you launched from becomes Claude,
resuming the picked session, with a live tail and a shell beside it — all `cd`'d
to that **session's** folder (wherever it was, not necessarily `$PWD`):

```
┌──────────┬──────────┐
│ claude   │          │   A = claude --resume <picked id>  (the pane you were in)
│ --resume │ entire-  │   B = entire-tail, following that session
├──────────┤ tail     │   C = a plain shell
│ shell    │          │
└──────────┴──────────┘
```

(The `claude --resume` command is queued into the current pane and runs the
moment `entire-tail` exits, so that pane becomes A.)

The workspace only fires when the current window is a **single pane**. If the
window already has splits, `Enter` just **tails the session in the current pane**
— no scripting, no new window — so your existing layout is never touched.

Off iTerm (or non-macOS), `Enter` likewise falls back to tailing in place, same
as `t`. `-w`/`--workspace` just forces the picker (it's already the default).
tmux / other terminals are a possible follow-up.

## Search

Can't find the session where you said *"fire socks"*? `--search` (or `-S`) finds
sessions by their **content**, not just titles, and ranks them by relevance:

```sh
entire tail fire socks                   # bare words = search — no flag needed
entire tail --search "fire socks"        # explicit flag, identical
entire tail --list fire socks            # static ranked dump
entire tail fire socks --local           # local transcripts only, no network
```

Any bare arguments are treated as a search query (a single argument that's an
existing file still tails that file). So `entire tail fire socks` just works.

```
🔎 "fire socks" — 11 result(s), best match first
  b7dd3e4a  just now  [dvydra/claude-tail]  …we mentioned "fire socks" but i can't fin…
  8e6bd2c4  72d ago   [browser-extension]   This is a chrome extension for the entire.io…
  edec5f4a  46d ago   [infra]               we have set up a new datadog account…
```

It searches two sources and merges them by session:

- **Local transcripts** via [ripgrep](https://github.com/BurntSushi/ripgrep)
  (a literal, case-insensitive scan of `~/.claude`) — the exact phrase you typed.
- **`entire` checkpoint search** — hybrid semantic + keyword across all your
  repos, so it also surfaces sessions that *mean* the same thing without the
  exact words (skipped with `--local`, or when offline).

**Ranking**: an exact local phrase match weighs heaviest (you typed those words),
`entire`'s semantic score adds on top, and matching both sources ranks highest;
recency breaks ties. Each row shows the **matching snippet** so you can see why
it hit. `Enter`/`t` resume or tail the result like any tree row. Results are
capped at the top 50 (a ubiquitous term otherwise matches everything).

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

Tool rendering is a **tristate** — set it with `--tool-style` (default `dots`)
or cycle it live with the `t` key (full → dots → hidden):

- `dots` — the colored-dot streak described above.
- `full` — Claude-Code-style tool rendering: a `⏺ Label(arg)` line per call
  (`⏺ Update(main.go)`, `⏺ Bash(go test ./...)`, `⏺ Read(render.go)`) and, under
  a `⎿`, the result — a **line-numbered red/green diff** for edits (from the
  session's `structuredPatch`), the command's **full** output, or a short
  summary (`Read 1304 lines`). Full means full: command output is never
  truncated. (aliases: `lines`; `--no-compact-tools`.)
- `hidden` — drop tool events entirely; just user + assistant text. Useful
  when re-reading a long session as prose. (alias: `none`.)

The rich diff/output detail comes from Claude's `toolUseResult` records, so it's
fullest for Claude sessions; Codex/Antigravity show the `⏺ Label(arg)` line
without the diff. Tip: cycle to `full` with `t` and press `r` to re-render the
whole transcript as diffs. (Tool calls batched into one assistant turn render as
a group of `⏺` lines followed by their `⎿` results, rather than strictly
interleaved.)

Override the default via `ENTIRE_TAIL_TOOL_STYLE=full|dots|hidden`.

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
