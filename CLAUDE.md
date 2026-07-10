# CLAUDE.md — entire-tail

## What this is

`entire-tail` is a **live pretty-viewer for AI coding-agent sessions** — Claude
Code, Codex CLI, and Antigravity (agy). Agents render their TUIs straight into
the terminal with cursor moves (no alt-screen), so scrollback is a mess of
partial repaints and there's no `/transcript`. But every event is appended to a
JSONL file on disk. entire-tail discovers that file for the agent you're using,
follows it (`tail -F`-style), and renders each turn — markdown bodies through
in-process [glamour](https://github.com/charmbracelet/glamour), tool calls as
colored dots or Claude-style `⏺/⎿` lines. Run it in a second pane next to the
agent.

It ships as an [`entire`](https://docs.entire.io) plugin (`entire tail`) and
also runs standalone (`entire-tail`).

**This is a Go rewrite of an original bash script** (`entire-tail.bash`, kept as
a reference oracle). The Go port is validated **byte-identical** (ANSI-stripped)
to the bash output for Claude/Codex/agy in `dots` and `none` modes. The rewrite
fixed two things bash couldn't: full truecolor bodies (bash piped `glow`, capped
at 256 colors) and a multi-second autodetect latency (per-file codex rollout
scans). See the `CT-2` plan in agent-planner for the full investigation/decision.

## Build / test / install

```sh
go build -o entire-tail .        # build
go test ./...                    # unit + golden-file suite (no external deps)
RUN_ORACLE=1 go test ./...       # ALSO diff Go output vs entire-tail.bash (needs bash/jq/glow)
go vet ./... && go test -race ./...   # what CI/the review gate expects
./install.sh                     # build + symlink ~/.local/bin + register entire plugin
```

After editing source **or** a theme, rebuild (`go build -o entire-tail .` or
`./install.sh`) — themes are **embedded into the binary** at build time.

Quick manual render of a fixture (backfill then exit):
`timeout 2 ./entire-tail --agent claude --no-pick --tool-style full testdata/claude_session.jsonl`

## Architecture (single `package main`, ~10 files)

Per-agent **adapters** lower each JSONL event to a canonical `Record`
(`Kind` = USER | ASSISTANT | TOOLUSE | TOOLRESULT). Everything downstream is
agent-agnostic and consumes only `Record`s.

- `adapter_claude.go` / `adapter_codex.go` / `adapter_agy.go` — `normalize(line) []Record`
- `adapter.go` — the `Record`/`Kind` types and the adapter interface
- `discovery.go` — find the session file for `$PWD` per agent
- `tree.go` — the interactive session **tree** picker (the DEFAULT): sessions
  grouped by repo/folder, arrow-key navigable, recency-colored, type-to-filter;
  also the static `--list` dump. Pure build/reduce/render split from a thin tty
  driver (alt-screen + `setRaw`), so navigation/render are unit-tested without a tty
- `entire.go` — the DEFAULT tree source: the `entire` CLI's cloud inventory
  (`entire api /me/sessions`, grouped by repo, generated titles, no local file
  reads). `buildSessionTree` dispatches: entire when available, else (or with
  `--local`) the `~/.claude` crawl in `tree.go`. A session's uuid is resolved to
  its local jsonl by a name-only glob so it stays tailable/resumable
- `picker.go` — picker glue: live-cwd detection (`pgrep`+`lsof`, optional) for
  the `--local` view's live markers, plus `runPicker`/`resolveTreeChoice`. The
  tree is the DEFAULT entry point (bare `entire-tail` on a tty); `--no-pick` /
  piped runs / explicit SESSION_FILE skip it and tail directly
- `iterm.go` — macOS/iTerm2 automation via `osascript`: the tree's `Enter`
  opens the 3-pane workspace (`claude --resume` + live tail + shell) in the
  CURRENT window, cd'd to the picked session's folder. Pure `workspaceScript`
  builder split from the `osaRun` executor so quoting/layout are unit-tested
  without launching iTerm. The queued-claude trick: the command is written to
  the current pane's tty and runs once entire-tail exits
- `render.go` — the **rendering state machine** (one path shared by backfill +
  live): tracks previous participant (consecutive same-participant turns collapse
  to a dim `⋯ ts` marker) and dot-streak state; tool tristate lives here
- `toolresult.go` — parse Claude `toolUseResult` into diffs / output / read-summary
- `tail.go` — follow loop (byte-offset resume for claude/codex; whole-file
  re-read + `step_index` dedup for agy)
- `theme.go` / `config.go` / `main.go` — themes, flags+env, wiring
- `keyboard.go` — live single-key toggles via cbreak (`t`/`c`/`r`/`q`)
- `jqutil.go` — tiny JSON-value-to-string helpers (replaces shelling out to `jq`)

Adding a new agent = write a `normalize` + a discovery function. Nothing else
needs to change.

## Things that are load-bearing (don't "clean up" without care)

- **Byte-for-byte bash parity** is a tested contract. The box-header dash counts
  (`render.go` `userHdrBody`/`claudeHdrBody`), blank-line squeezing, and dot
  coloring all match the bash oracle on purpose. Changing rendered bytes can
  break `equiv_test.go` / the goldens in `testdata/*.golden`. Regenerate goldens
  deliberately, never blindly.
- **Tool rendering is a tristate** (`toolStyleKind`: `full`/`dots`/`hidden`),
  stored as an `atomic.Int32` so the keyboard goroutine can flip it live without
  racing the render goroutine. Same for the collapse threshold. The live loop is
  a single `select` on the render goroutine — keyboard only *signals*; it never
  renders (that's what keeps `-race` clean). Keep it that way.
- **`full` mode flattens Bash commands** to one line (newlines→spaces) and
  truncates to 120 runes — that's deliberate: long/badly-indented commands stay
  one predictable line instead of wrapping into scrollback garbage. Command
  output under `⎿` is shown in **full** (no truncation — "full means full").
- **Word wrap is off** (`glamour.WithWordWrap(0)`): each paragraph is one logical
  line the terminal soft-wraps, so resizing reflows on the next render. Don't
  re-enable wrap.
- Themes are pairs under `themes/<name>.{json,sh}`, embedded via `go:embed`. The
  `.json` is the glamour style; the `.sh` holds `THEME_*_ANSI` box/timestamp
  colors (parsed directly — we do **not** shell out to bash). Chroma is strict:
  every 6-char hex in the JSON must be `#`-prefixed.

## Conventions

- Tests are non-negotiable: golden files for render output + unit tests for pure
  functions. Run the full suite (and `-race`) before presenting.
- **No runtime dependencies** beyond the binary. The picker *optionally* uses
  `pgrep`+`lsof` if present; never make them required.
- Reasoning/"thinking" blocks are intentionally skipped for every agent.
- Per the global workflow: fresh branch per change, PR with thorough description,
  code review + security review before merge. Don't push straight to `main`.

## Pointers

- `README.md` — the user-facing manual (flags, themes, picker, tool styles).
- `entire-tail.bash` — the original; the oracle for parity tests, not shipped.
- `testdata/` — synthetic fixtures + `*.golden` expected output.
- agent-planner plan **CT-2** ("Investigate rewriting from bash to golang") —
  the rewrite investigation, the GO decision, and per-PR notes.
