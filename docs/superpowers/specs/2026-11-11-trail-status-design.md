# Trail status in the tail

## Why

When an agent pushes to a trail, or reviews one, Daniel wants to know without opening a browser whether the build passed, the agents passed, the branch is up to date and whether there are new findings. Right now that means switching to entire.com or running `entire trail show`. The idea came from herdrui-trails (github.com/entireio/herdrui-trails), a herdr sidebar that mirrors the entire.io change pane.

entire-tail can't have a sidebar. It has no alt-screen and relies on native scrollback, and the pinned status bar was removed on 2026-10-17 because scroll-region resets leaked stale copies into scrollback (see CLAUDE.md, "There is no status bar, on purpose"). Rewriting the tail as a full-screen TUI to get a pinned area was considered and rejected: it would lose native scrollback, Cmd-F, drag-select copy and resize reflow.

So the design is: when trail state changes, print a status line into the tail at that moment, and a key opens a scrollable modal with the full state.

## Goals

1. Track every trail this session is pushing to or reviewing, both the project trail (`P32`) and each repo change (`k3s-config/10`). Keep the two kinds of id separate everywhere: in state, in output, and in the commands we run.
2. When something changes, print one status line into the tail naming what happened. The line is a point-in-time record and stays in scrollback.
3. A key (`g`) opens a scrollable alt-screen modal with the full state of every tracked trail.

## Non-goals

- No actions in v1: no merge, approve, resolve/dismiss finding, run agent. Read-only. (My call; herdrui-trails has these and they can come later.)
- No sidebar, no pinned row, no renderer rewrite.
- No iTerm badge. It was considered and dropped in favour of the inline line.
- No trail tracking for Codex or agy sessions in v1. Claude and Amp only, because those are the agents whose transcripts `trails_scan.go` already reads. (My call.)

## Anti-goals (things that would make this a failure even if it works)

- A status line on a poll where nothing changed. The line has to mean something happened, or it becomes noise to scroll past.
- One line per finding while working through a list of them.
- Errors printed every poll when `entire` is missing, logged out, offline or sandboxed.
- Status lines disappearing on re-render (`c`, `t`, `T`, `w`, `r`, resize), since re-render wipes the screen and reprints the transcript.
- Confusing a project trail number with a change number, or passing one where the other is expected.
- Changing any existing golden. Pipes and `--no-pick` runs must render byte-identically unless trail tracking is on and a trail is found.

## Which trails a session tracks

Two sources, both re-evaluated as the session moves:

1. **Pushing.** The session's current cwd (`sessionCwdNow` in `agentpane.go`, not `sessionCwd`, which is where it started). Its branch (`git branch --show-current`) is looked up with `entire trail show --branch <b> --repo <r> --json`. The entry in `changes[]` with `current: true` is the change; the top level is its project trail. Re-check when the cwd or branch changes (worktree hop, checkout). Exit code 1 with "no discoverable project trail" means "no trail", not an error (per herdrui-trails RECIPE).
2. **Reviewing.** Trails the transcript names, in user messages, assistant text and tool-call input (my call):
   - entire.com and entire.io URLs in every form the `entire agent-help trail` table lists, including project-trail URLs (`<forge>/<project>/-/trails/<P>`) and nested change URLs. `parseTrailsURL` and the `trailURL` regex (`wtf_scan.go`) only match `https://entire.io/(gh|et)/<owner>/<repo>/trails/<n>` today, so they need extending; `threads_check.go` already handles the project-trail form.
   - `entire trail <subcommand> <selector>` in a tool call's input. `trailsLine` skips tool input on purpose (`trailClaimSource`), so this is a new parser. A bare selector is resolved against the repo of that tool call's cwd, using `--project` when the command has one.
   - A project trail URL tracks every change in it. A change URL tracks that change plus its project trail header.

**Lifetime (decided by Daniel, 2026-11-11).** A pushing trail is tracked for as long as the session's branch has it. A mentioned trail is tracked only while the session is live (the tail is following, not just backfilling a dead session) and only if it was mentioned in the last 24 hours. Any tracked change drops out once it is merged or closed, after printing that event. At most 10 tracked changes per tail; past that, the most recently mentioned win and pushing trails always win.

Map URLs to selectors with the table in `entire agent-help trail`. Never pass a URL to the CLI. Look changes up as `<repo>/<n>`, never `<n> --repo <r>`: `threads_check.go` records that `entire trail show <n> --repo <r>` ignores the repo and returns project trail `<n>`. After every lookup, check the returned identity matches what was asked for (as `trailsLookup` does) and drop it if not.

## Data

Verified against the au API on 2026-11-11 (change `midnight-magic/5`, project `et/dvydra`):

- `entire trail show <P|repo/n> --json`: project trail `number`, `title`, `status`, `url`, `project.reference` (`forge`, `project`), `project.jurisdiction`, and `changes[]` with `repositoryId`, `repository`, `number`, `branch`, `status`, `url`, `current`.
- `entire api -j <jurisdiction> /api/v1/repos/{repositoryId}/trails/{n}`: `agents[]` (verdicts and history, shape documented in herdrui-trails RECIPE.md), `mergeability` (`mergeable`, `behind_by`, `conflict_status`, `gates[]`), `ready_for_review`, `status`, `base`.
- `…/trails/{n}/gates`: `gates[]` with `gate_key`, `gate_type`, `status`, `state`, `outcome`, `value` (the `up_to_date` gate carries `value.behind_by`), `finding_count`. CI shows up as a plugin gate (seen: `gate_key: "buildkite"`, `gate_type: "plugin"`). Gates with `state: "disabled"` are hidden.
- `/api/v1/trails/{forge}/{project}/{repo}/{n}/reviews/comments`: findings. Open unless `status` is `resolved` or `dismissed`.

`entire trail watch <repo>/<n> --json` streams events (SSE) and could replace polling. v1 polls, because it's simpler and herdrui-trails shows polling is enough. The builder may switch to `watch` if it turns out simpler; the event detection below must work either way.

Several tails often watch the same trail. My call: each tail diffs and prints on its own, but fetched state is shared through a small disk cache keyed by change id (`~/.claude/entire-tail/trailstatus/`), reused while younger than the poll interval, so N tails cost about one set of API calls. The `entire trails` daemon is not used; it stores trail metadata and associations, not agent/gate/finding state.

Polling cadence (from herdrui-trails): every 10s while any agent or gate is running, every 60s once settled, and immediately when the session's HEAD changes (a push usually follows a commit). All network calls run off the render goroutine with a timeout (8s, as `trailsLookup` already does). Only the render goroutine prints (the existing rule: the keyboard and background workers only signal).

## Events

On each fetch, diff the new state against the last one for that change. Each row below is one event. The first fetch for a trail sets the baseline and prints one line with the current state, so it's visible that the trail is now tracked (my call).

| Event | Severity | Source |
|---|---|---|
| `<gate> failed` / `<gate> green again` | bad / good | each enabled `plugin` gate, named by its `gate_key` (e.g. "buildkite failed"), so a non-CI plugin gate isn't mislabelled "build" (my call) |
| out of date: N behind main | warn | `up_to_date` gate / `mergeability.behind_by`, printed when it goes from 0 to >0, not on every increase |
| merge conflict / conflict resolved | bad / good | `mergeability.conflict_status` |
| N new findings (k high) | bad if any high, else warn | open finding count went up |
| findings cleared | good | open count went to 0 (no line for decreases above 0) |
| agent X: Pass → Fail (or back) | bad / good | agent verdict changed |
| needs review / approved | warn / good | gate with `gate_key: "approvals"` (seen in the API) |
| ready to merge | good | `mergeability.mergeable` became true |
| merged / closed | neutral | change `status` |

Recovery events exist so a red line is never the last word on something that has since been fixed.

## The status line

One line per change per fetch, only when that fetch produced events. Events are joined with ` · `. Every line names the change and its project trail, so a line read alone in scrollback is complete:

```
⛬ k3s-config/10 (P32) · build failed · 2 new findings (1 high)
```

- Daniel asked for "a border or background colour representing the goodness/badness". My call: a full-width background band (red, amber, green, or dim grey for neutral) with contrasting text, because the box headers already use borders and a filled band reads as different from a turn. Severity of the line is the worst event on it.
- On a tty the band is padded to the terminal width. On a pipe there's no padding and no band, just the text, so goldens stay byte-identical (same rule as `wrapWidth`).
- `k3s-config/10` and `P32` are OSC 8 links to their entire.com pages (`osc8` in `preview.go`). On a tty only.
- The line is printed between turns: it must close any open body line / dot streak first (`endLine()`), like other markers.
- A status line also rings the terminal bell when it's bad (my call; same one-shot bell the question card uses). Easy to drop.

## Re-renders

`rerender` wipes the screen and scrollback and reprints the whole transcript. Status lines aren't in the transcript, so the renderer keeps them (wall-clock time + text + severity) and re-inserts each one on reprint after the last transcript record whose timestamp is at or before it. They survive `c`/`t`/`T`/`w`/`r`/resize and are cleared only when the tail switches session (Ctrl-X to the tree, lineage rollover keeps them). How exactly they're merged into the record stream is the builder's call.

## The modal (`g`)

`g` is unbound today (checked `keyboard.go`). It opens an alt-screen overlay using the same hand-off as `→` focus and `?` settings: the keyboard sends on `overlayCh` and parks on `resumeCh`, the render goroutine draws on the same tty fd. `↑↓`/PgUp/PgDn scroll, `r` refetches, `q`/Esc closes. Panel chrome from `help.go` (`drawPanel`).

Content, top to bottom, for each tracked project trail:

1. Project trail: `P32`, title, status, link.
2. Each tracked change in it: `repo/n`, branch → base, status, link. Then:
   - agents: name, current verdict, last 6 runs as small glyphs (oldest left), Failed/Waiting marker
   - gates: one row each with glyph and detail (`Required`, `11 of 11 passed`, `3 commits behind`); the merge summary (`Ready`, `Merge blocked: …`, `Needs review`, `Merged`)
   - open findings grouped High → Medium → Low → Unspecified: author, `file:line`, first paragraph
3. Then the session's status-line history, newest first.

With nothing tracked: "No trail for this session" and which branch was checked. With a fetch error: the CLI's last line.

## Opt-in or default

My call: on by default for Claude and Amp tails on a tty, off on pipes. It shells out to `entire` and reaches the network, which is a change for a tool that's otherwise local; if that's unwelcome it becomes a `?` panel setting (persisted via `prefs.go`) defaulting off. Without `entire` on PATH it does nothing and says nothing.

## Constraints

- Single tty reader, render goroutine does all drawing, `go test -race` clean.
- No new runtime dependencies. `entire` is optional.
- Every IO step (`entire` calls, `git` calls) behind a package var so tests never touch the network or a real trail.
- Pure split as elsewhere: state diff → events (`trailEvents(old, new)`), events → line, state → modal lines, all unit-tested without a tty or network. Goldens for the status line in the pipe form.

## Left to the builder

- File layout (suggest `trailstatus.go`, `trailstatus_view.go`).
- How status lines are merged into the re-render.
- Poll vs `entire trail watch`.
- Exact colours (use the theme where it has a fitting colour).
- Caching across restarts.

## Deferred

- **Main build is red.** Daniel asked for it, but the change and gates endpoints carry no base-branch CI status. Dropped from v1 until a source is found.
