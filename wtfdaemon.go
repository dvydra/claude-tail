package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const wtfScanInterval = 2 * time.Second
const wtfRequestPollInterval = 100 * time.Millisecond
const wtfAgentLabel = "io.entire.entire-tail.wtf"

type wtfHealth struct {
	PID                int    `json:"pid"`
	Version            string `json:"version"`
	StartedAt          int64  `json:"startedAt"`
	LastAttemptedScan  int64  `json:"lastAttemptedScan,omitempty"`
	LastSuccessfulScan int64  `json:"lastSuccessfulScan,omitempty"`
	LastError          string `json:"lastError,omitempty"`
	// Degraded lists the sources the last successful scan couldn't read. The
	// scan still counts: its partial state was saved, and one stale export or
	// unreadable worktree must not make every scan look failed.
	Degraded []string `json:"degraded,omitempty"`
}

// recordWTFScan updates health after a scan. A scan that saved its state
// succeeded even when some sources degraded; only an unsaved scan or one that
// failed outright is an error.
func recordWTFScan(health *wtfHealth, now int64, scanErr, saveErr error) {
	var partial *wtfScanError
	if saveErr != nil || (scanErr != nil && !errors.As(scanErr, &partial)) {
		health.LastError = errors.Join(scanErr, saveErr).Error()
		return
	}
	health.LastSuccessfulScan = now
	health.LastError = ""
	health.Degraded = nil
	if partial != nil {
		for _, err := range partial.errors {
			health.Degraded = append(health.Degraded, err.Error())
		}
	}
}

type wtfDaemonDeps struct {
	Scan         func(context.Context, string, wtfState, wtfScanDeps) (wtfState, error)
	Load         func(string, int64) (wtfState, error)
	Save         func(string, wtfState) error
	Now          func() time.Time
	After        func(time.Duration) <-chan time.Time
	RequestAfter func(time.Duration) <-chan time.Time
	Notify       wtfNotifier
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
func wtfAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", wtfAgentLabel+".plist")
}
func wtfLogPath(home string) string { return filepath.Join(wtfDir(home), "daemon.log") }

func wtfPlistString(value string) string {
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

// wtfAgentPlist writes the LaunchAgent. path is the PATH to run the daemon
// with: launchd starts agents with /usr/bin:/bin:/usr/sbin:/sbin, and the
// daemon shells out to entire (trail metadata) and amp (thread exports), which
// live in user bin dirs. So install hands on the PATH of the shell that ran it.
func wtfAgentPlist(bin, logPath, path string) string {
	env := ""
	if path != "" {
		env = fmt.Sprintf("  <key>EnvironmentVariables</key>\n  <dict>\n    <key>PATH</key><string>%s</string>\n  </dict>\n", wtfPlistString(path))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>wtf</string>
    <string>daemon</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
%s</dict>
</plist>
`, wtfPlistString(wtfAgentLabel), wtfPlistString(bin), wtfPlistString(logPath), wtfPlistString(logPath), env)
}

func installWTFAgent(home, bin string, out io.Writer) (string, error) {
	path := wtfAgentPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(wtfDir(home), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(wtfAgentPlist(bin, wtfLogPath(home), os.Getenv("PATH"))), 0o644); err != nil {
		return "", err
	}
	if looksEphemeralBinary(bin) {
		fmt.Fprintf(out, "entire-tail wtf: WARNING: the agent points at temporary path %s\n", bin)
	}
	return path, nil
}

func waitForWTF(home string, timeout time.Duration) (wtfHealth, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if health, ok := readWTFHealth(home); ok && wtfDaemonRunning(health) {
			return health, true
		}
		if time.Now().After(deadline) {
			return wtfHealth{}, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var (
	wtfAgentLoad   = launchctlLoad
	wtfAgentUnload = func(path string) error { return launchctlUnloadLabel(path, wtfAgentLabel) }
	wtfAgentWait   = waitForWTF
	wtfDaemonRun   = runWTFDaemon
)

func runWTFCommand(args []string, home string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("wtf: unknown subcommand %q (want install|status|uninstall)", strings.Join(args, " "))
	}
	switch args[0] {
	case "install":
		bin, err := tapAgentBinary("", exec.LookPath)
		if err != nil {
			return fmt.Errorf("wtf install: resolve binary: %w", err)
		}
		path, err := installWTFAgent(home, bin, out)
		if err != nil {
			return err
		}
		if err := wtfAgentUnload(path); err != nil {
			return err
		}
		if err := os.Remove(wtfHealthPath(home)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := wtfAgentLoad(path); err != nil {
			return err
		}
		if health, ok := wtfAgentWait(home, 8*time.Second); ok {
			fmt.Fprintf(out, "entire-tail wtf: monitoring (pid %d)\n", health.PID)
			return nil
		}
		return fmt.Errorf("wtf install: agent loaded but health check failed; see %s", wtfLogPath(home))
	case "status":
		if _, err := os.Stat(wtfAgentPath(home)); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "entire-tail wtf: not installed")
			return nil
		} else if err != nil {
			return err
		}
		health, ok := readWTFHealth(home)
		if !ok || !wtfDaemonRunning(health) {
			fmt.Fprintln(out, "entire-tail wtf: installed, stale health")
			return nil
		}
		fmt.Fprintf(out, "entire-tail wtf: running (pid %d)\n", health.PID)
		return nil
	case "uninstall":
		path := wtfAgentPath(home)
		if err := wtfAgentUnload(path); err != nil {
			return err
		}
		for _, remove := range []string{path, wtfHealthPath(home)} {
			if err := os.Remove(remove); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		fmt.Fprintln(out, "entire-tail wtf: uninstalled; state.json preserved")
		return nil
	case "daemon":
		return wtfDaemonRun(context.Background(), home, wtfDaemonDeps{})
	default:
		return fmt.Errorf("wtf: unknown subcommand %q (want install|status|uninstall)", args[0])
	}
}

func lockWTFBreaker(path string, operation int) (func(), bool) {
	breaker, err := os.OpenFile(path+".breaker", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, false
	}
	if err := syscall.Flock(int(breaker.Fd()), operation); err != nil {
		_ = breaker.Close()
		return func() {}, false
	}
	return func() {
		_ = syscall.Flock(int(breaker.Fd()), syscall.LOCK_UN)
		_ = breaker.Close()
	}, true
}

func acquireWTFLock(home string) (func(), bool) {
	path := wtfLockPath(home)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return func() {}, false
	}
	unlockBreaker, ok := lockWTFBreaker(path, syscall.LOCK_EX|syscall.LOCK_NB)
	if !ok {
		return func() {}, false
	}
	defer unlockBreaker()

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
				unlockBreaker, ok := lockWTFBreaker(path, syscall.LOCK_EX)
				if !ok {
					return
				}
				defer unlockBreaker()
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
		fields := strings.Fields(string(data))
		owner := 0
		if len(fields) > 0 {
			owner, _ = strconv.Atoi(fields[0])
		}
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
	return len(fields) == 3 && filepath.Base(fields[0]) == "entire-tail" && fields[1] == "wtf" && fields[2] == "daemon"
}

func readWTFHealthFile(home string) (wtfHealth, error) {
	var health wtfHealth
	data, err := os.ReadFile(wtfHealthPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return wtfHealth{}, nil
	}
	if err != nil {
		return wtfHealth{}, fmt.Errorf("read wtf health: %w", err)
	}
	if err := json.Unmarshal(data, &health); err != nil {
		return wtfHealth{}, fmt.Errorf("read wtf health: %w", err)
	}
	return health, nil

}

func readWTFHealth(home string) (wtfHealth, bool) {
	health, err := readWTFHealthFile(home)
	return health, err == nil && health.PID > 0
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
	if deps.Notify == nil {
		deps.Notify = defaultWTFNotifier
	}
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	type deliveryJob struct {
		state    wtfState
		delivery wtfDelivery
	}
	jobs := make(chan deliveryJob, 4)
	results := make(chan wtfDeliveryResult, 4)
	slots := make(chan struct{}, 4)
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case job := <-jobs:
					result := deps.Notify(workerCtx, job.state, job.delivery)
					result.FindingID = job.delivery.FindingID
					result.Channel = job.delivery.Channel
					result.Occurrence = job.delivery.Occurrence
					result.Attempt = job.delivery.Attempt
					select {
					case results <- result:
					case <-workerCtx.Done():
						return
					}
				}
			}
		}()
	}
	defer func() {
		cancelWorkers()
		workers.Wait()
	}()

	started := deps.Now()
	health := wtfHealth{PID: wtfCurrentPID(), Version: version, StartedAt: started.Unix()}
	state, loadErr := deps.Load(home, started.Unix())
	if loadErr != nil {
		health.LastError = loadErr.Error()
	}
	if err := writeWTFHealth(home, health); err != nil {
		return err
	}

	queuePending := func() error {
		for _, delivery := range pendingWTFDeliveries(state, deps.Now().Unix()) {
			select {
			case slots <- struct{}{}:
			default:
				continue
			}
			markWTFDeliveryStarted(&state, delivery, deps.Now().Unix())
			delivery = identifyWTFDelivery(state, delivery)
			if err := deps.Save(home, state); err != nil {
				<-slots
				return err
			}
			snapshot, err := cloneWTFState(state)
			if err != nil {
				<-slots
				return err
			}
			jobs <- deliveryJob{state: snapshot, delivery: delivery}
		}
		return nil
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
		recordWTFScan(&health, now, scanErr, saveErr)
		if err := writeWTFHealth(home, health); err != nil {
			return err
		}
		if saveErr == nil {
			if err := queuePending(); err != nil {
				return err
			}
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
		case result := <-results:
			<-slots
			applyWTFDeliveryResult(&state, result, deps.Now().Unix())
			if err := deps.Save(home, state); err != nil {
				return err
			}
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

func cloneWTFState(state wtfState) (wtfState, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return wtfState{}, err
	}
	var clone wtfState
	if err := json.Unmarshal(data, &clone); err != nil {
		return wtfState{}, err
	}
	return clone, nil
}
