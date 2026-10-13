package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// layoutsnap.go is the IO half of the layout snapshot (layout.go). Every running
// tail tries a save every layoutEvery; a non-blocking flock lets one through and
// the freshness check makes the rest no-ops, so N tails cost one save per 30s.
// Every IO step is a package var so `go test` never touches iTerm or the machine.

const layoutEvery = 30 * time.Second

var (
	layoutOsa = osaOut
	layoutPS  = func() (string, error) {
		b, err := exec.Command("ps", "-A", "-o", "pid=,tty=,comm=").Output()
		return string(b), err
	}
	layoutCwd    = lsofCwd
	layoutAgents = runningAgentPanes
	layoutBoot   = bootSec
	layoutArgv   = psCommand
)

// bootSec identifies this boot. iTerm reuses small window ids across boots, so
// the boot is part of the snapshot's key.
func bootSec() int64 {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return 0
	}
	return tv.Sec
}

// saveLayoutSnapshot writes layout/<boot>/snapshot.json. It writes nothing when
// another process holds the lock, the snapshot is under layoutEvery old, iTerm
// lists no windows, or any IO step fails: on shutdown iTerm can quit before the
// tails, and an empty or partial snapshot must never replace a good one.
func saveLayoutSnapshot(home string, now time.Time) (bool, error) {
	boot := layoutBoot()
	if boot == 0 {
		return false, errors.New("boot time unavailable")
	}
	root := layoutRoot(home)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return false, err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false, nil
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	path := filepath.Join(root, strconv.FormatInt(boot, 10), "snapshot.json")
	if fi, err := os.Stat(path); err == nil && now.Sub(fi.ModTime()) < layoutEvery {
		return false, nil
	}
	out, err := layoutOsa(layoutListScript())
	if err != nil {
		return false, err
	}
	panes := parseItermPanes(out)
	if len(panes) == 0 {
		return false, nil
	}
	psOut, err := layoutPS()
	if err != nil {
		return false, err
	}
	byTTY := procsByTTY(parsePSProcs(psOut))
	cwds := map[string]string{}
	for _, p := range panes {
		if pid := paneShellPID(byTTY[p.TTY]); pid != 0 {
			if d := layoutCwd(pid); d != "" {
				cwds[p.UUID] = d
			}
		}
	}
	registry := prunePanes(readPaneRegistry(home), pidAlive)
	for _, p := range panes {
		if _, ok := registry[p.UUID]; ok {
			continue
		}
		for _, pr := range byTTY[p.TTY] {
			if procName(pr.Comm) != "entire-tail" {
				continue
			}
			if a, id := followFromArgv(layoutArgv(pr.PID)); id != "" {
				registry[p.UUID] = paneEntry{Follow: id, Agent: a, Pid: pr.PID}
			}
		}
	}
	snap := layoutSnapshot{Boot: boot, Taken: now.UTC(), Windows: buildLayout(panes, byTTY, cwds, layoutAgents(home), registry)}
	if err := writeJSONAtomic(path, snap); err != nil {
		return false, err
	}
	// The freshness check reads the mtime, so stamp the time we were given.
	_ = os.Chtimes(path, now, now)
	return true, nil
}

// startLayoutSaver saves once now and then every layoutEvery until stop is
// called. Errors are dropped: a snapshot is best-effort and must never disturb
// the tail it runs inside.
func startLayoutSaver(home string) (stop func()) {
	done := make(chan struct{})
	go func() {
		_, _ = saveLayoutSnapshot(home, time.Now())
		t := time.NewTicker(layoutEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				_, _ = saveLayoutSnapshot(home, now)
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
