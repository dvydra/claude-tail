# entire-tail — Session Handover Docs

**Date:** 2026-07-17
**Status:** Design — awaiting review
**Component:** `entire-tail` (new `handover` subcommand + `handover-sessions` skill)

## Overview

A new `entire-tail handover` subcommand that captures the state of today's
coding-agent sessions and turns each *unit of work* into a handover document in
the user's Obsidian vault. entire-tail (Go) does the cheap, deterministic part —
enumerate today's sessions, detect live vs ended, extract link seeds, and build a
manifest — then confirms the set with the user and hands off to an interactive
Claude session that enriches each work-thread with live external state and writes
the docs.

The split mirrors what already exists in the codebase: entire-tail knows how to
crawl `~/.claude`, group by repo, detect live sessions, and extract Trail/PR
links. The one thing it cannot do well — synthesise prose and reconcile live
state across Linear / GitHub / Entire — is exactly what a full Claude session
with MCP + CLIs does well.

## Goals

- One command produces dated handover docs for today's work, with a human
  confirmation step before anything external is touched.
- Each doc summarises the work and its exact stopping point, and links every
  associated artifact (Entire sessions, Trails, PRs, Linear issues, ADRs/docs).
- Each doc reconciles disagreements between those states (e.g. PR merged but
  Linear still In Progress) with a recommended action.
- Reuse existing entire-tail machinery; add no new runtime dependencies to the
  Go binary.

## Non-goals (v1)

- Codex / Antigravity sessions. v1 is **Claude-only** — the doc is Claude-centric
  ("claude session id to retrieve"). Multi-agent is a later extension.
- Headless / cron operation. v1 runs interactively so MCP auth matches the user's
  normal session (decided during design).
- Automatic reconciliation *actions*. The doc *recommends* reconciliations; it
  never mutates Linear/GitHub/Entire state itself.
- Cross-day rollups or a searchable index of past handovers.

## Architecture

Two halves with a JSON manifest as the interface between them.

```
entire-tail handover
  │
  ├─ (Go) enumerate today's Claude sessions        ── reuses entire.go crawl
  ├─ (Go) live vs ended                            ── reuses picker.go pgrep+lsof
  ├─ (Go) extract Trail/PR link seeds              ── reuses preview.go extractLinks
  ├─ (Go) grouping-picker gate: "found these — look right?"
  │        1-9=group · x=separate · -=skip · Enter=proceed · q=abort
  ├─ (Go) collapse assignment → manifest.json groups[] (temp file)
  └─ (Go) open interactive claude pane             ── reuses iterm.go workspace trick
              │  preloaded prompt: invoke handover-sessions skill @ manifest path
              ▼
       (Claude + skill) per work-thread:
         read transcript → group by work → live-fetch state
         (Linear MCP · gh · entire) → reconcile → write doc to vault
```

### Go side — `handover.go` (new file, `package main`)

Responsibilities (all pure/deterministic, no model):

1. **Enumerate today's sessions.** Reuse the `~/.claude` crawl from `entire.go`
   (`buildSessionTree` base). Filter to sessions whose **last activity is at or
   after local midnight today**. Claude only in v1.
2. **Live vs ended.** Reuse `picker.go`'s `pgrep`+`lsof` live-cwd detection; mark
   each session `live` or `ended`. `pgrep`/`lsof` absent → all `ended` (never
   required, per repo convention).
3. **Link seeds.** Run `extractLinks` (preview.go) over each transcript to seed
   `trailUrls` and `prUrls`. These are *hints* for Claude, not the final list.
4. **Repo + metadata.** cwd, git `origin`-derived `repo`, cloud `title` if the
   `--cloud` cache is warm (never blocks on network), first/last activity ts,
   token totals (`formatTokens` inputs).
5. **Grouping-picker gate** (see below) — the user tags/excludes sessions.
6. **Build manifest** (schema below): collapse the picker assignment into
   `groups[]`, write to a temp file.
7. **Launch the claude pane** with the preloaded prompt (see below).

The today-filter, the gate's group-assignment reducer, and the
assignment→`groups[]` collapse are split as pure functions so they are
unit-tested without a tty — matching the existing `tree.go` build/reduce/render
split.

### Manifest schema

The manifest is **group-oriented** — the picker's grouping decision is baked in,
so the skill writes exactly one doc per group and makes no grouping judgment.

```json
{
  "date": "2026-07-17",
  "generatedAt": "2026-07-17T14:12:03+10:00",
  "vaultHandoverDir": "/Users/dvydra/Library/Mobile Documents/iCloud~md~obsidian/Documents/Handover/2026-07-17",
  "groups": [
    {
      "groupId": "solo-1",
      "sessions": [ { "...session object..." } ]
    },
    {
      "groupId": "g2",
      "sessions": [ { "...ci session A..." }, { "...ci session B..." } ]
    }
  ]
}
```

Each session object:

```json
{
  "sessionId": "656c39a3-0ca2-4f62-bee4-002126eaecdd",
  "agent": "claude",
  "cwd": "/Users/dvydra/src/entirehq/entiredb",
  "repo": "entirehq/entiredb",
  "title": "COR-562 CRDB cutover dry-run",
  "state": "live",
  "firstActivity": "2026-07-17T09:03:11+10:00",
  "lastActivity": "2026-07-17T14:05:52+10:00",
  "tokens": 184213,
  "transcriptPath": "/Users/dvydra/.claude/projects/-Users-dvydra-src-entirehq-entiredb/656c39a3-....jsonl",
  "trailUrls": ["https://entire.io/gh/entirehq/entiredb/trails/abcd"],
  "prUrls": ["https://github.com/entirehq/entiredb/pull/812"],
  "entireSessionIds": ["sess_..."]
}
```

`groupId` is `solo-<n>` for an independent session and `g<digit>` for a
user-tagged group. `title` and `entireSessionIds` are best-effort (empty when the
cloud cache is cold). Everything else is always populated from local data.

### Confirmation gate (grouping-picker)

Reuse the tree TUI (`tree.go` render + a thin reducer). Sessions listed grouped by
repo, each row showing its **group tag**, state marker (live/ended), repo,
title/last-activity, token count. The picker's job is twofold: confirm the set
**and** let the user assign how sessions merge into docs.

Each session is in one of three states, shown as a leading tag:

- `[x]` — **independent**: its own handover doc. **This is the default for every
  session.**
- `[1]`…`[9]` — **grouped**: all sessions sharing a digit merge into one doc.
- `[ ]` — **excluded**: skipped entirely.

Keys:

- **digit `1`–`9`** — tag the highlighted session to that group. Tagging is how
  the user says "these belong together" (e.g. three CI sessions → all `[2]`).
- **`x` / space** — set the highlighted session back to **independent** `[x]`.
- **`-`** — **exclude** the highlighted session `[ ]`.
- **Enter** — proceed with the current assignment.
- **q / Esc** — abort, write nothing, launch nothing.

Header: **"Found N sessions from today — tag with 1-9 to group, x = separate, - =
skip, Enter to write handovers."** On Enter, the reducer collapses the assignment
into the manifest's `groups[]`: each distinct digit is one group; each independent
session is a group of one; excluded sessions are dropped. If nothing remains,
abort with a note.

MVP fallback (no tty / piped): print the grouped list (all independent) and read a
single `y/N` — grouping is a tty-only affordance.

### Launch

Reuse `iterm.go`'s workspace trick: open a claude pane in the current window,
cwd'd to a neutral dir, with the initial command queued to run once entire-tail
exits. The queued command invokes Claude with a prompt that (a) names the
`handover-sessions` skill and (b) passes the manifest path, e.g. the initial
prompt is `/handover-sessions <manifest-path>` (skill args carry the path). The
user watches and can steer; MCP auth is the user's normal session.

### Skill side — `handover-sessions`

A user skill (lives in `~/.claude/skills/handover-sessions/`, versioned outside
the Go binary so the prompt can change without a rebuild). It receives the
manifest path as an argument and executes the prompt below.

## Handover doc — template

One doc per **unit of work** (see grouping). A doc may cover multiple sessions.

```markdown
# <repo> — <short work title>

- **Sessions:** <id1> · <id2> …   (resume: `claude --resume <id1>`)
- **State:** <live | ended>  ·  last activity <ts>

## Summary & where we left it
<2–4 sentences: what the work is, what's done, what's mid-flight, the next
concrete step.>

## Entire sessions
- [[<entire session id / title>]] — <one line>

## Trails & PRs
- <trail/PR link> — <one-line description> — **<current state>** (open/draft/merged/closed, CI status)

## Linear issues
- <KEY> [[<issue title>]] — <one-line description> — **<current status>**

## ADRs / documents / artifacts
- <path or [[wikilink]]> — <what it is>

## ⚠ State mismatches & recommended reconciliation
- <mismatch> → **recommended:** <action>
```

## Skill prompt (prompt-engineered)

> You are generating **handover documents** for a developer's coding-agent
> sessions. You are given a manifest of today's sessions at the path passed as
> your argument (`$1` / the skill args). Read it first.
>
> **Output:** the manifest's `groups[]` is authoritative — **write exactly one
> Markdown file per group**, no more, no fewer. Do not split or merge groups
> yourself; the user already decided the grouping in the picker. Write each file
> to the manifest's `vaultHandoverDir`, named `<repo-basename>--<work-slug>.md`
> (slug = kebab-case of the short work title; for a multi-session group derive one
> slug from the combined work). Create the directory if missing. Overwrite an
> existing file for the same work.
>
> **For each group doc** (a group may hold one or several sessions — read every
> session's transcript in the group and synthesise across them):
> 1. **Summary & where we left it** — read each session's transcript at
>    `transcriptPath`. Write 2–4 sentences: what the work is, what is done, what is
>    mid-flight, and the **next concrete step**. Be specific; name the files,
>    branches, and commands in play.
> 2. **Sessions** — list every merged session id and a `claude --resume <id>`
>    command for the most recent one. Note live vs ended.
> 3. **Entire sessions** — from the manifest's `entireSessionIds` plus any
>    referenced in the transcript.
> 4. **Trails & PRs** — start from the manifest's `trailUrls`/`prUrls`, then scan
>    the transcript for any others. For each, **fetch current state**: PRs via
>    `gh pr view <url> --json title,state,isDraft,statusCheckRollup,body`; Trails
>    via the `entire` CLI. Record title, a one-line description, and the **current
>    state** (open/draft/merged/closed + CI).
> 5. **Linear issues** — scan the transcript for issue keys (e.g. `COR-\d+`,
>    `[A-Z]+-\d+`). For each, fetch via the Linear MCP (`get_issue`): title,
>    one-line description, **current status**.
> 6. **ADRs / documents / artifacts** — from transcript file-write tool calls and
>    mentions: ADRs (`ADR-*`, `docs/adr/*`), design docs, generated artifacts.
>    Link with Obsidian `[[wikilinks]]` where a vault note plausibly exists.
> 7. **State mismatches & recommended reconciliation** — cross-reference the
>    fetched states. Flag disagreements and recommend an action, e.g.:
>    - PR merged but Linear issue still In Progress → *recommend: move issue to Done.*
>    - Trail/PR open but work described as finished → *recommend: open/land the PR
>      or update the Trail.*
>    - ADR proposed in-session but no PR/commit → *recommend: commit the ADR.*
>    - Work says "done" but issue untouched → *recommend: update the issue + link
>      the PR.*
>    If there are no mismatches, write "No mismatches found."
>
> **Rules:**
> - Never invent external state. If a fetch fails or an entity can't be resolved,
>   write "state unknown (fetch failed)" and move on.
> - Use Obsidian `[[wikilinks]]` for titles that are (or should be) vault notes.
> - Keep each doc scannable — short bullets, no filler.
> - After writing all docs, print a one-line summary per file written.

## Output & idempotency

- Directory: `<vault>/Handover/YYYY-MM-DD/`.
- File: `<repo-basename>--<work-slug>.md`, one per work-thread.
- Re-running the same day overwrites the day's files in place (safe to re-run
  after more work).

## Assumptions

- **Today** = last activity at/after local midnight.
- **Claude sessions only** in v1.
- **All repos** in the crawl, not just Entire repos.
- Vault root: `/Users/dvydra/Library/Mobile Documents/iCloud~md~obsidian/Documents/`.
- Linear (MCP), `gh`, and `entire` are reachable in the launched session (they
  are in the user's normal environment).

## Testing

- **Go:** unit tests for the today-filter, the gate's group-assignment reducer
  (digit tags, `x`, `-`, collapse into `groups[]`), and the manifest builder (fed
  a fixture crawl) — all pure, no tty, matching the `tree.go` convention. The new
  picker screen gets its own small render test.
- **Skill:** manual — it drives live MCP/CLI and writes to the real vault. A dry
  fixture manifest can exercise the transcript-reading + grouping without external
  calls by pointing at test transcripts and a temp output dir.

## Risks / open questions

- **Grouping quality** depends on Claude's judgement; the "when unsure, keep
  separate" rule biases safe.
- **Trail fetch** — exact `entire` CLI command for a Trail's current state needs
  confirming during implementation (the `entire` CLI surface).
- **iCloud vault** — writing into the iCloud-synced folder is fine locally;
  sync/conflict is iCloud's problem, not ours.
- **Interactive pane on non-iTerm terminals** — v1 targets the existing iTerm
  workspace path; other terminals fall back to printing the command to run.
