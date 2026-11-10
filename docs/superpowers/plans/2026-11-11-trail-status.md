# Trail Status in the Tail Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a trail this session pushes to or reviews changes state, print a coloured status line into the tail; `g` opens a scrollable modal with the full state.

**Architecture:** A tracker goroutine resolves which trails to watch (the session's branch, plus trail URLs and `entire trail …` commands in the transcript), polls them through the `entire` CLI, diffs each fetch against the last, and sends updates on a channel. The render goroutine prints the line, remembers it by transcript byte offset so re-renders put it back in place, and keeps the latest state for the modal.

**Tech Stack:** Go, single `package main`; `entire` CLI as an optional subprocess.

**Spec:** `docs/superpowers/specs/2026-11-11-trail-status-design.md`

## Global Constraints

- Only the render goroutine writes to the screen. The tracker goroutine and keyboard only send on channels. `go test -race ./...` stays clean.
- Every `entire` and `git` call goes through a `wtfCommandRunner` value (`wtf_scan.go:20`) held in a package var or passed in, so tests never touch the network, git, or a real trail.
- Never pass a URL to the CLI. Look up a change as `entire trail show <repo>/<n> --project <forge>/<project> --json`, never `<n> --repo`. After every lookup, check the returned change's repository and number match the ref; drop it otherwise.
- Project trail keys and change keys never share a namespace: change `et/dvydra/k3s-config/10`, project `et/dvydra/-/32`.
- Off on pipes, off without `entire` on PATH, off with `--no-trails` or `ENTIRE_TAIL_NO_TRAILS=1`. No existing golden changes.
- Errors from `entire` (missing login, offline, sandbox, 404) never print to the tail. They show only in the modal.
- Poll every 10s while any agent or gate on that change is running, else 60s; refetch immediately on a new HEAD in the session's cwd. 8s timeout per CLI call.
- Mentions older than 24h are ignored. At most 10 tracked changes; pushing changes always kept, then most recently mentioned. Merged or closed changes print that event once and are dropped.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. A session mentioning the same trail many times (every tool call in a review) must track it once and print nothing extra. Test in Task 4.
2. A change URL whose project trail is also mentioned must produce one tracked change, not a project expansion plus a duplicate. Test in Task 4.
3. `entire` failing on every call (logged out) must produce zero status lines and no stderr output, and keep retrying at the slow cadence. Test in Task 4.
4. A re-render after a lineage rollover must not print notes recorded against the previous transcript file. Test in Task 5.
5. A fetch where every field is unchanged except `updated_at`-style noise must produce no events. Test in Task 3.

---

### Task 1: Trail references and mention parsing

**Files:**
- Create: `trailref.go`, `trailref_test.go`

**Interfaces:**
- Produces:
  - `type trailRefKind int` with `trailRefChange`, `trailRefProject`
  - `type trailRef struct { Kind trailRefKind; Forge, Project, Repo string; Number int }`
  - `func (r trailRef) Key() string` (change `et/dvydra/k3s-config/10`, project `et/dvydra/-/32`)
  - `func (r trailRef) Label() string` (change `k3s-config/10`, project `P32`)
  - `func (r trailRef) ProjectFlag() string` (`et/dvydra`)
  - `func parseTrailRefURL(raw string) (trailRef, bool)`
  - `type trailMention struct { Ref trailRef; Partial string; Cwd string; At time.Time }` (`Partial` set and `Ref` zero when a command gave a selector with no `--project`)
  - `func trailMentionsInLine(line []byte) []trailMention`

- [ ] **Step 1: Write the failing tests**

`TestParseTrailRefURL`, table-driven. https only, host `entire.com` or `entire.io`, forge `gh` or `et`, extra trailing path segments ignored:
- `https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10` → change `{et dvydra k3s-config 10}`
- `https://entire.com/et/dvydra/k3s-config/-/trails/32/changes/10` → change `{et dvydra k3s-config 10}`
- `https://entire.io/gh/entirehq/entiredb/trails/4844` → change `{gh entirehq entiredb 4844}`
- `https://entire.com/et/dvydra/-/trails/32` → project `{et dvydra "" 32}`
- `https://entire.com/et/dvydra/-/trails/32/changes/k3s-config/10/files` → change (trailing segment ignored)
- rejected: `http://` scheme, `github.com` host, forge `gl`, number `0`, `abc`, `https://entire.com/et/dvydra/-/trails`.

`TestTrailRefKeysNeverCollide`: project 10 in `et/dvydra` and change `-/10` can't be built from URLs, and `Key()` of project `{et dvydra 32}` != `Key()` of change `{et dvydra x 32}`; `Label()` gives `P32` and `x/32`.

`TestTrailMentionsInLine` on raw Claude JSONL lines:
- a user line with `"timestamp":"2026-11-11T01:02:03.000Z","cwd":"/r"` and text containing the first URL above → one mention, full `Ref`, `At` = that time, `Cwd` `/r`
- an assistant tool_use whose input is `entire trail finding k3s-config/10 --project et/dvydra` → full change ref
- `entire trail show 32` with no `--project` → `Partial: "32"`, `Cwd` set, `Ref` zero
- `entire trail checkout k3s-config/10` → `Partial: "k3s-config/10"`
- `entire trail list --project et/dvydra` and `entire trail create` → no mention
- a line naming the same URL twice → one mention
- a line with no `timestamp` → `At` zero

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'TestParseTrailRefURL|TestTrailRefKeysNeverCollide|TestTrailMentionsInLine' ./...`
Expected: FAIL, undefined names.

- [ ] **Step 3: Implement in `trailref.go`**

URL finding: regex `https://entire\.(?:com|io)/[^\s"'\\<>)\]]+` over the raw line, then `parseTrailRefURL`. Commands: regex for `entire trail (show|checkout|finding|watch|approvals|approve|resume|merge|comment|update|link)\s+(\S+)` plus an optional `--project\s+(\S+)` anywhere after it in the same command; selector `^[A-Za-z0-9_.-]+/\d+$` is a change, `^\d+$` a project trail, anything else (flags, URLs) is skipped. Timestamp and cwd from `"timestamp":"…"` and `"cwd":"…"` by regex, not a full JSON decode (this runs on every line). Dedupe by `Key()` or `Partial` within the line.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -run 'TestParseTrailRefURL|TestTrailRefKeysNeverCollide|TestTrailMentionsInLine' ./...`
Expected: PASS

- [ ] **Step 5: Commit**

`git add trailref.go trailref_test.go` then `git commit -m "feat(trailstatus): parse trail references from transcripts"`

---

### Task 2: Change state: parse and fetch

**Files:**
- Create: `trailstate.go`, `trailstate_test.go`, `testdata/trailstatus/show.json`, `testdata/trailstatus/change.json`, `testdata/trailstatus/comments.json`

**Interfaces:**
- Consumes: `trailRef` (Task 1), `wtfCommandRunner`.
- Produces:
  - `type trailSev int` with `trailSevNeutral, trailSevGood, trailSevWarn, trailSevBad` (ordered so `max` gives the worst)
  - `type trailGate struct { Key, Type, Outcome, Detail string }` (`Outcome` is `passed`, `failed` or `""` for not finished; disabled gates are never stored)
  - `type trailAgent struct { Name, Verdict, RunStatus string; Sev trailSev; History []trailSev }` (`Verdict` is `Pass`/`Fail`/`Low`/`Med`/`High`/`N%`/`""`)
  - `type trailFinding struct { ID, Severity, Author, File string; Line int; Body string }` (open only)
  - `type changeState struct { Ref trailRef; ProjectNumber int; ProjectTitle, ProjectURL, URL, Title, Branch, Base, Status string; Agents []trailAgent; Gates []trailGate; Findings []trailFinding; Mergeable bool; BehindBy int; Conflict string; FetchedAt time.Time }`
  - `func (s changeState) Running() bool` (any agent `RunStatus` in `queued`/`running`/`pending`, or any gate with empty `Outcome`)
  - `func (s changeState) Gate(key string) (trailGate, bool)`
  - `func parseChangeState(ref trailRef, show, change, comments []byte) (changeState, error)`
  - `func fetchChangeState(ctx context.Context, run wtfCommandRunner, ref trailRef, home string, now time.Time) (changeState, error)`
  - `func fetchProjectChanges(ctx context.Context, run wtfCommandRunner, ref trailRef) ([]trailRef, error)` (project ref → its open change refs)
  - `func fetchBranchChange(ctx context.Context, run wtfCommandRunner, repo, branch string) (trailRef, bool, error)` (`repo` is `forge/owner/repo`; `false, nil` on "no discoverable project trail")

- [ ] **Step 1: Capture fixtures**

Save trimmed real responses (spec's "API shapes" section; names and bodies may be rewritten to synthetic values) for one open change with three agents, all five gate keys (`approvals`, `base_checks`, `checks`, `findings`, `up_to_date` disabled) and two findings (one `open` high, one `resolved` low). Commands: `entire trail show <repo>/<n> --project <p> --json`, `entire api -j <j> /api/v1/repos/<repositoryId>/trails/<n>`, `entire api -j <j> /api/v1/trails/<f>/<p>/<repo>/<n>/reviews/comments`.

- [ ] **Step 2: Write the failing tests**

`TestParseChangeState` on the fixtures: `ProjectNumber`, `ProjectTitle`, `URL` is the change entry's `url` (the one with `current: true`), three agents with expected `Verdict`/`Sev`, the disabled `up_to_date` gate absent, `Gate("checks").Outcome == "passed"`, `Gate("base_checks")` present, one finding with `File`/`Line`/`Author`/first paragraph only in `Body`.
`TestAgentVerdict` table: boolean `true` with polarity `lower_is_better` → `Fail`/bad; `false` → `Pass`/good; percent 70 higher-is-better → `70%`/good, 50 → warn, 20 → bad (good from 67, warn from 34, inverted for lower-is-better); size `small` with lower-is-better → `Low`/good, `large` → `High`/bad.
`TestParseChangeStateRejectsOtherChange`: a show response whose changes don't include `ref.Repo`/`ref.Number` → error.
`TestFetchChangeStateUsesCache`: fake runner counting calls; two fetches 5s apart with the same `home` → one set of calls; second fetch 61s later → two sets. Cache file lives under `home/.claude/entire-tail/trailstatus/`.
`TestFetchBranchChangeNoTrail`: runner returns exit error with stderr text `no discoverable project trail` → `(trailRef{}, false, nil)`; other errors → returned.

- [ ] **Step 3: Run to verify they fail**

Run: `go test -run 'TestParseChangeState|TestAgentVerdict|TestFetchChangeState|TestFetchBranchChange' ./...`
Expected: FAIL, undefined names.

- [ ] **Step 4: Implement in `trailstate.go`**

`fetchChangeState`: read cache file `<Key with / → _>.json` and return it if `now - FetchedAt < 60s` and not running, or `< 10s` if running; else run the three calls (each with an 8s `context.WithTimeout`), take `project.jurisdiction` and the change's `repositoryId` from the show response, parse, write the cache via temp+rename. `fetchBranchChange` runs `entire trail show --branch <b> --repo <repo> --json` and returns the `current: true` change. `exec.ExitError.Stderr` carries the "no discoverable" text.

- [ ] **Step 5: Run to verify they pass**

Run: `go test -run 'TestParseChangeState|TestAgentVerdict|TestFetchChangeState|TestFetchBranchChange' ./...`
Expected: PASS

- [ ] **Step 6: Commit**

`git add trailstate.go trailstate_test.go testdata/trailstatus` then `git commit -m "feat(trailstatus): read change state through the entire CLI"`

---

### Task 3: Events and the status line

**Files:**
- Create: `trailevents.go`, `trailevents_test.go`

**Interfaces:**
- Consumes: `changeState`, `trailSev` (Task 2).
- Produces:
  - `type trailEvent struct { Text string; Sev trailSev }`
  - `func trailEvents(prev *changeState, cur changeState) []trailEvent` (`prev == nil` is the first fetch)
  - `func trailLineText(cur changeState, evs []trailEvent, links bool) string`
  - `func trailLineSev(evs []trailEvent) trailSev`
  - `func trailBand(text string, sev trailSev, width int) string`

- [ ] **Step 1: Write the failing tests**

`TestTrailEvents`, one case per row of the spec's Events table, built from a base `changeState` with one field changed:
- `checks` outcome `passed` → `failed`: `{"build failed", bad}`; back: `{"build green again", good}`
- `base_checks` `passed` → `failed`: `{"main build is red", bad}`; back: `{"main green again", good}`
- plugin gate `buildkite` `passed` → `failed`: `{"buildkite failed", bad}`; back: `{"buildkite green again", good}`
- `BehindBy` 0 → 3: `{"out of date: 3 behind main", warn}` (uses `Base`); 3 → 5: no event; 5 → 0: no event
- `Conflict` `clean` → `conflict`: `{"merge conflict", bad}`; back: `{"conflict resolved", good}`
- findings 0 → 2 with one high: `{"2 new findings (1 high)", bad}`; 0 → 1 low: `{"1 new finding", warn}`; 2 → 1: none; 1 → 0: `{"findings cleared", good}`
- agent `Code review` `Pass` → `Fail`: `{"Code review: Pass → Fail", bad}`; back: good
- `approvals` `""` → `failed`: `{"needs review", warn}`; → `passed`: `{"approved", good}`
- `Mergeable` false → true: `{"ready to merge", good}`
- `Status` `open` → `merged`: `{"merged", neutral}`; → `closed`: `{"closed", neutral}`
- identical states except `FetchedAt` → no events
- `prev == nil`: one event summarising current state: `agents 3/3 ✔` (count of good agents over all), `N findings`, and the merge summary `Ready` / `Merge blocked` / `Needs review` / `Merged`; sev = worst of those.

`TestTrailLineText`: `trailLineText(s, evs, false)` == `⛬ k3s-config/10 (P32) · build failed · 2 new findings (1 high)`; with `links` true the label and `P32` are wrapped with `osc8` (`preview.go:94`) to `s.URL` and `s.ProjectURL`.
`TestTrailBand`: width 40 pads the visible text to 40 columns (measure with `visWidth`), wraps it in the sev's background + foreground SGR and ends with `\x1b[0m`; width 0 → no padding. Backgrounds: bad `48;2;150;30;30`, warn `48;2;150;100;0`, good `48;2;30;110;50`, neutral `48;2;60;60;60`; foreground `38;2;255;255;255` bold.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'TestTrailEvents|TestTrailLineText|TestTrailBand' ./...`
Expected: FAIL

- [ ] **Step 3: Implement in `trailevents.go`**

Compare gate outcomes only when both sides have the gate and the new outcome is non-empty (a gate restarting is not an event). Events are ordered as in the spec table.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -run 'TestTrailEvents|TestTrailLineText|TestTrailBand' ./...`
Expected: PASS

- [ ] **Step 5: Commit**

`git add trailevents.go trailevents_test.go` then `git commit -m "feat(trailstatus): turn state changes into status lines"`

---

### Task 4: The tracker

**Files:**
- Create: `trailtrack.go`, `trailtrack_test.go`

**Interfaces:**
- Consumes: Tasks 1–3; `sessionCwdNow` (`agentpane.go:76`), `trailsRepoRemote` (`trails_scan.go:429`).
- Produces:
  - `type trailUpdate struct { State changeState; Events []trailEvent; Dropped bool }`
  - `type trailTrackerDeps struct { Run wtfCommandRunner; Now func() time.Time; Home string }`
  - `type trailTracker struct` (unexported fields)
  - `func newTrailTracker(deps trailTrackerDeps, out chan<- trailUpdate) *trailTracker`
  - `func (t *trailTracker) Observe(ms []trailMention)` (non-blocking; safe from the render goroutine)
  - `func (t *trailTracker) Follow(path string)` (the transcript now being tailed; cwd re-read from it)
  - `func (t *trailTracker) Refresh()` (the modal's `r`)
  - `func (t *trailTracker) Errors() map[string]string` (last error per key, for the modal; returns a copy)
  - `func (t *trailTracker) Run(ctx context.Context)` (blocks; one tick per 10s)
  - `func (t *trailTracker) step(ctx context.Context)` (one tick; tests call this directly)

- [ ] **Step 1: Write the failing tests**

All with a fake runner keyed on the joined args and a fake clock; `step` called directly.
- `TestTrackerPushingBranch`: `Follow` a transcript fixture whose last line has `"cwd":"/r"`; runner answers `git -C /r branch --show-current` with `feat`, `git -C /r remote get-url origin` with `entire://x.entire.io/et/dvydra/claude-tail`, `git -C /r rev-parse HEAD` with a sha, and the trail show/api calls with Task 2's fixtures. One `step` → one `trailUpdate` with the baseline event.
- `TestTrackerMentionOnce` (Review Focus 1): `Observe` the same mention 50 times, `step` twice with unchanged state → exactly one update (the baseline).
- `TestTrackerChangeAndProjectMention` (Review Focus 2): mentions of change `k3s-config/10` and project `32` whose changes are `k3s-config/10` and `midnight-magic/6` → two tracked changes, not three.
- `TestTrackerIgnoresOldMentions`: mention with `At` 25h before `Now` → nothing fetched.
- `TestTrackerResolvesPartial`: `Partial: "k3s-config/10"`, `Cwd: "/r"` with origin `et/dvydra/…` → tracks `et/dvydra/k3s-config/10`.
- `TestTrackerCap`: 12 mentioned changes plus a pushing one → 10 tracked; the pushing one is among them; the two oldest mentions are not.
- `TestTrackerDropsMerged`: a change whose status goes `open` → `merged` sends one update with the `merged` event and `Dropped: true`, then is never fetched again.
- `TestTrackerCadence`: settled state fetched at t=0, not at t=10s or t=50s, again at t=60s; running state refetched at t=10s; new HEAD sha at t=10s refetches the pushing change even when settled.
- `TestTrackerSilentOnErrors` (Review Focus 3): every call errors → no updates over 7 steps spanning 70s, `Errors()` has the key, the runner was called again after 60s.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'TestTracker' ./...`
Expected: FAIL

- [ ] **Step 3: Implement in `trailtrack.go`**

State guarded by a mutex: mentions by key (latest `At`), pushing ref, previous `changeState` per key, next-due time per key, last HEAD, errors. `Observe` and `Follow` only record under the lock. `step` resolves partials (git remote of `Cwd` → `trailsRepoRemote`, project = first two segments), expands project refs with `fetchProjectChanges` (cache the expansion for 60s), re-reads the pushing branch when `Follow`'s path or the cwd's HEAD changed, applies the 24h/cap rules, fetches what's due, and sends an update only when `trailEvents` returns any. Sends use `select` with `ctx.Done()` so a closed tail never blocks the goroutine.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -race -run 'TestTracker' ./...`
Expected: PASS

- [ ] **Step 5: Commit**

`git add trailtrack.go trailtrack_test.go` then `git commit -m "feat(trailstatus): track the session's trails and poll them"`

---

### Task 5: Status lines in the tail

**Files:**
- Modify: `render.go` (add `trailNote`), `main.go` (`tailSession`: tracker start, `trailCh` case in the live `select`, `rerender` interleave, `emit` call sites feed `Observe`), `config.go` (`NoTrails` flag + env)
- Test: `trailnote_test.go`, `config_test.go`

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces:
  - `func (r *Renderer) trailNote(line string, bell bool)` (closes an open line with `endLine()`, writes `line + "\n"`, rings `\a` when `bell && r.live`)
  - `type trailNoteRec struct { Path string; Offset int64; Text string; Sev trailSev }`
  - `func notesUpTo(notes []trailNoteRec, path string, off int64, from int) (due []trailNoteRec, next int)` (notes for `path` with `Offset <= off`, starting at index `from`)
  - `Config.NoTrails bool` set by `--no-trails` and `ENTIRE_TAIL_NO_TRAILS=1`
  - in `tailSession`: `trailStates map[string]changeState` (render goroutine only), `trailNotes []trailNoteRec`, `tracker *trailTracker` (nil when off)

- [ ] **Step 1: Write the failing tests**

`TestTrailNoteClosesOpenLine`: renderer with an open dots streak; `trailNote("X", false)` output ends `]\nX\n`.
`TestNotesUpTo`: notes at offsets 10, 20, 30 for `a.jsonl` and 15 for `b.jsonl` (Review Focus 4); `notesUpTo(notes, "a.jsonl", 20, 0)` → the 10 and 20 notes, `next` = 2; the `b.jsonl` note is never returned for `a.jsonl`.
`TestConfigNoTrails`: `--no-trails` and `ENTIRE_TAIL_NO_TRAILS=1` each set `NoTrails`.
`TestGolden` unchanged (no tracker on pipes).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'TestTrailNoteClosesOpenLine|TestNotesUpTo|TestConfigNoTrails' ./...`
Expected: FAIL

- [ ] **Step 3: Implement**

- `config.go`: flag and env next to `--no-hook-install`; add to the usage text.
- `main.go` `tailSession`: start the tracker only when `isCharDevice(os.Stdout)`, agent is Claude or Amp, `!cfg.NoTrails`, and `exec.LookPath("entire")` succeeds. Run it on a context cancelled when `tailSession` returns. Call `tracker.Follow(cur)` at start and after every rollover.
- Feed `tracker.Observe(trailMentionsInLine(l))` for each line in the live `poll` path and in backfill (the 24h rule makes old ones inert). Not from `rerender`.
- New `select` case `case u := <-trailCh:` — store `trailStates[u.State.Ref.Key()] = u.State` (delete when `Dropped`), build `text := trailLineText(u.State, u.Events, true)`, `sev := trailLineSev(u.Events)`, call `r.trailNote(trailBand(text, sev, w), sev == trailSevBad)` where `w` is `wrapWidth(os.Stdout, false)`, append `trailNoteRec{cur, offset, text, sev}`, flush `out`.
- `rerender`: keep a running byte offset over `all` (each line's length + 1). After `emit(l)`, print `notesUpTo(trailNotes, cur, runningOffset, idx)` through `r.trailNote(trailBand(…), false)` into the same buffer; after the loop, print any remaining notes for `cur`. Bands use the current width, so a resize re-pads them.

- [ ] **Step 4: Run the full suite**

Run: `go vet ./... && go test -race ./...`
Expected: PASS, goldens untouched.

- [ ] **Step 5: Manual check**

`go build -o entire-tail . && ./entire-tail --agent claude --no-pick <a live session in a repo whose branch has a trail>`: one baseline band appears within ~10s; `c` twice re-renders with the band in the same place.

- [ ] **Step 6: Commit**

`git add render.go main.go config.go trailnote_test.go config_test.go` then `git commit -m "feat(trailstatus): print trail status lines into the tail"`

---

### Task 6: The `g` modal

**Files:**
- Create: `trailview.go`, `trailview_test.go`
- Modify: `keyboard.go` (`keyTrails` for `g`/`G`, overlay hand-off like `keyDrift`), `main.go` (`case keyTrails:` in the overlay switch), `settings.go` (`settingsKeys` lists `g  trail status`)

**Interfaces:**
- Consumes: `changeState`, `trailNoteRec`, `trailTracker.Errors`/`Refresh`, `drawPanel` (`help.go:52`), `setRawTimed` (`keyboard.go:291`), `termSize`.
- Produces:
  - `func trailModalLines(states []changeState, notes []trailNoteRec, errs map[string]string, branch string, theme Theme, w int) []string`
  - `func runTrailModal(tty *os.File, theme Theme, load func() ([]changeState, []trailNoteRec, map[string]string), refresh func())`

- [ ] **Step 1: Write the failing tests**

`TestTrailModalLines` with Task 2's parsed fixture, ANSI stripped: contains `P32` and the project title before the change; `k3s-config/10` with `branch → main`; one row per agent with its `Verdict`; one row per enabled gate; the merge summary; findings grouped `High` before `Low` with `file:line` and author; the status-line history newest first. Two changes in one project share one project header.
`TestTrailModalEmpty`: no states → `No trail for this session` and `branch feat checked`.
`TestTrailModalError`: an error for a key → its last line shown under that change.
`TestKeyActionForTrails`: `keyActionFor('g') == keyTrails`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'TestTrailModal|TestKeyActionForTrails' ./...`
Expected: FAIL

- [ ] **Step 3: Implement**

`runTrailModal` follows `runDrift` (`driftview.go:16`) for the alt-screen and raw-timed read, and the settings panel for scrolling: `↑↓`, PgUp/PgDn, `r` calls `refresh` and redraws on the next timed read, `q`/Esc returns. Re-measure the terminal every frame. Agent history glyphs: `■` coloured by sev, oldest left, last 6. Gate glyphs: `✔` good, `✗` bad, `…` running.

- [ ] **Step 4: Run the full suite**

Run: `go vet ./... && go test -race ./...`
Expected: PASS

- [ ] **Step 5: Commit**

`git add trailview.go trailview_test.go keyboard.go main.go settings.go` then `git commit -m "feat(trailstatus): g opens the trail status modal"`

---

### Task 7: Docs

**Files:**
- Modify: `CLAUDE.md` (architecture bullet for `trailref.go`/`trailstate.go`/`trailevents.go`/`trailtrack.go`/`trailview.go`, and a load-bearing note: notes are keyed by transcript byte offset and re-inserted by `rerender`; `<n> --repo` is never used; errors never print to the tail), `README.md` (the `g` key, `--no-trails`, what the band means)

- [ ] **Step 1: Write the docs**

One sentence per line, no em dashes (Daniel's STYLE.md).

- [ ] **Step 2: Commit**

`git add CLAUDE.md README.md` then `git commit -m "docs: trail status lines and the g modal"`
