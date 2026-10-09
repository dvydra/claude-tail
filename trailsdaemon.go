package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const trailsAgentLabel = "io.entire.entire-tail.trails"

var errTrailsBusy = errors.New("trails collector already running")

type trailsHealth struct {
	PID      int    `json:"pid"`
	Mode     string `json:"mode"`
	LastScan int64  `json:"lastScan"`
	Request  string `json:"request"`
	Error    string `json:"error,omitempty"`
}

func acquireTrailsLock(home string) (func(), bool, error) {
	if err := os.MkdirAll(trailsDir(home), 0700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(trailsDir(home), "collector.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }) }, true, nil
}

func readTrailsHealth(home string) trailsHealth {
	var h trailsHealth
	b, err := os.ReadFile(filepath.Join(trailsDir(home), "health.json"))
	if err == nil {
		_ = json.Unmarshal(b, &h)
	}
	return h
}

func requestTrailsScan(home string) (string, error) {
	token := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	return token, writeTrailsJSON(filepath.Join(trailsDir(home), "refresh.json"), token)
}

func trailsRequest(home string) string {
	b, _ := os.ReadFile(filepath.Join(trailsDir(home), "refresh.json"))
	var token string
	_ = json.Unmarshal(b, &token)
	return token
}

func collectTrailsOnce(ctx context.Context, home string, prior trailsCatalog, d trailsScanDeps, mode string) (trailsCatalog, error) {
	request := trailsRequest(home)
	c, err := scanTrails(ctx, prior, d)
	if err == nil {
		err = saveTrails(home, c)
	}
	h := readTrailsHealth(home)
	h.PID, h.Mode = os.Getpid(), mode
	if err != nil {
		h.Error = err.Error()
	} else {
		h.LastScan, h.Request, h.Error = c.UpdatedAt, request, ""
	}
	if healthErr := writeTrailsJSON(filepath.Join(trailsDir(home), "health.json"), h); healthErr != nil {
		err = errors.Join(err, healthErr)
	}
	if err != nil {
		return prior, err
	}
	return c, nil
}

func runTrailsCollector(ctx context.Context, home string, d trailsScanDeps, mode string) error {
	release, acquired, err := acquireTrailsLock(home)
	if err != nil {
		return err
	}
	if !acquired {
		return errTrailsBusy
	}
	defer release()
	c, err := loadTrails(home)
	if err != nil {
		return fmt.Errorf("catalog preserved: %w", err)
	}
	if d.Now == nil {
		var closeDeps func()
		d, closeDeps = newTrailsScanDeps(home)
		defer closeDeps()
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var next time.Time
	var attempted string
	for {
		if ctx.Err() != nil {
			return nil
		}
		request := trailsRequest(home)
		if next.IsZero() || time.Now().After(next) || request != attempted {
			attempted = request
			c, err = collectTrailsOnce(ctx, home, c, d, mode)
			if err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "trails:", err)
			}
			next = time.Now().Add(2 * time.Second)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func trailsAgentPlist(bin, log, path string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>trails</string><string>daemon</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>10</integer>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, trailsAgentLabel, wtfPlistString(bin), wtfPlistString(path), wtfPlistString(log), wtfPlistString(log))
}

var trailsAgentLoad = launchctlLoad
var trailsAgentUnload = func(path string) error { return launchctlUnloadLabel(path, trailsAgentLabel) }
var trailsAgentWait = func(home string, since int64) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		h := readTrailsHealth(home)
		if h.Mode == "daemon" && h.LastScan >= since && h.Error == "" && pidAlive(h.PID) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func runTrailsCommand(args []string, home string, out io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(out, "entire trails: Right now and Today\n\n/ search  Enter open trail  s view session  r refresh  q quit\n\nentire trails install|uninstall|status\nBackground collection is opt-in; without it, collection runs while this view is open.")
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("trails: expected install|uninstall|status")
	}
	path := filepath.Join(home, "Library", "LaunchAgents", trailsAgentLabel+".plist")
	switch args[0] {
	case "daemon":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return runTrailsCollector(ctx, home, trailsScanDeps{}, "daemon")
	case "status":
		h := readTrailsHealth(home)
		installed := isFile(path)
		fmt.Fprintf(out, "trails: monitoring installed=%t; collector=%s; last scan=%s\n", installed, trailsHealthLabel(h), trailsScanTime(h.LastScan))
		if h.Error != "" {
			fmt.Fprintln(out, h.Error)
		}
		if c, err := loadTrails(home); err != nil {
			return err
		} else if len(c.Errors) > 0 {
			fmt.Fprintln(out, strings.Join(c.Errors, "\n"))
		}
		return nil
	case "install":
		h := readTrailsHealth(home)
		release, ok, err := acquireTrailsLock(home)
		if err != nil {
			return err
		}
		if !ok && h.Mode != "daemon" {
			return fmt.Errorf("close the foreground trails collector before installing monitoring")
		}
		if ok {
			release()
		}
		bin, err := tapAgentBinary("", exec.LookPath)
		if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(bin)
		if err != nil {
			return err
		}
		if looksEphemeralBinary(resolved) || strings.Contains(resolved, "/.amp/worktrees/") {
			return fmt.Errorf("install a stable entire-tail binary first; refusing worktree binary %s", resolved)
		}
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err = os.WriteFile(path, []byte(trailsAgentPlist(bin, filepath.Join(trailsDir(home), "daemon.log"), os.Getenv("PATH"))), 0600); err != nil {
			return err
		}
		if err = trailsAgentUnload(path); err != nil {
			return err
		}
		if err = os.Remove(filepath.Join(trailsDir(home), "health.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		since := time.Now().Unix()
		if err = trailsAgentLoad(path); err != nil {
			return err
		}
		if !trailsAgentWait(home, since) {
			return fmt.Errorf("trails agent loaded but no persisted scan yet; inspect %s", filepath.Join(trailsDir(home), "daemon.log"))
		}
		fmt.Fprintln(out, "trails: background collection running")
		return nil
	case "uninstall":
		if err := trailsAgentUnload(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(out, "trails: monitoring uninstalled; catalog preserved")
		return nil
	default:
		return fmt.Errorf("unknown trails command %q", args[0])
	}
}

func trailsHealthLabel(h trailsHealth) string {
	if h.PID <= 0 || !pidAlive(h.PID) {
		return "off"
	}
	if h.LastScan == 0 || time.Now().Unix()-h.LastScan > 30 {
		return h.Mode + " (stale or scanning)"
	}
	return h.Mode
}

func trailsScanTime(at int64) string {
	if at == 0 {
		return "never"
	}
	return time.Unix(at, 0).Local().Format("15:04:05")
}
