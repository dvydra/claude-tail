---
name: handover
description: Hand over this session. With threads enabled, update its long-running thread in the Entire vault (thread.md, knowledge, log); otherwise write an Obsidian handover doc (where we left it, next step, loose ends) under today's date.
---

Arguments: [optional scope hint or title, e.g. "focus on the RFD, not the CI porting"]

0. Run `entire-tail threads enabled`. If it exits 0, load and follow the `thread-handover` skill instead of everything below, and stop. If it exits with anything else (including the command not existing), continue with the flow below unchanged.

Write a handover document for **this session** into the Obsidian vault.

SCOPE HINT (may be empty): (whatever the user said when invoking this skill)

The reader is a colleague — or you, in three weeks — with none of this conversation in context. Everything they need to pick the work up has to be on the page.

## 1. Gather metadata first

Run these before writing anything. Never guess a value; if one can't be resolved, say so on the page.

```bash
date -Iseconds                    # last-activity stamp, e.g. 2026-08-16T14:24:17+10:00
date +%Y-%m-%d                    # today's folder
git rev-parse --show-toplevel     # repo root
git branch --show-current         # branch
git rev-parse --git-common-dir    # differs from .git when in a worktree
git log --oneline -3              # what landed
```

**Session id** is the UUID in your scratchpad path (`/private/tmp/claude-*/<project-slug>/<SESSION-ID>/scratchpad`). Cross-check it against the newest transcript:

```bash
ls -t ~/.claude/projects/<project-slug>/*.jsonl | head -1
```

If this session spans more than one repo or worktree, list each one.

## 2. Choose the path

```
<vault>/Entire/Handover/<YYYY-MM-DD>/<YYYY-MM-DD>--<scope>--<slug>.md
```

`<vault>` is the Obsidian vault root, the same one `restore-handovers` reads from:

```bash
echo "${ENTIRE_TAIL_HANDOVER_VAULT:-$HOME/Library/Mobile Documents/iCloud~md~obsidian/Documents}"
```

`mkdir -p` the date folder.

- `<scope>` — the system or repo the work is about: `entiredb-ci`, `ci-webhooks`, `fleet-infra`, `security`, `datadog`. Not a person, not a session id.
- `<slug>` — 3–6 real words describing **what the work was about**, kebab-case. Never a session id, never a bare work word (`work`, `session`, `wip`, `misc`, `adr`, `handover`), never a slash-command name.

Good: `2026-08-15--entiredb-ci--automation-principal-flip-executed.md`, `2026-08-14--security--xbow-staging-triage.md`
Bad: `2026-08-16--entiredb--work.md`, `2026-08-16--adr-fc38828c.md`

Re-running is safe — overwrite the file for the same work rather than making a second one.

**One doc per thread of work, not one per session.** If the session covered two unrelated threads, write two docs. If the scope hint names one, write only that one.

## 3. Check for a predecessor

```bash
ls "<vault>/Entire/Handover/" | tail -5
```

Skim the last few days' folders. If a note covers the same thread, link it near the top as `Continues [[<note-name-without-.md>]] — <one line on what changed since>`, and say plainly whether its "Next step" is now done, still open, or was abandoned.

## 4. Structure

```markdown
# <scope> — <what actually happened> (<the key detail>)

<Optional single line of framing: repos touched, "no code changed — triage only", etc.>

- **Sessions:** `<uuid>`   (resume: `claude --resume <uuid>`)
- **Tab:** <catchy 1–4 word title for the work, e.g. Mirror Parity Hunt>
- **State:** live  ·  last activity <ISO8601 from `date -Iseconds`>
- **Repo / branch:** `<absolute working dir, e.g. /Users/dvydra/src/entirehq/infra>` — `<repo>` on `<branch>`, <worktree or main checkout>
- **Tracker:** <plan file, Linear epic, program doc — omit if none>
- Continues [[<earlier note>]] — <what changed since>

## Where we left it

2–5 sentences. What the work is, what is done, what is mid-flight. **Bold the facts someone would otherwise have to re-derive** — IDs, build numbers, branch names, which of two things is the live one. Name the exact files and commands in play.

## Next step

1. Numbered, concrete, ≤5. Each one starts with a verb and says where.
2. Give the actual command when there is one, not a description of it.
3. Mark anything that needs the human — an approval, a console UI, a secret — as **needs you**.

## Merged / opened today

- repo **#NNN** — one line on what it does — **OPEN / MERGED / DRAFT**
- Fetch real state, never remember it: `gh pr view <n> --repo <owner>/<repo> --json title,state,isDraft`

## <Session-specific section — use the real name>

The thing that would otherwise be lost. Pick the honest heading:
`## The two traps this hit`, `## What we verified`, `## Decisions`, `## Why we parked it`.
This is usually the most valuable part of the document. Include the reasoning, not just the outcome.

## Loose ends

- Known-open things that are not the main thread but will bite someone.
- Environment gotchas, half-finished branches, rotated credentials, things left uncommitted.
- Say who or what is blocked, and on what.
```

Drop any section that would be empty. Do not pad.

## 5. Rules

- **Never invent external state.** Fetch PR, issue and build status. If a fetch fails, write "state unknown (fetch failed)".
- **Concrete over abstract.** `svc_01KSS2K32Z48SNRSYEGH9BPJG3`, `build #26`, `core/oidcfed/service.go:134`. A handover full of nouns like "the service" is useless.
- **Record what was decided against, and why.** The rejected option is what stops the next person redoing the analysis.
- **Uncommitted work is a loose end.** Name the path and the branch.
- **The Repo / branch line starts with the absolute working dir** (`/Users/...`, the worktree path if in one), and no other local path appears above it. `restore-handovers` opens the first local path in the doc; a doc that only names the repo in words (`infra`, `entirehq/infra`) gets skipped.
- **The Tab line is 1–4 words, plain text, no backticks or links.** Catchy and specific to this thread of work (`Mirror Parity Hunt`, `Vanta Test Rework`), not the scope or a generic word. `restore-handovers` uses it as the iTerm tab title.
- **No hard-wrapped lines** — one sentence or paragraph per line; Obsidian wraps.
- **Wikilinks** (`[[note-name]]`) for other vault notes; full URLs for PRs, builds, dashboards.
- Match the register of the existing docs: terse, specific, no preamble, no "in this session we".

## 6. Finish

Print the full path of the file you wrote, and nothing else.
