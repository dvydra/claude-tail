# `entire wtf` Monitoring and Warnings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the registry scan continuously under a macOS LaunchAgent, notify Daniel once per new conflict occurrence, and tell the challenger session to stop and check with Daniel.

**Architecture:** A single daemon owns state writes and runs one scan at a time every two seconds. New findings enter a delivery queue after the state is safely written. Each channel uses at-most-once crash semantics: persist `sending`, attempt delivery, then persist the result. A crash while sending becomes `unknown` and is not retried automatically. The foreground dashboard reads state, requests scans through a marker file, and performs a read-only one-shot scan when the daemon is absent.

**Tech Stack:** Go, macOS launchd, Unix domain sockets, Amp CLI execute mode, AppleScript notifications.

## Prerequisite

Complete `2026-09-29-wtf-dashboard-today.md` and `2026-09-29-wtf-trail-registry.md` in order.

## Scope and constraints

- `entire wtf install`, `status`, and `uninstall` manage `io.entire.entire-tail.wtf`.
- The daemon is the only durable-state writer once installed.
- Warning text reports deterministic evidence, says stop, and says check with Daniel. It never authorizes cleanup, branch switching, or edits.
- Deliver only findings with a live challenger. Historical findings remain dashboard-only.
- One finding occurrence produces at most one successful or unknown attempt per channel.
- Tests never contact a real Claude socket, run Amp, call `osascript`, or modify launchd.
- Claude's socket location is documented, but its full NDJSON frame is not a stable public API. Keep that protocol in one adapter and pin it with a fake Unix-socket test.

---

### Task 1: Add daemon lock, health, and scan requests

**Files:**
- Create: `wtfdaemon.go`
- Create: `wtfdaemon_test.go`
- Modify: `wtf.go`
- Modify: `wtf_test.go`

**Interfaces:**

```go
const wtfScanInterval = 2 * time.Second

type wtfHealth struct {
	PID                int    `json:"pid"`
	Version            string `json:"version"`
	StartedAt          int64  `json:"startedAt"`
	LastAttemptedScan  int64  `json:"lastAttemptedScan,omitempty"`
	LastSuccessfulScan int64  `json:"lastSuccessfulScan,omitempty"`
	LastError          string `json:"lastError,omitempty"`
}

func wtfLockPath(home string) string
func wtfHealthPath(home string) string
func wtfScanRequestPath(home string) string
func acquireWTFLock(home string) (func(), bool)
func readWTFHealth(home string) (wtfHealth, bool)
func wtfDaemonRunning(health wtfHealth) bool
func requestWTFScan(home string) error
func runWTFDaemon(ctx context.Context, home string, deps wtfDaemonDeps) error
```

- [ ] **Step 1: Write failing lock and health tests**

Cover:

- first lock succeeds, second lock fails;
- dead PID lock is replaced;
- release removes only the lock held by the current PID;
- health JSON round trips atomically;
- live PID plus an `entire-tail` process name is running;
- recycled PID with another process name is not running;
- scan request is an atomic marker and coalesces repeated requests.

Inject `pidAlive` and process-name checks. Do not inspect real PIDs in tests.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run 'TestWTFLock|TestWTFHealth|TestRequestWTFScan' -v`

Expected: FAIL with undefined functions.

- [ ] **Step 3: Implement single-daemon coordination**

Follow `acquirePaneLinkLock`, but put files under `wtfDir(home)`. Write the PID with mode `0600`. Break a stale lock only after the PID is absent or its command does not contain `entire-tail`.

- [ ] **Step 4: Implement the scan loop without delivery**

```go
type wtfDaemonDeps struct {
	Scan  func(context.Context, string, wtfState, wtfScanDeps) (wtfState, error)
	Load  func(string, int64) (wtfState, error)
	Save  func(string, wtfState) error
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}
```

On startup, acquire the lock, load state, and write health. Scan immediately, then every two seconds. Remove the request marker at the start of a requested scan. Never overlap scans. Update `LastAttemptedScan` before scanning and `LastSuccessfulScan` only after state saves.

On a partial scan error, save the returned partial state, keep the loop alive, and put the error in health.

- [ ] **Step 5: Verify green**

Use a fake clock channel to run three ticks and one manual request. Assert four sequential scans, no overlap, and updated health.

Run: `go test ./... -run 'TestWTFLock|TestWTFHealth|TestRequestWTFScan|TestRunWTFDaemon' -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtfdaemon.go wtfdaemon_test.go wtf.go wtf_test.go
git commit -m "feat(wtf): add monitor loop and health"
```

---

### Task 2: Add LaunchAgent lifecycle commands

**Files:**
- Modify: `wtfdaemon.go`
- Modify: `wtfdaemon_test.go`
- Modify: `wtf.go`
- Modify: `wtf_test.go`

**Interfaces:**

```go
const wtfAgentLabel = "io.entire.entire-tail.wtf"

func wtfAgentPath(home string) string
func wtfLogPath(home string) string
func wtfAgentPlist(bin, logPath string) string
func installWTFAgent(home, bin string, out io.Writer) (string, error)
func waitForWTF(home string, timeout time.Duration) (wtfHealth, bool)
func runWTFCommand(args []string, home string, out io.Writer) error
```

- [ ] **Step 1: Write failing plist and command tests**

Assert the plist includes:

```text
Label: io.entire.entire-tail.wtf
ProgramArguments: <stable binary>, wtf, daemon
RunAtLoad: true
KeepAlive: true
ThrottleInterval: 10
StandardOutPath and StandardErrorPath: daemon.log
```

Stub package vars for load, unload, and wait. Test:

- `install` writes, unloads stale config, loads, waits for successful health, and reports the PID;
- failed health returns an error naming the log path;
- `status` distinguishes running, stale health, and not installed;
- `uninstall` unloads, removes plist and health, but preserves `state.json`;
- `daemon` calls `runWTFDaemon`;
- unknown subcommands return `want install|status|uninstall` without side effects.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run 'TestWTFAgent|TestRunWTFCommand' -v`

Expected: FAIL.

- [ ] **Step 3: Reuse stable-binary and launchctl seams**

Use the existing `tapAgentBinary`, `looksEphemeralBinary`, `launchctlLoad`, and `launchctlUnload` behavior. Add WTF-specific package vars so tests cannot invoke launchd:

```go
var (
	wtfAgentLoad   = launchctlLoad
	wtfAgentUnload = launchctlUnload
	wtfAgentWait   = waitForWTF
	wtfDaemonRun   = runWTFDaemon
)
```

Generalize `launchctlUnload` to accept a label, or add a small `launchctlUnloadLabel(path, label string)` source-of-truth helper and keep the tap wrapper. Do not copy the implementation with a hard-coded second label.

- [ ] **Step 4: Wire `runWTF` subcommands**

Bare `wtf` remains the dashboard. `install`, `status`, and `uninstall` call `runWTFCommand`. `daemon` is internal but accepted because launchd must invoke it.

- [ ] **Step 5: Verify green**

Run:

```sh
go test ./... -run 'TestWTFAgent|TestRunWTFCommand' -v
go test ./...
```

Expected: PASS, with no LaunchAgent created during tests.

- [ ] **Step 6: Commit**

```sh
git add wtfdaemon.go wtfdaemon_test.go wtf.go wtf_test.go tapdaemon.go tapdaemon_test.go
git commit -m "feat(wtf): manage monitor launchagent"
```

---

### Task 3: Build exact warning text and delivery policy

**Files:**
- Create: `wtf_notify.go`
- Create: `wtf_notify_test.go`
- Modify: `wtf.go`

**Interfaces:**

```go
type wtfDelivery struct {
	FindingID string
	Channel   string
	Target    string
	Message   string
}

type wtfDeliveryResult struct {
	FindingID string
	Channel   string
	State     string
	Error     string
}

func warningText(state wtfState, finding wtfFinding) string
func pendingWTFDeliveries(state wtfState, now int64) []wtfDelivery
func markWTFDeliveryStarted(state *wtfState, delivery wtfDelivery, now int64)
func applyWTFDeliveryResult(state *wtfState, result wtfDeliveryResult, now int64)
```

- [ ] **Step 1: Write exact warning golden tests**

For a duplicate claim, pin this shape:

```text
entire wtf found a conflict for entiredb#1223.
It is already owned by Claude session api-refactor-a1 in ~/src/entiredb/.amp/worktrees/api-1223.
This session is in ~/src/entiredb/.amp/worktrees/checkpoint-fix. Stop before changing this trail or either worktree and check with Daniel.
Evidence: first claim at 10:14; canonical worktree has 3 dirty files and 2 commits not on origin/main.
```

Use agent name when present, otherwise `Claude <short id>` or `Amp <short id>`. Ensure every warning contains `Stop` and `check with Daniel`.

- [ ] **Step 2: Test policy and crash states**

Assert:

- an active finding with an active challenger yields one `session` delivery and one `mac` delivery;
- an owner-only or historical finding yields no session delivery;
- `sent`, `held`, `refused`, and `unknown` do not retry during one occurrence;
- `failed` retries at 5s, 30s, 2m, 10m, then hourly;
- recurrence resets channels to pending;
- `sending` loaded after daemon restart becomes `unknown`, preventing a duplicate after a crash.

- [ ] **Step 3: Verify red**

Run: `go test ./... -run 'TestWarningText|TestPendingWTFDeliveries|TestWTFDeliveryCrash' -v`

Expected: FAIL.

- [ ] **Step 4: Implement policy as pure state transitions**

Channel keys are `session:<sessionKey>` and `mac`. Persist `sending` before invoking any external effect. Increment attempts only when moving to `sending`. A process restart changes leftover `sending` to `unknown` during state load.

- [ ] **Step 5: Verify green**

Run: `go test ./... -run 'TestWarningText|TestPendingWTFDeliveries|TestWTFDeliveryCrash' -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtf.go wtf_notify.go wtf_notify_test.go
git commit -m "feat(wtf): define conflict warning policy"
```

---

### Task 4: Deliver warnings to Claude sessions

**Files:**
- Modify: `wtf_notify.go`
- Modify: `wtf_notify_test.go`

**Interfaces:**

```go
type claudePeerFrame struct {
	MessageVersion int               `json:"msgV"`
	MessageID      string            `json:"msg_id"`
	Type           string            `json:"type"`
	Message        claudePeerMessage `json:"message"`
	Priority       string            `json:"priority"`
	SessionID      string            `json:"session_id"`
	From           string            `json:"from,omitempty"`
}

type claudePeerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func claudeWarningFrame(sessionID, messageID, from, text string) []byte
func sendClaudeWarning(ctx context.Context, target wtfSession, text string) wtfDeliveryResult
```

- [ ] **Step 1: Write pure frame tests**

Assert one newline-terminated JSON object with:

```json
{
  "msgV": 1,
  "msg_id": "fixed-uuid",
  "type": "user",
  "message": {"role": "user", "content": "warning"},
  "priority": "next",
  "session_id": "target-session-id"
}
```

The target session ID is required to prevent a stale registry entry from delivering to a reused socket path.

- [ ] **Step 2: Write a fake Unix-socket integration test**

Bind a socket under a temporary mode-`0700` directory. Read one line and return a control receipt to a temporary sender socket:

```json
{"type":"control","action":"peer_message_status","status":"held","orig_msg_id":"fixed-uuid","reason":"crossSessionInbound hold"}
```

Assert `sendClaudeWarning` returns `held`. Repeat for `delivered`, `denied` mapped to `refused`, no receipt mapped to `sent`, missing target socket mapped to `failed`, and deadline expiry after write mapped to `sent` rather than retryable failure.

- [ ] **Step 3: Verify red**

Run: `go test ./... -run 'TestClaudeWarningFrame|TestSendClaudeWarning' -v`

Expected: FAIL.

- [ ] **Step 4: Implement the isolated socket adapter**

Use `net.Dialer.DialContext(ctx, "unix", target.SocketPath)`. Create an optional temporary receipt socket beside the target socket, mode `0600`, and encode it as `uds:<path>` in `From`. Write the frame, call `CloseWrite`, and wait up to two seconds for a matching receipt.

Validate before dialing:

- target is active Claude;
- `SocketPath` is non-empty;
- socket path exists and is a socket;
- its owner UID is the current UID;
- parent directory is not group/world writable;
- session ID is non-empty.

Do not read or log Claude messaging tokens. The documented macOS socket accepts an unauthenticated local-user connection; inbound settings may still hold or refuse it, which the result records.

Keep every reverse-engineered field and receipt parser in `wtf_notify.go`. Add a comment linking `https://code.claude.com/docs/en/cross-session-messaging` and naming the tested Claude Code version.

- [ ] **Step 5: Verify green**

Run: `go test ./... -run 'TestClaudeWarningFrame|TestSendClaudeWarning' -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtf_notify.go wtf_notify_test.go
git commit -m "feat(wtf): warn claude challengers"
```

---

### Task 5: Deliver warnings to Amp and macOS

**Files:**
- Modify: `wtf_notify.go`
- Modify: `wtf_notify_test.go`

**Interfaces:**

```go
type wtfExec func(ctx context.Context, name string, args ...string) ([]byte, error)

func sendAmpWarning(ctx context.Context, target wtfSession, text string, run wtfExec) wtfDeliveryResult
func notificationArgs(trailKey, text string) []string
func sendMacNotification(ctx context.Context, trailKey, text string, run wtfExec) wtfDeliveryResult
```

- [ ] **Step 1: Write exact Amp command tests**

Assert:

```text
amp threads continue T-123 --execute <warning>
```

The warning is one argv value, not shell-quoted text. Successful exit becomes `sent`. Start/lookup failure becomes `failed`. Context timeout after the command starts becomes `unknown`, because the CLI may have submitted the message before timing out and retrying could duplicate it.

- [ ] **Step 2: Write exact macOS notification tests**

Use argv rather than interpolating evidence into AppleScript:

```text
osascript
-e on run argv
-e display notification (item 1 of argv) with title "entire wtf" subtitle (item 2 of argv)
-e end run
--
<short explanation>
<trail key>
```

Assert quotes, backslashes, and newlines remain argv data. Successful exit becomes `sent`; command failure becomes `failed`.

- [ ] **Step 3: Verify red**

Run: `go test ./... -run 'TestSendAmpWarning|TestMacNotification' -v`

Expected: FAIL.

- [ ] **Step 4: Implement bounded delivery**

Use a 15-second Amp timeout and five-second notification timeout. Never include command output in state unless reduced to one safe error line. Do not log warning bodies.

- [ ] **Step 5: Verify green**

Run: `go test ./... -run 'TestSendAmpWarning|TestMacNotification' -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtf_notify.go wtf_notify_test.go
git commit -m "feat(wtf): notify amp and macos"
```

---

### Task 6: Integrate delivery with the single-writer daemon

**Files:**
- Modify: `wtfdaemon.go`
- Modify: `wtfdaemon_test.go`
- Modify: `wtf_notify.go`
- Modify: `wtf_notify_test.go`

**Interfaces:**

```go
type wtfNotifier func(context.Context, wtfState, wtfDelivery) wtfDeliveryResult
```

Add `Notify wtfNotifier` to `wtfDaemonDeps`.

- [ ] **Step 1: Write the end-to-end daemon test**

Feed three scans:

1. new duplicate finding;
2. same finding still active;
3. cleared then recurring finding.

Assert two total rounds of delivery, one for each occurrence. For each round assert:

- state with `sending` is saved before notifier invocation;
- session and Mac channels each run once;
- final delivery state is saved after results;
- a slow notifier does not block the next local conflict scan;
- notifier results return through a channel and only the daemon goroutine writes state.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run TestWTFDaemonDelivery -v`

Expected: FAIL.

- [ ] **Step 3: Add a bounded delivery worker**

Queue deliveries only after the scan state is saved. Before enqueueing each delivery, mark it `sending` and save again. Use at most four workers, one delivery per finding/channel, and a buffered result channel. The daemon loop applies results and saves state.

If the queue is full, leave the channel `pending`; the next scan can try again. If the daemon exits, cancel workers and preserve their `sending` state for restart recovery to `unknown`.

- [ ] **Step 4: Verify green and race safety**

Run:

```sh
go test ./... -run TestWTFDaemonDelivery -v
go test -race ./... -run 'TestWTFDaemon|TestWTFDaemonDelivery'
```

Expected: PASS with no race report.

- [ ] **Step 5: Commit**

```sh
git add wtfdaemon.go wtfdaemon_test.go wtf_notify.go wtf_notify_test.go
git commit -m "feat(wtf): deliver new conflict alerts"
```

---

### Task 7: Make the dashboard daemon-aware

**Files:**
- Modify: `wtf.go`
- Modify: `wtf_test.go`
- Modify: `wtf_view.go`
- Modify: `wtf_view_test.go`

- [ ] **Step 1: Write command behavior tests**

Assert:

- running daemon: dashboard reads `state.json`, never calls `scanWTF`, and `r` writes the request marker;
- absent daemon: dashboard runs one read-only scan, does not save, and shows `monitoring off · run entire wtf install`;
- stale state: dashboard keeps rendering it and shows the last successful scan age and health error;
- malformed state with no daemon: foreground leaves the file untouched, reports the parse error, and renders its one-shot result.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run 'TestRunWTFDaemonAware|TestRenderWTFHealth' -v`

Expected: FAIL.

- [ ] **Step 3: Implement read-only foreground behavior**

Remove the temporary foreground `saveWTFState` call from the trail-registry plan. Split non-mutating `readWTFState` from daemon-only `loadWTFState`, which owns corrupt-file preservation and rebuilt-state persistence. With no daemon, malformed state stays untouched. Poll state mtime once per second in the tty view. `r` requests a scan and waits for `UpdatedAt` to advance, but keeps the current display while waiting.

- [ ] **Step 4: Verify green**

Run:

```sh
go test ./... -run 'TestRunWTFDaemonAware|TestRenderWTFHealth' -v
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add wtf.go wtf_test.go wtf_view.go wtf_view_test.go
git commit -m "feat(wtf): read monitor state in dashboard"
```

---

### Task 8: Document, verify, and live-test with approval

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `help_test.go`
- Modify: `main.go` or `help.go` where the CLI help text is owned

- [ ] **Step 1: Update help and documentation**

Document:

- `entire wtf install|status|uninstall`;
- LaunchAgent label, state path, health path, and log path;
- two-second polling and manual `r` requests;
- Claude held/refused behavior and Amp unknown-after-timeout behavior;
- one notification per finding occurrence;
- uninstall preserves registry history;
- no automatic cleanup, reassignment, branch switch, or trail mutation.

- [ ] **Step 2: Add help assertions**

Pin all three lifecycle commands and the phrase `stop and check with Daniel` in user-facing help.

- [ ] **Step 3: Run the full automated gate**

```sh
gofmt -w wtf.go wtf_test.go wtf_scan.go wtf_scan_test.go wtf_summary.go wtf_summary_test.go wtf_view.go wtf_view_test.go wtf_notify.go wtf_notify_test.go wtfdaemon.go wtfdaemon_test.go tapdaemon.go tapdaemon_test.go
go test ./...
go vet ./...
go test -race ./...
go build -o entire-tail .
```

Expected: all commands exit 0.

- [ ] **Step 4: Test degraded modes without external effects**

Run with injected or PATH-hidden `fm`, Entire, Amp, and `osascript` failures. Verify local scanning continues, findings stay visible, and each failed channel records one safe error line.

- [ ] **Step 5: Ask before installing the real LaunchAgent**

The automated suite proves plist and launchctl construction without modifying the user's machine. Before running `./entire-tail wtf install`, ask for explicit approval to load the LaunchAgent and send disposable test notifications/messages.

- [ ] **Step 6: Perform approved live verification**

Use disposable Claude and Amp sessions plus disposable Git worktrees. Verify:

1. first mention claims one trail and chooses the expected canonical worktree;
2. a second session mentions the same trail and receives the stop/check warning;
3. macOS shows one notification;
4. repeated scans send neither channel again;
5. an old dirty worktree triggers WIP elsewhere;
6. clearing and recreating the conflict sends one new occurrence;
7. daemon restart preserves ownership and does not duplicate a `sending` attempt;
8. `status` reports current health and `uninstall` preserves `state.json`.

Capture and inspect one dashboard screenshot showing delivery status. Do not use a production trail for this test.

- [ ] **Step 7: Commit**

```sh
git add README.md CLAUDE.md main.go help.go help_test.go
git commit -m "docs(wtf): document monitoring and warnings"
```

## Final completion gate

The feature is complete only when:

- dashboard, registry, daemon, and three delivery channels pass unit and integration tests;
- one daemon is the sole state writer;
- a challenger is told to stop and check with Daniel;
- Daniel gets one Mac notification for the same occurrence;
- old WIP worktrees remain visible indefinitely;
- repeated scans and daemon restarts do not duplicate successful or unknown deliveries;
- uninstall leaves registry history intact;
- tests, vet, race, build, visual inspection, and approved live verification pass.
