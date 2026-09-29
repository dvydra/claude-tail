# `entire wtf` Trail Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `entire wtf` with an indefinite trail-to-session-to-worktree registry, deterministic canonical placement, local WIP inspection, and active conflict findings.

**Architecture:** The scanner takes the prior durable state and the current today snapshot, extracts new timestamped trail evidence only from active sessions, refreshes related Entire metadata and Git worktrees, then reconciles claims and findings. The first valid session/worktree claim stays the owner. Server trail metadata can choose the initial canonical worktree, but no later scan silently moves it. Ended-today sessions remain visible but cannot create a trail or claim. The model does not participate in this path.

**Tech Stack:** Go, Git subprocesses, Entire CLI JSON output, existing Claude and Amp transcript formats.

## Prerequisite

Complete `2026-09-29-wtf-dashboard-today.md`. Keep its tests green while adding registry state.

## Scope and constraints

- Create trail records and claims only from active session evidence. Branches, commits, and diffs may associate worktrees with trails already in the registry, but cannot discover a new trail. Never list a global trail backlog.
- Accept Entire URLs, `owner/repo#1223`, `repo#1223`, `trail 1223`, and `trail #1223` under the resolution rules in the approved design.
- A claim requires a resolved trail and a local Git worktree.
- Session rows expire after local midnight. Trail, worktree, claim, and finding history do not expire.
- WIP means dirty status or commits absent from the local remote default branch. The scanner does not fetch.
- Unknown Git state stays unknown. Missing paths never become clean.
- Tests use temporary repositories and injected command runners. They do not inspect or mutate real repositories.

---

### Task 1: Add versioned durable state and recovery

**Files:**
- Modify: `wtf.go`
- Modify: `wtf_test.go`

**Interfaces:**

```go
const wtfStateVersion = 1

type wtfState struct {
	Version      int                        `json:"version"`
	UpdatedAt    int64                      `json:"updatedAt"`
	Sessions     map[string]wtfSession      `json:"sessions"`
	Trails       map[string]wtfTrail        `json:"trails"`
	Worktrees    map[string]wtfWorktree     `json:"worktrees"`
	Findings     map[string]wtfFinding      `json:"findings"`
	SummaryCache map[string]wtfSummaryCache `json:"summaryCache,omitempty"`
}

type wtfTrail struct {
	Key               string               `json:"key"`
	Owner             string               `json:"owner"`
	Repo              string               `json:"repo"`
	Number            int                  `json:"number"`
	URL               string               `json:"url"`
	Title             string               `json:"title,omitempty"`
	Status            string               `json:"status,omitempty"`
	SourceBranch      string               `json:"sourceBranch,omitempty"`
	TargetBranch      string               `json:"targetBranch,omitempty"`
	MetadataUpdatedAt int64                `json:"metadataUpdatedAt,omitempty"`
	MetadataError     string               `json:"metadataError,omitempty"`
	MetadataAttempts  int                  `json:"metadataAttempts,omitempty"`
	MetadataNextRetry int64                `json:"metadataNextRetry,omitempty"`
	CanonicalWorktree string               `json:"canonicalWorktree,omitempty"`
	OwnerSession      string               `json:"ownerSession,omitempty"`
	FirstClaim        *wtfClaim             `json:"firstClaim,omitempty"`
	Associations      []wtfAssociation      `json:"associations,omitempty"`
	FirstSeen         int64                `json:"firstSeen"`
	LastSeen          int64                `json:"lastSeen"`
	LastWIPAt         int64                `json:"lastWipAt,omitempty"`
}

type wtfClaim struct {
	SessionKey string `json:"sessionKey"`
	Worktree   string `json:"worktree"`
	At         int64  `json:"at"`
	Evidence   string `json:"evidence"`
}

type wtfAssociation struct {
	SessionKey string `json:"sessionKey,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	At         int64  `json:"at"`
	Evidence   string `json:"evidence"`
	Source     string `json:"source"`
}

type wtfGitEvidence struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

type wtfWorktree struct {
	Repo            string           `json:"repo"`
	Path            string           `json:"path"`
	Branch          string           `json:"branch,omitempty"`
	Head            string           `json:"head,omitempty"`
	Exists          bool             `json:"exists"`
	DirtyFiles      int              `json:"dirtyFiles"`
	DirtySummary    []string         `json:"dirtySummary,omitempty"`
	DefaultBranch   string           `json:"defaultBranch,omitempty"`
	UnmergedCommits int              `json:"unmergedCommits"`
	GitError        string           `json:"gitError,omitempty"`
	SessionKeys     []string         `json:"sessionKeys,omitempty"`
	TrailKeys       []string         `json:"trailKeys,omitempty"`
	GitEvidence     []wtfGitEvidence `json:"gitEvidence,omitempty"`
	FirstSeen       int64            `json:"firstSeen"`
	LastSeen        int64            `json:"lastSeen"`
	LastWIPAt       int64            `json:"lastWipAt,omitempty"`
}

type wtfFinding struct {
	ID          string                       `json:"id"`
	Kind        string                       `json:"kind"`
	Severity    int                          `json:"severity"`
	TrailKey    string                       `json:"trailKey"`
	Owner       string                       `json:"owner,omitempty"`
	Challenger  string                       `json:"challenger,omitempty"`
	Worktrees   []string                     `json:"worktrees,omitempty"`
	Explanation string                       `json:"explanation"`
	Evidence    []string                     `json:"evidence"`
	FirstSeen   int64                        `json:"firstSeen"`
	LastSeen    int64                        `json:"lastSeen"`
	Active      bool                         `json:"active"`
	Occurrence  int                          `json:"occurrence"`
	Delivery    map[string]wtfDeliveryStatus `json:"delivery,omitempty"`
}

type wtfDeliveryStatus struct {
	State       string `json:"state"`
	Attempts    int    `json:"attempts"`
	LastAttempt int64  `json:"lastAttempt,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}

func wtfDir(home string) string
func wtfStatePath(home string) string
func loadWTFState(home string, now int64) (wtfState, error)
func saveWTFState(home string, state wtfState) error
func expireWTFSessions(state *wtfState, midnight int64)
```

- [ ] **Step 1: Write failing persistence tests**

Cover:

- a round trip keeps historical trails and missing worktrees;
- temp-file plus rename leaves no `.tmp` file;
- missing state returns initialized maps at `wtfStateVersion`;
- malformed state moves to `state.corrupt-<unix>.json` and returns an explicit recovery error plus initialized state;
- yesterday's ended session expires while an active session and both sessions' trail associations remain.

Use a temporary home and fixed Unix timestamps.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run 'TestWTFState|TestExpireWTFSessions' -v`

Expected: FAIL with undefined state functions.

- [ ] **Step 3: Implement paths and atomic persistence**

```go
func wtfDir(home string) string {
	return filepath.Join(home, "Library", "Application Support", "entire-tail", "wtf")
}

func wtfStatePath(home string) string { return filepath.Join(wtfDir(home), "state.json") }
```

Write mode `0600`, directory mode `0700`, `json.MarshalIndent`, temp file in the same directory, `Sync`, close, then rename. On corrupt JSON, rename the original before returning a rebuilt state. Do not delete the corrupt copy.

- [ ] **Step 4: Move phase-one summary cache into state**

The dashboard may read cached summaries from state, but the one-shot foreground scan passes the updated cache only to its in-memory render. Persistent writes remain in one function so the daemon plan can become the only caller without refactoring the scanner.

- [ ] **Step 5: Verify green**

Run: `go test ./... -run 'TestWTFState|TestExpireWTFSessions' -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtf.go wtf_test.go
git commit -m "feat(wtf): persist versioned registry state"
```

---

### Task 2: Extract and resolve timestamped trail references

**Files:**
- Create: `wtf_scan.go`
- Create: `wtf_scan_test.go`
- Create: `testdata/wtf/claude-trails.jsonl`
- Create: `testdata/wtf/amp-trails.json`

**Interfaces:**

```go
type trailTextEvent struct {
	At     int64
	Source string
	Text   string
}

type trailContext struct {
	CurrentRepo string
	KnownRepos  []string
}

type trailEvidence struct {
	Key        string
	Owner      string
	Repo       string
	Number     int
	URL        string
	Matched    string
	Source     string
	At         int64
	Resolved   bool
	Resolution string
}

func extractTrailEvidence(events []trailTextEvent, ctx trailContext) []trailEvidence
func claudeTrailEvents(path string, observedAt int64) []trailTextEvent
func ampTrailEvents(export ampExport, observedAt int64) []trailTextEvent
func transcriptTrailEvents(s wtfSession, home string, observedAt int64) []trailTextEvent
```

- [ ] **Step 1: Write table tests for every syntax and false positive**

Use these cases:

```go
tests := []struct {
	name, text, current string
	known               []string
	wantKey             string
	wantResolved        bool
}{
	{"url", "see https://entire.io/gh/acme/api/trails/12", "x/y", nil, "acme/api#12", true},
	{"qualified", "acme/api#13", "x/y", nil, "acme/api#13", true},
	{"repo shorthand current", "api#14", "acme/api", nil, "acme/api#14", true},
	{"repo shorthand unique known", "api#15", "", []string{"acme/api"}, "acme/api#15", true},
	{"repo shorthand ambiguous", "api#16", "", []string{"acme/api", "other/api"}, "", false},
	{"bare current", "trail #17", "acme/api", nil, "acme/api#17", true},
	{"bare no current", "trail 18", "", []string{"acme/api"}, "", false},
	{"ordinary hash", "color #123", "acme/api", nil, "", false},
	{"email boundary", "xapi#19@example.com", "acme/api", nil, "", false},
}
```

Also assert deduplication preserves the earliest event timestamp, not input scan order.

- [ ] **Step 2: Write fixture tests for source extraction**

The Claude fixture must include references in:

- user text;
- assistant text;
- a tool input;
- a tool result;
- one synthetic task notification that must be ignored.

The Amp fixture must include the same four meaningful sources with RFC3339Nano `createdAt` values. Assert chronological event times and source labels.

- [ ] **Step 3: Verify red**

Run: `go test ./... -run 'TestExtractTrailEvidence|TestClaudeTrailEvents|TestAmpTrailEvents' -v`

Expected: FAIL with undefined functions.

- [ ] **Step 4: Implement resolution as pure parsing**

Compile separate regular expressions in precedence order: full URL, owner/repo shorthand, repo shorthand, bare trail. Require numeric trail numbers and token boundaries. Canonicalize as lowercase owner/repo plus decimal number. Preserve the exact matched text separately.

For repo shorthand, compare the shorthand to the basename of `CurrentRepo` first. Otherwise resolve only when exactly one `KnownRepos` entry has that basename. Bare trail resolves only with `CurrentRepo`.

Unresolved evidence is returned for dashboard explanation but never creates a `wtfTrail` or claim.

- [ ] **Step 5: Implement agent-specific event extraction**

For Claude, decode user and assistant records and inspect textual message blocks plus tool-use input and tool-result payloads. Reuse `isTaskNote` to exclude synthetic user records. Use each record's RFC3339 timestamp. If absent or invalid, use `observedAt`.

For Amp, iterate exported messages and inspect text blocks, tool inputs, and tool result `Run` payloads. Parse `CreatedAt`; use `observedAt` only when absent.

Do not scan model reasoning blocks, system prompts, or raw JSON envelopes.

- [ ] **Step 6: Verify green**

Run: `go test ./... -run 'TestExtractTrailEvidence|TestClaudeTrailEvents|TestAmpTrailEvents' -v`

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add wtf_scan.go wtf_scan_test.go testdata/wtf/claude-trails.jsonl testdata/wtf/amp-trails.json
git commit -m "feat(wtf): extract trail evidence"
```

---

### Task 3: Inspect observed repositories and worktrees

**Files:**
- Modify: `wtf_scan.go`
- Modify: `wtf_scan_test.go`

**Interfaces:**

```go
type wtfCommandRunner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

type gitWorktreeEntry struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
}

func parseGitWorktreePorcelain(data []byte) []gitWorktreeEntry
func inspectRepoWorktrees(ctx context.Context, repo, cwd string, now int64, run wtfCommandRunner) []wtfWorktree
func resolveRemoteDefault(ctx context.Context, cwd string, run wtfCommandRunner) string
func inspectWorktree(ctx context.Context, repo string, entry gitWorktreeEntry, now int64, run wtfCommandRunner) wtfWorktree
func worktreeHasWIP(w wtfWorktree) bool
```

- [ ] **Step 1: Test the Git parsers and unknown states**

Cover:

- multiple `git worktree list --porcelain` blocks;
- branch refs normalized from `refs/heads/feat/x` to `feat/x`;
- detached HEAD;
- `refs/remotes/origin/HEAD` resolving to `origin/main`;
- fallback to existing `origin/main`, then `origin/master`;
- no remote default leaves `DefaultBranch` empty and `UnmergedCommits` at `-1`;
- two porcelain lines count as two dirty files;
- missing worktree reports `Exists=false`, `GitError="worktree path missing"`, and WIP unknown rather than clean.
- branch, unmerged commit subjects, and an uncommitted diff are retained as bounded association evidence.

- [ ] **Step 2: Add a real temporary-repository test**

Create a bare origin, clone it, create an initial main commit, add a linked worktree, then:

1. make one untracked file;
2. add one commit not on `origin/main`;
3. inspect;
4. assert `DirtyFiles == 1` and `UnmergedCommits == 1`.

This catches swapped rev-list arguments, which a symmetric clean repository would miss.

- [ ] **Step 3: Verify red**

Run: `go test ./... -run 'TestParseGitWorktree|TestInspectWorktree|TestResolveRemoteDefault' -v`

Expected: FAIL with undefined functions.

- [ ] **Step 4: Implement bounded Git calls**

Use a five-second context per repository. Run:

```text
git -C <cwd> worktree list --porcelain
git -C <worktree> status --porcelain
git -C <worktree> symbolic-ref --quiet refs/remotes/origin/HEAD
git -C <worktree> show-ref --verify --quiet refs/remotes/origin/main
git -C <worktree> show-ref --verify --quiet refs/remotes/origin/master
git -C <worktree> rev-list --count origin/<default>..HEAD
git -C <worktree> log --format=%s origin/<default>..HEAD
git -C <worktree> diff --no-ext-diff --unified=0 HEAD --
```

Store at most ten porcelain summary lines, 50 unmerged commit subjects, and 128 KiB of diff evidence. Branch evidence comes from the porcelain worktree entry. Preserve the prior historical record when a path disappears, but update `Exists`, `LastSeen`, and `GitError`.

- [ ] **Step 5: Verify green**

Run: `go test ./... -run 'TestParseGitWorktree|TestInspectWorktree|TestResolveRemoteDefault' -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtf_scan.go wtf_scan_test.go
git commit -m "feat(wtf): inspect worktree wip"
```

---

### Task 4: Fetch and cache Entire trail metadata

**Files:**
- Modify: `wtf_scan.go`
- Modify: `wtf_scan_test.go`

**Interfaces:**

```go
type entireTrailJSON struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	Branch         string `json:"branch"`
	OriginalBranch string `json:"original_branch"`
	Base           string `json:"base"`
	Title          string `json:"title"`
	Status         string `json:"status"`
}

func fetchTrailMetadata(ctx context.Context, trail wtfTrail, now int64, run wtfCommandRunner) (wtfTrail, error)
func trailMetadataDue(trail wtfTrail, activeWIP bool, now int64) bool
```

- [ ] **Step 1: Write failing decode and command tests**

Assert the command is exactly:

```text
entire trail show 1223 --repo gh/entirehq/entiredb --json
```

Decode a response where `branch` is empty and `original_branch` is `shallow`; `SourceBranch` must become `shallow`. Decode another where `branch` is populated; it wins. `base` becomes `TargetBranch`.

Test refresh policy: new metadata immediately, active WIP after ten minutes, merged clean history after 24 hours, and failed lookup after exponential backoff capped at one hour. Use asymmetric failure attempts to assert delays of one, two, four, and eight minutes rather than checking only the cap.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run 'TestFetchTrailMetadata|TestTrailMetadataDue' -v`

Expected: FAIL.

- [ ] **Step 3: Implement the read-only CLI boundary**

Use a 12-second timeout. On failure, increment `MetadataAttempts`, set `MetadataNextRetry` to `now + min(1m << (attempts-1), 1h)`, and keep prior successful metadata. On success, clear the error, attempt count, and next-retry timestamp. Never remove local evidence or a claim because Entire is unavailable.

- [ ] **Step 4: Verify green**

Run: `go test ./... -run 'TestFetchTrailMetadata|TestTrailMetadataDue' -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add wtf_scan.go wtf_scan_test.go
git commit -m "feat(wtf): enrich observed trails"
```

---

### Task 5: Reconcile first claims and canonical worktrees

**Files:**
- Modify: `wtf_scan.go`
- Modify: `wtf_scan_test.go`

**Interfaces:**

```go
type wtfObservation struct {
	SessionKey string
	Active     bool
	Repo       string
	Worktree   string
	Branch     string
	Evidence   trailEvidence
}

func reconcileTrails(prior wtfState, sessions []wtfSession, evidence map[string][]trailEvidence, worktrees map[string]wtfWorktree, now int64) wtfState
func chooseInitialCanonical(trail wtfTrail, claims []wtfClaim, worktrees map[string]wtfWorktree) string
func addAssociation(existing []wtfAssociation, next wtfAssociation) []wtfAssociation
```

- [ ] **Step 1: Write failing ownership tests**

Cover:

- two observations supplied in reverse scan order still choose the earlier event timestamp;
- restart with the observations reversed preserves the persisted owner;
- a session with no local worktree is related but does not claim;
- two distinct session IDs in the same worktree still produce a later challenger;
- one session ID moving worktrees keeps one owner and becomes outside-canonical later;
- an ended-today session containing a trail mention cannot create a trail or claim, while its previously persisted claim remains;
- duplicate association evidence is idempotent.

- [ ] **Step 2: Write canonical selection tests**

Use one earlier claim on `feat/other` and one later associated worktree on trail source branch `feat/1223`. On first selection, source-branch match must win. With metadata unavailable, first claim must win. After canonical is persisted, later metadata or a new matching worktree must not move it.

- [ ] **Step 3: Verify red**

Run: `go test ./... -run 'TestReconcileTrail|TestChooseInitialCanonical' -v`

Expected: FAIL.

- [ ] **Step 4: Implement claim ordering and stability**

Sort candidate claims by evidence timestamp, then session key and worktree path for deterministic ties. Set `OwnerSession`, `FirstClaim`, and `CanonicalWorktree` only when currently empty. Never replace them in normal reconciliation.

Associate a worktree when:

- a session claimed there;
- its branch equals the trail source branch;
- branch, unmerged commit message, or diff contains a full key, full URL, or unambiguous repo-qualified shorthand.

Do not associate on a bare number in Git evidence.

- [ ] **Step 5: Verify green**

Run: `go test ./... -run 'TestReconcileTrail|TestChooseInitialCanonical' -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtf_scan.go wtf_scan_test.go
git commit -m "feat(wtf): persist first trail claims"
```

---

### Task 6: Generate and retain findings

**Files:**
- Modify: `wtf.go`
- Modify: `wtf_scan.go`
- Modify: `wtf_scan_test.go`

**Interfaces:**

```go
func detectWTFFindings(state wtfState, now int64) map[string]wtfFinding
func mergeWTFFindings(prior, current map[string]wtfFinding, now int64) map[string]wtfFinding
func wtfFindingID(kind, trail string, sessions, worktrees []string) string
```

- [ ] **Step 1: Write one focused test per finding**

Pin these exact differences:

1. `duplicate-active-claim`: two active, distinct session IDs claim one trail. One session seen twice does not trigger it.
2. `existing-wip-elsewhere`: active challenger claims while a different associated worktree has dirty files or positive unmerged count, including a worktree last seen two days ago.
3. `outside-canonical`: active session worktree differs from canonical, including the same owner session moving paths.
4. `default-branch`: active claimed worktree branch equals its resolved default branch.
5. `missing-canonical`: canonical path is missing while another association is active or has WIP.

Assert stable IDs are insensitive to input ordering.

- [ ] **Step 2: Test clear and recurrence**

First scan active, second scan clear, third scan active again. Assert one retained finding record, `Active` false then true, preserved `FirstSeen`, incremented `Occurrence`, reset delivery map on recurrence, and no reset while continuously active.

- [ ] **Step 3: Verify red**

Run: `go test ./... -run 'TestDetectWTFFindings|TestMergeWTFFindings' -v`

Expected: FAIL.

- [ ] **Step 4: Implement pure finding generation**

Use severity 3 for duplicate claims and default-branch work, 2 for WIP elsewhere and outside-canonical, 1 for missing canonical. Build explanations from IDs, paths, dirty counts, and unmerged counts only. Never use model summaries as evidence.

- [ ] **Step 5: Verify green**

Run: `go test ./... -run 'TestDetectWTFFindings|TestMergeWTFFindings' -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtf.go wtf_scan.go wtf_scan_test.go
git commit -m "feat(wtf): detect trail conflicts"
```

---

### Task 7: Compose one deterministic scan

**Files:**
- Modify: `wtf_scan.go`
- Modify: `wtf_scan_test.go`
- Modify: `wtf.go`

**Interfaces:**

```go
type wtfScanDeps struct {
	Inventory     wtfInventoryDeps
	Run           wtfCommandRunner
	Summarize     wtfSummarizer
	Now           func() time.Time
}

func scanWTF(ctx context.Context, home string, prior wtfState, deps wtfScanDeps) (wtfState, error)
```

- [ ] **Step 1: Write the integration fixture test**

Build a temporary home and two temporary Git worktrees. Seed prior state with a Claude claim for `acme/api#1223` and its canonical worktree, last seen two days ago with dirty files. Feed only one active Amp fixture claiming the same trail today elsewhere. Do not feed the old Claude session through today's inventory.

Assert the final state has:

- only today's active session in `Sessions`;
- one indefinite trail record;
- both indefinite worktree records;
- the ended Claude session still recorded as first owner;
- the old worktree still canonical;
- active `existing-wip-elsewhere` and `outside-canonical` findings;
- exactly one summary call per changed current session;
- no global Entire list command.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run TestScanWTFIntegration -v`

Expected: FAIL.

- [ ] **Step 3: Implement scan order**

The order is load/initialize, collect sessions, expire old session rows, scan active-session transcript evidence, inspect observed repos, create new trail records from active-session evidence, fetch due metadata, reconcile claims and canonical paths, inspect Git association evidence for known trails, detect findings, summarize changed current sessions, set `UpdatedAt`. Ended-today sessions contribute dashboard rows and summaries only.

Conflict detection must run before metadata refresh and summary calls can block it. Implement this by doing the local evidence and first finding pass first, then enrich and rerun pure reconciliation before returning.

Return a joined error that names degraded sources while preserving the partial state.

- [ ] **Step 4: Verify green**

Run:

```sh
go test ./... -run TestScanWTFIntegration -v
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add wtf.go wtf_scan.go wtf_scan_test.go testdata/wtf
git commit -m "feat(wtf): reconcile registry scans"
```

---

### Task 8: Render Badness and WIP trails

**Files:**
- Modify: `wtf_view.go`
- Modify: `wtf_view_test.go`
- Modify: `wtf.go`
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add failing view tests**

Assert section order is `Badness`, `Now`, `WIP trails`, `Recently stopped`. Within Badness, sort severity descending then first seen ascending. Within WIP trails, include a trail when any session is active, any associated worktree is dirty, or unmerged count is positive. Exclude merged clean history.

Each finding must show owner, challenger, canonical and actual paths, evidence, and delivery state. Delivery is `not attempted` until the monitoring plan.

Each WIP trail must show canonical path, owner, active sessions, dirty count, unmerged count, and association reason.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run 'TestRenderWTFBadness|TestRenderWTFWIPTrails' -v`

Expected: FAIL.

- [ ] **Step 3: Extend pure rendering**

Update the header to:

```text
WTF  6 active · 4 ended today · 3 WIP trails · 2 findings       14:37:08
```

Keep sessions selectable. Findings and trails are informational in this version.

- [ ] **Step 4: Use durable state in the command**

The one-shot foreground command loads prior state, runs `scanWTF`, renders the returned in-memory state, and saves it only after a successful complete local reconciliation. Print degraded source errors in a footer without blanking the dashboard. The daemon plan removes foreground writes once the daemon owns state.

- [ ] **Step 5: Update documentation**

Document supported trail forms, first-mention ownership, canonical selection, WIP rules, indefinite trail/worktree history, and every finding kind.

- [ ] **Step 6: Run the full gate and inspect output**

```sh
gofmt -w wtf.go wtf_test.go wtf_scan.go wtf_scan_test.go wtf_view.go wtf_view_test.go
go test ./...
go vet ./...
go test -race ./...
go build -o entire-tail .
./entire-tail wtf </dev/null
```

Inspect a tty render at normal and narrow widths. Capture and inspect one screenshot containing a finding and a WIP trail.

- [ ] **Step 7: Commit**

```sh
git add wtf.go wtf_view.go wtf_view_test.go README.md CLAUDE.md
git commit -m "feat(wtf): show trail ownership and badness"
```

## Completion gate

Do not start monitoring until:

- first ownership and canonical placement survive restarts;
- `repo#1223` and bare trail resolution tests pass;
- old dirty or unmerged worktrees remain visible indefinitely;
- all five finding kinds clear and recur correctly;
- the dashboard shows Badness and WIP trails from deterministic evidence;
- no warnings or macOS notifications are sent yet;
- tests, vet, race, build, and visual inspection pass.
