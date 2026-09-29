package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

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

func warningText(state wtfState, finding wtfFinding) string {
	trail := state.Trails[finding.TrailKey]
	ownerKey := firstNonEmpty(finding.Owner, trail.OwnerSession)
	owner := state.Sessions[ownerKey]
	challenger := state.Sessions[finding.Challenger]
	ownerPath := firstNonEmpty(owner.Cwd, trail.CanonicalWorktree)
	challengerPath := challenger.Cwd
	claimAt := int64(0)
	if trail.FirstClaim != nil {
		claimAt = trail.FirstClaim.At
	}
	worktree := state.Worktrees[trail.CanonicalWorktree]
	defaultBranch := firstNonEmpty(worktree.DefaultBranch, "the default branch")

	return fmt.Sprintf("entire wtf found a conflict for %s.\n", finding.TrailKey) +
		fmt.Sprintf("It is already owned by %s in %s.\n", wtfWarningSessionLabel(owner, ownerKey), ownerPath) +
		fmt.Sprintf("This session is in %s. Stop before changing this trail or either worktree and check with Daniel.\n", challengerPath) +
		fmt.Sprintf("Evidence: first claim at %s; canonical worktree has %d dirty files and %d commits not on %s.", wtfWarningTime(claimAt), worktree.DirtyFiles, worktree.UnmergedCommits, defaultBranch)
}

func wtfWarningSessionLabel(session wtfSession, key string) string {
	agent := session.Agent
	id := session.ID
	if value, rest, ok := strings.Cut(key, ":"); ok {
		if agent == "" {
			agent = Agent(value)
		}
		if id == "" {
			id = rest
		}
	}
	prefix := "Claude"
	if agent == AgentAmp {
		prefix = "Amp"
	}
	if session.Name != "" {
		return prefix + " session " + session.Name
	}
	return prefix + " " + shortID(id)
}

func wtfWarningTime(unix int64) string {
	if unix == 0 {
		return "unknown time"
	}
	return time.Unix(unix, 0).Local().Format("15:04")
}

func pendingWTFDeliveries(state wtfState, now int64) []wtfDelivery {
	var deliveries []wtfDelivery
	for _, id := range sortedMapKeys(state.Findings) {
		finding := state.Findings[id]
		if !finding.Active {
			continue
		}
		message := warningText(state, finding)
		channels := []wtfDelivery{{FindingID: id, Channel: "mac", Message: message}}
		if challenger, ok := state.Sessions[finding.Challenger]; finding.Challenger != "" && ok && challenger.Active {
			channels = append(channels, wtfDelivery{FindingID: id, Channel: "session:" + finding.Challenger, Target: finding.Challenger, Message: message})
		}
		for _, delivery := range channels {
			if wtfDeliveryDue(finding.Delivery[delivery.Channel], now) {
				deliveries = append(deliveries, delivery)
			}
		}
	}
	sort.Slice(deliveries, func(i, j int) bool {
		if deliveries[i].FindingID != deliveries[j].FindingID {
			return deliveries[i].FindingID < deliveries[j].FindingID
		}
		return deliveries[i].Channel < deliveries[j].Channel
	})
	return deliveries
}

func wtfDeliveryDue(status wtfDeliveryStatus, now int64) bool {
	switch status.State {
	case "":
		return true
	case "failed":
		delays := [...]int64{5, 30, 120, 600, 3600}
		attempt := status.Attempts
		if attempt < 1 {
			attempt = 1
		}
		index := attempt - 1
		if index >= len(delays) {
			index = len(delays) - 1
		}
		return now >= status.LastAttempt+delays[index]
	default:
		return false
	}
}

func markWTFDeliveryStarted(state *wtfState, delivery wtfDelivery, now int64) {
	finding, ok := state.Findings[delivery.FindingID]
	if !ok {
		return
	}
	if finding.Delivery == nil {
		finding.Delivery = make(map[string]wtfDeliveryStatus)
	}
	status := finding.Delivery[delivery.Channel]
	status.State = "sending"
	status.Attempts++
	status.LastAttempt = now
	status.LastError = ""
	finding.Delivery[delivery.Channel] = status
	state.Findings[delivery.FindingID] = finding
}

func applyWTFDeliveryResult(state *wtfState, result wtfDeliveryResult, now int64) {
	_ = now
	finding, ok := state.Findings[result.FindingID]
	if !ok || finding.Delivery == nil {
		return
	}
	status, ok := finding.Delivery[result.Channel]
	if !ok || status.State != "sending" {
		return
	}
	status.State = result.State
	status.LastError = result.Error
	finding.Delivery[result.Channel] = status
	state.Findings[result.FindingID] = finding
}

func recoverWTFDeliveries(state *wtfState) {
	for id, finding := range state.Findings {
		for channel, status := range finding.Delivery {
			if status.State == "sending" {
				status.State = "unknown"
				finding.Delivery[channel] = status
			}
		}
		state.Findings[id] = finding
	}
}
