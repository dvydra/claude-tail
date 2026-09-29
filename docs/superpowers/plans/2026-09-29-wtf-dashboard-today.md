# `entire wtf` Today Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `entire-tail wtf`, a terminal dashboard that shows every active Claude and Amp session plus every session with activity since local midnight, summarized with Apple Foundation Models.

**Architecture:** Reuse the existing Claude registries, Amp inventory, merged session tree, transcript adapters, pending markers, and `fmRunner`. A new `wtfSession` snapshot normalizes those sources. Pure collection, summarization, reduction, and rendering stay separate from the tty driver. This first plan does not assign trails or send warnings.

**Tech Stack:** Go, `golang.org/x/term`, Apple Foundation Models CLI through the existing `fmRunner` boundary.

## Scope and constraints

- Work only in the existing `feat/wtf-dashboard` worktree.
- Keep the single `package main` layout.
- Add no runtime dependency.
- Active identity comes from `buildLiveSessionsWithAmp`; ended-today inventory comes from the existing two-day Claude and Amp trees plus `localMidnight` and `flattenToday`.
- `fm` produces only `summary` and `needsUser`. Agent state, repo, cwd, branch, identity, and pending prompts remain deterministic.
- Piped output is one ANSI-free snapshot. A tty refreshes once per second and supports `q`, Escape, `r`, arrows, and Enter.
- Tests never invoke `fm`, Amp, a real tty, or live processes.

---

### Task 1: Add the `wtf` command boundary

**Files:**
- Modify: `config.go`
- Modify: `config_test.go`
- Modify: `main.go`
- Create: `wtf.go`
- Create: `wtf_test.go`

**Interfaces:**

Add `WTFArgs []string` to `Config`, add `ActionWTF` to the existing action constants, and add:

```go
func runWTF(cfg Config) error
```

- [ ] **Step 1: Write the failing CLI test**

```go
func TestParseCLIWTF(t *testing.T) {
	cfg, action, err := parseCLI([]string{"wtf"}, func(string) string { return "" }, savedPrefs{})
	if err != nil || action != ActionWTF || len(cfg.WTFArgs) != 0 {
		t.Fatalf("parseCLI(wtf) = action %v args %v err %v", action, cfg.WTFArgs, err)
	}
	cfg, action, err = parseCLI([]string{"wtf", "status"}, func(string) string { return "" }, savedPrefs{})
	if err != nil || action != ActionWTF || !reflect.DeepEqual(cfg.WTFArgs, []string{"status"}) {
		t.Fatalf("parseCLI(wtf status) = action %v args %v err %v", action, cfg.WTFArgs, err)
	}
}
```

- [ ] **Step 2: Verify red**

Run: `go test ./... -run TestParseCLIWTF -v`

Expected: FAIL because `ActionWTF` and `WTFArgs` do not exist.

- [ ] **Step 3: Parse `wtf` before normal flags**

Add `WTFArgs []string` beside `TapArgs` and `LinkArgs`, add `ActionWTF`, and add this branch before the option loop:

```go
if len(args) > 0 && args[0] == "wtf" {
	c.WTFArgs = append([]string(nil), args[1:]...)
	return c, ActionWTF, nil
}
```

- [ ] **Step 4: Dispatch from `main`**

```go
case ActionWTF:
	if err := runWTF(cfg); err != nil {
		die(err.Error())
	}
	return
```

Start `runWTF` with a temporary explicit error for non-empty args and an empty snapshot render for the bare command. Later tasks replace both.

- [ ] **Step 5: Verify green**

Run: `go test ./... -run TestParseCLIWTF -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add config.go config_test.go main.go wtf.go wtf_test.go
git commit -m "feat(wtf): add command boundary"
```

---

### Task 2: Normalize today and live sessions into one snapshot

**Files:**
- Modify: `wtf.go`
- Modify: `wtf_test.go`
- Modify: `live.go`
- Modify: `live_test.go`

**Interfaces:**

```go
type wtfSession struct {
	Agent        Agent
	ID           string
	Name         string
	Repo         string
	Cwd          string
	Branch       string
	Transcript   string
	State        string
	Active       bool
	StartedAt    int64
	LastActivity int64
	SocketPath   string
	Summary      string
	NeedsUser    string
}

type wtfSnapshot struct {
	GeneratedAt int64
	Sessions    []wtfSession
}

type wtfInventoryDeps struct {
	Today func(home string, now int64, loc *time.Location) []handoverItem
	Live  func(home string) []liveSession
}

func collectWTFSessions(home string, now int64, loc *time.Location, deps wtfInventoryDeps) []wtfSession
```

- [ ] **Step 1: Write a failing merge test with asymmetric inputs**

Use one Claude session present in both sources, one ended Claude session only in today, one local Amp session in both, and one remote Amp session only in live. Assert that:

- the six source rows become four unique sessions;
- live state, current cwd, branch, socket, and name win for the overlapping Claude row;
- ended-today stays `ended` and inactive;
- remote Amp remains visible with an empty local cwd;
- output sorts active first, busy before idle, then newest activity.

```go
func TestCollectWTFSessionsMergesExactLiveState(t *testing.T) {
	today := []handoverItem{
		{Agent: AgentClaude, SessionID: "c1", Repo: "o/r", Cwd: "/old", Branch: "old", Title: "old title", LastActivity: 100, Path: "/t/c1.jsonl"},
		{Agent: AgentClaude, SessionID: "c2", Repo: "o/r", Cwd: "/ended", Title: "done", LastActivity: 90, Path: "/t/c2.jsonl"},
		{Agent: AgentAmp, SessionID: "T-a", Repo: "o/a", Cwd: "/amp", Title: "amp", LastActivity: 80, Path: "/t/a.jsonl"},
	}
	live := []liveSession{
		{Agent: AgentClaude, SessionID: "c1", Cwd: "/new", Branch: "feat/x", Name: "api-work", Status: "busy", SocketPath: "/tmp/c1.sock", StartedAt: 10_000, UpdatedAt: 120_000, Path: "/t/c1.jsonl"},
		{Agent: AgentAmp, SessionID: "T-a", Cwd: "/amp", Branch: "feat/a", Name: "amp", Status: "idle", UpdatedAt: 110_000, Path: "/t/a.jsonl"},
		{Agent: AgentAmp, SessionID: "T-remote", Name: "remote", Status: "busy", UpdatedAt: 130_000, Path: "/t/remote.jsonl"},
	}
	deps := wtfInventoryDeps{
		Today: func(string, int64, *time.Location) []handoverItem { return today },
		Live:  func(string) []liveSession { return live },
	}
	got := collectWTFSessions("/h", 200, time.UTC, deps)
	if len(got) != 4 || got[0].ID != "T-remote" || got[1].ID != "c1" || got[3].ID != "c2" {
		t.Fatalf("sessions = %+v", got)
	}
	if got[1].Cwd != "/new" || got[1].State != "busy" || got[1].SocketPath != "/tmp/c1.sock" {
		t.Fatalf("live overlay = %+v", got[1])
	}
}
```

- [ ] **Step 2: Verify red**

Run: `go test ./... -run TestCollectWTFSessions -v`

Expected: FAIL with undefined types/functions.

- [ ] **Step 3: Add an injected live inventory wrapper**

Extract only the orchestration around the existing live collector, without changing its discovery rules:

```go
func currentLiveSessions(home string) []liveSession {
	watcher := startAmpTop()
	defer watcher.close()
	active, available := watcher.wait(ampFocusRefreshTimeout)
	sessions, _ := buildLiveSessionsWithAmp(home, active, available)
	return sessions
}
```

Keep `buildLiveSessionsWithAmp` unchanged. Unit tests inject `Live` and never start the watcher.

- [ ] **Step 4: Implement the deterministic merge**

Seed a map keyed by `agent + ":" + id` from `todaysSessions`. Overlay live rows by the same key. Convert millisecond registry fields to seconds. Resolve repo from cwd with `repoForCwd` only when the row lacks one. Active rows with unknown status use `active`, and historical rows use `ended`.

Do not infer active from mtime. `liveSession` membership is the active fact.

- [ ] **Step 5: Verify green and regression tests**

Run:

```sh
go test ./... -run 'TestCollectWTFSessions|TestBuildLiveSessions|TestFlattenToday' -v
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add wtf.go wtf_test.go live.go live_test.go
git commit -m "feat(wtf): collect today and live sessions"
```

---

### Task 3: Add Apple Foundation Models session summaries

**Files:**
- Create: `wtf_summary.go`
- Create: `wtf_summary_test.go`
- Modify: `wtf.go`
- Modify: `wtf_test.go`

**Interfaces:**

```go
type wtfSummary struct {
	Summary   string `json:"summary"`
	NeedsUser string `json:"needsUser"`
}

type wtfSummaryCache struct {
	InputHash string
	Value     wtfSummary
}

func wtfSummaryInput(path, home string) string
func summarizeWTFSession(s wtfSession, home string, cached wtfSummaryCache) (wtfSummary, wtfSummaryCache, error)
func deterministicNeed(home string, s wtfSession) string
func fallbackWTFSummary(s wtfSession) string
```

Use this schema and instruction exactly:

```go
const wtfSummarySchema = `{
  "title": "WorkSummary",
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "needsUser"],
  "x-order": ["summary", "needsUser"],
  "properties": {
    "summary": {"type": "string", "description": "One concise sentence describing the work and current progress."},
    "needsUser": {"type": "string", "description": "One concise sentence naming a concrete decision or action Daniel owes, or an empty string."}
  }
}`

const wtfSummaryInstructions = "Summarize this coding-agent session. State the concrete work and current progress in one sentence. Set needsUser only for a concrete decision or action Daniel owes. Do not infer repository, branch, trail identity, ownership, or safety."
```

- [ ] **Step 1: Write failing tests for caching, invalid output, and deterministic override**

Test these competing implementations:

1. Same transcript sample and same hash must not call `fmRunner` twice.
2. Changed sample must call it again.
3. Invalid JSON must return `fallbackWTFSummary`, not a blank row.
4. A pending question marker must replace a model-produced `needsUser`, not append to it.
5. Tool/task synthetic records must not enter the model input. Pin this through `wtfSummaryInput`, which must reuse `transcriptText`.

Stub `fmRunner` and restore it with `t.Cleanup`.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run 'TestSummarizeWTF|TestWTFSummaryInput' -v`

Expected: FAIL with undefined functions.

- [ ] **Step 3: Implement structured decoding and hash cache**

Use SHA-256 over `wtfSummaryInput`; store the full lowercase hex string. If it matches `cached.InputHash`, return the cached value without calling `fmRunner`. Parse only the outermost JSON object, as `parseSummaryJSON` already does.

For no transcript or any `fm` error, return:

```go
wtfSummary{Summary: fallbackWTFSummary(s), NeedsUser: deterministicNeed(home, s)}
```

`deterministicNeed` reads the existing pending marker. Questions become `Waiting for Daniel: <question text>`. Permissions become `Waiting for Daniel to approve <permissionSummary>.` It always wins over the model field.

- [ ] **Step 4: Enrich a snapshot without blocking collection tests**

```go
type wtfSummarizer func(wtfSession, string, wtfSummaryCache) (wtfSummary, wtfSummaryCache, error)

func summarizeWTFSnapshot(snapshot wtfSnapshot, home string, cache map[string]wtfSummaryCache, summarize wtfSummarizer) (wtfSnapshot, map[string]wtfSummaryCache)
```

Key the cache by `agent + ":" + id`. Summarize only rows with a transcript. Keep remote rows on their deterministic title fallback.

- [ ] **Step 5: Verify green**

Run:

```sh
go test ./... -run 'TestSummarizeWTF|TestWTFSummaryInput' -v
go test ./...
```

Expected: PASS and no real `fm` invocation.

- [ ] **Step 6: Commit**

```sh
git add wtf.go wtf_test.go wtf_summary.go wtf_summary_test.go
git commit -m "feat(wtf): summarize sessions with foundation models"
```

---

### Task 4: Render static and interactive dashboard sections

**Files:**
- Create: `wtf_view.go`
- Create: `wtf_view_test.go`
- Modify: `wtf.go`
- Modify: `wtf_test.go`

**Interfaces:**

```go
type wtfUI struct {
	Snapshot wtfSnapshot
	Cursor   int
	Top      int
	Width    int
	Height   int
	Refresh  bool
	Quit     bool
	Chosen   *wtfSession
}

func updateWTF(ui wtfUI, key treeKey, r rune) wtfUI
func renderWTFSnapshot(snapshot wtfSnapshot, width int, color bool) string
func renderWTF(ui wtfUI, theme Theme) string
func runWTFDashboard(home string, cfg Config) (*wtfSession, error)
```

- [ ] **Step 1: Write failing renderer tests**

Create a snapshot with busy Claude, idle Amp, and two ended sessions in different repos. Assert:

- header counts say `2 active · 2 ended today`;
- section order is `Now`, then `Recently stopped` for this phase;
- active rows group by repo;
- busy precedes idle;
- recently stopped sorts newest first;
- `needsUser` is shown directly below the matching summary;
- width 48 clips every visible line to 48 columns;
- `color=false` contains no escape byte;
- an empty snapshot says `No sessions active or seen today.`

Write reducer tests for arrows, Home/End, `r`, `q`, Escape, and Enter.

- [ ] **Step 2: Verify red**

Run: `go test ./... -run 'TestRenderWTF|TestUpdateWTF' -v`

Expected: FAIL with undefined renderer/reducer.

- [ ] **Step 3: Implement pure row composition**

Use `truncVisible`, `tildify`, `shortID`, and existing theme ANSI values. Each session block contains:

```text
C  api-work      busy   feat/checkpoints   ~/…/wt/checkpoints
   Reworking checkpoint writes; tests are running.
   NEEDS DANIEL  Choose whether to preserve the old API.
```

Keep row selection as a separate flat list of session IDs so headers never receive the cursor.

- [ ] **Step 4: Implement the tty driver**

Follow `runLiveTUI`: alt screen, `setRawTimed`, one render goroutine, one timed input loop. Recollect and summarize once per second. `r` forces the next collection. Enter returns the selected session.

When stdout is not a character device, collect once, call `renderWTFSnapshot(..., false)`, print, and return.

When Enter chooses a row, route through the existing agent-aware tail path rather than building a second resume implementation. Add a small conversion from `wtfSession` to `treeChoice` and pass it to `resolveTreeChoice` from `runWTF`.

- [ ] **Step 5: Verify behavior**

Run:

```sh
go test ./... -run 'TestRenderWTF|TestUpdateWTF' -v
go test ./...
go run . wtf </dev/null
```

Expected: tests pass; piped command prints one ANSI-free snapshot and exits.

- [ ] **Step 6: Commit**

```sh
git add wtf.go wtf_test.go wtf_view.go wtf_view_test.go
git commit -m "feat(wtf): render today dashboard"
```

---

### Task 5: Document and verify the first usable slice

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `main.go` or `help.go` where the CLI help text is owned
- Modify: `help_test.go`

- [ ] **Step 1: Add failing help assertions**

Assert that command help contains `entire-tail wtf` and describes “active and today’s sessions”.

- [ ] **Step 2: Update user documentation**

Document:

- `entire wtf` and `entire-tail wtf`;
- active plus since-local-midnight retention;
- Apple Foundation Models summary and deterministic fallback;
- `q`, Escape, `r`, arrows, and Enter;
- this phase does not yet install monitoring or assign trails.

- [ ] **Step 3: Run the full gate**

```sh
gofmt -w config.go config_test.go main.go wtf.go wtf_test.go wtf_summary.go wtf_summary_test.go wtf_view.go wtf_view_test.go
go test ./...
go vet ./...
go test -race ./...
go build -o entire-tail .
```

Expected: all commands exit 0.

- [ ] **Step 4: Inspect the terminal UI**

Run `./entire-tail wtf` in a representative terminal at normal width and at approximately 60 columns. Inspect Now, Recently stopped, an FM summary, a fallback summary, a `needsUser` row, selection, and refresh. Capture and inspect one screenshot before calling the UI complete.

- [ ] **Step 5: Commit**

```sh
git add README.md CLAUDE.md main.go help.go help_test.go
git commit -m "docs(wtf): document today dashboard"
```

## Completion gate

Do not start the trail registry plan until:

- `entire-tail wtf` works with and without a tty;
- active Claude and Amp identity comes from live sources;
- ended sessions stop at local midnight;
- `fm` failure leaves useful rows;
- tests, vet, race, build, and visual inspection pass;
- no code scans trails or sends notifications yet.
