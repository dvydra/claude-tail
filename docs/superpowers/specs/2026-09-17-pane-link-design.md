# Pane link — select one side, the other side's window follows

**Date:** 2026-09-17
**Status:** Approved design, pre-implementation

## Problem

A claude session and the entire-tail watching it are often not in the same window. The layout in use right now is two iTerm windows: claude sessions as tabs of one window, their tails as tabs of another. Clicking a claude tab leaves the other window showing whichever tail happened to be selected last, so the pair has to be re-aligned by hand every time attention moves between sessions.

The 3-pane workspace (`⏎`/`n`, `iterm.go`) has no such problem: the agent and its tail are panes of one tab, both visible at once. This design is for the hand-made layout only.

## Goal

When a session is selected on either side, switch the *other* window to the tab holding its partner. Never move keyboard focus, never raise or activate anything.

Non-goals: linking panes within one tab (they are already both visible); raising or un-minimising a buried window (needs Accessibility permission and can steal key focus, which would fire the watcher again and bounce the user back); any visible marker or badge on the partner; any behaviour on Linux, or on macOS outside iTerm2.

## Evidence (measured 2026-09-17, iTerm2 3.6.11)

1. **Switching a tab in a background window does not move focus.** `select tab 1 of window id 2877` while window 4930 was current: `current window` and `current session` were unchanged before and after. This is what makes the whole feature safe — no focus ping-pong, no suppression window needed.
2. **AppleScript session ids are the same UUIDs as `$ITERM_SESSION_ID`.** A shell reporting `w0t2p0:D2AAFDDB-347F-4B9A-96F6-AEC3A4EA227C` was found by that UUID at window 2877, tab 3.
3. **Locate and select fit in one osascript pass.** One script returns the focused session's window and tab index plus the target's, so no tab index is ever cached across calls. Measured cost of an osascript round trip: ~135ms wall, ~5ms CPU.
4. **`it2api monitor-focus` is one-shot** — it awaits a single update, prints it and exits — so the event-driven route needs a script of our own, not the bundled CLI.
5. **The iTerm Python API socket is live** (`~/Library/Application Support/iTerm2/iterm2-daemon-1.socket`). The `iterm2` module is not installed, and `python3` resolves first to a mise-managed 3.12.

## Approach

Detection is event-driven via a small embedded Python script using `iterm2.FocusMonitor`; it reports the active session UUID and nothing else. All pairing, decision-making and tab switching stay in Go.

Rejected alternatives:

- **osascript polling from Go.** No runtime dependency, ~1% of a core at 250ms, but it wakes iTerm twice a second forever and adds up to ~400ms of latency.
- **An iTerm AutoLaunch script doing the whole job.** No Go daemon at all, but the pairing logic would live outside the binary in a second language, unable to share the existing claude-resolution code or the Go tests.
- **Scraping both sides with `ps eww`, no registry.** Breaks on `lineage.go`: a worktree fork or `/clear` moves a tail onto a new session id while its argv still names the old one, so a tail's current target is not in its argv.
- **The tail writing the whole pair at adopt time.** Fewest lookups, but the pair goes stale as soon as claude is restarted in a different tab, with nothing to correct it.

## Components

| File | Contents |
|---|---|
| `panelink.go` | Pure: registry read/write/prune, `linkAction` decision, osascript builder |
| `panelinkd.go` | Daemon: python child, focus loop, osascript execution, state file |
| `panelink.py` | Embedded (`go:embed`) focus reporter, ~15 lines |

Named `panelink` rather than `focus` because `focus.go` is already the subagent focus overlay.

## Registry

Each entire-tail writes `~/.claude/entire-tail/link/panes/<its-iterm-uuid>.json`:

```json
{"follow_session": "<transcript session id>", "pid": 12345}
```

It is rewritten whenever the followed session changes — a worktree fork, a `/clear`, a relocation — and removed on exit. A tail that is killed leaves its file behind, so liveness is the pid, never the file's presence; the daemon prunes entries whose pid is dead. This is the same rule `live.go` applies to Claude Code's own session registry, and for the same reason.

The daemon's own state is `link/daemon.json` = `{pid, started}`, health-checked with a pid match, mirroring `readTapState`.

The claude side is not registered by anyone. The daemon resolves running claudes to transcript ids every 5s, and on any focus event for a UUID it does not recognise, reusing the existing `claudeProcs` + `psItermID` + `resolveClaudeSession` join that `nearby.go` already performs. That is what makes the pairing self-healing: claude can be restarted in a new tab and the link re-forms without the tail doing anything.

## Decision

`linkAction(focused uuid, panes, claudes) → (window, tabIndex, ok)`:

1. If the focused UUID is a registered tail, the partner is the claude pane whose session id equals that tail's `follow_session`. If the focused UUID is a claude pane, the partners are every tail following that session.
2. No partner → do nothing.
3. Partner in the same window as the focused session → do nothing. This covers the 3-pane workspace and any same-window tab pair, where showing both at once is impossible.
4. Otherwise select the partner's tab in its window.

Same-window checks come from the live tree in the same osascript pass, **not** from the `wNtN` prefix of `ITERM_SESSION_ID`. A running process's environment cannot be rewritten, so that prefix is stale the moment a tab is moved between windows.

Where two tails in different windows follow one session, both windows are switched.

## Lifecycle

A tail registers its pane file, and the daemon auto-starts, **only when `link-choice` says yes**. With the feature off or unanswered, nothing is written and nothing is spawned. When it is on, the first entire-tail to register a pane health-checks the daemon and spawns it if absent, under a lock so two tails starting together cannot both spawn one. It exits after 60s with no live pane entries. There is no LaunchAgent and no install command needed for normal use, because the watcher is worthless without a running tail.

## Setup and the offer gate

`shouldOfferPaneLink` mirrors `shouldOfferHookInstall`: macOS and iTerm, interactive tty, Claude agent, no `--no-pane-link`, no recorded choice, and a linkable pair already present — a registered tail whose claude resolves into a different window. It is checked once before backfill and never mid-stream; a streaming viewer must not stop to ask a question. With no pair present it stays quiet and asks on a later run. The answer is remembered in `~/.claude/entire-tail/link-choice`.

The gate runs in the tail itself, with no daemon involved: claude resolution is the in-process `nearby.go` join, and the different-window test is one osascript call. That call happens only on a run that is otherwise eligible and has never answered the question, so it is paid at most once per machine, not on every start.

On yes:

1. Choose a stable `python3`: `/opt/homebrew/bin/python3`, then `/usr/bin/python3`, then PATH, skipping any path under a version manager (mise, pyenv, asdf). Same hazard as `looksEphemeralBinary` in the tap plist — a version-managed interpreter path can disappear under a running daemon.
2. Create a venv at `~/.claude/entire-tail/link/venv`.
3. `pip install iterm2` into it (network, roughly 10s).
4. Start the daemon, which spawns the venv's python running the embedded script. iTerm shows its "allow this script to connect" prompt once.
5. Health-check before reporting success. Reporting a link that is not running is worse than reporting a failure — the lesson from `tap install`.

`entire-tail link status | install | stop` exposes the same steps directly, and the `?` settings panel gains a row showing the current state.

## Failure modes

All of these are quiet and none of them may delay or interrupt the tail.

| Failure | Behaviour |
|---|---|
| No usable python3, venv creation fails, pip offline | Recorded, feature off, never auto-retried; `link install` retries |
| iTerm's allow prompt denied | Stop, do **not** respawn — a KeepAlive-style retry would re-prompt forever |
| Python child dies (iTerm quit) | Restart with backoff, give up after a few attempts |
| Tail killed, pane file left behind | Pruned on the pid check each tick |
| Followed session has no running claude | No partner, silent |
| Tab closed between locate and select | Same script pass; a failed select is a no-op |

## Security

The registry holds transcript session ids and pids. No tokens, no paths beyond the session id, nothing the transcript does not already store in plaintext. The Python child receives no arguments carrying secrets. The daemon only selects tabs; it never activates an app, raises a window, or moves focus, so it cannot redirect keystrokes to a different pane.

## Testing

- `linkAction` is pure and table-tested: same window, no partner, tail to claude, claude to tail, two tails on one session, dead pid pruned.
- Registry read, write, prune and the atomic rewrite-on-lineage-change get unit tests.
- The osascript is produced by a builder function tested for quoting and escaping, as `workspaceScript` is.
- `osaRun` and the python spawn sit behind package vars so `go test` never touches iTerm, launchd, or the machine's real state.
- No goldens move. This feature renders nothing into the transcript.
- Manual verification: claude in window A tab 3, its tail in window B tab 1, some other tail selected in B. Clicking the claude tab flips B to tab 1 without B taking focus.

## Effort

About a day: half for the registry, decision and their tests; half for the daemon, python child and setup flow; then the two-window manual check.
