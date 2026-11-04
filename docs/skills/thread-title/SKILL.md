---
name: thread-title
description: Name this session's iTerm tab after its thread of work. Use when Daniel says "/thread-title", "title this tab", "name the tab", or "set the tab title". An optional argument is a title to use verbatim.
argument-hint: "[title]  (default: pick a 1 to 3 word title from the thread)"
---

# Thread title

Put a short title on the iTerm tab this session runs in. The tab, not the pane.

ARGUMENT (may be empty): $ARGUMENTS

## 1. Choose the title

If the argument is not empty, it is the title. Use it verbatim and skip to step 2.

Otherwise find this session's thread. The agent is `claude` and the session id is `$CLAUDE_CODE_SESSION_ID`, or the UUID in your scratchpad path (`/private/tmp/claude-*/<project-slug>/<SESSION-ID>/scratchpad`). In Amp the agent is `amp` and the id is the `T-` thread id.

```bash
entire-tail threads lookup claude "$CLAUDE_CODE_SESSION_ID"
```

On a hit it prints `{"slug": "..."}`. Read the thread's `name` and `thread.md` (the folder is `$(entire-tail threads path)/<slug>`). On a miss, or if threads are not enabled, use the work this session is doing: the branch, the trail, what Daniel asked for.

Pick a title of 1 to 3 plain words that names the work. No codenames, no ticket or trail numbers, no slugs. If the thread name is longer than it needs to be, shorten it.

## 2. Set it

```bash
entire-tail threads title <title>
```

It exits 0 and does nothing outside iTerm. A non-zero exit means the iTerm Python API call failed: say so in one line.

## 3. Reply

Reply with the title alone. Nothing else.
