# entire-tail — subagents & pending questions

**Date:** 2026-07-13
**Status:** approved design → implementation
**Branch:** `feat/subagents-and-questions` (single PR)

## Problem

entire-tail tails only the **main thread** of a session. Two things it misses:

1. **Subagents are invisible.** When the orchestrator spawns `Agent` (Task)
   subagents — increasingly, several background agents at once — the tail shows
   nothing of what they're doing. Their transcripts exist on disk but aren't
   surfaced. You can't tell what's running, what finished, or read a subagent's
   work.
2. **Questions freeze off-screen.** When Claude emits an `AskUserQuestion`, the
   prompt sits in the *agent's* pane awaiting your answer. If you're watching the
   tail pane you don't see that a question is waiting, nor what it's asking.

## Goal

A **hybrid** view: the main scrolling tail gains live **agent markers** and
**pending-question cards**, and a **`→` focus overlay** lets you open any
subagent's transcript full-screen and cycle between them.

Shipped as a **single PR**.

## Data model (verified against a real session)

For a main transcript at `<project>/<sessionId>.jsonl`:

- Subagent transcripts: `<project>/<sessionId>/subagents/agent-<agentId>.jsonl`.
  **Standard Claude JSONL** (`message`/`content`, `isSidechain:true`, plus an
  `agentId` field) → the existing `adapter_claude.normalize` renders them as-is.
- Sidecar `<...>.meta.json`: `{agentType, description, toolUseId, spawnDepth}`.
- The main-thread `Agent` **tool_use** input: `{description, subagent_type,
  prompt}`, with a tool-use `id`.
- The main-thread `Agent` **tool_result** carries `toolUseResult`:
  `{isAsync, status, agentId, description, resolvedModel, prompt, outputFile,
  canReadOutputFile}`. **`outputFile` is the absolute path to the subagent
  transcript** and `agentId` matches the sidecar filename — so the main stream
  itself links spawn → subagent file. (Async agents: this result only says
  "launched", not "finished".)
- `AskUserQuestion` **tool_use** input: `{questions:[{question, header,
  multiSelect, options:[{label, description, preview}]}]}`. The **answer** is that
  tool-use id's later **tool_result** (`toolUseResult` echoes the questions with
  the chosen option; a rejection is an `"Error: The user doesn't want to
  proceed..."` string). **No result yet ⇒ pending.**

### Channel discovery

Primary: scan the main transcript for `Agent` tool_use + tool_result pairs →
`{agentId, description, agentType, outputFile, spawnTs}`. Fallback / augment: glob
`<project>/<sessionId>/subagents/agent-*.jsonl` + read `.meta.json` (catches
synchronous subagents and any the main-stream scan missed).

### Status & duration (best-effort)

- **running** vs **done**: async completion is not cleanly stamped in the main
  stream (only `pendingBackgroundAgentCount`, and Claude computes "finished ·
  8m47s" from the subagent file's own timespan). So: read the subagent file's
  first and last record timestamps; **duration = last − first**. Treat a subagent
  as **done** when its file ends on a terminal assistant turn *and* has been idle
  (mtime older than a few seconds); otherwise **running**. If the main stream
  later exposes a clean finish record, prefer it. Done-detection is cosmetic — the
  feature degrades gracefully to "running + elapsed".

## Rendering (main scrolling stream)

All additions are **new output on records the bash oracle never emitted**, so the
byte-parity goldens (`equiv_test.go`, `testdata/*.golden`) are unaffected.

- **Agent spawn marker** — on an `Agent` tool_use:
  `⏺ ▸ agent: <description>  (<agentType>)` and, when known, a dim
  `[→ focus]` affordance. Colored like a tool line but distinct.
- **Agent finish line** — when the subagent is detected done:
  `⎿ ✔ <description> · <duration>`. (Best-effort; may render as
  `⎿ ▸ running · <elapsed>` while live.)
- **Pending-question card** — on an `AskUserQuestion` tool_use with no result
  yet: a bold bordered card
  ```
  ╭─ ⁉ WAITING FOR YOUR ANSWER ──────────────╮
  │ <header>: <question>                      │
  │   1. <option label>                       │
  │   2. <option label>                       │
  ╰───────────────────────────────────────────╯
  ```
  (multiple questions in one call → stacked). **Emit one terminal bell (`\a`)**
  the first time a given question id becomes pending — so it's noticeable when
  you're not looking. The bell fires at most once per question id.
- **Answered line** — when the tool_result arrives:
  `⎿ ✔ answered: <chosen label(s)>` (or `⎿ ✗ dismissed` on rejection).

Because entire-tail follows the file live, the tool_use line is rendered the
instant Claude asks; the answer record is written only after you reply — so the
card **always** appears before the answer.

## Focus overlay (`→`)

- A live key `→` (added to the cbreak handler) enters an **alt-screen** view over
  the **selected** subagent (default: most recent). Reuses `preview.go`'s pager,
  extended to **auto-follow** (re-read the subagent file on an interval / on
  keypress so it grows as the agent works).
- `←` / `→` inside the overlay cycle to the previous/next channel (subagents
  ordered by spawn time). A header shows `focus: <description>  (<n>/<total>) ·
  <status> <duration>`.
- `Esc` / `q` returns to the live tail (which kept scrolling underneath — on
  return we re-attach the follow loop at the current file offset).
- If there are **no subagents**, `→` is a no-op (optionally a dim hint).

## Architecture / files

- **`subagents.go`** (new) — `type channel {AgentID, Description, AgentType,
  Path, SpawnTs int64}`; `discoverChannels(mainPath) []channel` (main-stream scan
  + dir-scan fallback); `channelStatus(ch) (running bool, dur time.Duration)`.
  Pure functions where possible for unit testing.
- **`adapter_claude.go`** — surface what render needs on `Record`: the tool name
  is already implied by TOOLUSE; add optional fields (e.g. `Record.Tool`,
  `Record.AgentDesc`, `Record.AgentID`, and a parsed `Record.Question *question`)
  OR two dedicated kinds (`KindAgentSpawn`, `KindQuestion`). Chosen approach:
  **keep `Kind` set, add optional payload fields** to avoid churn in every
  downstream switch. Reasoning recorded in the plan.
- **`render.go`** — special-case the new payloads into the marker / finish /
  question-card / answered output. Bell emission tracked by a `seenQuestions`
  set on the render state.
- **`focus.go`** (new) — the alt-screen focus overlay + channel cycling, built on
  the existing pager; pure `focusView`/reducer split from the tty driver per the
  repo convention, so navigation is unit-tested without a tty.
- **`keyboard.go`** — handle `→` to signal "enter focus" (keyboard only signals;
  the render/main goroutine performs the alt-screen switch, keeping `-race`
  clean, exactly as `t`/`c`/`r` already work).
- **`tail.go`** — on the focus signal, pause follow, run the overlay, resume.

## Testing

- **Golden fixtures** (`testdata/`): a synthetic main transcript containing an
  `Agent` spawn (+ async result with `outputFile`), an `AskUserQuestion` that is
  **pending**, a second that is **answered**, and one that is **rejected**; plus a
  tiny subagent transcript file. New `*.golden` for the marker + card + answered
  output. Existing goldens must remain byte-identical.
- **Unit tests**: `discoverChannels` (main-stream + dir fallback, dedup),
  pending-vs-answered detection, chosen-answer extraction, duration calc, the bell
  fires once per question id, and the `focusView` reducer (`←/→` cycling, `Esc`).
- `go vet`, full suite, and `go test -race` (focus loop) all green.

## Non-goals (this PR)

- Fold-in interleaving of full subagent *bodies* into the main stream (markers
  only; bodies live in the focus overlay).
- Non-Claude agents (Codex/agy have no subagent concept in scope here).
- Editing/answering questions from entire-tail — it's a viewer; you still answer
  in the agent pane.

## Open implementation details (resolve in the plan, not blocking)

- Exact async **done** heuristic (idle threshold) and whether a cleaner finish
  record exists in newer Claude versions.
- Whether the focus overlay follows on a timer vs inotify-style; timer (≈500ms)
  is simplest and matches the existing agy whole-file re-read.
