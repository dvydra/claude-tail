# Amp Support Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-25-amp-support-design.md`

**Goal:** Make Amp a first-class session source across entire-tail without weakening the existing file-backed agents or making Claude depend on Amp availability.

**Constraints:** Use only supported Amp CLI commands; combine Claude and Amp by default; keep `--local` network-free; preserve cached Amp data on failures; keep Claude-only hooks, tap, profiles, lineage, and sidecar subagents scoped to Claude; use `c` for Claude and `a` for Amp, retaining `n` as a hidden Claude alias.

## File responsibility map

- `amp.go`: Amp CLI commands, tolerant wire models, cache, URI/path conversion, snapshots.
- `adapter_amp.go`: Amp export message blocks to canonical `Record`s.
- `adapter.go`, `config.go`, `discovery.go`: register `AgentAmp`, validation, exported-file detection, direct discovery.
- `tree.go`, `entire.go`, `picker.go`, `preview.go`, `search.go`: source-aware sessions, combined inventory, markers, preview/info, merged search.
- `main.go`: route file sources versus Amp snapshots; Amp polling/re-render; direct follow and wait-new.
- `live.go`, `adopt.go`, `nearby.go`, `panelink*.go`: local Amp process/thread correlation and merged activity.
- `iterm.go`: Amp launch/resume workspace commands and `c`/`a` picker actions.
- `handover*.go`, `hunk.go`, `settings.go`: agent-aware capabilities and commands.
- `README.md`, `CLAUDE.md`, help text: user contract and extension architecture.
- `testdata/amp_*.json`, `*_test.go`: scrubbed real-shape fixtures, unit tests, and render goldens.

## Task 1: Amp CLI and cache boundary

- [ ] Add failing tests for exact `threads list`, `threads export`, `threads search`, and `top` command construction; tolerant list/export decoding; file URI conversion; timeout/error classification; atomic cache fallback.
- [ ] Run `go test ./...` and confirm the new Amp client tests fail because the client does not exist.
- [ ] Implement `amp.go` with injectable command execution, bounded calls, cache reads/writes, and last-good fallback.
- [ ] Run the focused Amp client tests, then `go test ./...`.
- [ ] Commit `feat(amp): add CLI and cache boundary`.

## Task 2: Canonical Amp transcript adapter

- [ ] Add a scrubbed export fixture containing user/assistant text, thinking, tool calls/results, shell output, failure, question, `Task`, and `create_thread`.
- [ ] Add failing adapter and golden tests covering hidden thinking, tool summaries/results, question cards, spawn markers, child ids, and idle-only done state.
- [ ] Run the focused tests and confirm they fail for missing `AgentAmp`/normalization.
- [ ] Implement `adapter_amp.go`, register `AgentAmp`, and preserve unknown-block tolerance.
- [ ] Generate the Amp goldens deliberately, inspect them, and run all adapter/golden tests.
- [ ] Commit `feat(amp): render exported threads`.

## Task 3: Snapshot source and direct follow

- [ ] Add failing tests for `T-…` recognition, `--follow-session`, cwd-preferred Amp discovery, no-network cached discovery, snapshot version dedup, append versus rewrite detection, and wait-for-new scoped by cwd/time.
- [ ] Run focused tests and confirm source-routing failures.
- [ ] Add the session-source locator and Amp snapshot tail driver. Export immediately, refresh from local-log changes or polling, append complete messages, and full-rerender non-append updates.
- [ ] Ensure Amp failures retain the current rendered snapshot and report one deduplicated status error.
- [ ] Run focused tests and `go test ./...`.
- [ ] Commit `feat(amp): follow thread snapshots`.

## Task 4: Combined tree, list, preview, and search

- [ ] Add failing tests for mixed Claude/Amp grouping and sorting, fixed-width `C`/`A` markers, agent filters, local cache-only mode, non-local groups, Amp preview metadata/content, and search result dedup by agent plus id.
- [ ] Run focused tests and confirm the mixed-source expectations fail.
- [ ] Generalize `treeSession` and `treeChoice` with `Agent` and source locator; merge Amp inventory into the default tree and server search into search results.
- [ ] Route selected Amp rows to snapshot follow, use cached exports for filter/preview, and disable local-only actions for remote-only rows.
- [ ] Run tree/search/preview tests and `go test ./...`.
- [ ] Commit `feat(amp): combine Amp with the session tree`.

## Task 5: Live, nearby, adoption, and pane linking

- [ ] Add failing tests for parsing open Amp thread logs, excluding noninteractive processes, merging local mappings with `amp top`, reconnect retention, nearby ranking, and pane pairing by agent plus id.
- [ ] Run focused tests and confirm the live model is Claude-only.
- [ ] Implement exact PID-to-thread mapping via open log files and cwd/TTY, merge remote activity, and add agent identity to nearby and pane-link registries.
- [ ] Run live/adopt/nearby/panelink tests and `go test -race ./...`.
- [ ] Commit `feat(amp): add combined live sessions`.

## Task 6: Workspace lifecycle and remaining capabilities

- [ ] Add failing tests for `c`, `a`, hidden `n`, Amp resume/fresh scripts, Amp binary errors, handover manifests, Amp hunk targeting, session resume strings, and hiding Claude-only settings.
- [ ] Run focused tests and confirm the capability routing is absent.
- [ ] Implement `amp threads continue`, fresh `amp` plus wait-new tail, agent-aware handover/hunk/settings, and focus for exported child threads when an id is available.
- [ ] Run focused tests and `go test ./...`.
- [ ] Commit `feat(amp): complete Amp workflows`.

## Task 7: Documentation and verification

- [ ] Update help, `README.md`, and `CLAUDE.md` with combined defaults, filters, keys, cache/network behavior, remote executors, and explicit Claude-only features.
- [ ] Scan for stale claims and placeholders: `rg -n 'Claude-only tree|Adding a new agent|TODO|FIXME|PLACEHOLDER' README.md CLAUDE.md main.go docs`.
- [ ] Re-read the complete diff against the approved spec and check type/signature consistency across every session selection path.
- [ ] Run `gofmt` on changed Go files.
- [ ] Run `go test ./...`, `go vet ./...`, and `go test -race ./...`.
- [ ] Manually exercise real Amp list/export/search, cached offline list, a local active thread, and one remote thread; inspect combined picker and rendered transcript.
- [ ] Commit `docs: document first-class Amp support`.
