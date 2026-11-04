---
name: thread-handover
description: Update a long-running thread in the Obsidian vault at the end of a session. Use when Daniel says "handover", "hand over", "/handover", "update the thread", or "park this thread". Works from Claude and Amp. Uses only `entire-tail threads` subcommands.
---

# Thread handover

A thread is one piece of long-running work: a folder in the vault with `thread.md` (what is true now), `knowledge.md` (decisions, gotchas) and `log/<date>.md` (what happened, by day). A handover updates that thread. It does not write a new dated doc.

Never edit `.registry.json` and never change `status` in the frontmatter yourself. Status moves only through `park`, `unpark`, `close`, `reopen`. Plain words, no em dashes, no hard-wrapped lines (one paragraph per line).

First check the feature is on:

```bash
entire-tail threads enabled
```

If it exits non-zero, stop and tell Daniel threads are not enabled.

## 1. Find your agent and session id

Gather facts first. Never guess a value.

```bash
date +%Y-%m-%d
git rev-parse --show-toplevel
git branch --show-current
git rev-parse --git-common-dir
git log --oneline -3
entire-tail threads path
```

`git rev-parse --git-common-dir` differs from `.git` when you are in a worktree.

**Claude:** agent is `claude`. The session id is the UUID in your scratchpad path, `/private/tmp/claude-*/<project-slug>/<SESSION-ID>/scratchpad`.

**Amp:** agent is `amp`. The session id is the thread id, `T-` followed by a UUID. Amp does not pass it in the prompt. Try, in order:

```bash
echo "$AMP_CURRENT_THREAD_ID"
echo "$AMP_THREAD_ID"
```

Both names appear in the Amp binary, but neither is confirmed set in an interactive session. If both are empty, run `amp threads list --json --limit 5`, pick the thread whose title matches this conversation, and confirm it with Daniel. If you still cannot tell, ask him for the `T-…` id. Do not guess.

## 2. Match the session to a thread

```bash
entire-tail threads lookup <agent> <session-id>
```

It prints `{"slug": "..."}` and exits 0 on a hit, exits 1 on a miss. Then check the thread's status with `entire-tail threads list --all` (look the slug up in the JSON).

On a hit:

- open: use it. Do not ask.
- parked: say so, and ask "This thread is parked (<reason>). Unpark it?" The default is no. Write the update either way. Only on a yes run `entire-tail threads unpark <slug>`.
- closed: ask whether to reopen it or start a new thread. On reopen run `entire-tail threads reopen <slug>`. Nothing changes status without that answer.

On a miss:

1. Run `entire-tail threads list`. Compare this session to the open and parked threads in this order: trail (the `trails` and trail claims), then worktree (the `claims` with `kind: worktree`), then topic. Propose the best match: "This looks like <name>. Update it?" Daniel confirms, picks another, or says new.
2. For a new thread, propose a name from the work and let Daniel accept or edit it. Then run `entire-tail threads new "<name>"`. It prints the slug. Exit 2 means the slug exists; ask Daniel what to do.
3. If the session drifted across topics, ask once which thread to file it under. One session is never split across two threads.

After any outcome, record the mapping:

```bash
entire-tail threads register <slug> <agent> <session-id>
```

To pause a thread Daniel asks you to park, run `entire-tail threads park <slug> "<reason>"`. Daniel decides; do not park on your own.

## 3. Write the update

```bash
entire-tail threads begin <slug>
```

It prints JSON: `base` (a hash), `dir` (the thread folder) and `thread_md` (the current text). Keep `base`.

Rewrite `thread.md` from scratch. Inputs: the old `thread.md`, this session, and live state (git status, the trail, the Linear issue). The rule is "only what's true now". Fetch real state with `git`, `entire trail show` and Linear. Never write state from memory. If a fetch fails, write "state unknown (fetch failed)".

Write the new file to a scratch path outside the vault, not into `dir`. Keep the frontmatter keys below. Leave `status`, `park_reason` and `parked_at` as they were in the old file; commit forces them back anyway.

```markdown
---
name: Mirror GitHub checks
status: open
waiting_on: review from Sam on the trail
needs_you: true
customer: acme
linear:
  - ENT-482
trails:
  - gh/entirehq/entiredb/1234
claims:
  - kind: worktree
    path: /Users/dvydra/src/entirehq/entiredb/.claude/worktrees/mirror-checks
    branch: mirror-checks
    head: 9f3c2ab
  - kind: trail
    trail: gh/entirehq/entiredb/1234
    expect: open
  - kind: linear
    linear: ENT-482
    expect: In Review
sessions:
  - agent: claude
    id: 0b6e5f3a-1111-4222-8333-444455556666
created: 2026-10-30
updated: 2026-11-04
as_of:
  What it is: 2026-11-04
  Where it got to: 2026-11-04
  What's in the way: 2026-11-04
  People: 2026-11-04
  Recent: 2026-11-04
---

## What it is

What the work is and why it matters.

## Where it got to

Current state in plain words. Bold the facts someone would otherwise re-derive: ids, branch names, which of two things is live.

## What's in the way

The next step. Mark it **needs you** when it needs Daniel.

## People

Who it waits on or overlaps with.

## Recent

- 2026-11-04: one line.
- 2026-11-03: one line.
- 2026-11-02: one line.
```

Rules for the file:

- Five sections, in this order: What it is, Where it got to, What's in the way, People, Recent. No length limit, but no padding.
- `waiting_on` is one line: who or what the thread waits on. `needs_you` is true only when the next step is Daniel's.
- Claims are what triage checks against live state. Write one for every worktree (`path`, `branch`, and `head` set to the pushed SHA, or empty for unpushed), every trail (`expect`: `open`, `merged` or `closed`), and every Linear issue (`expect`: its current state name).
- Set `as_of` to today for every section you rewrote. Leave the old date on a section you did not touch. Triage dims a section older than 7 days.
- Recent holds the last 3 working days only, one line each. Older lines move to the log.
- `sessions` lists agent and id, newest first. Keep what is there; `register` already added this one.

Nothing is lost by the rewrite. In `<dir>/log/<today>.md` (create `log/` if needed; if today's file exists, add to it, do not overwrite) put what is leaving `thread.md`: finished steps, old SHAs, resolved blockers, and a short account of this session.

Revise `<dir>/knowledge.md`; do not just append. Add new decisions and gotchas. Replace a decision this session reversed: move the old one to a `## Rejected` section as one line with the date and `see log/<date>`. Create the file if missing.

Then commit the thread:

```bash
entire-tail threads commit <slug> --base <base> --file <path-to-new-thread.md>
```

- Exit 0: done.
- Exit 3: someone else wrote the thread since `begin`. Stdout is the current `begin` JSON. Merge your changes onto that `thread_md`, then commit again with the new `base`. Retry once. If it is stale again, stop and tell Daniel.
- Exit 4: the frontmatter does not parse. Fix the YAML and commit again.

## 4. Finish

Print the thread path (`<dir>/thread.md`) and its `waiting_on` line, and nothing else.
