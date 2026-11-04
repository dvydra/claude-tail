---
name: threads-bootstrap
description: One-time bootstrap that turns the open dated handover docs in the Obsidian vault into threads. Use when Daniel says "bootstrap threads", "convert handovers to threads", or "/threads-bootstrap". Uses only `entire-tail threads` subcommands. Run once, after the threads feature has merged.
---

# Threads bootstrap

This turns the open dated handover docs into threads, once. Read `thread-handover`'s skill first (`~/.agents/skills/thread-handover/SKILL.md`). For each thread you write, follow its `## 3. Write the update` (its `begin`, `thread.md` rules, `knowledge.md` rules, `commit` and exit codes) instead of inventing your own. Skip its `## 1` and `## 2`: there is no live session to find or match here, the groups below replace them. Only what differs for a bootstrap is below.

Plain words, no em dashes, no hard-wrapped lines.

Hard rules:

- Never edit, move or delete a source handover doc. Read only.
- Write only through `entire-tail threads` subcommands, plus `knowledge.md` and `log/<date>.md` inside the thread dir that `begin` returns.
- Do not create `.enabled`. Daniel does that himself at the end.
- Write nothing until Daniel confirms the grouping table.

First check the feature is built in:

```bash
entire-tail threads path
```

`path` does not depend on `.enabled`, so a failure means the binary has no threads support. Say "the threads command is not available: run install.sh from the threads build" and stop.

## 1. Find the docs

The vault root is `${ENTIRE_TAIL_HANDOVER_VAULT:-$HOME/Library/Mobile Documents/iCloud~md~obsidian/Documents}`. Day folders are `<vault>/Entire/Handover/<YYYY-MM-DD>/`.

List every day folder and the `*.md` files directly inside each. Use `/bin/ls` (plain `ls` adds colour codes). Only top-level `*.md` files count. Skip the `complete/` and `grouped/` subfolders. Do not skip any day folder: this reads all of them, not just yesterday.

## 2. Read every doc

Read each doc in full. Up to about 10 docs, read them yourself. Past that, give batches to subagents (read-only: they report, they write nothing) and have each return, per doc: file name, date (the folder name), title, Sessions line, Repo / branch line, Tracker, `Continues [[...]]` links, the Next step list, and whether any next step is still open.

For each doc note:

1. Working dir (the path on the Repo / branch line) and repo.
2. Tracker: trail, Linear issue, plan file.
3. `Continues [[...]]` links and wikilinks to other docs.
4. Open next steps, and which are marked **needs you**.
5. The Sessions line: `- **Sessions:** ` followed by Claude ids (a UUID in backticks) and Amp ids (`T-` plus a UUID).

Drop docs with nothing left to do: every next step done, or the doc says the work was abandoned or merged with no follow-up. List the dropped docs under the table below so Daniel can overrule.

## 3. Group

Put two docs in the same group when any of these hold:

1. Same trail, Linear issue, or plan file on the Tracker line.
2. One `Continues` or links the other, or both continue the same earlier doc.
3. Same worktree path.
4. Same repo and scope, and one doc's next step depends on the other's (a rebase onto it, a review it's waiting on, a shared migration).

Keep docs apart when they only share a repo or a scope word. Two unrelated fixes in one repo are two groups.

A doc with no Sessions line still gets grouped. Register nothing for it, and say so in the table.

## 4. Check existing threads, show the table and stop

Before the table, check what already exists (read-only):

```bash
entire-tail threads list --all
entire-tail threads lookup <agent> <id>
```

Run `lookup` for every session id in the docs. A hit means that session is already in a thread (from an earlier run, or one Daniel made by hand). Also compare each group against the listed threads by trail, worktree and topic.

Print one table: proposed thread name, the source docs (date and file name), the working dir, which sessions are already threaded (with the slug), and an action for the group: `new`, `append to <existing slug>`, or `skip` (already fully covered). Under it, list the dropped docs and why, and the docs with no Sessions line. Then stop and ask Daniel to confirm, merge, split, rename or drop rows.

Apply his edits and print the table again. Repeat until he says go. Run no subcommand that writes (`new`, `register`, `begin`, `commit`) before that.

## 5. Write each thread

For each confirmed group, by its action. `skip` rows do nothing. For `append to <slug>` rows, do not run `new`: go straight to register, begin and commit on that slug, and merge into its existing `thread.md` and `knowledge.md` rather than replacing them. For `new` rows, in order:

```bash
entire-tail threads new "<name>"
```

It prints the slug. Exit 2 means the slug exists: stop and ask Daniel (a rerun after a partial bootstrap lands here; skip groups he says are done).

Register every session from every doc in the group that is not already registered to that thread, newest doc first, so `sessions` ends up newest first:

```bash
entire-tail threads register <slug> <claude|amp> <id>
```

Claude ids are the UUIDs, agent `claude`. Amp ids start with `T-`, agent `amp`. A session already registered to another thread should have shown up in the table; if one slips through, that is a conflict: stop and ask Daniel which thread keeps it.

Then run `entire-tail threads begin <slug>` and write the files per `thread-handover`'s `## 3. Write the update`, with these differences:

- The source is the group's docs, not a live session. Fetch live state (git, trail, Linear) as that skill says; a failed fetch is "state unknown (fetch failed)".
- `waiting_on`, `needs_you` and the open steps under "What's in the way" come from the newest doc's open next steps. A step marked **needs you** sets `needs_you: true`.
- Claims come from the newest doc's Repo / branch line (a worktree claim) and Tracker line (trail and Linear claims), checked against live state.
- Set `created` to the oldest doc's date, `updated` and every `as_of` to today.
- Recent holds the last 3 source dates, one line each.
- Write one `log/<date>.md` per source doc date. If two docs share a date, put both in that one file. Each log holds what that doc recorded: state, decisions, finished steps. Create `log/` if needed.
- `knowledge.md` collects the decisions and gotchas across all the group's docs. Resolve contradictions toward the newest doc and move the losing one to `## Rejected`.

Then commit:

```bash
entire-tail threads commit <slug> --base <base> --file <path-to-new-thread.md>
```

Handle exits 3 and 4 as `thread-handover` does. Move on to the next group only after a commit succeeds.

## 6. Finish

Print a short list: each thread's slug, path and `waiting_on`. Then end with this as your last line, and do not run it:

```
touch "$(entire-tail threads path)/.enabled"
```

Say that running it switches the handover paths over to threads.
