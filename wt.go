package main

import (
	"fmt"
	"os"
	"os/exec"
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
	_, err := exec.LookPath("wt.exe")
	return err == nil
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
	wtPath, err := exec.LookPath("wt.exe")
	if err != nil {
		return fmt.Errorf("wt.exe not found on PATH")
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

	tailCmd := fmt.Sprintf("%s --follow-session %s", self, sessionID)

	args := []string{
		"-d", cwd, "cmd", "/k", agentCmd,
		";", "split-pane", "-v", "-d", cwd, "cmd", "/k", tailCmd,
		";", "move-focus", "left",
		";", "split-pane", "-h", "-d", cwd,
	}

	cmd := exec.Command(wtPath, args...)
	return cmd.Start()
}
