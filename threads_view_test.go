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

func TestUpdateThreadsParkNeedsReason(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'p')
	if ui.Mode != "park" {
		t.Fatalf("mode %q", ui.Mode)
	}
	ui, a := press(ui, kEnter, 0)
	if a.Kind != "" || ui.Mode != "park" || ui.Note != "a reason is required" {
		t.Fatalf("got %+v %+v", ui, a)
	}
	ui, _ = press(ui, kEsc, 0)
	if ui.Mode != "list" {
		t.Fatalf("esc should cancel, mode %q", ui.Mode)
	}
}

func TestUpdateThreadsParkEmits(t *testing.T) {
	ui, _ := press(tvUI(), kRune, 'p')
	for _, r := range "soon" {
		ui, _ = press(ui, kRune, r)
	}
	ui, a := press(ui, kEnter, 0)
	if a.Kind != "park" || a.Slug != "a" || a.Reason != "soon" || ui.Mode != "list" {
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
