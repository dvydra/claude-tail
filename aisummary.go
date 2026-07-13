package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// aisummary.go drives the on-device Apple Intelligence summarizer
// (entire-tail-aisum, built from aisum.swift by install.sh). It feeds a session's
// cleaned transcript to the helper and parses the structured summary the `i` card
// renders. All best-effort: no helper / model unavailable → the card falls back
// to metadata only.

type aiSummary struct {
	Headline  string   `json:"headline"`
	Summary   string   `json:"summary"`
	KeyPoints []string `json:"keyPoints"`
	Outcome   string   `json:"outcome"`
}

// aisumHelper returns the summarizer binary's path (beside our own binary, else
// on PATH), or "" when it wasn't built.
func aisumHelper() string {
	if cand := filepath.Join(filepath.Dir(selfPath()), "entire-tail-aisum"); isFile(cand) {
		return cand
	}
	if p, err := exec.LookPath("entire-tail-aisum"); err == nil {
		return p
	}
	return ""
}

// aiSummarize runs the helper over transcript text, returning the structured
// summary (ok=false if the helper is missing, times out, the model is
// unavailable, or the output is unusable).
func aiSummarize(text string) (aiSummary, bool) {
	h := aisumHelper()
	if h == "" || strings.TrimSpace(text) == "" {
		return aiSummary{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h)
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil {
		return aiSummary{}, false
	}
	var s aiSummary
	if json.Unmarshal(out, &s) != nil || strings.TrimSpace(s.Summary) == "" {
		return aiSummary{}, false
	}
	return s, true
}

// transcriptText extracts the tail of a session as plain "User:/Assistant:" text
// (system/tool noise dropped) — clean input for the summarizer.
func transcriptText(path, home string) string {
	agent := detectAgentForFile(home, path)
	var b strings.Builder
	for _, l := range tailLines(path, 400) {
		for _, rec := range normalize(agent, l, time.Local) {
			switch rec.Kind {
			case KindUser:
				b.WriteString("User: " + rec.Body + "\n")
			case KindAssistant:
				b.WriteString("Assistant: " + rec.Body + "\n")
			}
		}
	}
	return b.String()
}
