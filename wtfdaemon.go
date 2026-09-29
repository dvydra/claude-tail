package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const wtfScanInterval = 2 * time.Second
const wtfRequestPollInterval = 100 * time.Millisecond

type wtfHealth struct {
	PID                int    `json:"pid"`
	Version            string `json:"version"`
	StartedAt          int64  `json:"startedAt"`
	LastAttemptedScan  int64  `json:"lastAttemptedScan,omitempty"`
	LastSuccessfulScan int64  `json:"lastSuccessfulScan,omitempty"`
	LastError          string `json:"lastError,omitempty"`
}

type wtfDaemonDeps struct {
	Scan         func(context.Context, string, wtfState, wtfScanDeps) (wtfState, error)
	Load         func(string, int64) (wtfState, error)
	Save         func(string, wtfState) error
	Now          func() time.Time
	After        func(time.Duration) <-chan time.Time
	RequestAfter func(time.Duration) <-chan time.Time
}

var (
	wtfCurrentPID   = os.Getpid
	wtfPIDAlive     = pidAlive
	wtfProcessName  = psCommand
	wtfLockSequence atomic.Uint64
)

func wtfLockPath(home string) string        { return filepath.Join(wtfDir(home), "daemon.lock") }
func wtfHealthPath(home string) string      { return filepath.Join(wtfDir(home), "health.json") }
func wtfScanRequestPath(home string) string { return filepath.Join(wtfDir(home), "scan-request") }

func acquireWTFLock(home string) (func(), bool) {
	path := wtfLockPath(home)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return func() {}, false
	}
	breakerPath := path + ".breaker"
	breaker, err := os.OpenFile(breakerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}, false
	}
	_ = breaker.Close()
	defer os.Remove(breakerPath)

	pid := wtfCurrentPID()
	identity := fmt.Sprintf("%d %d\n", pid, wtfLockSequence.Add(1))
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err = file.WriteString(identity); err == nil {
				err = file.Close()
			} else {
				_ = file.Close()
			}
			if err != nil {
				_ = os.Remove(path)
				return func() {}, false
			}
			release := func() {
				data, err := os.ReadFile(path)
				if err == nil && string(data) == identity {
					_ = os.Remove(path)
				}
			}
			return release, true
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return func() {}, false
		}
		owner, _ := strconv.Atoi(strings.Fields(string(data))[0])
		if owner > 0 && wtfPIDAlive(owner) && wtfIsEntireTailProcess(wtfProcessName(owner)) {
			return func() {}, false
		}
		if os.Remove(path) != nil {
			return func() {}, false
		}
	}
	return func() {}, false
}

func wtfIsEntireTailProcess(command string) bool {
	fields := strings.Fields(command)
	return len(fields) > 0 && filepath.Base(fields[0]) == "entire-tail"
}

func readWTFHealth(home string) (wtfHealth, bool) {
	var health wtfHealth
	data, err := os.ReadFile(wtfHealthPath(home))
	if err != nil || json.Unmarshal(data, &health) != nil {
		return wtfHealth{}, false
	}
	return health, true
}

func writeWTFHealth(home string, health wtfHealth) error {
	data, err := json.MarshalIndent(health, "", "  ")
	if err != nil {
		return err
	}
	return writeWTFAtomic(home, "health-*.tmp", wtfHealthPath(home), append(data, '\n'))
}

func wtfDaemonRunning(health wtfHealth) bool {
	return health.PID > 0 && wtfPIDAlive(health.PID) && wtfIsEntireTailProcess(wtfProcessName(health.PID))
}

func requestWTFScan(home string) error {
	path := wtfScanRequestPath(home)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeWTFAtomic(home, "scan-request-*.tmp", path, nil)
}

func writeWTFAtomic(home, pattern, destination string, data []byte) (err error) {
	dir := wtfDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(path, destination); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = dirFile.Sync()
	if closeErr := dirFile.Close(); err == nil {
		err = closeErr
	}
	return err
}

func runWTFDaemon(ctx context.Context, home string, deps wtfDaemonDeps) error {
	release, ok := acquireWTFLock(home)
	if !ok {
		return errors.New("wtf daemon already running")
	}
	defer release()
	if deps.Scan == nil {
		deps.Scan = scanWTF
	}
	if deps.Load == nil {
		deps.Load = loadWTFState
	}
	if deps.Save == nil {
		deps.Save = saveWTFState
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.After == nil {
		deps.After = time.After
	}
	if deps.RequestAfter == nil {
		deps.RequestAfter = time.After
	}

	started := deps.Now()
	health := wtfHealth{PID: wtfCurrentPID(), Version: version, StartedAt: started.Unix()}
	state, loadErr := deps.Load(home, started.Unix())
	if loadErr != nil {
		health.LastError = loadErr.Error()
	}
	if err := writeWTFHealth(home, health); err != nil {
		return err
	}

	scan := func() error {
		now := deps.Now().Unix()
		health.LastAttemptedScan = now
		if err := writeWTFHealth(home, health); err != nil {
			return err
		}
		next, scanErr := deps.Scan(ctx, home, state, defaultWTFScanDeps())
		saveErr := deps.Save(home, next)
		state = next
		if scanErr != nil || saveErr != nil {
			health.LastError = errors.Join(scanErr, saveErr).Error()
		} else {
			health.LastSuccessfulScan = now
			health.LastError = ""
		}
		if err := writeWTFHealth(home, health); err != nil {
			return err
		}
		return nil
	}
	if err := scan(); err != nil {
		return err
	}
	periodic := deps.After(wtfScanInterval)
	requestPoll := deps.RequestAfter(wtfRequestPollInterval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-periodic:
			if err := scan(); err != nil {
				return err
			}
			periodic = deps.After(wtfScanInterval)
		case <-requestPoll:
			if err := os.Remove(wtfScanRequestPath(home)); err == nil {
				if err := scan(); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			requestPoll = deps.RequestAfter(wtfRequestPollInterval)
		}
	}
}
