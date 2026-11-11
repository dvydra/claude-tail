package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func tvThread(slug, name string, st threadStatus) thread {
	return thread{
		Dir:      filepath.Join("/v", slug),
		Meta:     threadMeta{Name: name, Status: st, Updated: "2026-11-03", WaitingOn: "review from Sam"},
		Sections: map[string]string{"What it is": "A thing about " + name},
	}
}

func tvUI() threadsUI {
	return threadsUI{
		Threads: []thread{
			tvThread("a", "alpha", threadOpen),
			tvThread("b", "beta", threadOpen),
			func() thread {
				t := tvThread("c", "gamma", threadParked)
				t.Meta.ParkReason = "waiting for Q1"
				return t
			}(),
		},
		Facts: map[string]threadFacts{}, Width: 100, Height: 30, Today: "2026-11-04",
	}
}

func idRender(s string) (string, error) { return s, nil }

func press(ui threadsUI, k treeKey, r rune) (threadsUI, threadsAction) {
	return updateThreads(ui, k, r)
}

func TestUpdateThreadsParkDefaultsToNotToday(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'p')
	if ui.Mode != "park" || ui.ParkPick != 0 {
		t.Fatalf("mode %q pick %d", ui.Mode, ui.ParkPick)
	}
	ui, a := press(ui, kEnter, 0)
	if a.Kind != "park" || a.Slug != "a" || a.ParkKind != "not_today" || a.Reason != "" || ui.Mode != "list" {
		t.Fatalf("got %+v %+v", ui, a)
	}
}

func TestUpdateThreadsParkEscCancels(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'p')
	ui, a := press(ui, kEsc, 0)
	if ui.Mode != "list" || a.Kind != "" {
		t.Fatalf("esc should cancel: %+v %+v", ui, a)
	}
}

func TestUpdateThreadsParkUpPicksAndTypingAddsNote(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'p')
	ui, _ = press(ui, kUp, 0)
	ui, _ = press(ui, kUp, 0)
	for _, r := range "Acme SSO" {
		ui, _ = press(ui, kRune, r)
	}
	ui, _ = press(ui, kBackspace, 0)
	_, a := press(ui, kEnter, 0)
	if a.ParkKind != "customer" || a.Reason != "Acme SS" {
		t.Fatalf("got %+v", a)
	}
}

func TestUpdateThreadsParkPickClamps(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'p')
	ui, _ = press(ui, kDown, 0)
	if ui.ParkPick != 0 {
		t.Fatalf("down from the bottom: %d", ui.ParkPick)
	}
	for i := 0; i < 10; i++ {
		ui, _ = press(ui, kUp, 0)
	}
	if ui.ParkPick != len(parkKinds)-1 {
		t.Fatalf("up past the top: %d", ui.ParkPick)
	}
	// q is note text, not quit, while the picker is up.
	ui, _ = press(ui, kRune, 'q')
	if ui.Mode != "park" || ui.Input != "q" {
		t.Fatalf("q must type: %+v", ui)
	}
}

func TestRenderThreadsParkOverlay(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'p')
	ui, _ = press(ui, kUp, 0)
	out := renderThreads(ui, idRender, nil)
	rows := strings.Split(out, "\n")
	if len(rows) != ui.Height {
		t.Fatalf("rows %d, want %d", len(rows), ui.Height)
	}
	plain := stripANSI(out)
	for _, want := range []string{`park "alpha"`, "▸ waiting on review", "not today", "wakes tomorrow", "note: ▏", "someday"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in\n%s", want, plain)
		}
	}
	if !strings.Contains(out, bg256(parkKinds[1].BG)) {
		t.Error("selected row has no background colour")
	}
	for i, r := range rows {
		if visWidth(r) > ui.Width {
			t.Errorf("row %d is %d wide: %q", i, visWidth(r), stripANSI(r))
		}
	}
}

func TestRenderThreadsParkedRowShowsKind(t *testing.T) {
	ui := tvUI()
	ui.Threads[2].Meta = threadMeta{Name: "gamma", Status: threadParked, ParkKind: "not_today", WakesOn: "2026-11-05", ParkReason: "tired"}
	plain := stripANSI(renderThreads(ui, idRender, nil))
	if !strings.Contains(plain, `not today · wakes tomorrow · "tired"`) {
		t.Fatalf("parked row:\n%s", plain)
	}
}

func TestUpdateThreadsOpenAllConfirmsAndListsOpenInView(t *testing.T) {
	ui := tvUI()
	ui.Threads = append(ui.Threads, tvThread("d", "delta", threadOpen))
	ui, _ = press(ui, kRune, '/')
	for _, r := range "delt" {
		ui, _ = press(ui, kRune, r)
	}
	ui, _ = press(ui, kEnter, 0)
	ui, _ = press(ui, kRune, 'O')
	if ui.Mode != "openall" || !strings.Contains(footerFor(ui), "open 1 thread in new tabs here") {
		t.Fatalf("mode %q footer %q", ui.Mode, footerFor(ui))
	}
	_, a := press(ui, kRune, 'n')
	if a.Kind != "" {
		t.Fatal("n must cancel")
	}
	_, a = press(ui, kRune, 'y')
	if a.Kind != "openall" || fmt.Sprint(a.Slugs) != "[d]" {
		t.Fatalf("%+v", a)
	}
}

func TestUpdateThreadsOpenAllSkipsParked(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'O')
	_, a := press(ui, kRune, 'y')
	if fmt.Sprint(a.Slugs) != "[a b]" {
		t.Fatalf("slugs %v", a.Slugs)
	}
}

func TestUpdateThreadsParkEmits(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'p')
	for _, r := range "soon" {
		ui, _ = press(ui, kRune, r)
	}
	ui, a := press(ui, kEnter, 0)
	if a.Kind != "park" || a.Slug != "a" || a.Reason != "soon" || a.ParkKind != "not_today" || ui.Mode != "list" {
		t.Fatalf("got %+v %+v", ui, a)
	}
}

func TestUpdateThreadsCloseConfirm(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'x')
	if ui.Mode != "close" {
		t.Fatal(ui.Mode)
	}
	_, a := press(ui, kRune, 'n')
	if a.Kind != "" {
		t.Fatal("n must cancel")
	}
	_, a = press(ui, kRune, 'y')
	if a.Kind != "close" || a.Slug != "a" {
		t.Fatalf("%+v", a)
	}
}

func TestUpdateThreadsUnparkOnlyParked(t *testing.T) {
	_, a := press(tvUI(), kRune, 'u')
	if a.Kind != "" {
		t.Fatalf("open row must not unpark: %+v", a)
	}
	ui := tvUI()
	ui.Cursor = 2
	_, a = press(ui, kRune, 'u')
	if a.Kind != "unpark" || a.Slug != "c" {
		t.Fatalf("%+v", a)
	}
}

func TestUpdateThreadsOpenAndQuit(t *testing.T) {
	ui := tvUI()
	ui, _ = press(ui, kDown, 0)
	_, a := press(ui, kEnter, 0)
	if a.Kind != "open" || a.Slug != "b" {
		t.Fatalf("%+v", a)
	}
	_, a = press(ui, kRune, 'q')
	if a.Kind != "quit" {
		t.Fatalf("%+v", a)
	}
}

func TestUpdateThreadsFilter(t *testing.T) {
	ui, _ := press(tvUI(), kRune, '/')
	for _, r := range "GAMM" {
		ui, _ = press(ui, kRune, r)
	}
	ui, _ = press(ui, kEnter, 0)
	out := stripANSI(renderThreads(ui, idRender, nil))
	if strings.Contains(out, "alpha") || !strings.Contains(out, "gamma") {
		t.Fatalf("filter failed:\n%s", out)
	}
	ui, a := press(ui, kEsc, 0)
	if ui.Filter != "" || a.Kind != "" {
		t.Fatalf("esc should clear filter: %+v %+v", ui, a)
	}
}

func TestRenderThreadsEmpty(t *testing.T) {
	ui := threadsUI{Width: 80, Height: 20, Today: "2026-11-04"}
	out := stripANSI(renderThreads(ui, idRender, nil))
	if !strings.Contains(out, "no threads yet: run /handover in a session to start one") {
		t.Fatal(out)
	}
}

func TestRenderThreadsStaleSectionDimmed(t *testing.T) {
	ui := tvUI()
	ui.Threads[0].Meta.AsOf = map[string]string{"What it is": "2026-10-25"}
	if out := stripANSI(renderThreads(ui, idRender, nil)); !strings.Contains(out, "as of 25 Oct") {
		t.Fatalf("missing as of:\n%s", out)
	}
	ui.Threads[0].Meta.AsOf = map[string]string{"What it is": "2026-11-02"}
	if out := stripANSI(renderThreads(ui, idRender, nil)); strings.Contains(out, "as of") {
		t.Fatalf("unexpected as of:\n%s", out)
	}
}

func TestRenderThreadsNoTruncation(t *testing.T) {
	var words []string
	for i := 0; i < 600; i++ {
		words = append(words, fmt.Sprintf("w%03d", i))
	}
	ui := tvUI()
	ui.Threads[0].Sections["Where it got to"] = strings.Join(words, " ")
	seen := map[string]bool{}
	for s := 0; s < 200; s += 3 {
		ui.Scroll = s
		out := stripANSI(renderThreads(ui, idRender, nil))
		if n := strings.Count(out, "\n") + 1; n > ui.Height {
			t.Fatalf("scroll %d: %d rows > %d", s, n, ui.Height)
		}
		for _, w := range strings.Fields(out) {
			seen[w] = true
		}
	}
	for _, w := range words {
		if !seen[w] {
			t.Fatalf("word %s never visible", w)
		}
	}
}

func TestUpdateThreadsScrollCapped(t *testing.T) {
	ui := tvUI()
	for i := 0; i < 50; i++ {
		ui, _ = press(ui, kPageDown, 0)
		ui = clampThreadsScroll(ui, idRender, nil)
	}
	if ui.Scroll > 10 {
		t.Fatalf("scroll ran away: %d", ui.Scroll)
	}
	ui.Scroll = 1
	ui, _ = press(ui, kDown, 0)
	if ui.Scroll != 0 {
		t.Fatal("cursor move resets scroll")
	}
}

func TestRenderThreadsLayoutDump(t *testing.T) {
	ui := tvUI()
	ui.Facts["a"] = threadFacts{Stale: []string{"worktree gone"}}
	ui.Threads[0].Sections["Where it got to"] = "Wired the parser. Next is the view."
	t.Log("\n" + stripANSI(renderThreads(ui, idRender, nil)))
}

func TestThreadsScrollReachesEndWithExpandingRender(t *testing.T) {
	double := func(s string) (string, error) {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			out = append(out, l, l)
		}
		return strings.Join(out, "\n"), nil
	}
	var ls []string
	for i := 0; i < 60; i++ {
		ls = append(ls, fmt.Sprintf("line%03d", i))
	}
	ui := tvUI()
	ui.Threads[0].Sections["Where it got to"] = strings.Join(ls, "\n\n")
	for i := 0; i < 400; i++ {
		ui, _ = press(ui, kPageDown, 0)
		ui = clampThreadsScroll(ui, double, nil)
	}
	out := stripANSI(renderThreads(ui, double, nil))
	if !strings.Contains(out, "line059") {
		t.Fatalf("end unreachable:\n%s", out)
	}
	ui, _ = press(ui, kPageUp, 0)
	if ui.Scroll < 0 {
		t.Fatal("negative scroll")
	}
}

func TestRenderThreadsWideLineKeepsFooter(t *testing.T) {
	ui := tvUI()
	ui.Width = 80
	ui.Threads[0].Sections["Where it got to"] = "before\n\n" + strings.Repeat("x", 300) + "\n\nafter"
	for s := 0; s < 12; s++ {
		ui.Scroll = s
		out := stripANSI(renderThreads(ui, idRender, nil))
		lines := strings.Split(out, "\n")
		phys := 0
		for _, l := range lines {
			phys += physRows(l, 80)
		}
		if len(lines) > ui.Height || phys > ui.Height {
			t.Fatalf("scroll %d: %d rows, %d physical > %d", s, len(lines), phys, ui.Height)
		}
		if !strings.HasPrefix(lines[len(lines)-1], "⏎ open") {
			t.Fatalf("scroll %d: footer missing:\n%s", s, out)
		}
	}
}

func TestUpdateThreadsDetailModeIgnoresOtherKeys(t *testing.T) {
	for _, m := range []string{"details", "knowledge"} {
		ui := tvUI()
		ui.Mode = m
		for _, r := range "pxu/dk" {
			n, a := press(ui, kRune, r)
			if a.Kind != "" || n.Mode != m || n.Cursor != 0 {
				t.Fatalf("%s: %q changed state: %+v %+v", m, r, n, a)
			}
		}
		if n, _ := press(ui, kDown, 0); n.Cursor != 0 || n.Mode != m {
			t.Fatalf("%s: down moved cursor", m)
		}
		if n, _ := press(ui, kRune, 'q'); n.Mode != "list" {
			t.Fatalf("%s: q should leave", m)
		}
	}
}

func TestDimLinesReappliesDimAfterReset(t *testing.T) {
	got := dimLines("a\x1b[1mb\x1b[0mc")[0]
	if !strings.Contains(got, "\x1b[0m"+tvDim+"c") {
		t.Fatalf("dim lost after reset: %q", got)
	}
}
