package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func fyiUpdate() trailUpdate {
	return trailUpdate{
		State: changeState{
			Ref:           trailRef{Kind: trailRefChange, Forge: "et", Project: "dvydra", Repo: "k3s-config", Number: 10},
			ProjectNumber: 32,
			URL:           "https://entire.io/et/dvydra/k3s-config/trails/10",
			ProjectURL:    "https://entire.io/et/dvydra/-/trails/32",
		},
		Events: []trailEvent{{Text: "CI failed", Sev: trailSevBad}},
	}
}

func TestTrailFYIText(t *testing.T) {
	got := trailFYIText(fyiUpdate())
	for _, want := range []string{"FYI from entire-tail", "no action needed", "k3s-config/10", "P32", "CI failed", "https://entire.io/et/dvydra/k3s-config/trails/10"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape sequence in %q", got)
	}
}

type fyiHarness struct {
	sessions map[string]liveSession
	sent     []string
	claimed  map[string]bool
}

func (h *fyiHarness) deps() trailFYIDeps {
	return trailFYIDeps{
		Lookup: func(id string) (liveSession, bool) { s, ok := h.sessions[id]; return s, ok },
		Send: func(_ context.Context, target wtfSession, text string) wtfDeliveryResult {
			h.sent = append(h.sent, target.ID+"|"+target.SocketPath+"|"+text)
			return wtfDeliveryResult{State: "sent"}
		},
		Claim: func(id, text string) bool {
			k := id + "|" + text
			if h.claimed[k] {
				return false
			}
			h.claimed[k] = true
			return true
		},
	}
}

func TestDeliverTrailFYI(t *testing.T) {
	h := &fyiHarness{
		sessions: map[string]liveSession{
			"busy": {SessionID: "busy", PID: 1, Status: "busy", SocketPath: "/s/busy.sock"},
			"idle": {SessionID: "idle", PID: 2, Status: "idle", SocketPath: "/s/idle.sock"},
		},
		claimed: map[string]bool{},
	}
	ctx := context.Background()
	base := fyiUpdate()
	base.Baseline = true
	cases := []struct {
		id   string
		u    trailUpdate
		want string
	}{
		{"busy", base, "baseline"},
		{"gone", fyiUpdate(), "no session"},
		{"idle", fyiUpdate(), "idle"},
		{"busy", trailUpdate{State: fyiUpdate().State}, "no events"},
		{"busy", fyiUpdate(), "sent"},
		{"busy", fyiUpdate(), "duplicate"},
	}
	for i, c := range cases {
		if got := deliverTrailFYI(ctx, h.deps(), c.id, c.u); got != c.want {
			t.Fatalf("case %d (%s): got %q want %q", i, c.id, got, c.want)
		}
	}
	if len(h.sent) != 1 || !strings.HasPrefix(h.sent[0], "busy|/s/busy.sock|FYI from entire-tail") {
		t.Fatalf("sent: %q", h.sent)
	}
}

// Two tails on one session must not both send the same FYI; a later repeat of
// the same text (CI red again after going green) is news and goes through.
func TestTrailFYIClaim(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	claim := func(at time.Time, text string) bool { return claimTrailFYI(dir, "s1", text, at) }
	if !claim(now, "a") {
		t.Fatal("first claim refused")
	}
	if claim(now.Add(time.Minute), "a") {
		t.Fatal("second tail claimed the same FYI")
	}
	if !claim(now.Add(time.Minute), "b") {
		t.Fatal("different text refused")
	}
	if !claim(now.Add(trailFYIDedupWindow+time.Second), "a") {
		t.Fatal("repeat after the window refused")
	}
	if names, _ := os.ReadDir(dir); len(names) != 2 {
		t.Fatalf("claim files: %d", len(names))
	}
}
