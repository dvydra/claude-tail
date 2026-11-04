package main

import (
	_ "embed"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// `entire-tail threads title <title>` names the iTerm TAB this session runs in
// (not the pane). The reassess prompt and the /thread-title skill call it, so
// the script has one fixed home: this binary. Outside iTerm it does nothing and
// exits 0, so an agent can run it anywhere.

//go:embed tabtitle.py
var tabTitlePy string

// tabTitleRun runs the iTerm API call. A seam so `go test` never touches iTerm.
var tabTitleRun = func(sessionUUID, title string) ([]byte, error) {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return nil, fmt.Errorf("uv not on PATH")
	}
	return exec.Command(uv, "run", "-q", "--with", "iterm2", "python", "-I", "-c", tabTitlePy, sessionUUID, title).CombinedOutput()
}

func threadsTitle(args []string, getenv func(string) string, errOut io.Writer) int {
	title := strings.TrimSpace(strings.Join(args, " "))
	if title == "" {
		return threadsUsage(errOut, "title <title>")
	}
	// ITERM_SESSION_ID is "w0t1p0:<uuid>"; the API wants the uuid.
	sid := getenv("ITERM_SESSION_ID")
	if i := strings.IndexByte(sid, ':'); i >= 0 {
		sid = sid[i+1:]
	}
	if sid == "" {
		return 0
	}
	if out, err := tabTitleRun(sid, title); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error() + " (is the iTerm Python API enabled?)"
		}
		fmt.Fprintln(errOut, "entire-tail threads title: "+msg)
		return threadsExitMiss
	}
	return 0
}
