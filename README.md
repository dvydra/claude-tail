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

On **macOS 26+** the `i` card's AI summary uses Apple's built-in Foundation
Models CLI (`fm`, `/usr/bin/fm`) on the on-device model — no build step, no extra
dependency. When `fm` is absent the card falls back to metadata only.

## Usage

```sh
entire tail                                # adopt the claude in this iTerm tab, else the tree picker
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
entire tail --no-wrap                      # don't wrap prose; let the terminal soft-wrap it
entire tail --list                         # static ls-style dump of every session
entire tail --list --days 3                # ...only sessions from the last 3 days
entire tail --list-themes                  # see what's available
entire tail --help                         # full options
```

All flags also have env-var equivalents (`ENTIRE_TAIL_AGENT`,
`ENTIRE_TAIL_THEME`, `ENTIRE_TAIL_BACKFILL`, `ENTIRE_TAIL_TOOL_STYLE`,
`ENTIRE_TAIL_COLLAPSE`, `ENTIRE_TAIL_PICK`, `ENTIRE_TAIL_DAYS`,
`ENTIRE_TAIL_CLAUDE_BIN`, `ENTIRE_TAIL_NO_WRAP`, `GLOW_STYLE`) for shell-rc
convenience — flags override env vars when both are set. The legacy
`CLAUDE_TAIL_*` variants are still honored.

### Auto-adopt the pane's agent (iTerm2, macOS)

entire-tail is built to run in a pane next to the agent — so when you launch it
bare and there's **exactly one `claude` in the same iTerm tab**, it skips the
picker and tails *that* session directly. Split a pane, run `entire-tail`, done —
no ids, no picking.

The catch it works around: Claude Code doesn't expose its session id to
outsiders — it's not in the process argv, not in the environment, and the
transcript file is opened-appended-closed per write (never held open), so you
can't `lsof` it either. Instead entire-tail pins the *process*: every terminal
carries `ITERM_SESSION_ID` (`wNtNpM:…` — window, tab, pane) in its environment,
so it takes its own tab and adopts the lone `claude` sharing it (read via `ps
eww`). A claude in another tab or window is **never** grabbed; with zero or
several claudes in the tab it quietly falls back to the tree. It then resolves
that claude's transcript — exactly if the claude was launched with
`--session-id`/`--resume`, otherwise the actively-written session in its project
dir. Worktree-fork and `/clear` rollovers are then followed as usual (see
[Following a Claude session across a fork](#following-a-claude-session-across-a-fork)).
Force the tree instead with `-p`. Off iTerm (or non-macOS) this is inert and the
tree/`--no-pick` behavior is unchanged.

### …and when it can't, the tree points at it

Adopt gives up on purpose in three cases — a tab holding **two** claudes, a
claude **one tab over**, and `Ctrl-X`, which asks for the tree — and you used to
land in a list with no clue which row was the agent you were just looking at.

So the tree runs the same placement without the "exactly one" rule: every running
claude is located by its `ITERM_SESSION_ID` and resolved to the transcript it's
writing. Sessions in **this tab** get a bright `◀`, sessions elsewhere in **this
window** a dim one, and the tree **opens with the cursor already on the closest
one** (expanding its group to get there). The footer names whichever marks are on
screen:

```
  11 folders · 35 sessions · ◀ runs in this tab (dim: this window)
```

It's strictly additive — nothing gets *un*-marked because no process was found
near you — and inert off iTerm or without `pgrep`/`lsof`, where the cursor starts
where it always did.

## The status bar

The bottom row of the terminal is a status bar:

```
 claude · claude-tail · ac2925b3 · 14 turns · 45s ago    dots · tokyo-night · collapse 5 · ? settings
```

Left is which session you're following and what it's doing — `45s ago` is how
long since the transcript last grew, and it's replaced by **`⁉ waiting for you`**
the moment the agent blocks on a question or a permission prompt. Right is how
it's being rendered. It narrows gracefully: the render settings give way first,
then the fields on the left, so the session id survives to about 40 columns.

Press `t`, `T`, `c`, `m`, `w` or `y` and the whole row turns **yellow with what just
happened** for three seconds, then goes back to normal:

```
 copied 2 messages as slack mrkdwn (1.2k chars — press y again to add the one before)
```

The row is real estate taken from the terminal, not repainted by us: the
scrolling region is shrunk by one line (`DECSTBM`) so the transcript scrolls
underneath a row entire-tail owns. Scrollback is untouched. `--no-status` turns
it off, and it's automatically absent when output is piped.

## Live keys

While following on an interactive terminal, single keypresses adjust what new
events show as they stream:

| key            | effect                                                        |
|----------------|---------------------------------------------------------------|
| `?`            | **settings** — a live panel of everything you can change, with its current value; see below |
| `y`            | **copy the last agent message as Slack mrkdwn** — press again within 3s to add the one before it |
| `m`            | toggle agent text between rendered markdown and **Slack mrkdwn source** |
| `w`            | toggle **word wrap** — off, each paragraph is one long logical line, so a mouse drag-select copies it unbroken (the bar shows `nowrap`) |
| `t`            | cycle tool-call rendering: **full → dots → hidden**           |
| `T`            | cycle the color **theme** — steps through the bundled themes and re-renders the whole transcript in the new theme |
| `c`            | toggle collapsing of long user pastes                         |
| `→`            | **focus subagents** — open the session's subagent transcripts (see below) |
| `r`            | re-render the whole transcript with current settings (`t`/`T`/`c`/`m`/`w` already do this themselves) |
| Ctrl-X         | **back to the tree** — pop out of the live tail into the session tree picker (Claude only); pick another with `t` to tail it in this same pane, or `Enter`/`n` for a workspace |
| `q` / Ctrl-D / Ctrl-C | quit                                                   |

`t`/`c` declutter the view on the fly — handy when an agent goes on a long
tool-call spree and you just want the prose. Each of `t`/`T`/`c`/`m`/`w`
**re-renders as it goes**, so what's on screen reflects the new setting
immediately. This is a streaming view, not an alt-screen TUI, so a re-render
appends a fresh copy rather than repainting in place — your terminal's /
Zellij's native scrollback keeps working.

A toggle re-renders **one screenful**, not the whole session (`⟳ tool calls
hidden · showing the last 22 lines`). Dumping a long transcript on every
keypress buries the screen in scrollback, and since the tail of the new copy
looks much like the tail of the old one, it reads as though nothing happened.
Press **`r`** for the full re-render when you want the whole history in the new
style.
### The settings panel (`?`)

A one-line `keys:` legend prints in the startup banner; **`?`** opens the panel
it points at. Every setting is a row with its **current value** beside it, so it
also answers "which tool style / theme am I in now?" after a few `t`/`T` presses
have scrolled the banner away:

```
╭─ entire-tail 0.26.0 ─────────────────────────────────────────────╮
│                                                                  │
│ ▸ theme         tokyo-night                                      │
│   tools         dots                                             │
│   collapse      user pastes > 5 lines                            │
│   wrap          on, 99 columns                                   │
│   bodies        rendered markdown  · this session only           │
│   status bar    on                                               │
│   prompt hooks  installed  · writes ~/.claude/settings.json      │
│   api tap       running  · launchd agent                         │
│                                                                  │
│ ── session ───────────────────────────────────────────────────── │
│   agent     claude                                               │
│   session   ~/.claude/projects/-repo/abc.jsonl                   │
│   backfill  all (1..188 of 188)                                  │
│                                                                  │
│ ── keys ────────────────────────────────────────────────────────  ⋯
╰──────────────────────────── ↑↓ move · ←→ change · q close ───────╯
```

`↑↓` moves, `←→` (or `⏎`) changes the row, `q`/`Esc`/`?` closes. Changes take
effect immediately; the transcript re-renders once when the panel closes, since
nothing underneath an alt-screen is visible while it's open. The session
context, the key map and the dot legend live below the settings in the same
scroll, so nothing the old help card showed was lost.

**Changes are remembered.** Theme, tool style, collapse, wrap and the status bar
are written to `~/.claude/entire-tail/settings.json` and picked up next launch.
They sit *below* flags and env vars in precedence — `--theme dracula` still wins
for that run — and the file is created only once you change something, so an
install that never opens the panel behaves exactly as it always did. Delete the
file to go back to the defaults.

Two rows are different:

- **bodies** (the `m` mrkdwn view) is a mode you flip to copy something out, not
  a preference, so it isn't remembered — coming back tomorrow to raw mrkdwn
  would read as a broken renderer.
- **prompt hooks** and **api tap** reach outside this process (they edit
  `~/.claude/settings.json` and load a launchd agent), so they don't move on a
  stray arrow key: `⏎` arms the row, a second `⏎` does it, and any other key
  cancels.

### Getting text out into Slack

Most of what you copy out of a tail ends up pasted into Slack, so `y` copies it
already converted: **`y` puts the last agent message on the clipboard as Slack
mrkdwn.** Press it again within 3 seconds and it extends backwards one message
at a time (`copied 2 messages as slack mrkdwn`).

The unit is one *message*, not everything since your last prompt — a working
turn can be twenty minutes of narration around tool calls, and what you want in
Slack is almost always the last thing the agent actually said. A message that
arrives as two records still yanks as one (they share a message id).

It converts from the transcript's **raw markdown**, not from the screen — a
mouse-drag gets you glamour's soft-wrapped, indented, ANSI-colored rendering,
while `y` gets the text itself. `**bold**` becomes `*bold*`, `*italic*` becomes
`_italic_`, `~~strike~~` becomes `~strike~`, headings become bold lines, bullets
become `•`. Links stay as `[text](url)` — the composer understands that form.

**Code is never rewritten.** Anything inside a fence or backticks is passed
through byte-for-byte, and a fence keeps its fence (minus the language tag,
which mrkdwn ignores) — so a command you copy still runs when you paste it into
a terminal, and in Slack it lands in a code box you can copy back out of. Each
` ``` ` is put on a line of its own, since a one-line ` ```code``` ` renders in
Slack as literal backticks.

The conversion targets the Slack **composer** — a human pasting into the message
box — not the Web API. So no HTML escaping (`&amp;` would paste literally) and
never `<url|text>` (that form is only parsed for API-posted messages, so it
would paste literally too).

### Tables get padded, and wide ones collapse into blocks

Slack renders no tables at all, so one that fits goes into a code fence — the box
is monospaced, so the columns line up — and the cells are **re-padded on the way
in**, because a source table almost never arrives aligned:

```
| Agent       | Discovery        | Notes     |
|-------------|------------------|-----------|
| Claude Code | projects/*.jsonl | default   |
| agy         | brain logs       | id lookup |
```

Alignment markers (`:--`, `:-:`, `--:`) are kept and decide which side the
padding goes on. Widths are measured in display columns, so a CJK glyph or an
emoji — two cells wide in a monospaced box — doesn't knock every column after it
out by one. Only the whitespace *between* cells is touched: a command in a cell
still runs when you paste it back out.

Past **80 columns laid out**, an aligned table stops fitting a message pane, so
it's turned inside out into one block per row instead:

```
*api-gateway* · prod · us-east-1 · healthy
• Cluster: eks-prod-use1 · Replicas: 6 · CPU req: 500m
• Mem req: 1Gi · Image tag: v2.14.3 · Owner: platform
• Last deploy: 2026-09-08 14:02
```

- The **first column is the heading**, in bold.
- The columns you'd *filter* on join it bare — short, repeating, word-like values
  (`prod`, `us-east-1`, `healthy`) that still read with their names removed. A
  bare `6` or `500m` would be a riddle, so numeric columns stay in the body.
- Everything else keeps its **column name inline**, so a line still makes sense
  once the header row has scrolled away, packed a few to a line.
- A column with the **same value in every row** is stated once above the blocks
  and dropped from them: `_Same for every row: Env prod · Status healthy_`.

A table inside a code fence is someone's output, not a table to reformat, and is
passed through untouched — as is a table with more cells in a row than in its
header, since reformatting that one would mean dropping the extras. This is a
**mrkdwn-only** transform: the terminal rendering keeps its box-drawn table,
which is what a monospaced pane is for.

`m` is the same conversion applied to the screen: agent text renders as mrkdwn
source instead of glamour, so whatever you drag-select with the mouse already
*is* mrkdwn. Handy when you want one paragraph rather than a whole turn. User
turns keep rendering normally, and like `t`/`c` it affects new events — press
`r` to redraw the history.

Clipboard access is `pbcopy` on macOS (`wl-copy`/`xclip` on Linux), falling back
to OSC 52 through the terminal, which is what makes `y` work over ssh/tmux.

`T` (shift-`t`) cycles the color theme. Unlike `t`/`c`, it re-renders the whole
transcript itself — glamour body colors already in the scrollback can't be
recolored in place, so it appends a fresh copy in the new theme (a `⟳ theme:
<name>` divider marks it). It steps through the bundled themes in order (same
set as `--list-themes` / the `-t` flag), wrapping around; a `-s/--style` body
override is not carried into the cycle.

## Subagents & pending questions (Claude)

When the agent you're tailing spawns **subagents** (the `Agent`/Task tool — often
several background agents at once), entire-tail surfaces them so the
orchestration isn't invisible:

- **Spawn markers** in the main stream — `⏺ ▸ agent: <task>  (<type>)` — appear
  in every tool style, so you always see what was launched.
- **`→` focus overlay** — press `→` to open a full-screen view of the subagent
  transcripts. `←`/`→` cycle between them (the header shows `focus 2/3 · <task> ·
  ✔ done 8m47s`), `↑↓`/PgUp/PgDn scroll, `r` reloads, `q`/Esc returns to the live
  tail. The focused subagent **live-follows** — new turns stream in while you
  watch. entire-tail finds the subagent files next to the main transcript
  (`…/<sessionId>/subagents/agent-*.jsonl`).

When the agent asks you an **AskUserQuestion**, entire-tail renders a prominent
card the instant it's asked — *before* you answer, since your answer is a
separate later event — and rings the terminal bell once so you notice even if
you've looked away:

```
╭─ ⁉ WAITING FOR YOUR ANSWER ────────────────────────────╮
│ Scope: What are we building this session?              │
│   1. Reconcile process, ledger first — Treat the led…  │
│   2. Reconcile process, ledger stays WIP — Design + …  │
╰────────────────────────────────────────────────────────╯
```

### Instant alert on permission requests (opt-in)

Claude Code blocks on permission requests *before* appending them to your
transcript, so a plain tail stays dark until you answer — you might miss that
the session is waiting. entire-tail can surface these instantly via **opt-in
hooks** that write a marker file the moment a prompt appears:

```sh
entire-tail install-hooks       # Install the hooks into ~/.claude/settings.json
entire-tail uninstall-hooks     # Remove them (reversible)
```

The hooks are **entirely optional** — install them only if you want the instant
alert. They edit `~/.claude/settings.json` (backed up automatically) and spawn a
single small bash script per event. Pass `--no-hook-install` to suppress the
one-time first-run offer. The alert surfaces the same question/permission cards
as the deferred JSONL — dedup prevents doubling once the real record arrives.

This feature is **Claude-only** and has no effect on Codex or Antigravity.

### The API tap: see the question's *reasoning*, not just the question (opt-in)

The hooks above tell you a question is waiting, but not *why*. Claude Code
withholds the entire message an `AskUserQuestion` belongs to — **including the
text it wrote just before asking** — until you answer. So the card appears alone,
and the paragraph that explains it only shows up afterwards, below the card,
reading backwards.

The transcript can't fix this: while a question is pending, those bytes are
nowhere on disk. The tap gets them from the wire instead — a local reverse proxy
that agents launched from the tree are pointed at:

```sh
./install-tap.sh                # turn it on for good (KeepAlive LaunchAgent)
./disable-tap.sh                # turn it off again
```

`install-tap.sh` makes sure entire-tail is installed at a stable path first — a
LaunchAgent outlives the shell that wrote it, so pinning it to a git worktree or a
temp build gives you a daemon that quietly stops coming back. It then loads the
agent and **verifies something is listening** rather than telling you it probably
worked. `disable-tap.sh` unloads it, stops a hand-started daemon too, warns about
sessions that are already routed, and confirms the port is free.

Or drive it directly:

```sh
entire-tail tap start           # run it in the foreground (127.0.0.1:47391)
entire-tail tap status          # is it up? which sessions are generating?
entire-tail tap install         # write + load the KeepAlive LaunchAgent
entire-tail tap install --binary /usr/local/bin/entire-tail   # pin a specific one
entire-tail tap uninstall       # unload, remove, stop
entire-tail tap stop
```

With it running, a blocked question renders **preamble first, then the card**, at
the moment it's asked. When the transcript finally flushes the same text, it's
suppressed rather than repeated (matched on the provider's own message id, so the
match is exact).

It also gives the picker something no amount of file-mtime guessing can: the tap
knows *which* session has a request in flight, marked `◉` instead of `●`.

Deliberately conservative:

- **Opt-in and fail-open.** A session is routed only if the daemon answers a
  health check at launch. No daemon → agents launch exactly as they did before.
- **Only `POST /v1/messages` is inspected**; everything else is proxied
  untouched, and request headers are never logged or stored (they carry your auth
  token).
- **Ordinary turns still come from the transcript** — measured, it lands ~200ms
  after the wire, so there's nothing to win there and the tap stays out of it.
- **The cost:** a session launched through the tap depends on it. If the daemon
  dies mid-session, that session's API endpoint is gone until it's back — hence
  the KeepAlive agent (verified: kill the daemon and launchd returns it on the
  same port within seconds). `--no-tap` makes the tail ignore the tap entirely.

**The tap is never required.** With no daemon running, `entire-tail` behaves
exactly as it did before it existed: launched agents get no `ANTHROPIC_BASE_URL`,
the tail renders everything from the transcript, and a blocked question still
alerts via the opt-in hooks — you just don't see its preamble until you answer.
That fallback is asserted in `e2e_tap.sh` (stage 4) against a completely bare
`HOME`, not just assumed.

**If you route a session by hand, set `ENABLE_TOOL_SEARCH=true` too:**

```sh
ANTHROPIC_BASE_URL=http://127.0.0.1:47391 ENABLE_TOOL_SEARCH=true claude
```

A custom base URL makes Claude Code stop deferring MCP tool schemas (it can't
know a proxy forwards `tool_reference` blocks), so every schema ships inline —
with a big MCP fleet that alone can push you past the context limit and you'll
see **"Prompt is too long"** a turn or two in. The tap's own launcher sets both
vars for you; this only matters when you export `ANTHROPIC_BASE_URL` yourself.

Without the tap, entire-tail still fixes the *ordering*: when the withheld
preamble finally arrives, the question card is redrawn beneath it so the pane
reads in the order things actually happened.

### "I'm done" marker (Claude)

Most agent turns are the agent *continuing* — it says a sentence and fires more
tools. Only some turns actually hand control back. Claude records which is
which: every assistant record carries `message.stop_reason`, and it's `end_turn`
(not `tool_use`) exactly when the agent is finished and waiting on you.

entire-tail leads that closing message with a bright-green banner, so you can
tell "still working" from "your move" at a glance while scrolling:

```
─── ◀ AGENT ────────────────────────────── 2026-07-29 16:16:00
✔ DONE — over to you
Post this to #progress:
```

It's read straight from the transcript — no hooks, nothing to install. A
subagent's `end_turn` is ignored (that's the subagent finishing, not the agent),
and transcripts predating `stop_reason` simply render as before. Also
**Claude-only**: Codex and Antigravity don't report the signal.

### Background-task notes (Claude)

When the agent is watching something in the background — a `Monitor` on a CI
run, say — Claude Code feeds each update back into the transcript as a `user`
record, even though you never typed it. Rendered naively that's a full USER box
header over a task id, a temp output-file path, a pile of check statuses, and an
instruction addressed to the agent ("send a PushNotification if…"), all
attributed to you.

entire-tail recognises those records (`promptSource: "system"`) and renders one
dim line instead:

```
─── ◀ AGENT ────────────────────────────── 2026-08-21 15:45:28
✔ DONE — over to you
integration + test-db green — only lint left. Merge fires on the settle.
  ⧗ Monitor event: "CI on #3304 workload-rename — settle then I… · Entire Gates: fail, lint: pass, test: pass…
  ⧗ Monitor "CI on #3304 workload-rename — settle then I merge (authorized)" stream ended
```

Kept: the notification's own summary and the event body. Dropped: the task id,
the tool-use id, the output-file path, and the agent-directed instruction. When
the line has to be cut, the **title** gives way rather than the event — the
title repeats on every tick of the same Monitor, the checks are what changed.

## The session tree (default)

Can't remember which session that was? Just run `entire tail` — with no session
to tail, it opens an interactive tree of your sessions, grouped by **repo**:

```
  CLAUDE SESSIONS   ↑↓ move · → expand · ⏎ workspace↗ · t tail · / filter · q quit

▾ entirehq/infra  (4)  20h ago
    ○   8babea4d  20h ago  Monitor Kubernetes Node Disk Usage
    ○   3f23dd13  4d ago   Investigate ENT-977 telemetry regression
    ○ @ 9de20fff  4d ago   Rewire the greenhouse thermostat
▸ entirehq/entiredb  (7)  27h ago
▸ @ dvydra/side-quest  (3)  2d ago

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

Each session row shows its id (the session uuid), age, **token spend**
(compact — `300k`, `1.2m`; entire-tracked sessions only), and a one-line title;
the local view adds the git branch and live markers.

**Navigation:** arrow keys or `hjkl` move; `→` expands a group and `←`
collapses; `/` filters as you type (`Esc` clears) — matching name/title/id/branch
**and the session's recent transcript content** (the newest ~8KB of message
text), so you can find a session by what you talked about, not just what it's
called; `q`/`Esc` quits. The most recent group starts expanded. On a session:

- **`Enter`** → open the **iTerm workspace** for it (see below).
- **`i`** → the combined **info view**: an info card fixed at the top, a divider,
  then the session's recent transcript in a **scrollable** pane below (starts at
  the latest turns; works for cloud-only sessions too — reconstructed from git
  checkpoint refs). The card holds, in order:
  - an **on-device AI summary** (headline · 2-3 sentence summary · key points ·
    outcome), generated locally in ~1-2s via the `fm` Foundation Models CLI on
    macOS — no cloud, no keys, works offline (dropped when unavailable);
  - **entire's metadata** — repo, model, token spend, checkpoints, activity,
    **last-updated**, and the transcript **path**;
  - a **trails & prs** section listing the entire trails
    (`entire.io/gh/owner/repo/trails/id`) and GitHub PRs
    (`github.com/owner/repo/pull/n`) referenced in the transcript, each a
    clickable link (OSC 8 — ⌘-click in iTerm2), capped with a `+N more`.

  Metadata sits above the link list so path/last-updated stay visible even when
  the card is clipped on a short terminal. `↑↓`/PgUp/PgDn scroll the preview,
  `q`/`Esc` returns.
- **`t`** → just tail the session in the current pane.
- **`n`** → open a workspace for a **new** Claude session in the **highlighted
  folder's** directory (or `$PWD` if it has none) — a fresh agent (`claude` by
  default, see [`--claude-bin`](#which-agent-pane-a-launches---claude-bin)) +
  tail + shell. Pick a repo group, hit `n`, and it `cd`s there and starts fresh.
  Both panes **pin a shared session id**, so the tail latches onto exactly that
  session even with other Claude sessions live in the same repo. Under a wrapper
  that doesn't forward `--session-id` (happy included — see below) the pane
  instead uses `--wait-new` and waits for whatever session the agent creates.
- **`@`** → the same workspace, but for a new session on your **second Claude
  account** — see [Two Claude accounts](#two-claude-accounts-the-pink-) below.

The **current directory always appears** in the tree — even with no sessions yet
(shown as `▸ path  (no sessions — n to start one)`), so you can always land on
"here" and hit `n` to start one.

### Two Claude accounts (the pink `@`)

If you run a second Claude subscription alongside your main one, its sessions
live in a **different config dir** — `~/.claude-personal` — and, before this,
were invisible here: entire-tail only ever looked in `~/.claude`.

Both accounts are now scanned and **merged into the same tree**. Grouping is by
directory, not by account, so a repo you've worked in from both shows one group
holding both sets of sessions. Personal ones are marked with a **pink `@`**:

```
▾ dvydra/greenhouse  (3)  2h ago
    ● @ 892d905c  8m ago    [main] rewire the thermostat
    ○ @ 3dc641de  47m ago   [main] order the new sensors
    ○   b364d6d2  2d ago    [main] the work-account one
```

A group header gets the `@` when **every** session in it is personal, and a
dimmer `@` when it holds both — so a collapsed group never claims to be more
personal than it is. The marker also shows in `--list` (plain `@` when piped, so
it stays greppable) and the `i` card names the account.

**Filtering by account:** `/@` keeps only personal sessions. (It matches the `@`
you can see, not the word "personal" — otherwise filtering for `n` would drag in
every personal session.)

**Launching.** `⏎` on a personal session resumes it **as that account**:
entire-tail sets `CLAUDE_CONFIG_DIR` and pins the account's OAuth token for the
agent pane. `n` always starts a session on your main account; **`@`** starts one
on the personal account. The tail pane needs no account context — it watches both
config dirs, so it latches on either way.

The token is looked up from the **macOS Keychain by the launched shell**, in a
command substitution — it never passes through entire-tail's memory, never lands
in `argv` (where any process could read it via `ps`), and is never logged. If
it's missing, entire-tail prints a one-line warning and launches anyway (the
agent will just show its login screen).

None of this activates unless `~/.claude-personal/projects` exists: with one
account, entire-tail scans exactly the one root it always did.

**Setting the second account up** is out of scope for entire-tail — the short
version is `claude setup-token` with the personal account, store the token in the
Keychain under `claude-personal-token`, and point `CLAUDE_CONFIG_DIR` at
`~/.claude-personal`. (On macOS subscription logins live in the shared Keychain,
so two accounts using `/login` flip each other; a long-lived token is what keeps
them apart.)

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
`osascript` ships with the OS. The pane you launched from becomes the agent,
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

### Which agent pane A launches (`--claude-bin`)

Pane A runs plain **`claude`** by default. Any claude-compatible wrapper works
instead (a shim script, an absolute path, [`happy`](https://github.com/slopus/happy)
for mobile control) — a wrapper spawns the real Claude binary and its sessions
land in the same `~/.claude/projects/…` transcripts, so the tail in pane B reads
exactly the same thing either way.

```sh
entire-tail --claude-bin happy           # Claude Code with mobile control
export ENTIRE_TAIL_CLAUDE_BIN=happy      # ...as your default
```

If the named binary isn't on `PATH`, entire-tail falls back to `claude` —
silently for the built-in default, with a one-line warning when you asked for
something specific, so a typo isn't swallowed. The same preference picks the
agent that `entire-tail handover` launches.

**Why plain `claude` is the default.** happy held the job for one release and
lost it: `n` (a *fresh* session) came back already at its context limit —
"Context limit reached · /compact or /clear to continue" on the first turn —
because it resumes rather than starting clean. Which is the pinning problem
below, with teeth.

**How the id pinning differs per launcher.** `⏎` (resume) passes `--resume <id>`,
which wrappers generally forward — the session keeps appending to the same
`<id>.jsonl`, so the tail pins it exactly. `n` (fresh) passes `--session-id <id>`,
which plain `claude` honors, so both panes agree on one id up front. happy
**extracts that flag and doesn't forward it** (in the hook mode it runs
interactive sessions under, it only forwards `--resume`), so Claude mints its own
id. entire-tail therefore omits the id for any launcher that isn't plain `claude`
and has the tail pane use `--wait-new` instead — slightly racier, but it actually
finds the session.

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

## Handover docs

`entire-tail handover` turns a day's work into per-project handover notes in your
Obsidian vault — so tomorrow (or a teammate) can pick up any thread without
re-reading the transcripts.

```sh
entire-tail handover
```

It enumerates every Claude session with activity **today** and opens a grouping
picker:

```
[x] 656c39a3  entirehq/entiredb        184k  COR-562 CRDB cutover dry-run
[2] a1b2c3d4  entirehq/entiredb         42k  flaky CI on aurora monitors
[2] e5f6a7b8  entirehq/entiredb         31k  more CI: queue-wait alert
[ ] 9f62efce  entirehq/entiredb          8k  scratch / throwaway
```

- **`1`–`9`** tag a session into a group — sessions sharing a digit become **one**
  doc (e.g. tag all your CI sessions `2`).
- **`x`** (default) keeps a session on its own doc; **`-`** skips it; **⏎** writes;
  **`q`** aborts.

On confirm it launches an interactive agent (`--claude-bin`, `claude` by default —
a fresh iTerm window) that, for
each group, reads the transcripts, **live-fetches current state** — Linear issues
(MCP), GitHub PRs (`gh`), Entire Trails (`entire trail show`) — and writes one
Markdown doc per group to `Entire/Handover/YYYY-MM-DD/` in the vault. Each doc carries a
summary and where you left it, the session ids (with `claude --resume`), the
associated Entire sessions / Trails / PRs / Linear issues with their **current**
state, any ADRs or artifacts created, and — where states disagree (PR merged but
issue still open, Trail open but PR closed, …) — **recommended reconciliations**.

The writing is done by the **`handover-sessions` skill** (installed under
`~/.claude/skills/`; a reference copy lives in `docs/`), so you can tune the
prompt without rebuilding the binary. Set `ENTIRE_TAIL_HANDOVER_VAULT` to point at
a different vault root (default: the iCloud Obsidian Documents folder).

## Tool calls

By default, each tool call collapses to a **single colored dot**, and the streak
**rides the end of the agent's turn** as a bracketed group — a flurry of reads,
greps, and edits shows up appended to the last line of what the agent just said,
growing `[.]` → `[.....]`:

```
Reading the files, then I'll bump the version. [.....]
  ⋯ 14:03:11
Done — version is now 0.7.1.
```

rather than on a line of its own below it (which wasted a whole row for one or two
dots). The color encodes the kind: blue=read, green=edit, yellow=bash/exec,
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
… 29 more lines — press c to expand
```

(Piped output says `re-run with --no-collapse to expand` instead — there's no
keyboard on the other end of a pipe.)

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
to the terminal scrollback rather than running an alt-screen TUI. Pressing `c`
while following flips the setting and reprints the transcript with it applied —
already-printed lines stay where they are, the new copy is appended below them.
Expanding reprints the transcript **in full** rather than the last screenful
the other toggles redraw: the pastes it reveals are above the fold by
definition (the big one is usually the message that opened the session), so a
screenful would redraw a tail that already looked the same and the key would
read as dead.

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
- `install-tap.sh` / `disable-tap.sh` — turn the optional API tap on (KeepAlive
  LaunchAgent) and off again; see [the tap](#the-api-tap-see-the-questions-reasoning-not-just-the-question-opt-in)
- `e2e_tap.sh` — manual end-to-end check of the tap render path and the
  no-daemon fallback (`sh e2e_tap.sh`)
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

Which Claude *account* a session belongs to is a separate axis, owned by
`profile.go`: an ordered list of config dirs (`~/.claude`, plus
`~/.claude-personal` when it exists) that discovery, the tree, search and
auto-adopt all iterate instead of assuming one root — plus the pink `@` and the
env prefix that launches a session back under its own account.

The rendering state machine (`render.go`) is one path shared by backfill and
live: it tracks the previous participant (so consecutive same-participant turns
collapse to a dim `⋯ ts` marker) and the open-line/dot-streak state (a body
defers its trailing newline so a dots-mode streak rides the end of the turn as a
bracketed `[.....]` group), and renders each body through an in-process glamour
renderer.

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
- Prose is wrapped to your terminal width (one column short of it), so lines
  break between words — and only between words: a token too long for the line
  (a URL, a `--flag=value`) goes out whole and is soft-wrapped by the terminal,
  so it still pastes as one piece. Wrapped list items and block quotes indent
  their continuations under themselves; tables and rules are left alone.
  Resizing re-wraps: the transcript is re-rendered once the drag settles, and
  only when the width actually changed. Piped output is never wrapped.
- `--no-wrap` turns wrapping off for the session, and **`w`** toggles it live.
  Unwrapped, each paragraph goes out as one long logical line your terminal
  soft-wraps, which splits words at the column edge — but the terminal rejoins
  its own soft wraps on copy, so a mouse drag-select gives you unbroken
  paragraphs. There's no way to have both at once: any break entire-tail emits is
  a real newline your clipboard will keep. So the copy workflow is **press `w`,
  drag out the paragraph, press `w` again** — or skip it entirely, since `y` and
  `m` convert from the raw markdown rather than the screen and are unaffected by
  either setting.

## Live following

- **Claude / Codex** append to their jsonl, so the follower resumes from the
  byte offset where backfill ended and emits each new line.
- **Antigravity** rewrites the whole transcript on every step (atomic rename or
  truncate-in-place), so the follower re-reads the file on each change and
  dedups by `step_index` (seeded from the backfill snapshot).

### Following a Claude session across a fork

Claude Code mints a **new** `<id>.jsonl` (same project dir) when it re-enters a
worktree or when you `/clear` — the old file just stops. A plain tail would
freeze there. entire-tail instead keeps a *lineage* of the session ids it owns
and, once the current file falls quiet, adopts the sibling whose
`worktreeSession.sessionId` fork-pointer is in that lineage (matching the
explicit pointer, never "newest file", so a concurrent unrelated Claude in the
same repo is never picked up). At the flip it prints a two-line boundary naming
both ends so you can find either file on disk:

```
⟳ continued in <new-id>        ← tail of the old session
⟳ …continuing from <old-id>    ← head of the new session
```

That boundary lives only in the live window. Pass **`--mark-continuation`**
(off by default — entire-tail is otherwise strictly read-only on transcripts) to
*also* leave the forward pointer on disk: it appends one Claude-Code-native
`system`/`informational` record to the now-stopped file, so reopening that
session in Claude Code (`claude --resume <old-id>`) shows
`entire-tail · session continued in <new-id>`. It's the kind of record Claude
Code renders in the transcript but never feeds back to the model, so it's a
breadcrumb for you, not an instruction to Claude. Only the stopped file is
touched (the child is live and already records its own backward pointer), the
write is idempotent, and any failure is a silent no-op. Enable it persistently
with `ENTIRE_TAIL_MARK_CONTINUATION=1`.

## Tested against golden files

`go test` runs a golden-file suite (`testdata/*.golden`) over synthetic fixtures
— that's the rendering contract. The port *started* byte-identical to the
original bash implementation, but has since deliberately diverged (see the
improvements below), so the bash oracle (`entire-tail.bash`,
`RUN_ORACLE=1 go test`) is retired as a live parity gate and kept only for manual
A/B inspection.

Deliberate improvements over the bash version:

- **Full truecolor** bodies (the bash `glow` pipe capped them at 256 colors).
- **Dots ride the agent turn** — a `dots`-mode tool streak attaches to the end of
  the agent's last line as a bracketed `[.....]` group instead of a standalone row.
- **Claude questions / subagent spawns** render as a bordered card / a
  `⏺ ▸ agent:` marker instead of the bash oracle's raw markdown.
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
