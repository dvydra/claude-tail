package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// fmrun.go — one way in to Apple's on-device model, and one honest answer when
// it won't run.
//
// The old check was exec.LookPath("fm"), which passes whenever the binary is
// merely present. It is present and unusable in at least one common state: the
// machine-wide licence hasn't been accepted, so every invocation exits with
// "YOU HAVE NOT AGREED TO THE APPLE FOUNDATION MODELS CLI LEGAL NOTICE…". The
// card printed "Summarizing with Apple Intelligence…" and then silently showed
// nothing at all.
//
// No cheap subcommand separates usable from unusable — `fm --help` exits 0
// either way, `fm models` exits 1 even when healthy, `fm --version` exits 64 —
// and the only true probe is a real generation, which costs about a second. So
// there is no pre-flight: run the thing, and when it fails keep stderr, so the
// caller can say why instead of going quiet.

// fmRunner is the seam tests swap out. Returns fm's stdout, or an error whose
// text is already fit to show a human.
var fmRunner = runFM

var (
	errNoTranscript = errors.New("no transcript to read")
	errUnreadable   = errors.New("Apple Intelligence returned something unreadable")
)

const fmTimeout = 60 * time.Second

// fmInstalled is a presence check only — it says nothing about usability, and
// must never be treated as one. Its job is to skip the "Asking Apple
// Intelligence…" banner on a machine that has no fm at all.
func fmInstalled() bool {
	_, err := exec.LookPath("fm")
	return err == nil
}

func runFM(schemaJSON, instructions, stdin string) ([]byte, error) {
	schema, err := os.CreateTemp("", "entire-tail-*.schema.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(schema.Name())
	if _, err := schema.WriteString(schemaJSON); err != nil {
		return nil, err
	}
	schema.Close()

	ctx, cancel := context.WithTimeout(context.Background(), fmTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "fm", "respond",
		"--model", "system", "--no-stream", "--schema", schema.Name(), "-i", instructions)
	cmd.Stdin = strings.NewReader(stdin)
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		// A deadline kill surfaces from exec as the bare "signal: killed", which
		// tells a reader nothing. Name the actual cause.
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmError(fmt.Sprintf("Apple Intelligence took longer than %s", fmTimeout))
		}
		if reason := fmReason(errb.String()); reason != "" {
			return nil, fmError(reason)
		}
		return nil, err
	}
	return out, nil
}

type fmError string

func (e fmError) Error() string { return string(e) }

// fmReason reduces fm's stderr to one line fit for a card. fm shouts its licence
// refusal in capitals across several ANSI-coloured lines; the first non-empty
// line, de-shouted, carries the whole message.
func fmReason(stderr string) string {
	for _, ln := range strings.Split(stripANSI(stderr), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if len(ln) > 8 && ln == strings.ToUpper(ln) {
			ln = strings.ToUpper(ln[:1]) + strings.ToLower(ln[1:])
		}
		return strings.TrimSuffix(ln, ".")
	}
	return ""
}

// ansiRe matches SGR sequences and OSC strings.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]|\x1b\\][0-9];[^\x1b\x07]*(?:\x1b\\\\|\x07)")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }
