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
a reference oracle). The Go port started **byte-identical** (ANSI-stripped) to the
bash output for Claude/Codex/agy in `dots` and `none` modes; it has since
deliberately diverged (dots ride the agent turn; Claude questions/subagent spawns
render as cards/markers — see below), so the **committed goldens are the parity
contract now**, not the bash oracle (`RUN_ORACLE`/`TestEquivalenceVsBash` is
retired to skips). The rewrite fixed two things bash couldn't: full truecolor
bodies (bash piped `glow`, capped at 256 colors) and a multi-second autodetect
latency (per-file codex rollout scans). See the `CT-2` plan in agent-planner for
the full investigation/decision.

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
(`Kind` = USER | ASSISTANT | TOOLUSE | TOOLRESULT | AGENTSPAWN | QUESTION).
Everything downstream is agent-agnostic and consumes only `Record`s.

- `adapter_claude.go` / `adapter_codex.go` / `adapter_agy.go` / `adapter_entire.go` — `normalize(line) []Record`
  (`adapter_entire.go` handles entire's own transcript format — top-level
  `content`/`ts` — used for reconstructed cloud-only sessions)
- `reconstruct.go` — recovers a cloud-only session's transcript (not under
  `~/.claude`) from its repo's local `refs/entire/checkpoints/**` git objects
  (`git grep` the session id → largest `transcript.jsonl` → temp file), so search
  hits from pruned/other-machine sessions stay tailable when the repo is local
- `adapter.go` — the `Record`/`Kind` types and the adapter interface
- `discovery.go` — find the session file for `$PWD` per agent
- `tree.go` — the interactive session **tree** picker (the DEFAULT): sessions
  grouped by repo/folder, arrow-key navigable, recency-colored, type-to-filter;
  also the static `--list` dump. Pure build/reduce/render split from a thin tty
  driver (alt-screen + `setRaw`), so navigation/render are unit-tested without a tty
- `entire.go` — builds the DEFAULT tree, tuned to stay instant + local:
  `buildSessionTree` takes the complete local `~/.claude` crawl as the base and
  `mergeEntire` regroups it by repo via each cwd's git `origin` remote (for
  entire repos the remote is `entire://…/owner/repo` → same `owner/repo` the
  cloud uses). Cloud metadata (`entire api /me/sessions`: generated titles +
  cross-machine sessions) is **opt-in via `--cloud`** and disk-cached (~10 min,
  `cachedEntireSessions`), so the default never blocks on the network — it only
  reads a warm cache. `--local` skips git+cloud (pure folder-grouped crawl).
  `loadClaudeMeta` reads only each session's head (early-out) to keep the crawl
  cheap regardless of transcript size. `buildSessionTree`/`mergeEntire` always
  surface the **current directory's group even with zero sessions**
  (`ensureCurrentDirFolder` / the `curRepo` inject, `Dir`=cwd, `Mtime`=now) so it's
  always visible and `n`-able from the picker; `composeFolderRow` renders an empty
  group as `▸ path  (no sessions — n to start one)`
- `picker.go` — picker glue: live-cwd detection (`pgrep`+`lsof`, optional) for
  the `--local` view's live markers, plus `runPicker`/`resolveTreeChoice`. The
  tree is the DEFAULT entry point (bare `entire-tail` on a tty); `--no-pick` /
  piped runs / explicit SESSION_FILE skip it and tail directly
- `iterm.go` — macOS/iTerm2 automation via `osascript`: the tree's `Enter`
  opens the 3-pane workspace (`claude --resume` + live tail + shell) in the
  CURRENT window, cd'd to the picked session's folder; `n` opens the same
  workspace for a FRESH `claude` session in `$PWD` (new window if the current
  one is already split, since there's nothing to tail in place). Pure `workspaceScript`
  builder split from the `osaRun` executor so quoting/layout are unit-tested
  without launching iTerm. The queued-claude trick: the command is written to
  the current pane's tty and runs once entire-tail exits. Both panes **pin a
  shared session id** — fresh: `claude --session-id <id>` + `entire-tail
  --follow-session <id>`; resume: `claude --resume <id>` + `--follow-session
  <id>` — so the tail latches onto exactly that session even with other Claude
  sessions live in the same repo (replaces the racy `--wait-new` newest-file
  heuristic; `newSessionID` mints a v4 UUID via crypto/rand)
- `search.go` — `--search`: content search across local transcripts (ripgrep,
  literal) + `entire checkpoint search` (semantic session results), merged by
  session id and ranked (`searchHit.score`: exact local match dominates, entire
  score adds, recency tiebreak). Builds a single-group ranked `sessionTree`
  (reuses the same TUI/`renderList`); rows show the match snippet, capped at 50
- `preview.go` — the tree's `i` **combined info view** (`showInfo`): a fixed info
  card on top, a divider, and the session's recent transcript in a scrollable
  pane below (`pagerSplit`; `splitPaneHeights` divides the rows, reserving
  `minPreviewRows` so the CARD is what clips — its path/last-updated stay
  visible). The card body is the pure `summaryCardLines` (unit-tested without a
  tty): optional AI summary, then entire's metadata
  (repo/model/tokens/activity/**updated**/**path**), then a capped **trails & prs**
  section (`extractLinks` greps the transcript for `entire.io/gh/o/r/trails/id`
  and `github.com/o/r/pull/n` URLs, rendered as `osc8` clickable hyperlinks —
  metadata comes first so it survives clipping). `truncVisible` is OSC-8-aware so
  hyperlinks survive truncation. Runs inside the alt-screen, returns on q/Esc.
  Token totals (`formatTokens`) also show in tree rows + `--list`. (There's no
  separate `p` preview anymore — it folded into `i`.)
- `aisummary.go` — on-device AI summary for the `i` card via Apple's built-in
  Foundation Models CLI (`fm`, `/usr/bin/fm`, macOS 26+): `fm respond --model
  system --no-stream --schema <file> -i <instr>` with the transcript on stdin →
  structured {headline, summary, keyPoints, outcome} JSON. The schema needs fm's
  `title`+`x-order` keys (a bare JSON Schema is rejected). Always the on-device
  `system` model (no PCC). `transcriptText`/`sampleTurns` clean + head/tail-sample
  the transcript to fit the context. `fm` absent/unavailable → card is
  metadata-only (no build-time dependency)
- `render.go` — the **rendering state machine** (one path shared by backfill +
  live): tracks previous participant (consecutive same-participant turns collapse
  to a dim `⋯ ts` marker) and `lineOpen`/dot-streak state; tool tristate lives here.
  A body **defers its trailing newline** (`lineOpen`) so a following dots-mode tool
  streak **rides the end of the agent turn** as a bracketed group (` [.]` → ` [.....]`)
  instead of a standalone line; the first dot opens the `[`, `endLine()` writes the
  closing `]` + the owed newline before the next header/marker/block. Backfill and
  live leave the last line open (streaming); the quit path (and preview/focus) flush it
- `toolresult.go` — parse Claude `toolUseResult` into diffs / output / read-summary
- `tail.go` — follow loop (byte-offset resume for claude/codex; whole-file
  re-read + `step_index` dedup for agy)
- `lineage.go` — **follows a Claude session across a worktree fork.** Claude Code
  mints a NEW session id (new `<id>.jsonl`, same project dir) on a worktree
  re-enter; the fresh file's `worktree-state` record carries the id it forked
  FROM (`worktreeSession.sessionId`). Without this, a tail latched on the old
  file freezes at the fork (the "drift-noise orphaning"). `tailSession` keeps a
  **lineage set** and, once the current file goes quiet (`rolloverIdleTicks`),
  adopts a sibling whose `forkPointer` is in that set (`lineageChild`) — matching
  the explicit pointer, NOT "newest file", so a concurrent unrelated Claude in
  the same repo is never adopted. Rollover prints a two-line boundary naming both
  ids — `⟳ continued in <new-id>` (tail of the old session) then `⟳ …continuing
  from <old-id>` (head of the new) — because on disk the old file just stops with
  no forward pointer, so the printed ids are the only way to find the
  continuation; then streams the child from its start. `cur` (mutable) replaces the immutable
  `session` param inside the live loop's poll/reload/rollover closures. **A
  `/clear` is followed for free by this same path** — verified live, it mints a
  new `<id>.jsonl` whose `worktreeSession.sessionId` is the pre-clear session
  (same field as a worktree re-enter), so `forkPointer`/`lineageChild` adopt it
  with no `/clear`-specific code (`TestForkPointerClear`)
- `subagents.go` — discovers a Claude session's subagent transcripts
  (`<transcript>/<sessionId>/subagents/agent-*.jsonl` + `.meta.json`), ordered by
  spawn time, with best-effort running/done + duration from each file's timespan.
  Subagent files are standard Claude JSONL, so the normal renderer handles them
- `focus.go` — the `→` focus overlay: an alt-screen live view over the selected
  subagent, `←/→` to cycle channels, `↑↓`/PgUp/PgDn scroll, `r` reload, `q`/Esc
  back. Reuses the renderer (dots) to format the subagent; follows via a timed
  raw read (`setRawTimed`, MIN 0 TIME 5) so it re-reads the file between
  keystrokes. Runs on the render goroutine while the keyboard goroutine is parked
  on `resumeCh`, sharing the SAME tty fd (two fds on one tty race for input).
  **Gotcha:** a raw timed read reports a 0-byte timeout as `(0, io.EOF)` — treat
  that as a follow tick, not end-of-input, or the overlay exits instantly
- `theme.go` / `config.go` / `main.go` — themes, flags+env, wiring
- `keyboard.go` — live single-key toggles via cbreak (`t`/`c`/`r`/`q`), plus `→`
  which signals the render goroutine to run the focus overlay and parks until it
  returns, and `Ctrl-X` (0x18) which signals `treeCh` and STOPS reading so
  `tailSession` returns and `run`'s picker↔tail loop re-enters the tree
  (Claude-only, gated by `treeEnabled`; a no-op on codex/agy). Returns the tty fd
  so the overlay reuses it (single reader)
- `jqutil.go` — tiny JSON-value-to-string helpers (replaces shelling out to `jq`)
- `handover.go` — the `entire-tail handover` subcommand: `todaysSessions`
  enumerates this machine's Claude sessions active since local midnight
  (`flattenToday` over a 2-day `buildClaudeTree` crawl), the user groups them,
  then it writes a JSON manifest (`buildManifest`, group-oriented so the skill
  does zero grouping judgement — link seeds come from `extractLinks`) and launches
  an interactive `claude` (`handoverScript`, a fresh iTerm window) preloaded to
  invoke the **`handover-sessions` skill** at the manifest path. The skill (installed
  at `~/.claude/skills/handover-sessions/`, vendored copy in `docs/`) reads the
  transcripts, live-fetches Linear (MCP) / GitHub (`gh`) / Entire (`entire trail
  show`) state, reconciles mismatches, and writes one Obsidian doc per group to
  `$ENTIRE_TAIL_HANDOVER_VAULT/Entire/Handover/YYYY-MM-DD/` (default: the iCloud vault).
  Pure parts (`localMidnight`, `flattenToday`, `manifestSessionFrom`,
  `buildManifest`, `handoverVaultDir`) are unit-tested.
- `handover_picker.go` — the grouping-picker: a flat list of today's sessions the
  user tags into groups (`1`-`9` merge, `x` separate/default, `-` skip, ⏎ write,
  `q` abort). Pure `updateHandoverPick` reducer + `renderHandoverPick` + the
  `buildGroups` collapse, split from the tty driver `runHandoverPicker` — same
  reduce/render/driver split as `tree.go`.

Adding a new agent = write a `normalize` + a discovery function. Nothing else
needs to change.

## Things that are load-bearing (don't "clean up" without care)

- **The committed goldens are the rendering contract** (`testdata/*.golden`, via
  `TestGolden`). The box-header dash counts (`render.go`
  `userHdrBody`/`claudeHdrBody`), blank-line squeezing, and dot coloring are all
  load-bearing. Changing rendered bytes changes the goldens — regenerate them
  deliberately (`UPDATE_GOLDEN=1 go test -run TestGolden ./...`) and eyeball the
  diff, never blindly. (Bash byte-parity is no longer the contract — dots ride the
  agent turn, so the Go output intentionally diverges; see the divergence note.)
- **Tool rendering is a tristate** (`toolStyleKind`: `full`/`dots`/`hidden`),
  stored as an `atomic.Int32` so the keyboard goroutine can flip it live without
  racing the render goroutine. Same for the collapse threshold. The live loop is
  a single `select` on the render goroutine — keyboard only *signals*; it never
  renders (that's what keeps `-race` clean). Keep it that way.
- **`full` mode flattens Bash commands** to one line (newlines→spaces) and
  truncates to 120 runes — that's deliberate: long/badly-indented commands stay
  one predictable line instead of wrapping into scrollback garbage. Command
  output under `⎿` is shown in **full** (no truncation — "full means full").
- **Dots ride the agent turn (intentional divergence).** In `dots` mode the tool
  dots attach to the end of the agent's text line as a bracketed group (` [.]`
  growing to ` [.....]`) rather than a standalone line below it — short streaks no
  longer cost a whole extra row before the `⋯` marker. Mechanism: `body()` defers
  its final newline (`lineOpen`); the first dot of a streak opens a dim `[` (with a
  one-space join to an open body line; a fresh streak on an empty body starts the
  line, no leading space); `endLine()` writes the dim closing `]` + the owed newline
  before the next header/marker/block. The `*_dots` goldens were regenerated for
  this. Buffered renderers that `TrimRight` their output (`preview.go`, `focus.go`)
  must call `endLine()` after their emit loop or the trailing `]` is lost.
- **Subagent spawns + questions render Claude-only, and intentionally diverge
  from the bash oracle.** `AskUserQuestion` renders as a bold bordered card (+ a
  one-shot bell, live only, deduped per question id via `seenQuestions`) and
  `Agent`/`Task` as a `⏺ ▸ agent:` marker — replacing the old markdown question
  the oracle emits. Both are always shown regardless of tool style — they're
  orchestration, not routine tool calls. Together with the dots divergence above,
  the Go renderer no longer matches the bash oracle in any mode, so
  `TestEquivalenceVsBash` (`RUN_ORACLE=1`) is retired to skips; the goldens +
  units are the gate.
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
