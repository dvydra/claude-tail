# Threads: long-running named work in entire-tail

Status: design, approved pending review. Date: 2026-11-04.

## Why

Daniel's handovers work well for one day's work. They break down for work that runs across many days: work he picks up several days in a row, looks at and parks, or does a little on and parks again.

Three problems, seen in the vault (`Entire/Handover/`):

1. **Every handover is a new snapshot, never an update.** One piece of work becomes a chain of dated docs linked by `Continues [[...]]`. "Mirror GitHub checks" is 4 docs over 8 days (10-28, 10-29, 10-30, 11-04). Each new doc spends its first lines re-stating trackers and worktrees and doing bookkeeping on the previous doc's steps ("step 1 still open, step 3 not looked at").
2. **Two handovers for the same work don't know about each other.** Nothing on the page conflicts. The clash comes on restore: each doc opens its own workspace, and two agents land in the same worktree.
3. **Knowledge and status are mixed in every doc.** "Decisions" sections hold long-lived knowledge, but each day re-copies, drops or reverses some of it, and no single place holds the current version. Closing work is a manual sweep after the fact: on 11-03 Daniel audited 176 docs and moved 154 to `complete/`, and found two pieces of lost work that way.

`restore-handovers` already merges a day's docs into one per piece of work, but throws the grouping away; the next day works it out again from scratch.

## What a thread is

A thread is a named piece of work Daniel owns. Its identity is its name, not a branch, trail or ticket: sometimes it maps to a task, sometimes to a customer. Over its life it collects sessions, worktrees, trails and Linear issues. Its doc becomes the place that holds what is known about the topic. Threads eventually close.

Lifecycle: **open → parked → closed**, and parked can go back to open. Daniel parks and closes threads himself. Nothing parks or closes a thread automatically.

## Goals

1. One living doc per thread that is rewritten on each handover, so two handovers on the same day update the same doc.
2. A morning triage screen showing every open thread with enough context to decide in seconds which to pick up and which to park.
3. One workspace per thread on restore, so two agents are never sent into one worktree by a restore.
4. Knowledge that accumulates per thread and stays current: reversed decisions are replaced, not left beside the new one.
5. Status prose that stays fresh and does not bloat with stale info.

## Non-goals

- Automatic parking, closing or waking of threads. Daniel decides.
- Replacing Linear for customer work. Linear stays the record; a customer thread links to its issues.
- Changing how the live tail, tree or `--live` view work.
- A web UI. Obsidian is where the files are read outside the TUI.

## Anti-goals (would make this a failure even if it works)

- **Terse triage.** A list of half-sentences or a wall of SHAs and counts. Daniel asked for "more context, less data": the screen must explain, in prose, what each thread is and what is in the way.
- **Cramming for space.** Assume plenty of screen. The detail pane scrolls; it never truncates prose to fit.
- **Bloat.** A `thread.md` that grows a section per day and ends up full of things that were true last week.
- **Silent guessing about which thread a session belongs to.** A wrong guess files work in the wrong place; asking is cheaper.
- **Two workspaces for one thread** from any restore path.

## Constraints

- Lives in **entire-tail** (Go, single `package main`), following the repo's existing patterns: pure build/reduce/render split from a thin tty driver (`tree.go`, `handover_picker.go`, `wtf_view.go`), IO behind package vars so `go test` never touches iTerm, launchd or the real vault.
- Files live in the existing Obsidian vault (`$ENTIRE_TAIL_HANDOVER_VAULT`, default the iCloud vault, see `handoverVaultDir` in `handover.go`), as structured folders and files.
- **Go for everything Daniel looks at or presses keys in; Claude for everything that needs writing.** entire-tail never calls a model API. When writing is needed it execs `claude` with a skill queued as the first prompt, the way `entire-tail handover` launches `handover-sessions` today.
- Repo conventions apply: tests for pure functions, goldens untouched unless deliberate, `go vet` and `-race` clean, no new runtime dependencies.

## File layout (decided)

```
<vault>/Entire/Threads/
  <thread-slug>/
    thread.md        status prose + frontmatter; what triage shows
    knowledge.md     decisions, rejected options, gotchas, how things work
    log/
      YYYY-MM-DD.md  one file per day worked; a second handover that day adds to it
  _closed/
    <thread-slug>/   whole folder moved here on close
  .registry.json     session → thread map (see Matching)
```

`thread.md` frontmatter (fields decided; exact key names are the builder's call):

- name (display), slug
- status: open | parked | closed
- park reason and park date (when parked)
- trackers: list of trails (forge + repo + number), Linear issues (a customer thread links to one or more over time), plan files
- worktrees: list of paths, each with repo and branch
- customer (optional)
- sessions: list of agent + session id, newest first
- created, updated
- a per-section "as of" date for each prose section

`thread.md` body, five prose sections in this order, no length limit:

1. **What it is**: what the work is and why it matters.
2. **Where it got to**: current state, in plain words.
3. **What's in the way**: the next step, marked when it needs Daniel.
4. **People**: who it waits on or overlaps with.
5. **Recent**: the last 3 working days, one line each.

## Freshness rules (decided)

1. `thread.md` is **rewritten from scratch** on every handover from three inputs: the previous `thread.md`, the session, and live state. The instruction is "only what's true now".
2. Nothing is lost by the rewrite: finished steps, old SHAs and resolved blockers go to `log/<date>.md`; decisions go to `knowledge.md`.
3. "Recent" holds the last 3 working days only; older lives in `log/`.
4. Each prose section carries an "as of" date. Triage dims a section not rewritten in 7 days and labels it "as of <date>".
5. Triage compares the thread's written claims to live state and flags mismatches (says pushed but the branch isn't; says waiting on review but the trail merged). A mismatch replaces the thread's list line status with "stale: <claim> disagrees with <source>". Go can't read prose, so (my call) the handover writes **machine-checkable claims** into frontmatter alongside the prose, and triage checks those. First build covers: per worktree, expected branch and pushed head SHA (or "unpushed"); per trail, expected state (open / merged / closed); per Linear issue, expected state. Other claim kinds can follow.
6. `knowledge.md` is revised, not appended blindly. A reversed decision is replaced; the old one becomes a one-line entry under "Rejected" with the date and a pointer to the log.

## Triage screen (decided): `entire-tail threads`

Layout A from the brainstorm, in the terminal, alt-screen, Go only, opens instantly (reads files plus live state; no Claude).

- **Top: list**, one line per open thread: name, then in words what it's waiting on ("waiting on you: a design call before rebasing", "waiting on Pat's review", "at risk: work exists only on this laptop"), then age. Parked threads collapsed at the bottom with their park reason.
- Where the list text comes from (my call): a short Claude-written `waiting_on` frontmatter field plus a `needs_you` flag, written by each handover. Go overrides it, in this order: (1) "stale: …" from a claim mismatch, (2) a live risk Go detects itself (uncommitted work with no remote, conflicting, a running session), (3) the written `waiting_on`.
- **Below: detail pane** for the selected thread: the five prose sections from `thread.md`, rendered, scrolling, never truncated.
- **Keys:** `↑↓` select, scroll the detail pane (builder picks the keys), `⏎` open the thread's workspace, `p` park (prompts for a one-line reason, writes frontmatter + a log line directly, no Claude), `x` close (asks to confirm, moves folder to `_closed/`), `u` unpark (my call on the key), `d` details (SHAs, worktree paths, counts, live facts), `k` show `knowledge.md`, `/` filter, `q` quit.
- **Live facts** checked when the screen opens: per worktree dirty/unpushed/behind/conflicting, trail state, whether a session for the thread is running right now. Reuse `wtf.go` / `wtf_scan.go` and `live.go` rather than re-implementing.

The mockup the user approved is in this conversation; its key points are captured above. Source: `.superpowers/brainstorm/.../triage-v2.html` in the session scratchpad (not committed).

## Opening a thread (decided)

`⏎` opens one workspace for the thread, using the existing 3-pane workspace (`iterm.go` `workspaceScript` / `ampWorkspaceScript`): agent + live tail + shell, cd'd to the thread's primary worktree.

- If a session for the thread is already running (`live.go` registry / nearby), do not start a second agent; say where it is running. This is the same rule `restore-handovers` applies today.
- Otherwise resume the newest session (`claude --resume <id>` / `amp threads continue`) or start fresh with the agent pointed at `thread.md`. Which of these is the default is the builder's call; my lean is resume when the newest session is recent, fresh otherwise.
- The launch records the session id against the thread in `.registry.json`.

## Writing: how handovers update a thread (decided)

Daniel mostly hands over from inside each session with the `/handover` slash command (`~/.claude/commands/handover.md`). That stays the main path.

- `/handover` is changed to update a thread: rewrite `thread.md`, add to `log/<today>.md`, revise `knowledge.md`, update frontmatter. It stops writing to `Handover/<date>/`.
- The batch path, `entire-tail handover` + the `handover-sessions` skill, is changed the same way, for sessions that were never handed over. Its manifest names the target thread per group.
- The session that did the work writes its own update, because it has the context.
- **Amp too (decided: "make it work somewhere").** Today Daniel tells Amp "handover" and it works out what to do. Thread updates must work the same way from Amp. My call: the thread-update instructions live in ONE shared document (a skill both agents can load), and Claude's `/handover` and Amp's route to "handover" both point at it, so the two can't drift. The builder finds out how Amp currently resolves "handover" (AGENTS.md, an Amp skill, or the vault) and hooks in there. The `entire-tail threads` subcommands are agent-neutral, so Amp sessions register and lock the same way.

### Matching a session to a thread

1. **Registry hit:** session was opened from triage (or registered earlier), so `/handover` updates that thread without asking.
2. **Proposed match:** otherwise `/handover` compares the session to open threads by trail, worktree, then topic, and proposes one: "This looks like *mirror GitHub checks*. Update it?" Daniel confirms, picks another, or says new.
3. **New thread:** `/handover` proposes a name from the work; Daniel accepts or edits; the folder is created.
4. **Topic drift:** a session that wandered asks once which thread to file under. One session is never split across two threads.

Every outcome writes the session → thread mapping to `.registry.json`.

Parked and closed threads (my call, following "Daniel decides" and "no silent guessing"): parked threads are offered as matches, marked parked. Work landing on a parked thread is written, and `/handover` asks "unpark it?"; the default answer keeps it parked. A registry hit on a closed thread asks: reopen it, or start a new thread. Nothing changes status without that answer.

Batch path (my call): the `handover_picker.go` grouping UI is unchanged; the manifest gains an optional thread name per group. The `handover-sessions` skill runs the same matching dialogue as `/handover` for each group, so trail, worktree and topic matching live in one place (the skill), not in Go and the skill.

### Concurrency

Only this machine writes to the vault (decided), so a local lock is enough; iCloud cross-machine conflicts are out of scope. Two sessions can hand over to the same thread around the same time. Writes must not lose each other's update. The repo already uses a never-unlinked `flock` for this (`trails.go`, `layoutsnap.go`); the builder picks the mechanism. A handover that rewrites `thread.md` must start from the version on disk at write time, not the one read at session start.

The writing side runs inside a Claude skill, so the lock and the registry update should be exposed as small entire-tail subcommands the skill calls (for example `entire-tail threads lock|register|park`), rather than the skill editing `.registry.json` by hand. Exact subcommand surface is the builder's call.

## Bootstrap (decided, after the above ships)

A one-time pass once threads work: group the ~20 open docs in the dated `Handover/` folders into threads, using the grouping rules in the `restore-handovers` skill (same trail / Linear / plan; `Continues` chain; same worktree; dependent next steps). Daniel confirms the list, then the threads are written. After that, nothing new is written to `Handover/<date>/`; old folders stay as they are.

The bootstrap is a separate step, not part of the first build.

Cutover order (my call, from "bootstrap them once you have built the thread-tech"): `/handover` and `handover-sessions` keep writing dated docs until the bootstrap has run. Order: build the thread files, triage and subcommands; build the new skill versions behind a switch; run the bootstrap; then switch both handover paths to threads. This avoids a thread created on day one and a second one for the same work created by the bootstrap.

## Out of scope for the first build

- Auto-wake of parked threads on a date ("park until Monday"). Not asked for.
- Migrating `complete/` history.
- Changes to the `customers` skill beyond a customer thread linking to Linear issues.

## Left to the builder

- Exact frontmatter keys and the registry format.
- Scroll/select key bindings beyond those named above.
- How the live-fact check is cached so the screen opens instantly.
- The subcommand surface for skills to call.
- Resume vs fresh as the `⏎` default.
- Whether `restore-handovers` (the bash script) learns to read `Threads/`, or is retired in favour of `entire-tail threads`. My lean: retire it once triage `⏎` works, keep it for the dated folders until the bootstrap.
