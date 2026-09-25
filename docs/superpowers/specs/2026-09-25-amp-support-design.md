# Amp support across entire-tail

**Date:** 2026-09-25
**Status:** Approved design, pre-implementation

## Problem

entire-tail can render Claude Code, Codex, and Antigravity transcript files, but it cannot discover, list, follow, search, resume, or launch Amp threads. Adding only an Amp adapter would leave the session tree and the product's useful workflows unavailable because those paths are built around Claude transcript files and Claude process metadata.

Amp has a different storage contract. Threads are server-backed, may run locally, on a runner, or in an orb, and do not have a local transcript file. The supported CLI exposes the required data through `amp threads list --json`, `amp threads export`, `amp threads search --json`, `amp threads continue`, and `amp top --stream-jsonl`. A local Amp process also keeps a per-thread diagnostic log open at `~/.cache/amp/logs/threads/T-….log`; that log identifies the thread and signals changes but deliberately omits message bodies.

## Goal

Make Amp a first-class agent throughout entire-tail. Bare `entire-tail` shows Claude and Amp together. Every Amp thread owned by the user is eligible, whether its executor is local, a runner, or an orb. Agent-specific filters remain available.

The result must cover transcript rendering, discovery, the combined tree, preview and info, search, live view, process adoption, pane linking, workspace launch and resume, handover, hunk notification, status, yank, drift checks, tool display, questions, and child-thread markers. A missing Amp binary, expired login, network outage, or additive JSON schema change must not break Claude support.

## Source of truth

Use the Amp CLI as the compatibility boundary:

| Need | Command | Data used |
|---|---|---|
| Inventory | `amp threads list --json --include-archived` | id, title, updated time, working tree |
| Transcript | `amp threads export <id>` | version, messages, environment, mode, executor, current state |
| Search | `amp threads search --json <query>` | ranked server-side matches |
| Remote activity | `amp top --stream-jsonl` | changing active-thread set |
| Resume | `amp threads continue <id>` | interactive Amp TUI |
| Fresh session | `amp` | interactive Amp TUI in the selected directory |

The implementation must not call Amp's private HTTP endpoints or read its credential files. The local diagnostic log is only a notification and process-correlation source. Transcript content always comes from `amp threads export`.

Rejected alternatives:

- A direct API client would reduce subprocess overhead but bind entire-tail to private endpoints and authentication details.
- Parsing local logs would be fast and offline, but the logs omit message bodies and cannot cover runner or orb threads.
- Streaming a second attached Amp client per thread would alter thread presence, complicate terminal ownership, and still require inventory and search commands.

## Amp client and cache

Add an Amp client module that owns executable lookup, bounded command execution, JSON decoding, cache access, and error classification. Command functions are package variables or an interface so tests never call the real CLI or network.

Cache successful inventory and export payloads under the user's cache directory, namespaced to entire-tail and Amp. Writes use temp-file plus rename. Cache files contain thread metadata and transcript content already returned by Amp, never credentials, headers, or environment secrets.

Inventory refreshes are rate-limited. A warm cache opens the tree immediately and may be refreshed while the picker is active. With no cache, the first inventory call is bounded and reports a clear loading or connection error instead of falling back to an empty tree. A command failure keeps the last good data and retries with backoff.

An attached Amp tail exports immediately, then refreshes when one of these signals fires:

1. The local thread log changes.
2. `amp top` reports a state change for the thread.
3. The remote poll interval expires.

The export's top-level `v` is the snapshot version. Equal versions do no work. New complete messages are normalized and appended. Incomplete streaming blocks are held until Amp marks the message complete, avoiding duplicate partial text that the terminal cannot edit in place. A non-append change causes the existing full re-render path, which clears the prior copy before drawing the current snapshot.

## Canonical Amp adapter

Add `AgentAmp` and an adapter for the exported message objects:

- User text blocks become `KindUser`.
- Assistant text blocks become `KindAssistant`; thinking and redacted-thinking blocks remain hidden, matching every existing adapter.
- Assistant `tool_use` blocks become `KindToolUse` with the tool name, id, and input.
- User `tool_result` blocks become `KindToolResult`. Amp's `run` payload is reduced to output, error, duration, and summary fields without losing full command output in `full` mode.
- A final assistant message is marked done only when the export says the agent is idle and names that message as the last active message. Message completion alone is not a done signal because tool-use messages are also complete.
- `ask_user_choice` and Amp plugin input tools become `KindQuestion` cards when their exported input contains selectable or confirmable choices.
- `Task` becomes an agent-spawn marker. `create_thread` becomes a child-thread marker and stores the returned thread id when available.
- Result and execution errors render as tool failures rather than user turns.

The adapter ignores unknown content block types and unknown fields. It rejects malformed required fields with a visible source error. Additive Amp schema changes therefore degrade by omitting a new block, not by losing the whole transcript.

## Combined session model and tree

Generalize `treeSession` so a session has an `Agent`, stable session id, optional local transcript path, and a source locator. Claude continues to use a path. Amp uses its `T-…` id. Selection and follow code must stop treating `Path` as universally present.

The default tree merges the existing Claude/Entire tree with Amp inventory before grouping and sorting. A folder or repository may contain both agents. Rows carry fixed-width `C` and `A` markers so the owner of a session is always visible and columns stay aligned. Claude account marks remain separate. `--agent claude` and `--agent amp` filter the combined source; `auto` includes both.

Amp `file://` working-tree URIs normalize to local paths. Runner paths that exist locally group normally. Non-local paths and orb project metadata group under their project or remote path instead of inventing a local directory. Actions that require a local directory, such as hunk, are disabled with a reason on those rows.

The existing `--local` promise remains no-network. It may show cached Amp metadata but does not refresh it. `--cloud` retains its current Entire meaning; Amp inventory is part of normal combined mode because remote execution is intrinsic to Amp rather than optional enrichment.

Preview and info fetch or read the cached Amp export and feed it through the normal renderer. Metadata includes agent, mode, executor, working tree, updated time, thread URL, and the latest known state. The content filter searches cached preview text without exporting every row on each keystroke.

## Discovery and direct following

`--agent amp --no-pick` chooses the newest Amp thread whose working tree matches the current directory. If no exact match exists, it reports that fact before using the newest accessible Amp thread, matching the current explicit fallback behavior for other agents.

`--follow-session T-…` identifies Amp by its thread-id shape and follows its exported snapshots. `--agent amp --follow-session` is also accepted. Positional files remain file-backed and use content/path detection; exported Amp JSON is detected from its top-level id, messages, and environment shape when supplied as a file fixture.

Auto discovery compares the newest matching Claude transcript and Amp thread by update time. It does not classify an arbitrary unknown file as Amp.

## Live sessions and local process correlation

Amp local processes are discovered from top-level `amp` processes with a terminal. A process is associated with a thread only when `lsof` shows an open `~/.cache/amp/logs/threads/T-….log`. The same process supplies cwd and iTerm placement. Plugin runtimes and the `amp --no-tui` runner are excluded because they have no interactive terminal thread log.

`amp top --stream-jsonl` supplies runner and orb activity. Local process observations are merged with it, not replaced by it. The local mapping is exact even if `amp top` is delayed or unavailable; the remote set remains cached during reconnects.

`--live` renders Claude and Amp sessions through one live-session model. Amp busy/idle state comes from `amp top` or export metadata, never from transcript recency. Enter and `t` tail the selected live thread rather than launching a duplicate executor.

Bare startup and nearby marks inspect both Claude and Amp processes in the current iTerm tab. One nearby process selects that exact row in live view. Multiple nearby agents open the combined live view without guessing. Pane-link registry entries gain an agent field, and the daemon resolves both process types before pairing by agent plus session id.

## Workspace and keys

The combined tree uses these start keys:

- `c`: fresh Claude workspace.
- `a`: fresh Amp workspace.
- `n`: retained as an undocumented Claude alias for compatibility.
- `@`: fresh personal-account Claude workspace, unchanged.
- Enter: resume the selected session with its owning agent.

An Amp resume workspace runs `amp threads continue T-…` in pane A, `entire-tail --agent amp --follow-session T-…` in pane B, and a shell in pane C. A fresh Amp workspace runs `amp` in pane A and starts pane B in wait-for-new-Amp mode scoped to the directory and launch time. Once the new thread id appears, pane B follows that exact id. Amp does not accept a caller-provided thread id, so the Claude shared-UUID scheme does not apply.

Binary resolution uses `amp` from PATH with a clear error when unavailable. Claude's `--claude-bin` remains Claude-only. Resume commands shown in settings use the selected session's agent.

## Search, handover, hunk, and child threads

Search merges three sources: local Claude literal search, Entire checkpoint search, and `amp threads search`. Results deduplicate by agent plus session id and open through the same combined picker. Amp search is server-side and can return threads from other executors; cached metadata fills rows when available.

Today's handover inventory includes both Claude and Amp sessions. Manifest entries name their agent and use either a transcript path or Amp thread URL/id. Existing Claude launch remains the default handover writer for compatibility; `--agent amp` launches Amp with the same installed handover skill and manifest.

The hunk overlay reviews a local Amp thread's current working directory exactly as it does Claude's. Its notification targets the matching Amp pane and types the same handoff prompt there. A remote-only directory reports why local review is unavailable.

Amp `Task` calls have no separately exported subagent transcript, so the main stream shows spawn and result markers. A `create_thread` result that exposes a child id is focusable: the focus overlay reads that child through the Amp snapshot source. The UI must not offer focus when Amp does not expose the child transcript.

## Questions, permissions, and Claude-only features

An exported `ask_user_choice` call renders with the existing question card and bell. The card remains until its tool result arrives. Amp plugin select, confirm, and input calls use the same path when their exported input has enough structure.

Claude pending hooks, the Anthropic API tap, Claude account profiles, transcript lineage/relocation, and Claude sidecar subagent discovery remain Claude-only because their data contracts do not exist in Amp. The settings panel hides those actions for Amp or labels the Amp behavior rather than presenting controls that do nothing.

Amp permission dialogs are shown only if the supported export or activity stream exposes them. Local diagnostics must not be parsed for private prompt payloads, and absence of a public signal must not be presented as “no prompt pending.”

## Error handling

| Failure | Behavior |
|---|---|
| `amp` missing | Claude continues; Amp filter shows the install/path error |
| Logged out or access denied | Keep cached rows and transcript; show one status error |
| Network timeout | Keep last good snapshot; back off and retry |
| Malformed list entry | Skip that row and retain the rest |
| Malformed export | Keep prior snapshot and report the source error |
| `amp top` exits | Keep local live mappings, restart with bounded backoff |
| Thread deleted or access revoked | Mark unavailable, retain cached transcript for the current view |
| Remote working directory unavailable locally | Allow read/resume; disable local filesystem actions |

No Amp failure may delay or disable Claude file tailing. Errors are deduplicated so a polling failure does not print once per tick.

## Testing

Use scrubbed fixtures captured from real Amp CLI output:

- Inventory with local, runner, orb, archived, and non-local-path threads.
- Export with user text, assistant commentary/final text, ordinary tools, multiline shell output, failures, `ask_user_choice`, `Task`, `create_thread`, and an idle final message.
- Active-thread stream updates and reconnects.
- Local process/open-log mappings with interactive Amp, plugin runtimes, and a no-TUI runner.

Unit tests cover command construction, timeouts, error classification, atomic cache fallback, tolerant JSON parsing, snapshot version/dedup behavior, adapter records, mixed grouping and sorting, filters, previews, search merge, live merge, nearby selection, pane-link identity, workspace commands, wait-for-new matching, handover manifests, hunk targeting, and settings capability rows.

Add Amp render goldens for dots, full tools, question cards, child markers, and failures. Existing goldens must remain unchanged unless a deliberate combined-tree fixture changes.

Verification commands:

```sh
go test ./...
go vet ./...
go test -race ./...
```

Manual verification uses real data and covers the combined tree, `--agent amp`, a local active thread, an orb or runner thread, search, preview, resume, fresh `a` workspace, `--live`, nearby adoption, pane linking, question display, yank, drift, hunk, handover, an offline cached start, and recovery after reconnect.

## Documentation

Update `README.md`, command help, and `CLAUDE.md` to describe Amp as a first-class agent, the combined tree, `c`/`a` keys, network and cache behavior, supported Amp workflows, and the explicit Claude-only features. Remove the claim that adding an agent needs only an adapter and discovery function; the unified session-source contract becomes the extension point.
