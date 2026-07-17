<!-- Reference copy of the `handover-sessions` skill installed at
     ~/.claude/skills/handover-sessions/SKILL.md — launched by `entire-tail handover`.
     Kept here so the prompt is reviewable in-repo; the installed copy is authoritative. -->

---
name: handover-sessions
description: Generate Obsidian handover docs from an entire-tail session manifest. Use when invoked by `entire-tail handover` with a manifest path, or when asked to write session handover docs from a manifest.
---

# Handover Sessions

Turn a manifest of today's coding-agent sessions into one Obsidian handover
document per group. `entire-tail handover` invokes you with the manifest path in
the prompt.

## Input

A JSON manifest whose path is given in the invocation. **Read it first** with the
Read tool. Shape:

```json
{
  "date": "2026-07-17",
  "vaultHandoverDir": "/…/Obsidian/Documents/Handover/2026-07-17",
  "groups": [
    { "groupId": "g2", "sessions": [ { …session… }, { …session… } ] },
    { "groupId": "solo-1", "sessions": [ { …session… } ] }
  ]
}
```

Each session: `sessionId`, `repo`, `cwd`, `title`, `state` (live|ended),
`lastActivity`, `tokens`, `transcriptPath`, `trailUrls[]`, `prUrls[]`,
`entireSessionIds[]`.

## Output

Write **exactly one Markdown file per `groups[]` entry** — the user already chose
the grouping in the picker. **Never split or merge groups.** Write each file to
the manifest's `vaultHandoverDir` (create the dir if missing), named
`<repo-basename>--<work-slug>.md` where:

- `repo-basename` = the last path segment of the group's `repo` (e.g. `entiredb`).
- `work-slug` = kebab-case of a short title for the work; for a multi-session
  group, derive one slug from the combined work.

Overwrite an existing file for the same work (handover is safe to re-run).

## Per-group procedure

A group holds one or more sessions. **Read every session's transcript
(`transcriptPath`) in the group and synthesise across them.**

1. **Summary & where we left it** — 2–4 sentences: what the work is, what's done,
   what's mid-flight, and the **next concrete step**. Be specific — name the
   files, branches, and commands in play.
2. **Sessions** — list every session id in the group; give a
   `claude --resume <id>` command for the most recent one; note live vs ended.
3. **Entire sessions** — from each session's `entireSessionIds` plus any
   referenced in the transcript.
4. **Trails & PRs** — start from `trailUrls`/`prUrls`, then scan the transcript
   for any others. Fetch **current state** for each:
   - PR: `gh pr view <url> --json title,state,isDraft,statusCheckRollup,body`
     → title, one-line description, state (OPEN/MERGED/CLOSED + draft), CI rollup.
   - Trail: URL shape is `https://entire.io/gh/<owner>/<repo>/trails/<id>`. Run
     `entire trail show <id> --repo gh/<owner>/<repo>` → title, state, branch.
   Record link · one-line description · **current state**.
5. **Linear issues** — scan transcripts for issue keys (`COR-\d+`, `[A-Z]+-\d+`).
   For each, fetch via the Linear MCP `get_issue` → title, one-line description,
   **current status**.
6. **ADRs / documents / artifacts** — from transcript file-write tool calls and
   mentions: ADRs (`ADR-*`, `docs/adr/*`), design docs, generated artifacts. Link
   with `[[wikilinks]]` where a vault note plausibly exists.
7. **State mismatches & recommended reconciliation** — cross-reference the fetched
   states and flag disagreements, each with a recommended action:
   - PR merged but Linear issue still In Progress → *recommend: move issue to Done.*
   - Trail/PR open but work described as finished → *recommend: land the PR / update the Trail.*
   - ADR proposed in-session but no PR/commit → *recommend: commit the ADR.*
   - Work says "done" but the issue is untouched → *recommend: update the issue + link the PR.*
   If there are none, write "No mismatches found."

## Rules

- **Never invent external state.** If a fetch fails or an entity can't be
  resolved, write "state unknown (fetch failed)" and move on.
- Use Obsidian `[[wikilinks]]` for titles that are (or should be) vault notes.
- Keep each doc scannable — short bullets, no filler.
- After writing all files, print a one-line summary per file written.

## Doc template

```markdown
# <repo> — <short work title>

- **Sessions:** <id1> · <id2> …   (resume: `claude --resume <id1>`)
- **State:** <live | ended>  ·  last activity <ts>

## Summary & where we left it
<2–4 sentences>

## Entire sessions
- [[<entire session id / title>]] — <one line>

## Trails & PRs
- <link> — <one-line description> — **<current state>**

## Linear issues
- <KEY> [[<issue title>]] — <one-line description> — **<current status>**

## ADRs / documents / artifacts
- <path or [[wikilink]]> — <what it is>

## ⚠ State mismatches & recommended reconciliation
- <mismatch> → **recommended:** <action>
```
