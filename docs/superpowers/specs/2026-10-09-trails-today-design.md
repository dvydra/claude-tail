# entire trails: where are my trails?

## Purpose and agreed scope

Daniel wants a separate searchable TUI showing the trails he is working on right now and earlier today, associated with their agent sessions. Background harvesting must keep working when the TUI is closed. This is separate from the overloaded `entire wtf` command.

One row per trail shows repo, title, status, associated sessions, and last activity. Active sessions put their trails first; trails touched earlier today remain available after sessions stop. `/` searches, Enter opens the trail in the browser, and `s` selects an associated session to view through the existing tail flow.

No conflict detection, ownership assignments, worktree audits, notifications, AI summaries, automatic agent launches, or trail mutations. No historical browsing UI in the first version.

## Commands and collection

Expose `entire trails` through an `entire-trails` entry point to the existing binary, and support `entire-tail trails` directly. Keep `wtf` unchanged.

Use a separate opt-in LaunchAgent with `trails install`, `uninstall`, and `status`. Installing monitoring is an explicit action, not a side effect of opening the dashboard. Without it, the dashboard harvests while open and explains that background harvesting is off. Opening the dashboard catches up on today's sessions even if monitoring was stopped.

Reuse Claude/Amp inventory, activity signals, transcript readers, and existing tail navigation. Start with those two agents because they already have trail-event readers. Do not imply coverage of every agent supported by the renderer.

Poll for inventory/transcript changes every two seconds after the prior scan finishes. Skip unchanged transcripts; process appended Claude records incrementally and changed Amp snapshots by message identity. Retain incomplete records for the next pass, and detect replaced or truncated files. Reuse Amp's supported feed/cache/export paths rather than treating a stale export as current. Bound subprocess work and keep the TUI responsive while collection runs.

## Associations and time

Use the full trail identity: host, forge (`gh` or `et`), owner/project, repo, and number. Existing WTF keys omit the forge and metadata requests hardcode `gh`; do not copy those assumptions.

Keep many-to-many trail/session associations, with source, evidence, first observation, and last relevant activity. A full Entire URL in authored user/assistant text establishes a mention. A validated trail source branch matching the session's repo and branch establishes a stronger association. Resolve a session's non-default branch through `entire trail show --branch` when no URL is available, with caching and retry backoff. Never treat a trail number alone as globally unique.

Display mention-only associations as `mentioned`; do not promote every referenced trail to work currently in progress. Branch-matched trails with active sessions appear in Right now. Mention-only rows remain searchable under Today. Merely reading a tool result, repository file, or injected context does not establish work on a trail.

Today starts at local midnight in the machine's timezone, including Melbourne daylight-saving changes. Include active branch-associated work across midnight. A prior-day trail returns to Today when its branch-associated session does work today or the trail is mentioned again today. Repeated scans do not advance activity timestamps. Activity on an unrelated branch must not refresh old associations.

Persist session identity, title, agent, location, and transcript/thread reference with associations so stopped sessions remain usable throughout the day. If their source disappears, retain the row and report that the session is unavailable. Keep associations across midnight to reconnect continuing sessions; filter the view by activity rather than deleting the associations at midnight.

## Storage and metadata

Store a versioned catalog separately under `~/Library/Application Support/entire-tail/trails/`, using existing atomic JSON persistence patterns. Keep it private to the user. Store session references and association evidence, not a duplicate full transcript archive.

Allow one collector to write at a time. With monitoring running, the TUI reads its catalog and requests catch-up; without monitoring, the foreground collector acquires the same writer lock. Losing the lock leaves the TUI read-only. A malformed catalog is reported and preserved rather than silently overwritten.

Refresh title, status, and branch metadata independently of transcript harvesting. Cache successful responses, back off failures, and retain last-known data with a visible stale/error indicator. A remote lookup failure must not hide a discovered trail or erase its associations. Never invoke WTF's scanner or notification machinery to populate this catalog.

## TUI behavior

Right now comes first, then Earlier today, ordered by actual activity time with a stable trail identity tie-breaker. Merged and closed trails remain visible if touched today. Search filters title, repo, trail number, and associated session titles; full transcript search is outside this version.

Preserve the selected trail across refreshes and filtering. Enter opens only its canonical Entire URL. `s` opens a session picker when several sessions are associated; returning from the tail restores the trail list. `r` requests refresh; `q` quits. Footer shows collection freshness, monitoring state, and degraded sources. Empty results distinguish no trails yet from no search matches.

## Validation

Test observable collection and catalog behavior with temporary homes, injected inventory/clock/commands, and Claude/Amp fixtures. Cover distinct `gh`/`et` identities, branch-only discovery, mention versus branch evidence, multiple sessions per trail, stopped-session retention, midnight/DST, branch changes, repeated scans, partial records, and stale metadata.

Test search, selection across refresh, session navigation, and small terminal sizes. Verify exclusive writes and recovery without touching real LaunchAgents or opening real browser windows in tests. Run the existing full test suite, race tests, vet, and build. Render and inspect representative populated, filtered, empty, and degraded TUI states before calling the implementation complete.

## Existing work to preserve

The separate `trails-continue` worktree contains URL-handler and Trails for Mac integration. It is not this dashboard and must remain untouched. Reuse only merged code from `origin/main` unless integration is explicitly agreed.

## Approval state

Daniel approved this written spec on 2026-10-09. No implementation or monitoring installation has been performed.
