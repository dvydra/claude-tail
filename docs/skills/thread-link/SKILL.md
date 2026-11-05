---
name: thread-link
description: Link this session to a long-running thread in the vault, or make a new thread for it, at any point in a session. Use when Daniel says "/thread-link", "link this to a thread", "link this to a relevant thread", "file this under a thread", "make a thread for this", or "which thread is this". An optional argument names the thread to use. Uses only `entire-tail threads` subcommands.
argument-hint: "[thread name or slug]  (default: find the best match)"
---

# Thread link

Attach this session to a thread so triage, `threads list` and the next handover know about it. This only records the link. It does not write `thread.md`; `/handover` does that at the end of the session.

ARGUMENT (may be empty): $ARGUMENTS

Never edit `.registry.json` and never change `status` yourself. Plain words, no em dashes.

```bash
entire-tail threads enabled
```

If it exits non-zero, stop and tell Daniel threads are not enabled.

## 1. Find your agent and session id

**Claude:** agent is `claude`. The session id is `$CLAUDE_CODE_SESSION_ID`, or the UUID in your scratchpad path (`/private/tmp/claude-*/<project-slug>/<SESSION-ID>/scratchpad`).

**Amp:** agent is `amp`. The session id is the `T-` thread id. Try `echo "$AMP_CURRENT_THREAD_ID"` and `echo "$AMP_THREAD_ID"`. If both are empty, run `amp threads list --json --limit 5`, pick the thread whose title matches this conversation and confirm it with Daniel. Do not guess.

## 2. Already linked?

```bash
entire-tail threads lookup <agent> <session-id>
```

Exit 0 prints `{"slug": "..."}`. Tell Daniel "Already linked to <slug>." If the argument names a different thread, or this session has clearly moved on to other work, ask once: "Move it to <other>?" A session belongs to one thread; on a yes, register it under the new one (step 4). Otherwise stop here.

Exit 1 means not linked. Go on.

## 3. Pick the thread

Gather facts first:

```bash
git rev-parse --show-toplevel
git branch --show-current
entire-tail threads list
```

If the argument is not empty, find the thread in the list whose slug or name matches it. One clear match: use it without asking. No match: treat the argument as the name for a new thread and confirm it.

Otherwise compare this session to the open and parked threads, in this order: trail (`trails` and trail claims), then worktree (`claims` with `kind: worktree`, matching the toplevel and branch above), then topic. Then ask one question with the best match first:

- "This looks like <name>. Link it?" with up to two other plausible threads, plus "New thread".

For a new thread, propose a short name from the work and let Daniel accept or edit it:

```bash
entire-tail threads new "<name>"
```

It prints the slug. Exit 2 means the slug exists; ask Daniel whether to link to that one instead.

A parked thread: link it anyway and say it is parked. Do not unpark it unless Daniel asks (`entire-tail threads unpark <slug>`). A closed thread: ask whether to reopen it (`entire-tail threads reopen <slug>`) or start a new one. No answer means link nothing.

## 4. Record the link

```bash
entire-tail threads register <slug> <agent> <session-id>
```

## 5. Finish

Print one line: `Linked to <slug> (<status>). /handover will update it.` Then carry on with whatever the session was doing before.
