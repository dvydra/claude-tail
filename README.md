# claude-tail

Pretty live-view of your current Claude Code session.

## Why

Claude Code renders its TUI directly into the main terminal buffer with cursor
moves (it does not use the alt-screen). The terminal scrollback captures every
partial repaint — so scrolling up to re-read a long conversation gives you
overlapping fragments of UI chrome, not a clean history. Claude Code also has
no in-app scroll keybind, no `/history`, no `/transcript`. Once a message has
left the visible viewport, there's no good way to read it back without exiting.

But the source of truth is on disk: every event in your session is appended
line-by-line to a JSONL file at `~/.claude/projects/<encoded-cwd>/<session>.jsonl`.

`claude-tail` follows that file, formats each event nicely, and renders the
markdown bodies through [glow](https://github.com/charmbracelet/glow) using a
custom flush-left style.

Open it in a separate Zellij pane next to Claude. New messages stream in.
Scroll back through the pane buffer to read clean, full-width markdown of the
session.

## Install

```sh
./install.sh   # symlinks claude-tail into ~/.local/bin/
```

The script resolves its own symlink to find the `themes/` directory next to
itself, so the symlink works fine — you can keep editing files here and the
installed command picks up changes immediately.

Requires `jq`, `glow`, `tail`, `base64` on `$PATH`. On macOS:

```sh
brew install glow jq
```

## Usage

```sh
claude-tail                                # follow the latest session for $PWD
claude-tail /path/to/session.jsonl         # follow a specific session
claude-tail --theme dracula                # pick a bundled theme (default: tokyo-night)
claude-tail -t nord -b 50                  # short flags also work
claude-tail --no-backfill                  # skip history, only follow new events
claude-tail --list-themes                  # see what's available
claude-tail --help                         # full options
```

All flags also have env-var equivalents (`CLAUDE_TAIL_THEME`, `CLAUDE_TAIL_BACKFILL`,
`GLOW_STYLE`) for shell-rc convenience — flags override env vars when both are set.

## Themes

Bundled dark IDE themes (run `claude-tail --list-themes` to see them with
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

## Files

- `claude-tail` — the script
- `themes/<name>.{json,sh}` — bundled themes (see Themes section above)
- `install.sh` — symlinks `claude-tail` into `~/.local/bin/`

## Notes

- The script defaults to the most recently modified jsonl for the **current
  directory** (Claude encodes `$PWD` by replacing `/` with `-` to name the
  project folder under `~/.claude/projects/`). If no session exists for the
  cwd yet, it falls back to the global latest with a stderr note. Pass an
  explicit path to pin a particular session.
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
- `thinking` blocks are skipped entirely.
- `tool_result` blocks are summarized as `↩ tool_result (×N)`.
- `glow -w 0` disables glow's own word wrap. Each markdown paragraph is one
  logical line in glow's output; your terminal soft-wraps it to whatever pane
  width you have, which means resizing the pane re-flows the text naturally
  on the next render.

## Format pipeline

```
backfill: sed -n M,Np session.jsonl | jq → concatenated markdown | glow (one call)
live:     tail -F -n +N+1 session.jsonl | jq → one base64 line per event
            | bash loop → ANSI header + glow render per message
```

Base64 in the live-mode middle stage keeps multi-line markdown bodies intact
across the shell pipeline without escaping/quoting headaches.

## Caveats / known weirdness

- Chroma (glow's code-block syntax highlighter) is strict about hex colors —
  all 6-char hex values in each theme JSON are prefixed with `#`. If you fork
  a theme, keep that prefix or glow will panic on the first code block.
- The script reads the session file from disk — there's a tiny delay between
  Claude emitting an event and the line appearing here (usually <100ms,
  whatever the OS flushes the append at).
- If Claude is mid-stream on a long assistant message, the partial text won't
  show up until the message completes and gets written as a final event.
  This is a session-log limitation, not something the script can work around.
