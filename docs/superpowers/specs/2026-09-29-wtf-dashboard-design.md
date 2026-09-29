# `entire wtf`: global work dashboard and trail ownership monitor

**Date:** 2026-09-29
**Status:** Approved design, awaiting written-spec review
**Component:** `entire-tail` (`wtf` dashboard, durable registry, and macOS LaunchAgent)

## Problem

Daniel works in many Claude and Amp sessions at once. The existing tree and `--live` views can find those sessions, but they present sessions as separate rows. They do not answer the higher-level questions: what is Daniel doing across all sessions, which session owns each trail, where that trail's work should live, and whether two sessions or worktrees are colliding.

The missing state cannot be reconstructed from active processes alone. A session can end while its dirty or unmerged worktree remains relevant for days. A later session can unknowingly claim the same trail. The system needs a durable map from trails to sessions and worktrees, plus a live view over today's sessions.

## Goals

- `entire wtf` summarizes every Claude and Amp session active now or active at any point since local midnight.
- The dashboard shows what each session is doing, its status, repo, branch, worktree, trail references, and whether it needs Daniel.
- A durable registry maps trails to their canonical worktrees and owning sessions with no age cutoff.
- The dashboard shows every trail that still has work in progress and where that work should live.
- A background monitor detects duplicate trail claims, stale work in another worktree, work outside the canonical worktree, and related placement mistakes.
- A new conflicting session is told to stop and check with Daniel. Daniel receives one macOS notification for the same finding.
- Trail identity, ownership, worktree state, and findings come from deterministic evidence. Apple's Foundation Models summarize sessions but never decide ownership or safety.

## Non-goals

- A global list of all Entire trails. Trails enter the registry only through observed sessions, worktrees, branches, commits, or explicit local reassignment.
- Automatic worktree cleanup, branch deletion, trail mutation, merging, or reassignment.
- A web app or menu-bar app. The first UI is a terminal dashboard.
- Using a model to infer that two differently identified trails are the same work.
- Replacing `entire-tail`'s existing session tree, live view, handover flow, or transcript renderer.

## Commands

The standalone and Entire-plugin forms are equivalent:

```text
entire wtf
entire-tail wtf
```

Daemon lifecycle:

```text
entire wtf install
entire wtf status
entire wtf uninstall
```

`install` writes and loads the `io.entire.entire-tail.wtf` LaunchAgent, then waits for a successful daemon health check before reporting success. It follows the existing tap LaunchAgent rules: resolve a stable installed binary, never pin launchd to a temporary path or worktree, and keep launchctl calls behind test seams.

`uninstall` stops and removes the LaunchAgent but preserves the durable registry.

## Architecture

One binary has two roles. The LaunchAgent runs the monitor and owns state updates. The foreground command reads the last complete state and renders it.

```text
Claude registries ─┐
Amp top + exports ─┼──▶ wtf daemon ──▶ durable state ──▶ entire wtf
session transcripts┤        │
git worktrees ─────┤        ├──▶ Claude/Amp warning
Entire trail CLI ──┤        └──▶ macOS notification
Foundation Models ─┘
```

The daemon polls active-session sources every two seconds. Expensive work is input-driven:

- A transcript is rescanned only when its file signature or Amp snapshot version changes.
- A worktree is rescanned only when its Git signatures change or a related claim changes.
- `entire trail show` runs when a new trail appears or cached metadata expires.
- Foundation Models runs when the transcript sample used for a session summary changes.

Conflict detection runs before network or model enrichment. A slow or failed summary must not delay a warning.

## Durable state

State lives under the user's Application Support directory, outside the cache, and is written with temp-file plus rename. The default macOS location is:

```text
~/Library/Application Support/entire-tail/wtf/state.json
```

The daemon is the only writer. Dashboard processes are readers. The file contains a schema version and these records.

### Session record

- Agent and session ID.
- Repo, cwd, branch, and worktree path.
- Active, idle, busy, waiting, or ended state.
- First and last activity.
- Transcript locator and incremental scan position or snapshot version.
- Extracted trail references with source text and event time.
- Foundation Models summary and optional `needsUser` text.
- Last successful warning delivery state.

Session retention is limited to today: keep sessions active now or with activity at or after local midnight. At midnight, ended sessions from the prior day leave the dashboard state. Their claims remain in trail and worktree history.

### Trail record

- Stable key in `owner/repo#number` form.
- Entire URL, title, state, source branch, target branch, and metadata refresh time when available.
- Canonical worktree.
- Owning session and first-claim evidence.
- Every observed session and worktree association.
- First seen, last seen, and last known WIP state.

Trail records have no age cutoff. A closed or fully merged trail remains historical state but is not shown in the default WIP section unless a worktree becomes dirty or unmerged again.

### Worktree record

- Repo, path, branch, and HEAD.
- Whether the path still exists.
- Dirty file count and a short porcelain summary.
- Default branch and count of commits not present on `origin/<default>`.
- Active and historical session IDs.
- Trail associations and the evidence for each association.
- First seen, last seen, and last WIP time.

Worktree records have no age cutoff. Deleted worktrees remain historical records marked missing.

### Finding record

- Stable identity derived from finding kind, trail, affected sessions, and affected worktrees.
- Kind, severity, human explanation, and deterministic evidence.
- First seen, last seen, active or cleared state.
- Mac notification state.
- Per-session warning delivery state and last error.

Findings are never silently deleted. Cleared findings remain historical and stop rendering in the default view. If the same condition clears and later recurs, it becomes active again and may notify again.

## Session inventory

Reuse the existing exact sources rather than creating another discovery path:

- Claude: per-profile running-session registries for exact live identity, plus the local transcript trees for today's ended sessions.
- Amp: local process correlation, `amp top --stream-jsonl` for runner and orb activity, thread inventory, and exported snapshots.
- Entire session metadata: cached titles and repo metadata where already available.

An active session is always included even if its transcript has not changed since midnight. An ended session is included when its last activity is today in the local timezone.

Remote Amp sessions remain visible. Worktree checks require a local path; a remote-only path is marked unavailable rather than treated as clean.

## Trail extraction

Trail references are extracted deterministically from user and assistant text, tool inputs and results, branch names, and unmerged commit messages. Supported text forms are:

- `https://entire.io/gh/<owner>/<repo>/trails/<id>`
- `<owner>/<repo>#<id>`
- `<repo>#<id>`
- `trail <id>` and `trail #<id>`

`<owner>/<repo>#<id>` and a full URL are self-contained. `<repo>#<id>` resolves against the session's current repo when the basename matches, then against known local repos when there is one unambiguous match. A bare trail number resolves only against the session's current repo. Ambiguous references are displayed as unresolved evidence and never create a claim.

The first mention is ordered by the transcript event timestamp, not daemon scan order. If a source has no event timestamp, the first observed time is the fallback. Persisting the first claim prevents daemon restarts from changing ownership.

The scanner records the exact matching text and source location so every claim and finding can explain why it exists.

## Claim rules

A valid session claim requires both:

1. A resolved trail reference.
2. A local git worktree associated with that session at the time of observation.

The earliest valid session/worktree pair becomes the owner. Later observations from that pair refresh the claim without changing ownership.

A session that mentions a trail without a local worktree is shown as related but does not become the owner. A remote Amp session may remain related until a local worktree association is available.

A later active session with a different session ID creates a claim conflict. The later session is the challenger even if its model summary sounds more relevant. One session moving outside the canonical worktree creates an outside-canonical finding instead.

## Canonical worktree

The canonical worktree answers where the trail should live. Selection order is:

1. A local worktree whose checked-out branch matches the trail's source branch from `entire trail show`.
2. The first valid worktree claim.

The daemon makes this selection once, when the trail first has enough local evidence. If trail metadata is unavailable then, the first valid worktree claim wins. The mapping stays stable across session exits, daemon restarts, and dashboard restarts. A different worktree never replaces it automatically.

When the source branch later matches a different worktree, that mismatch becomes a finding rather than an implicit move. This keeps a changed server-side branch or reused branch name from rewriting local ownership without Daniel seeing it.

## Finding rules

The first version emits these findings.

### Duplicate active claim

Two active sessions with different session IDs claim the same trail.

### Existing WIP elsewhere

A session claims a trail while another associated worktree is dirty or has commits absent from the local `origin/<default>` ref. The other worktree may come from an ended session and may have been untouched for any length of time.

### Outside canonical worktree

An active session claims a trail from a worktree other than the canonical path.

### Work on the default branch

An active claimed trail is being changed from the repository's default branch instead of a worktree branch.

### Missing canonical worktree

The canonical worktree path no longer exists while the trail still has another dirty, unmerged, or active worktree association.

### Association evidence

Another worktree may be associated with a trail by any of these deterministic facts:

- It was previously claimed by a session in the registry.
- Its branch matches the trail source branch.
- Its branch or an unmerged commit message contains the exact full trail key, full trail URL, or unambiguous repo-qualified shorthand.
- Its uncommitted diff contains a full trail URL or repo-qualified shorthand.

A bare number in a branch, commit, or diff is not enough. The dashboard shows the evidence that created the association.

## Git state

The daemon does not fetch. It uses the local refs already maintained by the machine.

Default branch resolution is `refs/remotes/origin/HEAD`, then `origin/main`, then `origin/master`. If none exists, unmerged status is unknown rather than zero.

A worktree has WIP when either condition holds:

- `git status --porcelain` is non-empty.
- `git rev-list --count origin/<default>..HEAD` is greater than zero.

The dashboard reports dirty files and unmerged commit count separately. A missing path is not clean.

## Foundation Models summaries

Use the existing `fm` boundary and the on-device system model. No remote model or new runtime dependency is added.

Each changed session produces structured output:

```json
{
  "summary": "One sentence describing the current work and state.",
  "needsUser": "One sentence describing a concrete decision or action Daniel owes, or empty."
}
```

The input is a cleaned head-and-tail sample of today's relevant transcript turns, using the existing synthetic-message filters. Tool plumbing, task notifications, local command caveats, and model reasoning are excluded.

Deterministic state overrides the model:

- A pending question or permission marker supplies `needsUser` directly.
- Busy, idle, waiting, and ended come from session activity sources.
- Repo, branch, worktree, trail identity, ownership, and findings never come from the model.

When `fm` is absent, unavailable, or returns invalid JSON, the dashboard falls back to the current prompt, generated title, or recent user message. Monitoring continues unchanged.

Summaries are cached by a hash of the sampled input. Unchanged sessions never spend another model call.

## Entire trail metadata

After extracting a new trail key, run the supported CLI boundary:

```text
entire trail show <id> --repo gh/<owner>/<repo>
```

Cache the successful title, state, source branch, and target branch. Refresh active WIP trails periodically with bounded backoff. A failed lookup leaves the trail visible with local evidence and `metadata unavailable`; it does not invalidate a claim.

The monitor never lists unrelated trails and never mutates a trail.

## Dashboard

`entire wtf` is a live terminal dashboard with four sections.

### Now

Every active session, grouped by repo. Each row shows agent, session name or short ID, busy/idle/waiting state, branch, worktree, trail, summary, and `needsUser` when present.

### Recently stopped

Ended sessions with activity today, newest first. At local midnight they leave this section. Their trail and worktree records remain.

### WIP trails

Every trail with an active session, dirty associated worktree, or commits absent from the default branch. Each block shows trail metadata, owner, canonical worktree, active sessions, every WIP worktree, and why each worktree is associated.

### Badness

Active findings first, ordered by severity and first-seen time. Each block names the affected trail, owner, challenger, canonical and actual worktrees, evidence, notification state, and whether the challenger received the warning.

Example:

```text
WTF  6 active · 4 ended today · 3 WIP trails · 2 findings       14:37:08

!! entiredb#1223  DUPLICATE CLAIM
   OWNER       C  api-refactor-a1   feat/1223-api    ~/…/wt/api-1223
               Reworking checkpoint writes; tests are running.
   CHALLENGER  A  T-01a0…           fix/checkpoints  ~/…/wt/checkpoint-fix
               Started changing the same checkpoint path.
   ACTION      challenger was told to stop and check with Daniel

!! infra#884  EXISTING WIP ELSEWHERE
   ACTIVE      C  monitor-cleanup   feat/884-alerts  ~/…/wt/alerts-new
   CANONICAL   ended 2d ago         old/884          ~/…/wt/alerts-old
               3 dirty files · 2 commits not on origin/main
```

Findings render first, followed by Now, WIP trails, and Recently stopped. A session or trail appears in its normal section even when a finding also references it.

Keys:

- `Enter`: tail the selected session with the existing agent-aware tail path.
- `r`: request an immediate daemon scan.
- `q` or Escape: exit.

Trail URLs and session URLs use OSC-8 links where supported. Piped output is a static ANSI-free snapshot suitable for search or scripts.

## Warning delivery

When a new active finding has a challenger session, the daemon sends this class of message:

```text
entire wtf found a conflict for entiredb#1223.
It is already owned by Claude session api-refactor-a1 in ~/…/wt/api-1223.
This session is in ~/…/wt/checkpoint-fix. Stop before changing this trail or either worktree and check with Daniel.
Evidence: first claim at 10:14; canonical worktree has 3 dirty files and 2 commits not on origin/main.
```

The wording reports evidence, tells the challenger to stop, and asks it to check with Daniel. It does not authorize cleanup, switching branches, or editing another worktree.

Delivery channels:

- Claude: the documented local cross-session Unix socket from `messagingSocketPath`. Missing sockets and inbound hold/refuse settings are recorded as delivery failures.
- Amp: `amp threads continue <id> --execute <warning>`, the supported non-interactive continuation path. The subprocess is bounded and detached from the scan loop so a running turn cannot stall monitoring.
- macOS: `osascript` `display notification`, titled `entire wtf`, with the trail and short conflict explanation.

The finding identity deduplicates all three channels. A channel retries failures with bounded backoff. It does not repeat a successful warning while the finding remains active. Once the finding clears, a later recurrence may warn again.

If session injection fails, the Mac notification and dashboard still report the finding and the failed channel.

## Daemon health and coordination

The daemon writes a small health record with PID, version, start time, last attempted scan, last successful scan, and last error. `entire wtf status` reads both launchd and this record.

The foreground dashboard never becomes a second state writer. `r` writes a scan-request marker or signals the daemon; if no daemon is running, `entire wtf` performs one read-only foreground scan for display and clearly reports that monitoring and warning delivery are off.

Only one daemon may hold the state lock. A second instance exits cleanly.

The daemon rotates its own bounded diagnostic log. It never logs transcript bodies, environment variables, credentials, or full tool payloads. Finding evidence is limited to matched trail text, git state, paths, IDs, and generated summaries already stored in state.

## Failure behavior

| Failure | Behavior |
|---|---|
| Claude registry missing or malformed | Keep other sessions; skip bad entry |
| Amp unavailable or logged out | Keep last good Amp state; Claude monitoring continues |
| Amp top disconnects | Keep local Amp mappings and last remote set; reconnect with backoff |
| Transcript read fails | Keep prior summary and claims; show stale source status |
| `fm` fails | Use deterministic title/current prompt fallback |
| Entire lookup fails | Keep local trail key and evidence; metadata marked unavailable |
| Git command fails | Worktree state becomes unknown, never clean |
| State file is malformed | Preserve it as a timestamped corrupt copy and rebuild session state; never discard recoverable trail/worktree history silently |
| Mac notification fails | Finding remains active; dashboard shows delivery failure |
| Session warning fails | Finding remains active; retry with backoff and show failure |

No single source may blank the dashboard or stop the daemon loop.

## Implementation boundaries

Keep the new code within the existing single `package main` structure, split by responsibility:

- `wtf.go`: state model, paths, atomic persistence, and command dispatch.
- `wtf_scan.go`: session collection, transcript scanning, trail extraction, worktree inspection, and reconciliation.
- `wtf_summary.go`: Foundation Models input and structured summary cache.
- `wtf_notify.go`: Claude socket, Amp CLI, and macOS notification delivery.
- `wtf_view.go`: pure dashboard rows, reducer, renderer, and thin tty driver.
- `wtfdaemon.go`: scan loop, health, lock, LaunchAgent install, and lifecycle.

Reuse existing Amp clients, Claude live registries, repo parsing, session metadata readers, terminal helpers, OSC-8 links, Foundation Models runner, and LaunchAgent helpers where their contracts fit. Do not create a second implementation of session discovery.

## Testing

Keep external effects behind injected function boundaries. Tests must never send a real session message, post a real notification, run Foundation Models, modify launchd, or change a real repository.

Unit tests cover:

- Every trail-reference form, repo resolution, ambiguity, false positives, and event-time ordering.
- First claim persistence across scan order and daemon restart.
- Canonical worktree selection from source branch, then first claim.
- Dirty and unmerged WIP detection, unknown default branch, detached HEAD, and missing paths.
- Historical session expiry at local midnight while trail/worktree history remains.
- Duplicate claim, existing WIP, outside-canonical, default-branch, and missing-canonical findings.
- Finding clear, recurrence, per-channel deduplication, and retry state.
- Foundation Models cache keys, structured decoding, fallback, and deterministic `needsUser` override.
- Dashboard ordering, narrow-width rendering, selection, static output, and links.
- State schema migration, atomic writes, corrupt-state recovery, daemon locking, health, and LaunchAgent plist.
- Exact warning text and command construction for Claude, Amp, and macOS.

Integration fixtures cover:

- Claude and Amp active sessions claiming the same trail.
- A current claimant encountering a dirty two-day-old worktree from an ended session.
- A clean worktree with commits absent from `origin/main`.
- A trail source branch identifying the canonical worktree over a later claim.
- A remote Amp session with no inspectable local worktree.
- Midnight rollover removing yesterday's ended sessions without removing claims.

Verification:

```sh
go test ./...
go vet ./...
go test -race ./...
```

Manual verification uses disposable sessions and worktrees. It checks the live dashboard, midnight filtering, persistent trail history, one Claude warning, one Amp warning, one Mac notification, deduplication across repeated scans, clearing and recurring findings, daemon restart recovery, and degraded operation with `fm`, Entire, Amp, and notification delivery unavailable in turn.

## Documentation

Update `README.md`, help output, and `CLAUDE.md` with:

- `entire wtf` and daemon lifecycle commands.
- Session-today versus indefinite trail/worktree retention.
- Supported trail-reference forms, including `repo#1223`.
- Canonical worktree and WIP rules.
- Warning behavior and its limits.
- Foundation Models fallback behavior.
- State and log locations.
