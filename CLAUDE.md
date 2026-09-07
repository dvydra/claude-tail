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
- `profile.go` — **which Claude ACCOUNT a session belongs to.** A second Claude
  subscription can't just `/login` on macOS (subscription logins live in the
  shared Keychain, so the last login flips every session, running ones
  included); it's pinned with a long-lived `claude setup-token` OAuth token and
  its own `CLAUDE_CONFIG_DIR` — by convention `~/.claude-personal`. Since Claude
  Code stores transcripts under `$CLAUDE_CONFIG_DIR/projects`, that's a SECOND
  parallel projects tree everything here used to be blind to. `claudeProfiles`
  returns the ordered roots (**default first — that order is the tiebreak** when
  two roots offer an equally good match, preserving pre-profiles behaviour), and
  the personal one appears **only when its `projects/` dir exists**, so a
  single-account machine does exactly the work it always did. Everything that
  used to derive a root from `home` now iterates `claudeProjectsRoots`:
  `findSessionClaude` (per *tier*, so an exact-cwd hit in one account beats a
  same-tree guess in the other), `detectAgentForFile`, `buildClaudeTree`,
  `localCandidates`/`localPathForID`, `resolveClaudeSession` (auto-adopt),
  `waitForNewSession`/`waitForSessionFile`, `localRepoDirs`. The one that must
  NOT is relocation: `relocatedSession` takes `projectsRootOf(cur)` — the root is
  already encoded in the path we're following, and re-deriving it from `home`
  would silently never find a personal session's worktree hop. Tree grouping is
  by **cwd, not cwd-and-account** (`buildClaudeTree` pools by slug across roots,
  then `sortSessions` — concatenating two newest-first lists isn't newest-first,
  and `sessions[0]` feeds `folder.Mtime`/`Cwd`/`Dir`). Marker: pink `@` per
  session (`profileMark`, a fixed 2-col cell so ids stay aligned), and on folder
  headers via `folderProfile` — `@` only when EVERY session is personal, dim `@`
  when mixed, nothing for work-only (folder rows reserve no cell, so work rows
  are unchanged). Both take a `restore` ANSI because `styleRow` colors the whole
  row — without handing the tier color back, everything after the `@` renders
  pink to EOL; `truncVisible` already skips CSI escapes uncounted, so truncation
  is safe. Launch: `accountEnvPrefix` prepends `CLAUDE_CONFIG_DIR` +
  `CLAUDE_CODE_OAUTH_TOKEN="$(security find-generic-password …)"` to pane A only
  (`workspaceScript`/`newWorkspaceScript` gained an `acctEnv` param, `""` for the
  default account → byte-identical launch). **Both halves are load-bearing:**
  `CLAUDE_CONFIG_DIR` alone moves the transcripts but not the credentials (the
  Keychain login still decides the account, so a "personal" resume quietly runs
  as work), and the token alone authenticates the right account into the wrong
  projects tree. The token is a **shell** command substitution on purpose — it
  never enters our memory, argv (`ps`-visible), or a log; the double quotes are
  required. Pane B needs no account context — it watches every root, which is
  also why `n`/`@` need no extra flag. Keys: `n` = default account (unchanged),
  `@` = personal (a separate key, not inference from the cursor: "which account
  am I starting as" is not a thing to guess). `/@` filters by account, matching
  the visible marker rather than the word "personal" — substring-matching that
  word would make a filter of `n` or `e` drag in every personal session
- `discovery.go` — find the session file for `$PWD` per agent
- `tree.go` — the interactive session **tree** picker (the DEFAULT): sessions
  grouped by repo/folder, arrow-key navigable, recency-colored, type-to-filter
  (`/` matches name/title/id/branch AND recent transcript content — each
  session's newest ~8KB of message text, extracted by `extractTailContent` from
  the tail window `loadClaudeMeta` already reads, so the filter costs no extra
  I/O);
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
- `adopt.go` — **auto-adopts the Claude in the sibling iTerm pane.** A bare
  `entire-tail` (no `--follow-session`/positional/search) run in a pane beside a
  `claude` tails THAT session with no flags, before falling to the tree. The
  session id isn't interrogable from a bare `claude` — it's absent from argv,
  env (no `CLAUDE_*` vars), and open files (the transcript is open-append-closed,
  never held; verified live) — so we pin the *process* and resolve its file. The
  process is matched by **iTerm tab**: every terminal carries `ITERM_SESSION_ID`
  ("wNtNpM:UUID") in its env, readable via `ps eww`; `itermTab` strips it to the
  `wNtN` window+tab prefix and `siblingPIDs` keeps the `claude`s sharing OUR tab
  (so one in another tab/window is never grabbed). Adopt only fires when the tab
  holds **exactly one** claude (`adoptPaneSession`); zero/many → fall through.
  `resolveClaudeSession` then locates the file: an id on the claude's command
  line (`scrapeSessionIDArg`: `--session-id`/`--resume`) pins it exactly, else the
  actively-written `.jsonl` in its cwd's project dir wins (`liveSessionInDir` →
  `newestClear`, with a brief mtime re-sample `activePick` only when two sessions
  are near-simultaneous). On adopt, `run` re-bases `pwd` onto the adopted claude's
  cwd (`lsofCwd`) so the cwd-mismatch note and Ctrl-X tree reflect what's watched.
  Self-disables off iTerm / without `pgrep`+`lsof`. Pure parsers (`itermTab`,
  `parsePsEnv`, `scrapeSessionIDArg`, `siblingPIDs`, `newestClear`) are
  unit-tested; the `ps`/`lsof` shell-outs are the thin IO layer
- `iterm.go` — macOS/iTerm2 automation via `osascript`: the tree's `Enter`
  opens the 3-pane workspace (`<bin> --resume` + live tail + shell) in the
  CURRENT window, cd'd to the picked session's folder; `n` opens the same
  workspace for a FRESH session in `$PWD` (new window if the current
  one is already split, since there's nothing to tail in place). Pure `workspaceScript`
  builder split from the `osaRun` executor so quoting/layout are unit-tested
  without launching iTerm. The queued-agent trick: the command is written to
  the current pane's tty and runs once entire-tail exits. Both panes **pin a
  shared session id** — fresh: `<bin> --session-id <id>` + `entire-tail
  --follow-session <id>`; resume: `<bin> --resume <id>` + `--follow-session
  <id>` — so the tail latches onto exactly that session even with other Claude
  sessions live in the same repo (replaces the racy `--wait-new` newest-file
  heuristic; `newSessionID` mints a v4 UUID via crypto/rand) — **but only for a
  launcher that forwards `--session-id`; see `pinsSessionID`**. `<bin>` is
  `resolveClaudeBin` (config.go): **plain `claude` by default** (`--claude-bin` /
  `ENTIRE_TAIL_CLAUDE_BIN` to pick another), falling back to `claude` when the
  named binary isn't on PATH — silently for the built-in default, with a warning
  when the choice was explicit (`ClaudeBinSet`), so a typo isn't swallowed. Any
  such launcher is a *wrapper*, not a different agent: it spawns the real Claude
  binary and its sessions land in the same
  `~/.claude/projects/<slug>/<id>.jsonl`, so discovery/lineage/pending-hooks and
  every golden are untouched. Auto-adopt needs no wrapper-specific code either —
  `pgrep -x claude` matches the spawned `claude`, and a wrapper that passes
  `--resume=<uuid>` hits the equals form `scrapeSessionIDArg` already handles. The
  same preference picks the agent `handover` execs (`launchClaude`).
  **happy was the default for one release (#47) and lost the job:** on a fresh
  `n` workspace it handed back a session already at its context limit — first
  turn dead with "Context limit reached · /compact or /clear to continue". Root
  cause is the trap below: it drops `--session-id` and its spawn only pushes
  `--resume`, so "new session here" silently became "resume something". Reverted
  to `claude` in #49, which also restores the pinned-id contract (`pinsSessionID`
  is true again, so the fresh workspace pins instead of racing `--wait-new`).
  Don't re-promote a wrapper to the default without checking that a fresh
  workspace actually yields a FRESH session.
  **The `--session-id` trap (cost us a broken `n` workspace twice):** happy
  forwards `--resume` but *extracts and drops* `--session-id` — in the hook mode
  it runs interactive sessions under, the spawn only ever pushes `--resume`
  (`dist/index-*.mjs`). So `happy --session-id X` makes Claude mint its OWN id,
  `X.jsonl` never appears, and a `--follow-session X` pane waits forever. Hence
  `pinsSessionID` (iterm.go) gates the pin on `filepath.Base(bin) == "claude"`
  and the fresh workspace falls back to `--wait-new` for everything else.
  Conservative on purpose: a wrapper that *does* forward the flag merely loses the
  pin, while wrongly assuming support strands the tail. Do NOT "restore" the pin
  for wrappers without verifying the spawned claude's argv (`pgrep -x claude` +
  `ps -o args=`), not the wrapper's own argv — the wrapper accepting a flag says
  nothing about whether it passes it on
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
  with no `/clear`-specific code (`TestForkPointerClear`). The live divider only
  names the flip in *this* window; **`--mark-continuation`** (opt-in, off by
  default — entire-tail is otherwise strictly read-only on transcripts) also
  leaves the pointer on disk: `markContinuation` appends a Claude-Code-native
  `system`/`informational` record to the now-stopped file, so reopening that
  session in Claude Code shows `entire-tail · session continued in <new-id>`.
  Only the STOPPED side is written — the child is live (Claude is mid-write) and
  already carries the backward `worktreeSession` pointer. The record type is one
  Claude Code renders in the transcript UI but never replays to the model, so it
  shows on resume without steering Claude; it chains off the last uuid-bearing
  record (parentUuid, so it renders as the thread leaf), copies that record's
  cwd/version/gitBranch/sessionId, is idempotent (skips if the tail already
  names `<new-id>`), and is best-effort (any failure is a silent no-op — a failed
  annotation must never disrupt the tail). `TestMarkContinuation`. A worktree
  *cwd switch* (the harness `EnterWorktree`/`ExitWorktree`, not a `claude`
  re-enter) is a different shape: Claude Code keeps the SAME id but moves the
  whole `<id>.jsonl` into the project dir matching the new cwd. `forkPointer` can't
  see it (no new id; `worktreeSession.sessionId` is the session's own id), so
  rollover falls back to `relocatedSession` — the newest same-id file under
  another project dir, adopted only once its size has caught up to our byte offset
  (so a mid-write candidate can't trip appendStep's truncation reset). The offset
  is KEPT across the hop (identical content prefix → stream only the continuation,
  no re-render); the divider is `⟳ following session into <dir>`
  (`TestRelocatedSession`)
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
- `help.go` — the `?` help modal: a centered bordered box in an alt-screen
  showing the startup banner's context plus the full key map and the dot legend.
  Same hand-off as `focus.go` (keyboard signals `helpCh` and parks on `resumeCh`;
  the render goroutine draws on the SAME tty fd), so there's one tty reader. The
  state is sampled when `?` is pressed, not at startup — `t`/`T`/`c` move it, and
  a modal that echoed the stale banner would be worse than no modal. Pure
  `helpLines`/`drawHelp`/`visWidth`/`padVisible` split from the tty driver
  `runHelp`, so content and box geometry are unit-tested without a tty
- `mrkdwn.go` / `yank.go` — **getting text out into Slack.** `y` copies the last
  agent turn as Slack mrkdwn (repeat within `yankExtendWindow` extends backwards
  a turn at a time); `m` renders agent bodies as mrkdwn source on screen so a
  mouse drag-select is already mrkdwn. Both convert from the transcript's RAW
  markdown, which is the whole point — the screen text has glamour's wrapping,
  indentation and ANSI baked in, so a screen scrape can never be as clean.
  The yank buffer lives on the `Renderer` (`yankTurns`, one entry per agent turn,
  closed by a user message) and is touched ONLY on the render goroutine — the
  keyboard signals `yankCh`, which is buffered rather than coalesced because a
  second `y` MEANS "one more turn" and a dropped press would copy the wrong
  thing. `reset()` clears it: a reload/theme-swap/rollover re-emits the whole
  transcript, which would otherwise buffer every turn twice
- `status.go` — the **bottom status bar**. The tail is a streaming view, so a
  row pinned to the bottom has to come from the terminal rather than from us
  repainting: `DECSTBM` (`ESC [ 1 ; h-1 r`) shrinks the scrolling region and the
  transcript scrolls underneath a row we own. Three things there are
  load-bearing. (1) **DECSTBM homes the cursor** — every region change is wrapped
  in `ESC 7`/`ESC 8` or the next line of transcript overwrites the backfill.
  (2) **The row must be free before the region shrinks**: after a full-screen
  backfill the cursor is ON the last row, which is about to stop scrolling, so
  `reserveRow` asks the terminal where the cursor is (DSR `ESC [ 6 n`) and
  scrolls one line if needed — and that query is why `openControlTTY` is split
  from `startKeyboardOn`, since a running reader goroutine would swallow the
  reply. It also means `r.endLine()` runs before the bar is created (the only
  place the deferred trailing newline is settled early). (3) **`close()` before
  restoring the terminal modes**, or the shell inherits a terminal that can only
  scroll h-1 rows. The alt-screen overlays get the whole screen back via
  `suspend`/`resume`. Pure `statusLine`/`statusLefts`/`statusRights` (widest
  variant that fits — the render settings give way before the session id) and
  `parseCursorReport` are unit-tested; a nil `*statusBar` is inert, which is the
  piped / `--no-status` path
- `theme.go` / `config.go` / `main.go` — themes, flags+env, wiring
- `keyboard.go` — live single-key controls via cbreak. **The keyboard only ever
  signals; the render goroutine does all of it.** Every display key
  (`t`/`T`/`c`/`m`/`w`/`r`/`y`) goes to `actionCh` and the live loop applies it, then
  re-renders and writes the status bar — the keyboard used to flip the atomic
  toggles itself and print to stderr, which stopped working the moment those keys
  had to re-render (a theme swap rebuilds the non-atomic glamour fn + header
  strings) and write a bar (only one goroutine may touch the screen). `actionCh`
  is buffered but NEVER coalesced: two `t` presses are two steps through the
  cycle and a second `y` means one more message, so a dropped press lands on the
  wrong state. The two alt-screen overlays (`→` focus, `?` help) go to
  `overlayCh` and the goroutine parks on `resumeCh` (single tty reader), and
  `Ctrl-X` (0x18) signals `treeCh` and STOPS reading so `tailSession` returns and
  `run`'s picker↔tail loop re-enters the tree (Claude-only, gated by
  `treeEnabled`; a no-op on codex/agy). `openControlTTY` is split from
  `startKeyboardOn` for the status bar's DSR query — see `status.go`
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
- `pending.go` — the marker protocol and model (Claude-only): when Claude blocks
  on a question or permission prompt, the opt-in hooks write a per-session marker
  file to `~/.claude/entire-tail/pending/<session_id>.json` the instant it appears,
  before Claude's deferred transcript flush. The live tail loop stats that marker
  each tick and renders the prompt early via `claudeParseQuestions`; a `contentKey`
  dedup suppresses the duplicate card when the real JSONL record finally arrives.
  Marker payload is the hook's tool_input verbatim — either a `{questions:[...]}` 
  object (for `AskUserQuestion`) or a `{tool_name, tool_input}` pair (for 
  `PermissionRequest`).
- `hookinstall.go` — opt-in installation of the pending-prompt hooks in
  `~/.claude/settings.json`, idempotent merge/unmerge, and the first-run offer
  gate. `shouldOfferHookInstall` predicts whether to ask (Claude-only, fresh
  session, interactive, no explicit flag/session id). The offer is remembered in
  `~/.claude/entire-tail/hook-choice` so it fires exactly once per user.
- `hooks/entire-tail-pending.sh` — the vendored hook script (embedded in the
  binary). Wired via `PreToolUse` / `PostToolUse` matchers on `AskUserQuestion`,
  plus bare `PermissionRequest` / `PermissionDenied` hooks. Writes/removes marker
  files atomically; safely handles half-written files and missing session ids.
- `tap.go` / `tapdaemon.go` — the **API tap**: an opt-in local reverse proxy
  (`entire-tail tap start`, fixed `127.0.0.1:47391`) that agents launched from
  the tree are pointed at via `ANTHROPIC_BASE_URL`, so the assistant stream is
  visible as it streams. `tapdaemon.go` is the IO half (httputil.ReverseProxy,
  `FlushInterval:-1` so SSE chunks forward immediately; the tee sits in the
  response body's `Read` path — `tapTeeBody` — so the proxy stays
  byte-transparent). `tap.go` is the pure half: `tapParser` turns SSE frames into
  block-complete `tapEvent`s (text/thinking accumulated from deltas, tool_use
  input reassembled from `input_json_delta` fragments and dropped unless it
  parses), appended as NDJSON to
  `~/.claude/entire-tail/tap/sessions/<session-id>.ndjson`. Sessions need **no
  correlation heuristics** — every Claude Code request carries
  `x-claude-code-session-id` (and `metadata.user_id` repeats it), verified live.
  `tapWatcher` follows a sidecar with a byte offset like the transcript
  follower, starting at EOF (never replays history) and rebinding across a
  lineage flip. See the tap notes below for what it deliberately does NOT do.

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
- **The "done" signal is read, never inferred.** Claude's assistant records carry
  `message.stop_reason` on EVERY jsonl line of a message — `tool_use` while the
  agent keeps going, `end_turn` (rarely `stop_sequence`/`max_tokens`/`refusal`)
  when it hands control back. `claudeTurnDone` maps that to `Record.Done`, and
  `doneBanner` leads the closing message with a bright-green `✔ DONE — over to
  you`. Two traps the shape of the data sets: a **sidechain** record's `end_turn`
  is a *subagent* finishing (skipped via `isSidechain`), and a single message
  occasionally spans two text records that BOTH say `end_turn` — deduped by
  `message.id` in the adapter (within one line) and by `Renderer.lastDoneMsgID`
  (across lines, cleared in `reset()`). Do NOT reimplement this as "no tool call
  followed" — that isn't knowable until the next event lands, which is exactly
  the latency the marker exists to remove. Other agents never set `Done`, so the
  goldens for claude/codex/agy are untouched.
- **Not every `type:"user"` record is the user** (`tasknote.go`). Claude Code
  injects a background task's progress — a `Monitor` tick, a task ending — as a
  `user` record it wrote itself. Rendered as a USER turn that is worse than
  noise: a box header attributing to the human a task id, a temp output-file
  path, a pile of CI statuses, and an instruction addressed to the AGENT ("send a
  PushNotification if…"). The `<task-notification>` wrapper doesn't even survive
  to hint at what it is — **glamour eats it as an HTML tag**, so the guts spill
  out bare. `isTaskNote` classifies on `promptSource`/`origin.kind` (the payload
  tag is only a fallback for transcripts predating `origin`) → `KindTaskNote` →
  one dim `⧗` line. **`promptSource` is what decides, not the tag**: a human who
  pastes a notification is still a human (`TestIsTaskNote`). Two things in
  `taskNoteLine` are deliberate. (1) It keeps ONLY `<summary>` + `<event>` — ids,
  paths and the agent-directed instruction are plumbing, and the tests assert
  they never leak. (2) The **summary is budgeted separately** from the line
  (`taskNoteSummaryMaxRunes` vs `taskNoteMaxRunes`), because the summary is the
  Monitor's title repeated verbatim on every tick while the event is the only
  part that changed — budgeting the line as one string spends it on the title and
  elides the news (seen live: a title long enough to leave exactly one check
  visible). An empty result renders NOTHING rather than a bare marker.
- **Instant pending-prompt alert dedup** — the marker-file render path and the
  eventual JSONL card both compute the SAME `contentKey` (questions via
  `claudeParseQuestions`→`questionsContentKey`, permissions via sha256) so the
  JSONL card suppresses itself once the marker already showed it. Both derive the
  key independently from their respective payloads, which differ slightly (marker
  lacks `tool_use_id`, JSONL may have it), so a raw-byte hash would never match.
- **The tap renders exactly one thing, on purpose.** Measured, don't re-derive:
  Claude Code appends an assistant message's transcript records at
  `message_stop` — **~200ms** after the wire (proven with a `sleep 20` tool: text
  + tool_use hit disk 194ms after the wire while the tool still had 20s to run).
  So for ordinary turns the JSONL is already fast enough and the tap would only
  duplicate/race it. The ONE case it can't cover is `AskUserQuestion`: Claude
  Code withholds the **whole message** — preamble text included — until the user
  answers (verified on a real session: 35+ minutes, zero bytes written). Hence
  `tapPending` only fires for a message whose `stop_reason` is `tool_use` AND
  whose last block is `AskUserQuestion`, and `Renderer.tapPreamble` is the only
  tap render path. Do NOT "finish the job" by rendering all tap events — that
  turns the tap into a second competing transcript with no latency win.
- **Tap→JSONL dedup is exact, not fuzzy.** Both sides carry the provider ids, so
  `earlyTextKey` = message id + the block's exact text (`earlyShown`, consumed
  one-shot, cleared by `reset()`), and the question reuses the existing
  `questionsContentKey`. Don't swap either for a similarity/hash-of-nearby-content
  scheme: the wire text and the transcript text are byte-identical, and message
  ids keep two identical short texts in different messages from colliding.
- **…and it runs BOTH ways, because the tap doesn't always win the race.** The
  tick polls the transcript before the sidecar (`main.go`), so a message whose
  JSONL and tap bytes land together renders from the FILE and the tap event
  arrives second — and a suppression that only pointed tap→JSONL let it reprint.
  Seen live as one question card three deep (hook marker, then the transcript's
  redraw-under-its-preamble in `question()`, then the tap) with the preamble
  printed twice, a second apart — the tap's own wire timestamp is the tell,
  since it renders BELOW a header stamped later than itself. So `shownText`
  mirrors `earlyShown`: `rememberShownText` records the transcript's assistant
  bodies (scoped to one message id — the tap never lags further behind than the
  message it reports, so remembering more would grow with the session), and
  `tapPreamble` skips a block already on screen and returns early when
  `seenQuestions` already holds its `QID`. Both `question()` paths set that id,
  including the one that suppresses the card, which is what makes the guard
  cover "the marker drew it and the transcript confirmed it" too. Fixing this by
  reordering the tick instead would only narrow the window — the tap can lag by
  more than a tick — and the dedup has to hold either way.
- **The tap is opt-in AND fail-open, and that's a safety property.** A launched
  agent gets `ANTHROPIC_BASE_URL` only when `tapBaseURL` health-checks the daemon
  *and* the reply's pid matches the state file (so a stranger squatting the port
  is never trusted). No daemon → empty string → the launch command is
  byte-identical to the pre-tap one (`TestWorkspaceScriptsTapEnv`). The port is
  **fixed** because a session bakes the URL in at launch: restarting on a
  different port would break every live session, which is also why `tap install`
  writes a `KeepAlive` LaunchAgent. A daemon that dies mid-session still takes
  that session's API endpoint with it — the known, documented cost of routing.
- **happy DOES inherit the tap's `ANTHROPIC_BASE_URL`** — verified live (`happy -p`
  through the daemon produced a routed session). Worth stating because happy
  advertises `--claude-env ANTHROPIC_BASE_URL=…` for custom endpoints, which
  reads like ambient env gets scrubbed the way `--session-id` is (see the
  `--session-id` trap above). It isn't: a plain env assignment reaches the claude
  happy spawns, so `tapEnvPrefix` needs no happy-specific branch. If a future
  happy sandbox starts filtering env, `--claude-env` is the escape hatch — but
  don't add it speculatively.
- **Routing through the tap CHANGES how Claude Code composes requests, and one
  env var undoes it.** Setting `ANTHROPIC_BASE_URL` to anything that isn't a
  first-party Anthropic host makes Claude Code **disable tool search** — it stops
  deferring MCP tool schemas behind `tool_reference` blocks and ships every schema
  inline, because it can't know a gateway forwards those blocks. On a machine with
  a large MCP fleet that is the difference between a normal prompt and **"Prompt is
  too long" on the second turn of a fresh session** (hit live, twice). Claude
  Code's own `--debug api` log states it: `[ToolSearch:optimistic] disabled:
  ANTHROPIC_BASE_URL=… is not a first-party Anthropic host. Set
  ENABLE_TOOL_SEARCH=true …`. Hence `tapEnvPrefix` always emits
  `ENABLE_TOOL_SEARCH=true` beside the base URL (verified to restore the
  first-party decision exactly: `mode=tst, ENABLE_TOOL_SEARCH=true, result=true`).
  Do NOT drop it, and when debugging anything context-shaped under the tap, diff
  `--debug api` logs with and without the base URL before suspecting the proxy
  itself — the proxy is byte-transparent; the CLIENT behaves differently.
- **`install-tap.sh` / `disable-tap.sh` + `tap install|uninstall` own the launchd
  lifecycle**, and two things there are load-bearing. (1) The plist must be pinned
  to a STABLE binary: it outlives the shell that wrote it, so a path under
  `.claude/worktrees/` or `/tmp` yields a daemon that silently stops returning
  once that path goes (`looksEphemeralBinary` warns, `tapAgentBinary` prefers the
  installed `entire-tail` on PATH, `--binary` overrides). (2) `tap install` loads
  the agent and then **waits for a real health check** before claiming success —
  reporting "installed" for a dead agent is worse than failing. The three
  side-effecting steps go through `tapAgentLoad`/`tapAgentUnload`/`tapAgentWait`
  package vars ONLY so tests can stub them: an earlier version bootstrapped a real
  KeepAlive agent pointing at the test binary during `go test`, i.e. a respawn
  loop in the developer's launchd. Never call `launchctl*` directly from a test path.
- **The tap daemon must never log or persist headers** — they carry the auth
  token. Only method/path/status and the assistant stream (which the transcript
  already stores in plaintext) are recorded; `TestTapHandlerTeesStreamAndPreservesBytes`
  asserts a token never reaches the sidecar.
- **The activity table describes ONE daemon's lifetime.** `runTapDaemon` clears
  `active.json` at start, because the tracker's in-memory map begins empty: a
  leftover file would have the tree reporting activity this daemon never saw
  (caught live — a 43-minute-old entry surviving a restart, still inside the
  15-minute live window when it was written). If the daemon is up, the table is a
  fact; if it's down, the table is simply absent. Don't "preserve history" across
  restarts here — the whole value of this signal is that it's observed, not
  remembered.
- **`applyTapActivity` is strictly additive.** The tap knows *which* session is
  generating (`in_flight`), which `liveCwds` (pgrep+lsof) fundamentally cannot —
  it sees a claude process in a folder but not which transcript it's writing, so
  `buildClaudeTree` guesses "the newest N". The overlay promotes what the tap
  confirms (and adds the `◉` glyph) but never clears a marker for a session it
  hasn't heard from: no recent API traffic means the agent is waiting on its
  human, not that the pane is gone.
- **A finished worktree's sessions still group under their repo.** `repoForCwd`
  asks git for the cwd's `origin`, and git cannot answer for a directory that no
  longer exists — which is the NORMAL end state of a worktree (its dir is deleted
  once the work merges). Every finished worktree therefore used to fall through to
  the `tildify(cwd)` fallback and show as its own orphan group right beside the
  repo group its sessions belong in. `worktreeParent` recovers the checkout from
  the path instead of from git (`<checkout>/.claude/worktrees/<name>`, stripped at
  the FIRST marker so a session run in a *subdirectory* of a worktree also
  resolves), and `repoForCwd` retries the origin lookup there. Pure string work on
  purpose: it has to keep working for a path that's gone, which is exactly when
  it's needed. The knock-on is in `mergeEntire`'s `add` — a group's newest session
  is now often one whose dir was deleted, so `g.Dir` (the `n` target) only accepts
  a cwd that `isDir()`, or `n` would cd into nothing. `TestWorktreeParent`,
  `TestMergeEntireSkipsDeadDirsForNewSessions`. Note this is the DEFAULT
  repo-grouped tree only; `--local` groups by folder path by design and still
  shows worktree paths as their own rows.
- **The pending hooks and the tap are already account-agnostic — verified, not
  assumed.** The hook script writes `$HOME/.claude/entire-tail/pending`
  (`hooks/entire-tail-pending.sh`), keyed on HOME rather than
  `$CLAUDE_CONFIG_DIR`, and `~/.claude-personal/settings.json` is a symlink to
  the work one, so a personal session's prompts already produce markers with no
  profile-specific code. Same for `tapDir`. One thing that could have broken
  this and doesn't: `hookinstall.go` writes settings with `os.WriteFile`, an
  in-place truncate that follows the symlink — a temp-file+rename would replace
  the symlink with a real file and silently fork the two accounts' configs (the
  exact hazard `setup-claude-personal.sh`'s README warns about). Don't
  "modernize" that write to an atomic rename without handling the symlink.
- **This is the first feature that writes global config** (`~/.claude/settings.json`),
  opt-in and reversible. `shouldOfferHookInstall` gates the offer so it fires
  only on a fresh interactive Claude run with no explicit flags; `--no-hook-install`
  suppresses it, and the choice is remembered in `~/.claude/entire-tail/hook-choice`
  so the user is never nagged again. The hook script itself is embedded in the
  binary and installed atomically.
- **The mrkdwn conversion targets the Slack COMPOSER, not the Web API**, and
  that inverts two rules the Slack docs state. (1) **No HTML escaping** —
  `&amp;`/`&lt;` are how the API accepts `&`/`<`; pasted into the message box
  they show as the literal five characters. (2) **Never `<url|text>`** — that
  form is parsed for API-posted messages, but a user-typed `<` is escaped
  server-side, so it renders literally; markdown's own `[text](url)` is passed
  through instead, which the composer understands (the URL is held out of the
  emphasis passes so a path with `__` isn't eaten; the label still converts).
  `TestToSlackMrkdwnAvoidsAPIOnlyForms` pins both.
  **Code is passed through byte-for-byte** (fences keep their fence, minus the
  language tag mrkdwn ignores): the reason to copy a command is that it still
  runs when pasted, so nothing inside a fence or backticks may be rewritten.
  **Every ``` gets its own line** — Slack renders a one-line ```` ```code````
  ```` as literal backticks, so a one-line fence is split into three
  (`TestToSlackMrkdwnSplitsOneLineFence`; an earlier version dropped the body).
- **`clipboardWrite` is a package var so tests can stub it.** The real path runs
  `pbcopy`; a test that exercised it would silently replace whatever the
  developer had on their clipboard. Same rule as the `launchctl` stubs — a
  `go test` must not touch the machine's real state.
- **A toggle re-renders one SCREENFUL; only `r` re-renders everything.**
  `rerender(banner, keep)` renders the whole transcript into a buffer — the
  renderer's state (turn boundaries, dot streaks, the yank buffer) must see every
  record — and then prints only the last `keep` lines. Dumping a long session on
  every keypress buries the screen in scrollback, and because the tail of the new
  copy looks much like the tail of the old one it reads as though the key did
  nothing (reported live). `keep` is the terminal height minus two, from the
  status bar; `r` passes 0 for all of it.
- **Word wrap is on for a tty, off everywhere else** — and the "everywhere else"
  half is load-bearing. `wrapWidth` (main.go) returns 0 unless stdout is a char
  device, so piped runs and the whole golden suite render exactly as they did
  before wrap existed; that's the only reason turning wrap on didn't rewrite
  every `testdata/*.golden`. On a tty it's `wrapWidthFor` = terminal width **minus
  one**: a line that fills the final column makes the terminal wrap the cursor
  itself, which reads as a phantom blank line after every full paragraph.
  Wrap was off entirely until #60, for a reason that's now paid for rather than
  gone: with `WithWordWrap(0)` a paragraph is ONE logical line, so the terminal
  reflows it free on resize *and* rejoins its own soft wraps on copy — a mouse
  drag-select gets an unbroken paragraph. Wrapping gives both of those up. The
  reflow is bought back by `setWrap` + a re-render on SIGWINCH (below); the copy
  is not buyable at all — a terminal has no "display but don't select" attribute,
  so any break we emit is a real newline in the clipboard. `y`/`m` are unaffected
  (they convert from raw markdown, never the screen), and `--no-wrap` restores
  the old behaviour for a session where drag-select matters more than legibility.
  The reason wrap went on: the terminal's own soft wrap breaks at the column
  edge, splitting words in half.
- **Turning wrap on turns glamour's right-padding on, and `trimWrapPad` undoes
  it.** With a wrap width set, glamour pads EVERY line out to that width. Three
  things break if you leave it: the pad eats the last column, it lands in the
  clipboard on a drag-select, and — the one that's actually visible — it pushes
  the dot streak riding the end of an agent turn past the terminal edge, so a
  two-dot streak soft-wraps onto a row of its own and the whole "dots ride the
  agent turn" layout collapses. The trap is that **a plain `TrimRight` takes off
  nothing**: glamour doesn't append spaces, it emits each pad column as its own
  styled cell (`\x1b[38;5;252m \x1b[0m` over and over), so the line ends in an
  escape. `trimWrapPad` matches the trailing run of spaces-and-escapes, drops the
  spaces and KEEPS the escapes (trimming them would leave a colour open to EOL),
  collapsing them to the single closing reset when the run ends in one. It's
  wired in `newGlamour` and only when `wrap > 0` — the wrap-0 path returns
  `md.Render` untouched, which is what keeps the goldens byte-identical.
  The `setsBackground` guard (skip a line that sets a background, where the pad
  is what makes a block a rectangle) is **currently inert**:
  `WithChromaFormatter("terminal16m")` emits foreground colours only, so no
  bundled theme produces a background-styled body line. It's parked there because
  the day one does, trimming would shred the block — and it parses SGR parameters
  rather than pattern-matching them, because a foreground RGB of `38;2;48;10;20`
  contains a literal `48` a regex reads as a background.
- **The resize re-render is debounced, and only on a width change.** SIGWINCH
  fires continuously while a window edge is dragged, and printed lines can't be
  rewrapped in place — the only way to apply a new width is to re-render, which
  costs a full transcript walk. So the winch case just records the time (and lets
  the status bar reclaim its row, which is cheap), and the ticker performs the
  re-wrap once the signals have been quiet for `winchSettle` (300ms — longer than
  `pollInterval`, so a drag still in flight always pushes it out another tick).
  `setWrap` returns whether the WIDTH actually moved, so dragging the bottom edge
  costs nothing. A glamour rebuild failure leaves the old width in place: a
  resize must never be able to break rendering.
- **The alt-screen overlays (`focus.go`, `preview.go`) stay at wrap 0** on
  purpose. They clip lines to the pane with `truncVisible` at draw time and
  re-measure every frame, so a width baked into the rendered buffer would go
  stale the moment the window resized mid-view. Making them wrap means moving the
  re-render inside their key loops too — a separate change.
- **The box headers don't wrap.** `userHdrBody`/`claudeHdrBody` are fixed-width
  strings whose dash counts the goldens pin, so they stay 51 columns regardless
  of the wrap width (and overflow a terminal narrower than that, as they always
  have). Wrapping applies to bodies, not chrome.
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
