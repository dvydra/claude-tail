package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/term"
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

// wtSinglePane reports whether the current Windows Terminal tab/window has
// exactly one pane, so we can lay out the 3-pane workspace without disturbing an
// existing split. If the window is already split into multiple panes, returns
// false so the caller just tails in-place.
func wtSinglePane() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil && (w < 90 || h < 22) {
		return false
	}
	return true
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

// launchWTWorkspace opens a 3-pane Windows Terminal workspace to resume an existing session.
// Returns (agentCmd, inPlace, error).
func launchWTWorkspace(home, cwd, path, resumeID string) (string, bool, error) {
	agent := detectAgentForFile(home, path)
	return execWTWorkspace(cwd, resumeID, agent, false)
}

// launchWTNewWorkspace opens a 3-pane Windows Terminal workspace for a new session.
// Returns (agentCmd, inPlace, error).
func launchWTNewWorkspace(cwd string) (string, bool, error) {
	return execWTWorkspace(cwd, newSessionID(), AgentClaude, true)
}

func execWTWorkspace(cwd, sessionID string, agent Agent, isNew bool) (string, bool, error) {
	wtPath, err := findWTPath()
	if err != nil {
		return "", false, err
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

	inWT := os.Getenv("WT_SESSION") != ""

	var args []string
	if inWT {
		// Target current window (-w 0): Pane B (tail) on right, Pane C (shell) on bottom-left.
		// Current process (Pane A) stays top-left and runs agentCmd in-place!
		args = []string{
			"-w", "0", "split-pane", "-V", "-d", cwd, "cmd", "/k", tailCmd,
			";", "move-focus", "left",
			";", "split-pane", "-H", "-d", cwd,
		}
	} else {
		// New window: Pane A top-left, Pane B right, Pane C bottom-left.
		args = []string{
			"-d", cwd, "cmd", "/k", agentCmd,
			";", "split-pane", "-V", "-d", cwd, "cmd", "/k", tailCmd,
			";", "move-focus", "left",
			";", "split-pane", "-H", "-d", cwd,
		}
	}

	cmd := exec.Command(wtPath, args...)
	if err := cmd.Run(); err != nil {
		return "", false, err
	}
	return agentCmd, inWT, nil
}

func runAgentInPlace(cwd, agentCmd string) error {
	args := strings.Fields(agentCmd)
	if len(args) == 0 {
		return nil
	}
	bin, err := exec.LookPath(args[0])
	if err != nil {
		cmd := exec.Command("cmd", "/c", agentCmd)
		cmd.Dir = cwd
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	cmd := exec.Command(bin, args[1:]...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
