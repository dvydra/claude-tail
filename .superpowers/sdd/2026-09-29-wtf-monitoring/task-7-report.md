# Task 7 report

Implemented daemon-aware dashboard reads.

- A running monitor is the only durable writer. The dashboard reads `state.json`, polls its mtime once per second, reads health, and writes only the scan-request marker when `r` is pressed.
- Without a monitor, the dashboard performs one in-memory scan and never saves it.
- Foreground state reads never rename or replace malformed durable state. The parse error is rendered with the one-shot scan result.
- The fixed footer reports monitoring status, last successful scan age, and the monitor's current error. Very small terminal heights retain the existing selected-row behavior.
- Refreshes preserve the selected session identity and remain asynchronous.

Tests cover malformed read preservation, monitoring off/running/stale health rendering, refresh requests, and selection preservation. Existing daemon lifecycle tests continue to stub all external effects.

Verification:

- `go test ./... -run 'TestRunWTFDaemonAware|TestRenderWTFHealth' -v`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `git diff --check`

All passed.

## Round 1

Fixed the review findings around live daemon transitions, durable-state polling, health failures, refresh requests, and fixed footer errors.

- The dashboard collector now checks health every tick. It reads durable state on every running tick, performs one read-only fallback scan each time monitoring stops, and never has a save dependency.
- State parse errors remain visible until a successful durable read. Fallback scans do not rename or replace malformed state.
- Health reads now return errors beneath the existing bool wrapper. Missing health is quiet; malformed or unreadable health is shown while the last valid health data remains available.
- Refresh checks health when `r` is pressed, creates the real marker only for a running daemon, and stays pending until durable `UpdatedAt` advances or monitoring stops.
- The fixed footer keeps health and the first state/source error visible at normal heights. One- and two-row layouts retain their prior clipping and selected-row behavior.
- Direct collector tests cover off/on/off transitions, one-shot fallback scans, repeated durable reads, malformed state and health, real marker files, and refresh polling. Existing identity-preservation tests cover reordered snapshots.

Verification:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `git diff --check`

All passed.

## Round 2

Fixed refresh ownership, request ordering, and the three-row dashboard layout.

- `RequestRefresh` now only sets an atomic request flag. The collection worker owns health, durable timestamps, pending state, marker writes, and request errors.
- A live worker consumes a request by reading durable state once, using that result as the refresh baseline, then writing the marker. Only a later durable update clears pending. Off mode writes no marker, and marker failures clear pending and appear in the snapshot.
- A blocked collection no longer delays refresh input. Race coverage verifies the handoff, and ordering coverage verifies that an update before request consumption becomes the baseline.
- Height 3 now reserves one body row for the selected session and one status footer. The key footer appears only from height 4 onward; heights 1 and 2 are unchanged.

Verification:

- Focused refresh and height tests, repeated five times
- Focused blocking test under `go test -race`, repeated three times
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `git diff --check`

All passed.

## Round 3

Fixed refresh marker ordering when the durable baseline cannot be read.

- A consumed refresh now writes its marker only after that collection successfully reads durable state and records its `UpdatedAt` baseline.
- A failed baseline read writes no marker, leaves refresh pending false, preserves the state read error, and reports `establish refresh baseline` with the underlying error.
- A later, separately queued refresh retries baseline establishment. A successful read writes the marker and restores normal pending-until-advance behavior.
- The regression test proves both the failed attempt and the later successful attempt without filesystem or daemon effects.

Verification:

- Focused refresh tests, repeated ten times
- Focused refresh and blocked-collection tests under `go test -race`, repeated five times
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `git diff --check`

All passed.
