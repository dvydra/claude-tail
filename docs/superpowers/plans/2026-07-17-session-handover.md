# Session Handover Docs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `entire-tail handover` — enumerate today's Claude sessions, let the user group them in a picker, then launch an interactive Claude that enriches each group with live Linear/GitHub/Entire state and writes one handover doc per group into the Obsidian vault.

**Architecture:** Go does the deterministic half (enumerate today → grouping-picker → JSON manifest → launch a claude pane). A `handover-sessions` skill holds the prose+enrichment prompt the launched Claude runs. The two halves meet at the manifest JSON file. Everything pure (today-filter, picker reducer, group collapse, manifest builder) is split from the imperative tty/exec driver and unit-tested, matching the existing `tree.go` convention.

**Tech Stack:** Go (single `package main`), `golang.org/x/term`, `osascript` (iTerm2), Claude Code skill (Markdown).

## Global Constraints

- Single `package main`; new files `handover.go`, `handover_picker.go`, `handover_test.go`.
- No new runtime dependencies in the Go binary. `pgrep`/`lsof`/`osascript` are optional, never required (repo convention).
- Tests non-negotiable: pure functions get unit tests; run `go vet ./... && go test -race ./...` before presenting.
- Never push to `main`; fresh branch, PR with description, code + security review (global workflow).
- Vault root default: `/Users/dvydra/Library/Mobile Documents/iCloud~md~obsidian/Documents`, overridable via `ENTIRE_TAIL_HANDOVER_VAULT`. Docs go under `<root>/Handover/YYYY-MM-DD/`.
- v1 is Claude-only; today = activity at/after local midnight.

---

### Task 1: Today-filter + flatten (pure)

**Files:**
- Create: `handover.go`
- Test: `handover_test.go`

**Interfaces:**
- Consumes: `sessionTree`, `treeFolder`, `treeSession` (tree.go), `repoForCwd` (entire.go).
- Produces: `type handoverItem`; `func localMidnight(now int64, loc *time.Location) int64`; `func flattenToday(t sessionTree, midnight int64, home string) []handoverItem`.

```go
type handoverItem struct {
	SessionID    string
	Repo         string // owner/repo, else ~path
	Cwd          string
	Title        string // snippet
	Live         bool
	LastActivity int64
	Tokens       int64
	Path         string // local transcript
}
```

- [ ] **Step 1: Write failing tests**

```go
func TestLocalMidnight(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 14, 5, 0, 0, loc).Unix()
	want := time.Date(2026, 7, 17, 0, 0, 0, 0, loc).Unix()
	if got := localMidnight(now, loc); got != want {
		t.Fatalf("localMidnight = %d, want %d", got, want)
	}
}

func TestFlattenTodayKeepsTodayLocalOnly(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, loc).Unix()
	mid := localMidnight(now, loc)
	yesterday := mid - 3600
	today := mid + 3600
	tree := sessionTree{Home: "/h", Folders: []treeFolder{{
		Cwd: "/h/src/repo", Dir: "/h/src/repo",
		Sessions: []treeSession{
			{ID: "a", Path: "/p/a.jsonl", Mtime: today, cwd: "/h/src/repo", Snippet: "work", Live: true},
			{ID: "b", Path: "/p/b.jsonl", Mtime: yesterday, cwd: "/h/src/repo"},
			{ID: "c", Path: "", Mtime: today, cwd: "/h/src/repo"}, // cloud-only: no transcript
		},
	}}}
	got := flattenToday(tree, mid, "/h")
	if len(got) != 1 || got[0].SessionID != "a" {
		t.Fatalf("flattenToday = %+v, want only session a", got)
	}
	if !got[0].Live {
		t.Fatalf("expected session a live")
	}
}
```

- [ ] **Step 2: Run to verify fail** — `go test ./... -run 'TestLocalMidnight|TestFlattenToday' -v` → FAIL (undefined).

- [ ] **Step 3: Implement**

```go
package main

import (
	"path/filepath"
	"time"
)

func localMidnight(now int64, loc *time.Location) int64 {
	t := time.Unix(now, 0).In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).Unix()
}

// flattenToday collapses a session tree to a flat list of local Claude sessions
// with activity at/after midnight and an on-disk transcript (needed to enrich).
func flattenToday(t sessionTree, midnight int64, home string) []handoverItem {
	cache := map[string]string{}
	var out []handoverItem
	for _, f := range t.Folders {
		for _, s := range f.Sessions {
			if s.Path == "" || s.Mtime < midnight {
				continue
			}
			cwd := firstNonEmpty(s.cwd, f.Dir, f.Cwd)
			out = append(out, handoverItem{
				SessionID:    s.ID,
				Repo:         repoForCwd(cwd, home, cache),
				Cwd:          cwd,
				Title:        s.Snippet,
				Live:         s.Live,
				LastActivity: s.Mtime,
				Tokens:       s.Tokens,
				Path:         s.Path,
			})
		}
	}
	return out
}
```

- [ ] **Step 4: Run to verify pass** — same command → PASS.
- [ ] **Step 5: Commit** — `git add handover.go handover_test.go && git commit -m "feat(handover): today-filter + flatten"`

---

### Task 2: Grouping-picker reducer + collapse (pure)

**Files:**
- Create: `handover_picker.go`
- Test: `handover_test.go`

**Interfaces:**
- Consumes: `handoverItem` (Task 1); `treeKey`, key constants, `decodeKey` (tree.go).
- Produces: `type handoverTag int`; `type handoverPickUI`; `type handoverGroup`; `func updateHandoverPick(ui handoverPickUI, k treeKey, r rune) handoverPickUI`; `func buildGroups(items []handoverItem, tags []handoverTag) []handoverGroup`; `func allIndependent(n int) []handoverTag`.

```go
type handoverTag int

const (
	tagExcluded    handoverTag = -1
	tagIndependent handoverTag = 0 // 1..9 = user group number
)

type handoverPickUI struct {
	Items  []handoverItem
	Tags   []handoverTag // parallel to Items
	Cursor int
	Top    int
	Width  int
	Height int
	Done   bool // Enter → proceed with current assignment
	Quit   bool // q/Esc → abort
}

type handoverGroup struct {
	GroupID  string
	Sessions []handoverItem
}
```

- [ ] **Step 1: Write failing tests**

```go
func TestUpdateHandoverPickTagging(t *testing.T) {
	ui := handoverPickUI{Items: make([]handoverItem, 3), Tags: allIndependent(3), Height: 10}
	ui = updateHandoverPick(ui, kRune, '2')      // tag row 0 → group 2
	ui = updateHandoverPick(ui, kDown, 0)
	ui = updateHandoverPick(ui, kRune, '2')      // tag row 1 → group 2
	ui = updateHandoverPick(ui, kDown, 0)
	ui = updateHandoverPick(ui, kRune, '-')      // exclude row 2
	if ui.Tags[0] != 2 || ui.Tags[1] != 2 || ui.Tags[2] != tagExcluded {
		t.Fatalf("tags = %v", ui.Tags)
	}
	ui = updateHandoverPick(ui, kRune, 'x')      // row 2 back to independent (cursor still 2)
	if ui.Tags[2] != tagIndependent {
		t.Fatalf("x did not reset, tags = %v", ui.Tags)
	}
}

func TestUpdateHandoverPickDoneQuit(t *testing.T) {
	ui := handoverPickUI{Items: make([]handoverItem, 1), Tags: allIndependent(1), Height: 10}
	if updateHandoverPick(ui, kEnter, 0).Done != true {
		t.Fatal("Enter should set Done")
	}
	if updateHandoverPick(ui, kRune, 'q').Quit != true {
		t.Fatal("q should set Quit")
	}
	if updateHandoverPick(ui, kEsc, 0).Quit != true {
		t.Fatal("Esc should set Quit")
	}
}

func TestBuildGroups(t *testing.T) {
	items := []handoverItem{
		{SessionID: "solo1", Repo: "o/a"},
		{SessionID: "ci1", Repo: "o/b"},
		{SessionID: "ci2", Repo: "o/b"},
		{SessionID: "skip", Repo: "o/c"},
	}
	tags := []handoverTag{tagIndependent, 2, 2, tagExcluded}
	got := buildGroups(items, tags)
	if len(got) != 2 {
		t.Fatalf("want 2 groups, got %d: %+v", len(got), got)
	}
	if got[0].GroupID != "solo-1" || len(got[0].Sessions) != 1 || got[0].Sessions[0].SessionID != "solo1" {
		t.Fatalf("group 0 = %+v", got[0])
	}
	if got[1].GroupID != "g2" || len(got[1].Sessions) != 2 {
		t.Fatalf("group 1 = %+v", got[1])
	}
}
```

- [ ] **Step 2: Run to verify fail** — `go test ./... -run Handover -v` → FAIL (undefined).

- [ ] **Step 3: Implement**

```go
package main

func allIndependent(n int) []handoverTag { return make([]handoverTag, n) }

func updateHandoverPick(ui handoverPickUI, k treeKey, r rune) handoverPickUI {
	switch k {
	case kUp:
		ui.Cursor--
	case kDown:
		ui.Cursor++
	case kHome:
		ui.Cursor = 0
	case kEnd:
		ui.Cursor = len(ui.Items) - 1
	case kEnter:
		ui.Done = true
	case kEsc, kCtrlC:
		ui.Quit = true
	case kRune:
		switch {
		case r >= '1' && r <= '9':
			ui.setTag(handoverTag(r - '0'))
		case r == 'x' || r == 'X' || r == ' ':
			ui.setTag(tagIndependent)
		case r == '-':
			ui.setTag(tagExcluded)
		case r == 'j':
			ui.Cursor++
		case r == 'k':
			ui.Cursor--
		case r == 'q' || r == 'Q':
			ui.Quit = true
		}
	}
	ui.clampPick()
	return ui
}

func (ui *handoverPickUI) setTag(tag handoverTag) {
	if ui.Cursor >= 0 && ui.Cursor < len(ui.Tags) {
		ui.Tags[ui.Cursor] = tag
	}
}

func (ui *handoverPickUI) clampPick() {
	if ui.Cursor >= len(ui.Items) {
		ui.Cursor = len(ui.Items) - 1
	}
	if ui.Cursor < 0 {
		ui.Cursor = 0
	}
	if ui.Height <= 0 {
		return
	}
	if ui.Cursor < ui.Top {
		ui.Top = ui.Cursor
	}
	if ui.Cursor >= ui.Top+ui.Height {
		ui.Top = ui.Cursor - ui.Height + 1
	}
	if ui.Top < 0 {
		ui.Top = 0
	}
}

// buildGroups collapses the picker assignment into output groups, in item order:
// each independent session is its own solo-N group; sessions sharing a digit merge
// into a g<digit> group (created on first touch); excluded sessions are dropped.
func buildGroups(items []handoverItem, tags []handoverTag) []handoverGroup {
	var groups []handoverGroup
	byDigit := map[handoverTag]int{} // digit → index into groups
	solo := 0
	for i, it := range items {
		tag := tagIndependent
		if i < len(tags) {
			tag = tags[i]
		}
		switch {
		case tag == tagExcluded:
			continue
		case tag == tagIndependent:
			solo++
			groups = append(groups, handoverGroup{GroupID: fmt.Sprintf("solo-%d", solo), Sessions: []handoverItem{it}})
		default:
			if gi, ok := byDigit[tag]; ok {
				groups[gi].Sessions = append(groups[gi].Sessions, it)
			} else {
				byDigit[tag] = len(groups)
				groups = append(groups, handoverGroup{GroupID: fmt.Sprintf("g%d", int(tag)), Sessions: []handoverItem{it}})
			}
		}
	}
	return groups
}
```

Add `import "fmt"` to `handover_picker.go`.

- [ ] **Step 4: Run to verify pass** — `go test ./... -run Handover -v` → PASS.
- [ ] **Step 5: Commit** — `git add handover_picker.go handover_test.go && git commit -m "feat(handover): grouping-picker reducer + group collapse"`

---

### Task 3: Manifest builder (pure + I/O)

**Files:**
- Modify: `handover.go`
- Test: `handover_test.go`

**Interfaces:**
- Consumes: `handoverItem`, `handoverGroup` (Tasks 1–2); `sessionLink`, `extractLinks` (preview.go).
- Produces: manifest structs; `func manifestSessionFrom(it handoverItem, now int64, links []sessionLink) manifestSession`; `func buildManifest(groups []handoverGroup, vaultDir, date string, now int64, linksOf func(string) []sessionLink) handoverManifest`; `func handoverVaultDir(getenv func(string) string, now int64, loc *time.Location) string`; `func writeManifestTemp(m handoverManifest) (string, error)`.

```go
type manifestSession struct {
	SessionID        string   `json:"sessionId"`
	Agent            string   `json:"agent"`
	Cwd              string   `json:"cwd"`
	Repo             string   `json:"repo"`
	Title            string   `json:"title"`
	State            string   `json:"state"` // live|ended
	LastActivity     string   `json:"lastActivity"`
	Tokens           int64    `json:"tokens"`
	TranscriptPath   string   `json:"transcriptPath"`
	TrailUrls        []string `json:"trailUrls"`
	PrUrls           []string `json:"prUrls"`
	EntireSessionIds []string `json:"entireSessionIds"`
}

type manifestGroup struct {
	GroupID  string            `json:"groupId"`
	Sessions []manifestSession `json:"sessions"`
}

type handoverManifest struct {
	Date             string          `json:"date"`
	GeneratedAt      string          `json:"generatedAt"`
	VaultHandoverDir string          `json:"vaultHandoverDir"`
	Groups           []manifestGroup `json:"groups"`
}
```

- [ ] **Step 1: Write failing tests**

```go
func TestManifestSessionFrom(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, loc).Unix()
	it := handoverItem{SessionID: "a", Repo: "o/r", Cwd: "/c", Title: "work", Live: true, LastActivity: now, Tokens: 1000, Path: "/p/a.jsonl"}
	links := []sessionLink{
		{Kind: "trail", URL: "https://entire.io/gh/o/r/trails/t1/"},
		{Kind: "PR", URL: "https://github.com/o/r/pull/9"},
	}
	ms := manifestSessionFrom(it, now, links)
	if ms.State != "live" || ms.Agent != "claude" {
		t.Fatalf("state/agent = %q/%q", ms.State, ms.Agent)
	}
	if len(ms.TrailUrls) != 1 || ms.TrailUrls[0] != "https://entire.io/gh/o/r/trails/t1/" {
		t.Fatalf("trails = %v", ms.TrailUrls)
	}
	if len(ms.PrUrls) != 1 || ms.PrUrls[0] != "https://github.com/o/r/pull/9" {
		t.Fatalf("prs = %v", ms.PrUrls)
	}
	if ms.EntireSessionIds == nil {
		t.Fatal("EntireSessionIds must be non-nil (empty slice) for stable JSON")
	}
}

func TestBuildManifestGroupsThrough(t *testing.T) {
	groups := []handoverGroup{
		{GroupID: "g1", Sessions: []handoverItem{{SessionID: "a", Path: "/p/a"}, {SessionID: "b", Path: "/p/b"}}},
	}
	m := buildManifest(groups, "/vault/Handover/2026-07-17", "2026-07-17", 0,
		func(string) []sessionLink { return nil })
	if len(m.Groups) != 1 || len(m.Groups[0].Sessions) != 2 || m.Groups[0].GroupID != "g1" {
		t.Fatalf("groups = %+v", m.Groups)
	}
	if m.VaultHandoverDir != "/vault/Handover/2026-07-17" || m.Date != "2026-07-17" {
		t.Fatalf("header = %+v", m)
	}
}

func TestHandoverVaultDirDefaultAndEnv(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, loc).Unix()
	def := handoverVaultDir(func(string) string { return "" }, now, loc)
	if def != "/Users/dvydra/Library/Mobile Documents/iCloud~md~obsidian/Documents/Handover/2026-07-17" {
		t.Fatalf("default = %q", def)
	}
	env := handoverVaultDir(func(k string) string {
		if k == "ENTIRE_TAIL_HANDOVER_VAULT" {
			return "/tmp/v"
		}
		return ""
	}, now, loc)
	if env != "/tmp/v/Handover/2026-07-17" {
		t.Fatalf("env = %q", env)
	}
}
```

- [ ] **Step 2: Run to verify fail** — `go test ./... -run 'Manifest|VaultDir' -v` → FAIL (undefined).

- [ ] **Step 3: Implement** (in `handover.go`; add imports `encoding/json`, `os`)

```go
const defaultHandoverVault = "/Users/dvydra/Library/Mobile Documents/iCloud~md~obsidian/Documents"

func handoverVaultDir(getenv func(string) string, now int64, loc *time.Location) string {
	root := firstNonEmpty(getenv("ENTIRE_TAIL_HANDOVER_VAULT"), defaultHandoverVault)
	date := time.Unix(now, 0).In(loc).Format("2006-01-02")
	return filepath.Join(root, "Handover", date)
}

func manifestSessionFrom(it handoverItem, now int64, links []sessionLink) manifestSession {
	state := "ended"
	if it.Live {
		state = "live"
	}
	trails, prs := []string{}, []string{}
	for _, ln := range links {
		if ln.Kind == "trail" {
			trails = append(trails, ln.URL)
		} else {
			prs = append(prs, ln.URL)
		}
	}
	return manifestSession{
		SessionID:        it.SessionID,
		Agent:            "claude",
		Cwd:              it.Cwd,
		Repo:             it.Repo,
		Title:            it.Title,
		State:            state,
		LastActivity:     time.Unix(it.LastActivity, 0).Format(time.RFC3339),
		Tokens:           it.Tokens,
		TranscriptPath:   it.Path,
		TrailUrls:        trails,
		PrUrls:           prs,
		EntireSessionIds: []string{},
	}
}

// buildManifest assembles the manifest; linksOf is injected so it's pure/testable
// (production passes extractLinks). generatedAt is left to the caller via now.
func buildManifest(groups []handoverGroup, vaultDir, date string, now int64, linksOf func(string) []sessionLink) handoverManifest {
	m := handoverManifest{
		Date:             date,
		GeneratedAt:      time.Unix(now, 0).Format(time.RFC3339),
		VaultHandoverDir: vaultDir,
	}
	for _, g := range groups {
		mg := manifestGroup{GroupID: g.GroupID}
		for _, it := range g.Sessions {
			mg.Sessions = append(mg.Sessions, manifestSessionFrom(it, now, linksOf(it.Path)))
		}
		m.Groups = append(m.Groups, mg)
	}
	return m
}

func writeManifestTemp(m handoverManifest) (string, error) {
	f, err := os.CreateTemp("", "entire-tail-handover-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return "", err
	}
	return f.Name(), nil
}
```

- [ ] **Step 4: Run to verify pass** — `go test ./... -run 'Manifest|VaultDir' -v` → PASS.
- [ ] **Step 5: Commit** — `git add handover.go handover_test.go && git commit -m "feat(handover): manifest builder"`

---

### Task 4: Picker render + driver

**Files:**
- Modify: `handover_picker.go`
- Test: `handover_test.go`

**Interfaces:**
- Consumes: `handoverPickUI` (Task 2); `Theme`, `tierColor`, `truncateRunes`, `reset`, `relAge`, `formatTokens`, `shortID`, `tildify` (render/theme helpers); `setRaw`, `restoreCbreak`, `termSize`, `decodeKey` (tree.go driver).
- Produces: `func renderHandoverPick(ui handoverPickUI) string`; `func rowTag(tag handoverTag) string`; `func runHandoverPicker(items []handoverItem, home string, theme Theme) ([]handoverGroup, bool)`.

- [ ] **Step 1: Write failing test** (pure render only)

```go
func TestRenderHandoverPickShowsTags(t *testing.T) {
	items := []handoverItem{
		{SessionID: "aaaaaaaa1", Repo: "o/a", Title: "first", LastActivity: 1000},
		{SessionID: "bbbbbbbb2", Repo: "o/b", Title: "second", LastActivity: 1000},
	}
	ui := handoverPickUI{Items: items, Tags: []handoverTag{2, tagExcluded}, Cursor: 0, Width: 100, Height: 10}
	out := renderHandoverPick(ui)
	if !strings.Contains(out, "[2]") {
		t.Fatalf("expected group tag [2] in:\n%s", out)
	}
	if !strings.Contains(out, "[ ]") {
		t.Fatalf("expected excluded tag [ ] in:\n%s", out)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("expected titles in:\n%s", out)
	}
}

func TestRowTag(t *testing.T) {
	if rowTag(tagIndependent) != "[x]" || rowTag(tagExcluded) != "[ ]" || rowTag(3) != "[3]" {
		t.Fatalf("rowTag mapping wrong: %q %q %q", rowTag(tagIndependent), rowTag(tagExcluded), rowTag(3))
	}
}
```

- [ ] **Step 2: Run to verify fail** — `go test ./... -run 'RenderHandoverPick|RowTag' -v` → FAIL.

- [ ] **Step 3: Implement render (pure) + driver (imperative)**

```go
func rowTag(tag handoverTag) string {
	switch {
	case tag == tagExcluded:
		return "[ ]"
	case tag == tagIndependent:
		return "[x]"
	default:
		return fmt.Sprintf("[%d]", int(tag))
	}
}

func handoverHeader() string {
	return "  HANDOVER — today's sessions   1-9 group · x separate · - skip · ⏎ write · q abort"
}

func renderHandoverPick(ui handoverPickUI) string {
	var b strings.Builder
	b.WriteString("\x1b[H")
	b.WriteString("\x1b[1m" + truncateRunes(handoverHeader(), ui.Width) + reset + "\x1b[K\n")
	end := ui.Top + ui.Height
	if end > len(ui.Items) {
		end = len(ui.Items)
	}
	shown := 0
	for i := ui.Top; i < end; i++ {
		it := ui.Items[i]
		tag := tagIndependent
		if i < len(ui.Tags) {
			tag = ui.Tags[i]
		}
		text := fmt.Sprintf("%s %-8s %-22s %-7s %6s  %s",
			rowTag(tag), shortID(it.SessionID), it.Repo, relAge(it.LastActivity, timeNowForRel(ui)), formatTokens(it.Tokens), it.Title)
		prefix := "  "
		if i == ui.Cursor {
			prefix = "❯ "
		}
		line := truncateRunes(prefix+text, ui.Width)
		if i == ui.Cursor {
			b.WriteString("\x1b[7m" + line + reset + "\x1b[K\n")
		} else {
			b.WriteString(line + "\x1b[K\n")
		}
		shown++
	}
	for ; shown < ui.Height; shown++ {
		b.WriteString("\x1b[K\n")
	}
	b.WriteString("\x1b[J")
	return b.String()
}
```

Note: `relAge` needs a reference "now". The picker doesn't track it separately, so pass the row's age relative to build time. Simplify — drop the age column to avoid threading `now`: replace the format string with one that omits `relAge`:

```go
	text := fmt.Sprintf("%s %-8s %-24s %6s  %s",
		rowTag(tag), shortID(it.SessionID), it.Repo, formatTokens(it.Tokens), it.Title)
```

(Delete the `timeNowForRel` reference — it does not exist; the age column is not worth threading a clock through the pure renderer.)

Driver (mirrors `runTreeTUI`, not unit-tested):

```go
func runHandoverPicker(items []handoverItem, home string, theme Theme) ([]handoverGroup, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, false
	}
	defer tty.Close()
	saved, ok := setRaw(tty)
	if !ok {
		return nil, false
	}
	defer restoreCbreak(tty, saved)
	if _, err := io.WriteString(tty, "\x1b[?1049h\x1b[?25l"); err != nil {
		return nil, false
	}
	defer io.WriteString(tty, "\x1b[?25h\x1b[?1049l")

	ui := handoverPickUI{Items: items, Tags: allIndependent(len(items))}
	buf := make([]byte, 16)
	for {
		w, h := termSize(tty)
		ui.Width, ui.Height = w, h-1
		if ui.Height < 1 {
			ui.Height = 1
		}
		ui.clampPick()
		io.WriteString(tty, renderHandoverPick(ui))
		n, err := tty.Read(buf)
		if err != nil || n == 0 {
			return nil, false
		}
		k, r := decodeKey(buf[:n])
		ui = updateHandoverPick(ui, k, r)
		if ui.Quit {
			return nil, false
		}
		if ui.Done {
			return buildGroups(ui.Items, ui.Tags), true
		}
	}
}
```

Add imports to `handover_picker.go`: `io`, `os`, `strings`.

- [ ] **Step 4: Run to verify pass** — `go test ./... -run 'RenderHandoverPick|RowTag' -v` → PASS. Then full suite `go vet ./... && go test -race ./...` → PASS.
- [ ] **Step 5: Manual smoke** — deferred to Task 5 (needs the subcommand entry).
- [ ] **Step 6: Commit** — `git add handover_picker.go handover_test.go && git commit -m "feat(handover): grouping-picker render + driver"`

---

### Task 5: Subcommand wiring + orchestration + launch

**Files:**
- Modify: `config.go` (add `ActionHandover`, detect the `handover` subcommand)
- Modify: `main.go` (dispatch → `runHandover`)
- Modify: `handover.go` (`todaysSessions`, `runHandover`, launch)
- Test: manual (driver + exec).

**Interfaces:**
- Consumes: `Config`, `Action`, `parseCLI` (config.go); `buildClaudeTree`, `claudeLiveCwds` (tree.go); `flattenToday`, `buildManifest`, `writeManifestTemp`, `handoverVaultDir` (Tasks 1,3); `runHandoverPicker` (Task 4); `extractLinks` (preview.go); `itermAvailable`, `osaRun`, `asEscape`, `shQuote`, `selfPath` (iterm.go); `ttyUsable`, `mustLoadTheme`, `mustHome`, `mustGetwd`, `die` (main.go).
- Produces: `func runHandover(cfg Config)`; `func todaysSessions(home string, now int64, loc *time.Location) []handoverItem`; `func handoverScript(cwd, manifestPath string) string`.

- [ ] **Step 1: Wire the subcommand** — in `config.go`, add to the `Action` const block:

```go
	ActionHandover // `entire-tail handover`: generate session handover docs
```

At the top of `parseCLI`, before the flag loop, detect the subcommand:

```go
	if len(args) > 0 && args[0] == "handover" {
		return c, ActionHandover, nil
	}
```

- [ ] **Step 2: Dispatch in `main.go`** — add a case to the `switch action` block in `main()`:

```go
	case ActionHandover:
		runHandover(cfg)
		return
```

- [ ] **Step 3: Implement orchestration** (in `handover.go`; add imports `bufio`, `fmt`, `os`, `strings`, `time`)

```go
// todaysSessions enumerates this machine's Claude sessions with activity since
// local midnight (a 2-day crawl window is cheap and safely spans midnight).
func todaysSessions(home string, now int64, loc *time.Location) []handoverItem {
	tree := buildClaudeTree(home, "", 2, now, claudeLiveCwds())
	return flattenToday(tree, localMidnight(now, loc), home)
}

func runHandover(cfg Config) {
	home := firstNonEmpty(os.Getenv("HOME"), mustHome())
	pwd := firstNonEmpty(os.Getenv("PWD"), mustGetwd())
	now := time.Now().Unix()
	loc := time.Local

	items := todaysSessions(home, now, loc)
	if len(items) == 0 {
		fmt.Fprintln(os.Stderr, "entire-tail: no Claude sessions with activity today.")
		return
	}

	var groups []handoverGroup
	if ttyUsable() {
		theme := mustLoadTheme(cfg)
		g, ok := runHandoverPicker(items, home, theme)
		if !ok {
			fmt.Fprintln(os.Stderr, "entire-tail: handover aborted.")
			return
		}
		groups = g
	} else {
		printHandoverList(items)
		if !confirmYN("Write handover docs for these " + fmt.Sprint(len(items)) + " sessions?") {
			return
		}
		groups = buildGroups(items, allIndependent(len(items)))
	}
	if len(groups) == 0 {
		fmt.Fprintln(os.Stderr, "entire-tail: nothing selected.")
		return
	}

	date := time.Unix(now, 0).In(loc).Format("2006-01-02")
	vaultDir := handoverVaultDir(os.Getenv, now, loc)
	manifest := buildManifest(groups, vaultDir, date, now, extractLinks)
	path, err := writeManifestTemp(manifest)
	if err != nil {
		die("cannot write manifest: " + err.Error())
	}

	if itermAvailable() {
		if err := osaRun(handoverScript(pwd, path)); err != nil {
			fmt.Fprintln(os.Stderr, "entire-tail: "+err.Error())
			printHandoverCmd(path)
		}
		return
	}
	printHandoverCmd(path)
}

func handoverPrompt(manifestPath string) string {
	return "Use the handover-sessions skill to write today's handover docs. Manifest JSON: " + manifestPath
}

// handoverScript opens a fresh iTerm window running an interactive claude with the
// handover prompt preloaded, cd'd to cwd.
func handoverScript(cwd, manifestPath string) string {
	a := "cd " + shQuote(cwd) + " && claude " + shQuote(handoverPrompt(manifestPath))
	return fmt.Sprintf(`tell application "iTerm2"
	create window with default profile
	tell current window
		tell current session to write text "%s"
	end tell
end tell`, asEscape(a))
}

func printHandoverCmd(manifestPath string) {
	fmt.Fprintln(os.Stderr, "entire-tail: run this to generate the docs:")
	fmt.Fprintf(os.Stderr, "  claude %q\n", handoverPrompt(manifestPath))
}

func printHandoverList(items []handoverItem) {
	fmt.Fprintf(os.Stderr, "Found %d Claude sessions from today:\n", len(items))
	for _, it := range items {
		state := "ended"
		if it.Live {
			state = "live"
		}
		fmt.Fprintf(os.Stderr, "  %-8s %-24s %-6s %s\n", shortID(it.SessionID), it.Repo, state, it.Title)
	}
}

func confirmYN(prompt string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(sc.Text()))
	return a == "y" || a == "yes"
}
```

- [ ] **Step 4: Build + verify wiring** — `go build -o entire-tail .` → success. `./entire-tail handover` in a dir with today's sessions opens the picker; tag a couple with `2`, `2`, Enter → an iTerm window opens running `claude "Use the handover-sessions skill…"`. Abort path (`q`) prints "handover aborted."
- [ ] **Step 5: Full suite** — `go vet ./... && go test -race ./...` → PASS.
- [ ] **Step 6: Commit** — `git add config.go main.go handover.go && git commit -m "feat(handover): entire-tail handover subcommand + launch"`

---

### Task 6: The `handover-sessions` skill

**Files:**
- Create: `~/.claude/skills/handover-sessions/SKILL.md`

- [ ] **Step 1: Write the skill** — frontmatter + the prompt-engineered instructions from the spec's "Skill prompt" section, with the manifest-path argument, the per-group doc procedure (summary, sessions, entire sessions, trails & PRs with live `gh`/`entire` fetch, Linear issues via MCP, ADRs/artifacts, reconciliation), the "never invent state" rule, `[[wikilinks]]`, and the doc template. Filename `<repo-basename>--<work-slug>.md`, one per group, overwrite.

```markdown
---
name: handover-sessions
description: Generate Obsidian handover docs from an entire-tail session manifest. Use when invoked by `entire-tail handover` with a manifest path, or asked to write session handover docs.
---

# Handover Sessions

You are given a manifest of today's coding-agent sessions (a JSON path in the
invocation). Read it first with the Read tool.

Write **exactly one Markdown file per `groups[]` entry** — the user already chose
the grouping; never split or merge groups. ... (full body per the spec's Skill
prompt section, including the doc template and reconciliation rules).
```

- [ ] **Step 2: Verify discovery** — `claude "Use the handover-sessions skill…"` (or `/handover-sessions`) resolves the skill. Manual: run the launch, confirm one doc per group appears under the vault's `Handover/<today>/` with the required sections and live-fetched states.
- [ ] **Step 3: Commit** — the skill lives outside the repo (`~/.claude/skills/`); note it in the PR description and (optionally) vendor a copy under `docs/` for reference. `git add docs/handover-sessions-skill.md` if vendored.

---

### Task 7: Docs

**Files:**
- Modify: `CLAUDE.md` (architecture: add `handover.go` / `handover_picker.go` bullets + the subcommand)
- Modify: `main.go` `helpText()` (document `entire-tail handover`)
- Modify: `README.md` (user-facing: the handover subcommand, vault env var)

- [ ] **Step 1** — Add `handover.go`/`handover_picker.go` to the architecture file list in `CLAUDE.md`, and a "Handover" subsection describing the flow (enumerate today → grouping-picker → manifest → launch claude + skill) and the `ENTIRE_TAIL_HANDOVER_VAULT` env var.
- [ ] **Step 2** — Add a `handover` entry to `helpText()` usage/examples and the env var to the ENVIRONMENT section.
- [ ] **Step 3** — README section mirroring the help.
- [ ] **Step 4: Commit** — `git add CLAUDE.md main.go README.md && git commit -m "docs(handover): document the handover subcommand"`

---

## Self-Review

**Spec coverage:** Trigger/flow (Tasks 5–6) ✓ · manifest incl. link seeds (Tasks 1,3) ✓ · live/ended (Task 1 via `buildClaudeTree`) ✓ · grouping-picker with 1-9/x/- (Tasks 2,4) ✓ · one doc per group, no Claude grouping (Tasks 3,6) ✓ · live enrichment + reconciliation (Task 6 skill) ✓ · vault path + env override (Task 3) ✓ · idempotent overwrite (Task 6 skill) ✓ · Claude-only/today assumptions (Task 1) ✓ · testing convention (Tasks 1–4) ✓.

**Open item carried from spec:** exact `entire` CLI command for a Trail's current state — resolve while writing Task 6; if none, the skill falls back to the PR/Linear state and notes the Trail as "state unknown".

**Type consistency:** `handoverItem`, `handoverTag`, `handoverPickUI`, `handoverGroup`, `manifestSession/Group`, `handoverManifest` used identically across tasks; `buildGroups`/`buildManifest`/`runHandoverPicker` signatures match their consumers.

**Placeholder note:** Task 6's skill body is summarized here but its full text is the spec's "Skill prompt" section verbatim — copy it in, not a paraphrase.
