package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

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

func collectWTFSessions(home string, now int64, loc *time.Location, deps wtfInventoryDeps) []wtfSession {
	byID := map[string]wtfSession{}
	for _, item := range deps.Today(home, now, loc) {
		s := wtfSession{
			Agent: item.Agent, ID: item.SessionID, Name: item.Title, Repo: item.Repo,
			Cwd: item.Cwd, Branch: item.Branch, Transcript: item.Path,
			State: "ended", LastActivity: item.LastActivity,
		}
		byID[wtfSessionKey(s.Agent, s.ID)] = s
	}

	repoCache := map[string]string{}
	for _, live := range deps.Live(home) {
		key := wtfSessionKey(live.Agent, live.SessionID)
		s := byID[key]
		s.Agent = live.Agent
		s.ID = live.SessionID
		s.Name = live.Name
		s.Cwd = live.Cwd
		s.Branch = live.Branch
		s.Transcript = live.Path
		s.State = firstNonEmpty(live.Status, "active")
		s.Active = true
		s.StartedAt = live.StartedAt / 1000
		s.LastActivity = live.UpdatedAt / 1000
		s.SocketPath = live.SocketPath
		if s.Repo == "" && s.Cwd != "" {
			s.Repo = repoForCwd(s.Cwd, home, repoCache)
		}
		byID[key] = s
	}

	out := make([]wtfSession, 0, len(byID))
	for _, session := range byID {
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		if (out[i].State == "busy") != (out[j].State == "busy") {
			return out[i].State == "busy"
		}
		if out[i].LastActivity != out[j].LastActivity {
			return out[i].LastActivity > out[j].LastActivity
		}
		return wtfSessionKey(out[i].Agent, out[i].ID) < wtfSessionKey(out[j].Agent, out[j].ID)
	})
	return out
}

func wtfSessionKey(agent Agent, id string) string {
	return string(agent) + ":" + id
}

func runWTF(cfg Config) error {
	if len(cfg.WTFArgs) > 0 {
		return fmt.Errorf("wtf: unsupported arguments: %s", strings.Join(cfg.WTFArgs, " "))
	}

	fmt.Println("No sessions active or seen today.")
	return nil
}
