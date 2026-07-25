package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// wtAvailable reports whether Windows Terminal (wt.exe) is available and running
// on Windows.
func wtAvailable() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if os.Getenv("WT_SESSION") != "" {
		return true
	}
	_, err := findWTPath()
	return err == nil
}

func findWTPath() (string, error) {
	if p, err := exec.LookPath("wt.exe"); err == nil {
		return p, nil
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData != "" {
		wtApp := filepath.Join(localAppData, "Microsoft", "WindowsApps", "wt.exe")
		if isFile(wtApp) {
			return wtApp, nil
		}
	}
	return "", fmt.Errorf("wt.exe not found on PATH")
}

// launchWTWorkspace opens a 3-pane Windows Terminal workspace to resume an existing session:
// Left-top (Pane A): claude --resume <id> or agy / codex
// Right-full (Pane B): entire-tail --follow-session <id>
// Left-bottom (Pane C): shell in cwd
func launchWTWorkspace(home, cwd, path, resumeID string) error {
	agent := detectAgentForFile(home, path)
	return execWTWorkspace(cwd, resumeID, agent, false)
}

// launchWTNewWorkspace opens a 3-pane Windows Terminal workspace for a new session:
// Left-top (Pane A): claude --session-id <id>
// Right-full (Pane B): entire-tail --follow-session <id>
// Left-bottom (Pane C): shell in cwd
func launchWTNewWorkspace(cwd string) error {
	return execWTWorkspace(cwd, newSessionID(), AgentClaude, true)
}

func execWTWorkspace(cwd, sessionID string, agent Agent, isNew bool) error {
	wtPath, err := findWTPath()
	if err != nil {
		return err
	}
	self := selfPath()

	var agentCmd string
	switch agent {
	case AgentAgy:
		agentCmd = "agy"
	case AgentCodex:
		agentCmd = "codex"
	default: // AgentClaude
		if isNew {
			agentCmd = fmt.Sprintf("claude --session-id %s", sessionID)
		} else {
			agentCmd = fmt.Sprintf("claude --resume %s", sessionID)
		}
	}

	tailCmd := fmt.Sprintf("\"%s\" --follow-session %s", self, sessionID)

	args := []string{
		"-d", cwd, "cmd", "/k", agentCmd,
		";", "split-pane", "-V", "-d", cwd, "cmd", "/k", tailCmd,
		";", "move-focus", "left",
		";", "split-pane", "-H", "-d", cwd,
	}

	cmd := exec.Command(wtPath, args...)
	return cmd.Start()
}
