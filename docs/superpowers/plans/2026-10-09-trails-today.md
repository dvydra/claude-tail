# Entire Trails Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task, or superpowers:subagent-driven-development if Daniel selects delegation. Steps use checkbox syntax for tracking.

**Goal:** Add `entire trails`, a searchable trail-first TUI for Right now and Today, with independent background harvesting and session navigation.

**Architecture:** Keep a separate versioned local catalog and collector, sharing existing Claude/Amp inventory and transcript primitives without invoking WTF scans. Foreground and LaunchAgent collection use the same exclusive writer lock; the TUI reads snapshots and sends refresh requests. Preserve evidence and session references across scans while deriving today's rows from event time and current branch associations.

**Tech Stack:** Existing Go package, standard-library JSON/files/processes, existing ANSI TUI helpers, macOS launchd, installed Entire and Amp CLI boundaries. No new runtime dependency.

**Spec:** `docs/superpowers/specs/2026-10-09-trails-today-design.md` (approved).

## Execution record

Implemented locally on `feat/trails-today`. Tasks 3 and 4 share one commit because the TUI requires exclusive collection from its first runnable version. `Observe` takes and returns a persisted cursor rather than keeping offsets in closures. Metadata lookups have a four-request scan budget; remaining entries stay due for subsequent scans.

Verification: full Go suite, race detector, vet, Amp plugin tests, build, shell syntax, and whitespace checks passed. Disposable-HOME PTY checks exercised populated/search/session-picker states, a real child tail and return, resizing, degraded sources, empty state, and clean exit. Captured terminal-emulator images were inspected. The 40-column check caught hidden quit instructions and now has compact hints and a regression assertion. Transcript rewriting, partial lines, Amp completion dedupe, branch-only discovery, identity rejection, degraded inventory, installer registration, lock contention, and persistence failure have targeted tests.

No actual LaunchAgent, authenticated live API integration, or remote publication was exercised. Installer and launchctl tests use stubs. Historical browsing, Codex/Antigravity harvesting, and remote branch identity without a local repository remain outside this implementation. The original checklist below records the planned approach, not a claim that every listed test scenario ran separately.

## Global constraints

- Keep `wtf` unchanged.
- No conflict detection, ownership assignments, worktree audits, notifications, AI summaries, automatic agent launches, or trail mutations.
- No historical browsing UI in the first version.
- Start with Claude and Amp. Today uses the machine's timezone and local midnight.
- Installing monitoring is an explicit action, not a side effect of opening the dashboard.

Work only in `/Users/dvydra/src/dvydra/claude-tail/.amp/worktrees/trails-today`, branch `feat/trails-today`. Preserve the separate `trails-continue` worktree. No installation, push, merge, or remote review creation is authorized by this plan alone. This repository currently has only a GitHub origin, so the standing Entire-repository publication rule does not apply.

## Review focus

1. A session switches branches: activity on the new branch must not revive its old trail (Task 2).
2. Amp completes a message after the export cache stopped updating: the catalog must read the live feed and avoid duplicates after restart (Task 2).
3. Midnight passes during an active session: keep the active trail, remove inactive yesterday rows, and honor the Melbourne DST day boundary (Tasks 1 and 3).
4. A daemon starts while a foreground collector owns the lock: neither may overwrite the other's state, and the UI must show the actual collector state (Task 4).
5. A trail title contains terminal escape sequences or a metadata response points at another host: display inert text and never open an unvalidated URL (Tasks 2 and 3).

## File ownership and existing APIs

Create `trails.go` for catalog types, persistence, and Today selection; `trails_scan.go` for discovery and enrichment; `trails_view.go` for reducer/render/TTY driver; `trailsdaemon.go` for exclusive collector lifecycle and LaunchAgent commands. Add corresponding `_test.go` files and synthetic fixtures under `testdata/trails/` only when a full transcript is needed.

Modify `config.go`, `main.go`, and `install.sh` for dispatch/registration; extend their existing tests. Update `README.md` and `CLAUDE.md` with the implemented behavior. Do not refactor unrelated code or copy the WTF conflict model.

Reuse `collectWTFSessions(home, now, loc, deps) []wtfSession` as an inventory-only input, `wtfSessionKey`, `localMidnight`, `trailMatches`, `claudeTrailEvents`/`ampTrailEvents` where their contracts fit, `ampLivePath`, `ampExportThreadWithin`, `ampEnvelope`, `termSize`, `decodeKey`, `setRawTimed`, and terminal restoration helpers. `extractTrailEvidence` is unsuitable as-is: its identity omits forge and it keeps only the earliest mention. `transcriptTrailEvents` reads Amp exports rather than newer feed data. `runWTF` is not a returning navigation loop.

### Task 1: Persistent catalog and Today selection

**Files:** Create `trails.go`, `trails_test.go`.

**Interfaces:** Define the following types and functions. JSON fields use lower-camel names; version is 1. Store times as Unix seconds, source offsets as bytes. The maps use canonical URL and agent-qualified session ID respectively.

```go
type trailsCatalog struct {
    Version int
    UpdatedAt int64
    Trails map[string]trailsEntry
    Sessions map[string]wtfSession
}
type trailsEntry struct {
    URL, Repo, Title, Status, Branch string
    MetadataAt, RetryAt int64
    Attempts int
    MetadataError string
    Associations map[string]trailsAssociation
}
type trailsAssociation struct {
    FirstAt, LastAt int64
    Branch, Evidence, Source string
    BranchMatched bool
}
type trailsRow struct {
    Key string
    Trail trailsEntry
    SessionKeys []string
    Active bool
    LastAt int64
}
func loadTrails(home string) (trailsCatalog, error)
func saveTrails(home string, catalog trailsCatalog) error
func selectTrails(catalog trailsCatalog, now time.Time, query string) []trailsRow
```

- [ ] Write catalog round-trip and selection tests. Use two URLs differing only in `/gh/` versus `/et/`; require two rows. Use one active branch-matched session, an active mention-only session, and a stopped session touched today. Require only the first row to have `Active=true`, retain the stopped row, and filter case-insensitively by title/repo/number/session name. A starting persistence test:

```go
func TestTrailsCatalogRoundTrip(t *testing.T) {
    home := t.TempDir()
    want := trailsCatalog{Version: 1, Trails: map[string]trailsEntry{}, Sessions: map[string]wtfSession{}}
    if err := saveTrails(home, want); err != nil { t.Fatal(err) }
    got, err := loadTrails(home)
    if err != nil { t.Fatal(err) }
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %#v, want %#v", got, want) }
}
```

- [ ] Run `go test ./... -run 'TestTrailsCatalog|TestSelectTrails' -count=1`; confirm the new tests fail before implementation.
- [ ] Implement private directory/file modes, temporary-write/sync/rename persistence, missing-file initialization, and rejection of corrupt/unknown-version state without overwriting it. Selection uses `localMidnight(now.Unix(), now.Location())`; active requires a current active session with matching repo/branch and a branch-backed association. Order active first, descending `LastAt`, then key. Never use scan time as trail activity.
- [ ] Extend selection tests at Melbourne midnight on 2026-10-04 and 2026-10-05: yesterday's inactive trail disappears but a still-active branch association stays. Cover absent session references and merged/closed trails touched today. Run targeted tests and `git diff --check`.
- [ ] Commit only Task 1 files: `git add trails.go trails_test.go` then `git commit -m 'feat(trails): persist today catalog'`.

### Task 2: Harvest session links and resolve branches

**Files:** Create `trails_scan.go`, `trails_scan_test.go`; extend catalog types in `trails.go` for source cursors and branch lookup cache.

**Interfaces:** Collection accepts injected inventory, transcript events, metadata lookup, and clock. Production readers own their incremental cursors. Persist updated cursors only with the catalog they produced, so a failed write cannot skip observations on restart.

```go
type trailsObservation struct {
    URL, Source, Evidence string
    At int64
}
type trailsScanDeps struct {
    Now func() time.Time
    Inventory func(context.Context) ([]wtfSession, error)
    Observe func(context.Context, wtfSession) ([]trailsObservation, error)
    Lookup func(ctx context.Context, repo, selector string, byBranch bool) (trailsEntry, error)
}
func scanTrails(ctx context.Context, prior trailsCatalog, deps trailsScanDeps) (trailsCatalog, error)
```

- [ ] Write scan tests with two sessions in different repos and the same branch name; lookup must not cross-link them. Make an inactive-today session introduce a trail, then remove it from inventory and require its stored association/session reference to survive. Replay an old observation at a later clock time and require unchanged `LastAt`. Switch the active session from branch A to B and require A to stop receiving activity.
- [ ] Run `go test ./... -run 'TestScanTrails' -count=1` and confirm red.
- [ ] Implement catalog merge on a fresh snapshot, marking missing sessions inactive only after a successful inventory read. Preserve prior activity with a degraded indication when inventory fails. Parse exact HTTPS Entire URLs, preserving forge in identity. Use `entire trail show <number> --repo <forge/owner/repo> --json`, or `entire trail show --branch <branch> --repo <forge/owner/repo> --json`. Resolve repo identity from remote or validated URL, not the display-only `owner/repo` inventory field. Do not guess a forge for unresolved remote sessions. Validate returned identity before accepting metadata. Skip default/detached/unknown branches. Cache no-result lookups as well as successes; retry failures with bounded backoff.
- [ ] Add production transcript-reader tests using a real temporary JSONL file: first write a partial line, scan, append its remainder, scan again, and require one observation with the original timestamp. Replace/truncate the file and require new messages to be read. Claude source filtering excludes tool content, task notifications, and injected context; where injection has no reliable marker, retain only labelled mention evidence, never branch proof. Missing timestamps use a stable first-observed fallback, not the current clock on every scan.
- [ ] For Amp, seed from the export and merge `ampEnvelope` feed lines keyed by `ProtocolMessageID`. An incomplete assistant message is eligible again when completed; duplicate complete feed/export messages are not. With no local feed, use bounded exports and last-good cache; track stale/error state. Test an export without a link followed by a completed feed message containing it, duplicate replay, and restart. Do not start a tail renderer for every harvested session.
- [ ] Run `go test ./... -run 'TestScanTrails|TestTrailsObserve|TestTrailsMetadata' -count=1`. Include title escape sequences, wrong-host/wrong-repo metadata, an unavailable Entire CLI, an Amp timeout, and failed metadata refresh preserving the last-good trail.
- [ ] Commit Task 2 files with `git commit -m 'feat(trails): harvest session associations'` after staging exact paths and checking whitespace.

### Task 3: Searchable TUI and returning session navigation

**Files:** Create `trails_view.go`, `trails_view_test.go`; modify `config.go`, `main.go`, `config_test.go`, and the existing `commandArgs` tests.

**Interfaces:** Pure reducer and renderer consume the catalog; the driver owns asynchronous collection and terminal input. Side effects are explicit results from the reducer.

```go
type trailsUI struct {
    Catalog trailsCatalog
    Query, SelectedKey, Error string
    Filtering bool
    Width, Height, Top int
    SessionKeys []string
    SessionCursor int
}
type trailsAction struct {
    OpenURL, SessionKey string
    Refresh, Quit bool
}
func updateTrails(ui trailsUI, key treeKey, r rune, now time.Time) (trailsUI, trailsAction)
func renderTrails(ui trailsUI, now time.Time) string
func runTrails(cfg Config) error
```

- [ ] Write reducer tests for `/`, text entry/backspace/Escape, selection retained after reordered refresh, no-match state, Enter canonical URL, and `s` choosing between two associated sessions. Test a session removed from disk reports unavailable instead of launching an agent. Test terminal controls in title/session text render inertly.
- [ ] Run `go test ./... -run 'TestTrailsUI|TestTrailsRender|TestTrailsDispatch' -count=1` and confirm red.
- [ ] Add `ActionTrails`, `Config.TrailsArgs`, pre-flag subcommand dispatch, and argv0 handling for `entire-trails`. Preserve `entire-wtf` behavior. Implement reducer/render with existing styles and width helpers; clip rows to height, retain footer, and distinguish empty catalog from search misses. Tick selection with the current clock so midnight does not require new file activity.
- [ ] Implement the timed raw TTY loop using the existing `(0, io.EOF)` timeout convention. Keep inventory/network calls in a cancellable worker. Restore tty/cursor/alt-screen on every exit. Enter uses `exec.Command("open", canonicalURL)` through an injectable runner, never a shell string.
- [ ] Make `s` hand the terminal to a child invocation of the current binary: Claude gets `--no-pick --no-hook-install <transcript>`, Amp gets `--no-pick --no-hook-install --agent amp --follow-session <id>`. Pass argv separately, inherit stdio, restore the dashboard afterward, and preserve query/selection. This avoids changing the existing `run(cfg)` loop or turning Ctrl-X into an unrelated picker. Tests replace the child runner and verify session choice plus return state.
- [ ] Add 80x24 and 40x12 render cases, refresh while filtered, no trails, missing session, and degraded metadata. Run targeted tests and the full Go suite. Commit as `feat(trails): add searchable today view`.

### Task 4: Exclusive foreground/background collection

**Files:** Create `trailsdaemon.go`, `trailsdaemon_test.go`; connect worker startup/refresh in `trails_view.go`.

**Interfaces:** One shared lock applies to foreground and daemon writers. Do not reuse WTF's process-name-specific stale lock logic.

```go
func acquireTrailsLock(home string) (release func(), acquired bool, err error)
func runTrailsCollector(ctx context.Context, home string, deps trailsScanDeps) error
func runTrailsCommand(args []string, home string, out io.Writer) error
```

Use a persistent `collector.lock` file with nonblocking OS `flock`; hold its descriptor for the writer lifetime and never unlink it. OS process exit releases the lock without PID-reuse guessing. Health records distinguish foreground/daemon collector mode, last persisted scan, and degraded sources; the lock, not a health PID alone, decides who writes.

- [ ] Write temporary-home tests for lock contention, release/reacquire, corrupt catalog preservation, startup scan, and refresh request acknowledgement only after successful persistence. Two collectors must not both scan/write. A failed save keeps a refresh pending and does not report fresh data.
- [ ] Run `go test ./... -run 'TestTrailsLock|TestTrailsCollector|TestTrailsAgent' -count=1` and confirm red.
- [ ] Implement an initial scan then serialized scans every two seconds after completion. Observe cancellation between operations and impose subprocess deadlines. Read `refresh.request` even while waiting for the next periodic scan; use a unique request token echoed in health after save, rather than second-resolution timestamps. A reader requesting refresh never writes catalog state.
- [ ] Implement `install`, `uninstall`, `status`, and internal `daemon` commands under label `io.entire.entire-tail.trails`. Preserve PATH in the plist; use existing stable-binary selection. Tests stub load/unload/wait calls. Install waits for a daemon-owned persisted scan; a foreground-held lock must produce an explicit failure rather than false readiness. Uninstall preserves catalog history. Status reports stale/degraded health honestly.
- [ ] Connect foreground collection to the same lock. If another writer holds it, stay read-only and request catch-up. If a writer disappears, attempt ownership on a later tick; do not spawn multiple workers. An install during foreground ownership is reported as busy with instructions to close that collector and retry, rather than killing it.
- [ ] Run `go test -race ./... -run 'TestTrails' -count=1`; commit as `feat(trails): add independent background collector`.

### Task 5: Registration, documentation, and end-to-end verification

**Files:** Modify `install.sh`, `README.md`, `CLAUDE.md`; extend installation tests without executing real registration. No changes to the other worktree or real LaunchAgents.

**Interfaces:** Installed `entire-trails` selects the same command as `entire-tail trails`; existing tail/wtf entry points keep working.

- [ ] Extend an isolated installer test using temporary HOME and fake `go`/`entire` executables. Require a third symlink and plugin registration, and require zero `launchctl` calls. Verify explicit `trails install` is still needed for monitoring.
- [ ] Update installation with the same existing symlink/registration pattern:

```bash
ln -sf "$BIN" "$LOCAL_BIN/entire-trails"
# Add to the existing best-effort plugin-registration conditional:
entire plugin install "$LOCAL_BIN/entire-trails" --force
```

- [ ] Document keys, Claude/Amp coverage, Today semantics, branch-backed versus mentioned rows, stale metadata, and monitoring opt-in. Record source-feed and writer-lock contracts in `CLAUDE.md`, without changing unrelated guidance.
- [ ] Run the complete validation from the worktree:

```bash
go test ./...
go test -race ./...
go vet ./...
bun test amp-plugin
go build -o /tmp/entire-tail-trails-check .
bash -n install.sh
git diff --check
```

- [ ] Use a disposable fixture HOME and a PTY to exercise default, filtered, empty, degraded, small-width, and session-return states. Capture targeted screenshots with installed terminal/browser tooling and inspect each with `view_media`; an ANSI dump alone is not visual verification. Place review artifacts under `.amp/in/artifacts/` only after adding `/.amp/in/` to the repository-local exclude file. Remove temporary executables and scratch harnesses; retain only inspected review artifacts. Do not run the real installer for verification.
- [ ] Re-read the full diff, check every spec requirement against the executed tests, and commit documentation/registration with `git commit -m 'feat(trails): register command and document usage'`. Report actual local/remote/install state. Request explicit authorization before publishing this GitHub-backed branch or installing monitoring.

## Execution choice

Recommended: implement directly in this thread. The five tasks share catalog, collection, and TUI contracts, so serial handoffs add coordination without useful parallelism. This plan awaits Daniel's review and execution-method choice before implementation.
