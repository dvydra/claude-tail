package main

import (
	"strings"
	"testing"
	"time"
)

func TestWarningTextDuplicateClaim(t *testing.T) {
	at := time.Date(2026, 9, 29, 10, 14, 0, 0, time.Local).Unix()
	state := newWTFState(at)
	state.Sessions["claude:owner-id"] = wtfSession{Agent: AgentClaude, ID: "owner-id", Name: "api-refactor-a1", Cwd: "~/src/entiredb/.amp/worktrees/api-1223"}
	state.Sessions["claude:challenger-id"] = wtfSession{Agent: AgentClaude, ID: "challenger-id", Cwd: "~/src/entiredb/.amp/worktrees/checkpoint-fix", Active: true}
	state.Trails["entiredb#1223"] = wtfTrail{
		Key: "entiredb#1223", OwnerSession: "claude:owner-id", CanonicalWorktree: "~/src/entiredb/.amp/worktrees/api-1223",
		FirstClaim: &wtfClaim{SessionKey: "claude:owner-id", Worktree: "~/src/entiredb/.amp/worktrees/api-1223", At: at},
	}
	state.Worktrees["~/src/entiredb/.amp/worktrees/api-1223"] = wtfWorktree{DirtyFiles: 3, UnmergedCommits: 2, DefaultBranch: "origin/main"}
	finding := wtfFinding{ID: "f1", Kind: "duplicate-claim", TrailKey: "entiredb#1223", Owner: "claude:owner-id", Challenger: "claude:challenger-id", Active: true}

	want := "entire wtf found a conflict for entiredb#1223.\n" +
		"It is already owned by Claude session api-refactor-a1 in ~/src/entiredb/.amp/worktrees/api-1223.\n" +
		"This session is in ~/src/entiredb/.amp/worktrees/checkpoint-fix. Stop before changing this trail or either worktree and check with Daniel.\n" +
		"Evidence: first claim at 10:14; canonical worktree has 3 dirty files and 2 commits not on origin/main."
	if got := warningText(state, finding); got != want {
		t.Fatalf("warning text:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestWarningTextFallbackLabelsAndSafetyInstruction(t *testing.T) {
	state := newWTFState(1)
	state.Sessions["amp:T-123456789"] = wtfSession{Agent: AgentAmp, ID: "T-123456789", Cwd: "/tmp/owner"}
	state.Sessions["claude:abcdefghijk"] = wtfSession{Agent: AgentClaude, ID: "abcdefghijk", Cwd: "/tmp/challenger"}
	state.Trails["o/r#1"] = wtfTrail{Key: "o/r#1", OwnerSession: "amp:T-123456789", CanonicalWorktree: "/tmp/owner", FirstClaim: &wtfClaim{At: 1}}
	state.Worktrees["/tmp/owner"] = wtfWorktree{DefaultBranch: "origin/main"}

	got := warningText(state, wtfFinding{TrailKey: "o/r#1", Owner: "amp:T-123456789", Challenger: "claude:abcdefghijk"})
	for _, want := range []string{"Amp T-12345", "Stop", "check with Daniel"} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning %q does not contain %q", got, want)
		}
	}
}

func TestPendingWTFDeliveriesActiveChallenger(t *testing.T) {
	state, finding := notificationState()
	got := pendingWTFDeliveries(state, 100)
	if len(got) != 2 {
		t.Fatalf("deliveries = %#v", got)
	}
	if got[0].FindingID != finding.ID || got[0].Channel != "mac" || got[0].Target != "" {
		t.Fatalf("first delivery = %#v", got[0])
	}
	if got[1].Channel != "session:"+finding.Challenger || got[1].Target != finding.Challenger {
		t.Fatalf("second delivery = %#v", got[1])
	}
	for _, delivery := range got {
		if !strings.Contains(delivery.Message, "Stop") || !strings.Contains(delivery.Message, "check with Daniel") {
			t.Fatalf("unsafe delivery = %#v", delivery)
		}
	}
}

func TestPendingWTFDeliveriesRequireActiveChallenger(t *testing.T) {
	state, finding := notificationState()
	finding.Challenger = ""
	state.Findings[finding.ID] = finding
	if got := pendingWTFDeliveries(state, 100); len(got) != 1 || got[0].Channel != "mac" {
		t.Fatalf("owner-only deliveries = %#v", got)
	}
	finding.Active = false
	state.Findings[finding.ID] = finding
	if got := pendingWTFDeliveries(state, 100); len(got) != 0 {
		t.Fatalf("historical deliveries = %#v", got)
	}
}

func TestPendingWTFDeliveriesTerminalStatesDoNotRetry(t *testing.T) {
	for _, terminal := range []string{"sent", "held", "refused", "unknown", "sending"} {
		t.Run(terminal, func(t *testing.T) {
			state, finding := notificationState()
			finding.Delivery = map[string]wtfDeliveryStatus{
				"mac":                           {State: terminal, Attempts: 1, LastAttempt: 1},
				"session:" + finding.Challenger: {State: terminal, Attempts: 1, LastAttempt: 1},
			}
			state.Findings[finding.ID] = finding
			if got := pendingWTFDeliveries(state, 10_000); len(got) != 0 {
				t.Fatalf("deliveries = %#v", got)
			}
		})
	}
}

func TestPendingWTFDeliveriesFailedRetrySchedule(t *testing.T) {
	delays := []int64{5, 30, 120, 600, 3600, 3600}
	for attempts, delay := range delays {
		state, finding := notificationState()
		finding.Delivery = map[string]wtfDeliveryStatus{
			"mac":                           {State: "sent"},
			"session:" + finding.Challenger: {State: "failed", Attempts: attempts + 1, LastAttempt: 100},
		}
		state.Findings[finding.ID] = finding
		if got := pendingWTFDeliveries(state, 100+delay-1); len(got) != 0 {
			t.Fatalf("attempt %d retried early: %#v", attempts+1, got)
		}
		if got := pendingWTFDeliveries(state, 100+delay); len(got) != 1 || got[0].Channel != "session:"+finding.Challenger {
			t.Fatalf("attempt %d due deliveries = %#v", attempts+1, got)
		}
	}
}

func TestPendingWTFDeliveriesRecurrenceStartsPending(t *testing.T) {
	state, finding := notificationState()
	finding.Delivery = map[string]wtfDeliveryStatus{"mac": {State: "sent"}, "session:" + finding.Challenger: {State: "refused"}}
	prior := map[string]wtfFinding{finding.ID: finding}
	finding.Delivery = nil
	finding.Active = true
	cleared := mergeWTFFindings(prior, nil, 110)
	state.Findings = mergeWTFFindings(cleared, map[string]wtfFinding{finding.ID: finding}, 120)
	if got := pendingWTFDeliveries(state, 120); len(got) != 2 {
		t.Fatalf("recurrence deliveries = %#v", got)
	}
}

func TestWTFDeliveryStateTransitions(t *testing.T) {
	state, finding := notificationState()
	delivery := pendingWTFDeliveries(state, 100)[0]
	markWTFDeliveryStarted(&state, delivery, 100)
	status := state.Findings[finding.ID].Delivery["mac"]
	if status.State != "sending" || status.Attempts != 1 || status.LastAttempt != 100 {
		t.Fatalf("started status = %#v", status)
	}
	applyWTFDeliveryResult(&state, wtfDeliveryResult{FindingID: finding.ID, Channel: "mac", State: "failed", Error: "offline"}, 101)
	status = state.Findings[finding.ID].Delivery["mac"]
	if status.State != "failed" || status.Attempts != 1 || status.LastAttempt != 100 || status.LastError != "offline" {
		t.Fatalf("failed status = %#v", status)
	}
}

func TestWTFDeliveryCrashRecoveryMarksSendingUnknown(t *testing.T) {
	home := t.TempDir()
	state, finding := notificationState()
	finding.Delivery = map[string]wtfDeliveryStatus{"mac": {State: "sending", Attempts: 2, LastAttempt: 90, LastError: "old"}}
	state.Findings[finding.ID] = finding
	if err := saveWTFState(home, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadWTFState(home, 100)
	if err != nil {
		t.Fatal(err)
	}
	status := loaded.Findings[finding.ID].Delivery["mac"]
	if status.State != "unknown" || status.Attempts != 2 || status.LastAttempt != 90 || status.LastError != "old" {
		t.Fatalf("recovered status = %#v", status)
	}
	if got := pendingWTFDeliveries(loaded, 10_000); len(got) != 1 || got[0].Channel != "session:"+finding.Challenger {
		t.Fatalf("crash recovery retried unknown channel: %#v", got)
	}
}

func notificationState() (wtfState, wtfFinding) {
	state := newWTFState(100)
	state.Sessions["claude:owner"] = wtfSession{Agent: AgentClaude, ID: "owner", Name: "owner", Cwd: "/wt/owner", Active: true}
	state.Sessions["amp:challenger"] = wtfSession{Agent: AgentAmp, ID: "challenger", Name: "challenger", Cwd: "/wt/challenger", Active: true}
	state.Trails["o/r#1"] = wtfTrail{Key: "o/r#1", OwnerSession: "claude:owner", CanonicalWorktree: "/wt/owner", FirstClaim: &wtfClaim{At: 50}}
	state.Worktrees["/wt/owner"] = wtfWorktree{DirtyFiles: 1, UnmergedCommits: 2, DefaultBranch: "origin/main"}
	finding := wtfFinding{ID: "finding-1", Kind: "duplicate-claim", TrailKey: "o/r#1", Owner: "claude:owner", Challenger: "amp:challenger", Active: true, Occurrence: 1}
	state.Findings[finding.ID] = finding
	return state, finding
}
