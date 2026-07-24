# Live pending-question / permission alert

**Date:** 2026-07-24
**Status:** Approved design, pre-implementation

## Problem

Claude Code streams a turn to the screen live, but only appends the **complete**
turn to `<id>.jsonl` at turn end. When a turn ends by *blocking on the user* — an
`AskUserQuestion` card or a tool-permission prompt — the record is deferred until
**after** the user answers. Empirically verified (see "Evidence" below): through
the entire pending window, and even at the instant the user answers, the
transcript file has not grown.

`entire-tail` follows the JSONL file, so it is **structurally blind** during a
pending question: the pane stays dark exactly when the user most needs to know
Claude wants them. Worse, `entire-tail`'s existing `AskUserQuestion` card + bell
(rendered from the JSONL record) fire only *after* the answer — useless as an
alert.

## Goal

When Claude blocks on an `AskUserQuestion` or a permission prompt, show it in the
tail pane **immediately** — before the deferred flush — instead of after the user
answers. Reuse the existing card renderer so the early render is byte-identical to
the eventual JSONL-sourced render.

Non-goals: generic idle detection (`idle_prompt` false-positives on every idle
session — see Evidence); tapping the terminal screen / PTY (re-inherits the
partial-repaint soup this tool exists to escape); any change to how completed
turns render.

## Evidence (probe, 2026-07-24)

A temporary `Notification` + `PreToolUse`/`PostToolUse` (matcher
`AskUserQuestion`) hook set logged each fire plus the transcript's size/line-count
at fire time. Firing a single `AskUserQuestion` and waiting ~8s before answering:

| event | wall-clock | transcript |
|---|---|---|
| `PreToolUse`/`AskUserQuestion` (card appears) | T+0.0s | size 116332, 19 lines |
| `Notification` (`permission_prompt`) | T+6.1s | size 116332, 19 lines |
| `PostToolUse`/`AskUserQuestion` (answered) | T+8.4s | size 116332, 19 lines |
| (later) turn flushed | — | size 123318 |

Findings:
1. **`PreToolUse`/`AskUserQuestion` fires pre-answer**, the instant the card
   appears, and its `tool_input` payload is the exact question shape the tail
   already renders: `{"questions":[{question,header,multiSelect,options:[{label,description}]}]}`.
2. **The JSONL flush is deferred** — size/lines identical at Pre, at Notification,
   and at Post; the turn landed only later.
3. **`Notification` is too noisy to be the primary signal** — `idle_prompt` fires
   on any idle session; the question session even surfaced as `permission_prompt`.
   So `PreToolUse`/`AskUserQuestion` and `PermissionRequest` are the precise
   signals, not `Notification`.

## Signals & hooks

Opt-in hooks jq-merged into `~/.claude/settings.json`, all keyed by `session_id`:

| Hook | Fires when | Action |
|---|---|---|
| `PreToolUse` matcher `AskUserQuestion` | question card appears (pre-answer) | write question marker |
| `PostToolUse` matcher `AskUserQuestion` | user answered | delete marker |
| `PermissionRequest` | permission prompt appears | write permission marker |
| `PermissionDenied` | permission denied | delete marker |
| `PostToolUse` (the permission-gated tool) | permission granted, tool ran | delete marker |

The hook command is a small vendored shell script (installed alongside the
binary) that reads the hook JSON on stdin and writes/removes the marker. It has no
dependency on `entire-tail` being running.

## Transport: marker protocol

- Directory: `~/.claude/entire-tail/pending/`.
- One file per pending prompt: `<session_id>.json`.
- Contents:
  ```json
  { "kind": "question" | "permission",
    "payload": <tool_input verbatim>,
    "tool_use_id": "<id if present in the hook payload, else null>",
    "ts": <unix seconds> }
  ```
- Written atomically (temp file + rename) so `entire-tail` never reads a partial
  file.
- `kind: "question"` `payload` is the `AskUserQuestion` `tool_input` (the
  `questions` array). `kind: "permission"` `payload` is the gated tool's
  `tool_name` + `tool_input`.

## entire-tail watch + render

- The live follow loop (`tail.go` / `main.go` `tailSession`) already polls on a
  tick. Add a cheap `stat` of `pending/<watched-sid>.json` on each tick.
- **New marker →** ring the bell (once per marker, deduped like the existing
  `seenQuestions` bell) and render:
  - `question`: the existing `render.go` `AskUserQuestion` card, built from
    `payload` — no new rendering code, byte-identical to the JSONL path.
  - `permission`: a one-line `⏳ waiting: <tool>(<summary>)` marker built from
    `payload` (e.g. `⏳ waiting: Bash(rm -rf …)`), flattened/truncated like `full`
    mode flattens Bash.
- **Dedup / clear when the file catches up:** when the real JSONL record lands,
  the existing `seenQuestions` set suppresses a second card. The dedup key is the
  `tool_use_id` when the hook payload carries it, otherwise a stable hash of the
  `questions` payload. The on-screen alert is dismissed when *either* the marker
  file disappears (Post/Denied hook removed it) *or* the watched transcript's byte
  offset advances past the point the marker was observed (the flush happened).
- Watching is enabled whenever the markers directory exists (i.e. the hook is
  installed). No extra runtime flag.
- **Adapter-agnostic to entry path:** because markers are keyed by `session_id`,
  this works identically whether the tailed session was resolved via adopt
  (`adopt.go`), `--follow-session`, or the picker — no per-path code. (This is the
  answer to the original "does workspace mode work with adopt" thread: the alert
  is a property of the session id, not how it was found.)

## Install / enable UX

Chosen model: **auto-detect on first run + one-time interactive prompt** (a
prompt, never a silent write), hardened with gates so the read-only charter holds.

- On the first **interactive, bare** `entire-tail` run where the hook is absent
  and the user hasn't previously answered: show a one-time
  `Add the live-question hook to ~/.claude/settings.json? [y/N]`.
- On **yes**: jq-style merge (backup + validate + print the added entries — the
  exact flow proven by the probe), create `~/.claude/entire-tail/pending/`, install
  the vendored hook script.
- The decision (yes or no) is persisted to `~/.claude/entire-tail/hook-choice` so
  the prompt **never appears again**.
- **Hard gates — the prompt never appears when:** stdout is not a tty, input is
  piped, `--follow-session` is set, the session was adopted from a sibling pane,
  or `--no-pick` is set. So the workspace tail pane and all automated/piped runs
  stay silent.
- Explicit escape hatches: `entire-tail install-hooks` and
  `entire-tail uninstall-hooks` subcommands (idempotent), and `--no-hook-install`
  to suppress the offer for a run.

## Testing

Per the charter (pure functions unit-tested; goldens for render; shell-outs are a
thin untested IO edge):

- Marker (de)serialize round-trip.
- Dedup key resolution: `tool_use_id` present → use it; absent → content hash;
  same question via marker and via JSONL → same key (no double render).
- **Golden:** an early card rendered from a marker payload is byte-identical to
  the card rendered from the equivalent JSONL `tool_use` record.
- Permission marker → one-line `⏳ waiting: …` render (golden), Bash flattening
  reused.
- The prompt-gate predicate over the matrix (tty / piped / `--follow-session` /
  adopted / `--no-pick` / prior-choice-recorded) — pure, table-tested.
- Hook install: jq-merge into a fixture `settings.json` preserves existing hooks
  and adds exactly the new entries (pure merge function, tested on a fixture; the
  real file write is the IO edge).

## Footprint note

This is the first time `entire-tail` writes **global** config (beyond the
transcript-local, opt-in `--mark-continuation`). The install guardrails (explicit
prompt, persisted decline, `uninstall-hooks`, hard gates) are what keep it
charter-compatible. The transcript itself remains untouched by this feature — it
only reads the JSONL and the separate markers directory.

## Files (anticipated)

- `pending.go` — marker read/watch/serialize + dedup-key logic (pure) + the
  tail-loop stat hook.
- `render.go` — reuse existing `AskUserQuestion` card path from a marker payload;
  add the permission one-liner.
- `hookinstall.go` — jq-style settings merge (pure), the first-run prompt gate
  (pure predicate), the `install-hooks` / `uninstall-hooks` subcommands.
- vendored hook script under the repo (installed by `install.sh` /
  `install-hooks`), mirrored in `docs/` like the handover skill.
- `config.go` / `main.go` — `--no-hook-install`, subcommand wiring, the
  markers-dir-exists watch enable.
- `testdata/*.golden` + unit tests as above.
