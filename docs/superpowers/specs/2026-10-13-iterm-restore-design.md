# iTerm restore after reboot

## Problem

After a reboot iTerm restores every window and tab, but nothing runs in them: each pane is an idle shell in its old cwd.
Rebuilding a tab today means `--pick`, find the session, start the tail, open `?` to copy the resume line, start the agent, for every tab.

## Goal

One command brings back the last known state: every agent + tail workspace that was open before the reboot, on the same sessions, in the same tab order.

Success: `entire-tail restore` once, answer one `y/N`, and every tab is back.

## What we checked

- Restored panes get new iTerm session UUIDs. On 2026-10-13 (reboot 12:29), 298 pane-link registry files from before the reboot matched 0 live panes. Nothing keyed on UUID survives, so the existing registry cannot drive a restore.
- Restored panes keep their cwd (tab 3, `midnight-magic`: both panes idle in the right folder).
- iTerm reuses small window ids across boots, so a window id alone is not a key.
- `sysctl -n kern.boottime` gives a stable per-boot value (`sec = 1791854990`).

## Decisions

- Restore builds a new window with fresh workspace tabs. It does not type into the restored shells and does not reuse the restored panes.
- Default is no picker: restore the newest pre-reboot snapshot, all windows. `--pick` covers the rare case.
- The old windows are closed only after a `close the restored windows? y/N` prompt.
- The layout is saved by running tails. No new daemon or LaunchAgent.

## Part 1: layout snapshot

### Who writes it

Every running tail, on its normal tick, attempts a snapshot every 30s.
A non-blocking `flock` on `~/.claude/entire-tail/layout/.lock` lets one process through; it skips if the current boot's snapshot is under 30s old.
The flock file is never unlinked (same rule as the trails lock).

### What it reads

1. One `osascript` call lists windows, their tabs in order, and each tab's panes (unique id, tty, columns/rows for pane order).
2. Each pane is classified by the processes on its tty (`ps -t`):
   - tail pane: an `entire-tail` process. Its session comes from the pane-link registry entry for that pane's UUID (`readPaneRegistry`, pid alive), which already records agent + followed session.
   - agent pane: a `claude` or `amp` process. Its session comes from the tail in the same tab when the agents match, otherwise from `runningAgentPanes` (the existing `resolveClaudeSession` / amp lsof join).
   - shell pane: anything else. Only its cwd is kept (`lsof` on the login shell).
3. Per tab the snapshot keeps: agent, session id, cwd of each pane, and the pane roles in order.

### Where it goes

`~/.claude/entire-tail/layout/<boot-sec>/snapshot.json`, one file holding every window, rewritten with temp+rename.
One file per boot means a window closed before the reboot drops out of the next snapshot by itself.

```json
{
  "boot": 1791854990,
  "taken": "2026-10-13T13:44:41Z",
  "windows": [
    {
      "iterm_window": 155,
      "tabs": [
        {"agent": "claude", "session": "30f0b4cc-…", "cwd": "~/src/entireio/cli/.claude/worktrees/credential-helper-non-jwt-fallback", "panes": ["agent", "shell", "tail"]},
        {"agent": "", "session": "", "cwd": "~/src/dvydra/midnight-magic", "panes": ["shell", "shell"]}
      ]
    }
  ]
}
```

### Guards

- iTerm not running, or zero windows listed: no write. On shutdown iTerm can quit before the tails, and an empty snapshot must never replace a good one.
- Any osascript/ps/lsof failure: no write, silent. A snapshot is best-effort and must never disturb the tail.
- Cost: about 0.2s of osascript + ps + lsof every 30s, in one process.

## Part 2: `entire-tail restore`

### Trigger

- `entire-tail restore` (also `entire tail restore`), from any pane.
- The first bare `entire-tail` after a reboot shows one footer line in the tree: `⟲ N tabs from before the reboot · R to restore`. `R` runs the same restore. The line is gone once the snapshot is marked restored, or after `entire-tail restore --dismiss`.

### Default flow

1. Find the newest `layout/<boot>/` whose boot is older than the current boot. None, or already marked restored: print why and exit 0.
2. For each saved window, in saved order, open one new iTerm window. For each saved tab, in order:
   - Claude with a session whose transcript exists: the `workspaceScript` layout, agent `--resume <id>`, tail `--follow-session <id>`, plus `accountEnvPrefix` and `tapEnvPrefix`.
   - Amp with a thread id: `ampWorkspaceScript`.
   - No session: a single-pane shell tab cd'd to its saved cwd.
   - cwd is `workspaceCwd` for sessions, so a deleted worktree falls back to its checkout; a plain shell tab whose cwd is gone opens in `~`.
3. Print one line per tab: `✔ <repo> · <branch> · <title>` or `skipped: <reason>` (transcript missing, iTerm error).
4. Write `layout/<boot>/restored`. A second run, or `R`, prints `already restored (--force to redo)`.
5. Ask `close the restored windows? y/N`. On `y`, close them by window id (see below), the window restore runs in last.

### Which windows count as "restored"

Windows open when restore started, excluding the ones it created, where every pane is an idle login shell. The pane running `entire-tail restore` counts as idle.
A window with any running agent, tail, or other foreground process is never closed. The prompt lists the windows it would close with their tab counts.

### Flags

- `--pick`: list saved windows from the newest pre-reboot snapshot (tabs as `repo · branch · agent · title`), choose one by number, restore only that window.
- `--force`: ignore the `restored` marker.
- `--dismiss`: write the marker without restoring.
- `--dry-run`: print the plan, open nothing.

### Script building

A new pure builder, `restoreWindowScript(tabs)`, emits one AppleScript per window: `create window … command` for the first tab, `create tab … command` for the rest, then the existing `splitScript` splits. Every pane starts through `paneCommand` (`$SHELL -lc '<cmd>; exec $SHELL -l'`), so nothing is typed into a pane and each pane is still a shell when its command ends.
Pane A cannot use `execAgent` here (it is not our pane), so it starts as a `command` too, the same way a fresh workspace in an already-split window does today.

## Files

- `layout.go`: snapshot types, pane classification, `chooseSnapshot`, `restoredWindows` (pure).
- `layoutsnap.go`: the IO half (osascript, ps, lsof, flock, write), called from the tail tick.
- `restore.go`: the `restore` subcommand, `restoreWindowScript`, report and prompt.
- `tree.go`: footer line and `R` key.
- `main.go`: subcommand dispatch.

## Testing

- Unit: pane classification from fixture `ps`/registry data, snapshot choice across boots, restored marker, the idle-window filter, `restoreWindowScript` output (quoting, tab order, account and tap env), report lines.
- `osascript`, `ps`, `lsof` sit behind package vars so `go test` never touches iTerm or the machine (same rule as the `launchctl` and `clipboardWrite` stubs).
- `go vet ./... && go test -race ./...` must pass; no golden changes expected.
- By hand: run a tail for 30s, check `snapshot.json`, close a workspace tab, run `entire-tail restore --force --dry-run`, then for real.

## Limits

- Only layouts saved after this ships can be restored. The pre-reboot layout from 2026-10-13 was never recorded.
- If no tail was running before the reboot, there is no snapshot.
- Splits are rebuilt with the standard workspace layout, not the exact pane sizes.
