package main

import (
	"errors"
	"fmt"
	"strings"
)

// driftcard.go — rendering a drift report, including the reports that failed.
//
// Every path returns a card. An unusable model or a session too young to judge
// is still something to say, and saying it is the point: the old summary card's
// failure mode was a silent blank, which reads as "nothing to report" when it
// means "I never asked".

const driftIndent = "  "

// verdictANSI colours the verdict alone. The theme carries speaker and dim
// colours, not a semantic ramp, so these are raw SGR: green / amber / red.
func verdictANSI(v string) string {
	switch v {
	case verdictOnTrack:
		return "\x1b[32m"
	case verdictDrifted:
		return "\x1b[31m"
	default:
		return "\x1b[33m"
	}
}

// driftCardLines renders the card body. width is the usable terminal width; every
// returned line fits inside it.
func driftCardLines(r driftReport, err error, theme Theme, width int) []string {
	dim := theme.DimANSI
	reset := ""
	if dim != "" {
		reset = "\x1b[0m"
	}
	body := max(width-len(driftIndent)*2, 20)

	var L []string
	add := func(s string) { L = append(L, s) }
	addWrapped := func(prefix, s string) {
		for i, w := range wrapText(s, body-len([]rune(prefix))) {
			pad := prefix
			if i > 0 {
				pad = strings.Repeat(" ", len([]rune(prefix)))
			}
			add(driftIndent + pad + w)
		}
	}

	add("")
	if err != nil {
		if errors.Is(err, errDriftTooShort) {
			addWrapped("", "Not enough of this session yet to judge drift — ask again after a few more turns.")
			return L
		}
		addWrapped("", "Can't check drift: "+err.Error()+".")
		if _, isFM := err.(fmError); isFM {
			add("")
			addWrapped(dim, "Apple Intelligence runs this on-device; entire-tail only asks."+reset)
		}
		return L
	}

	add(driftIndent + verdictANSI(r.Verdict) + r.Verdict + reset)
	add("")
	addWrapped("Asked for:  ", r.Asked)
	addWrapped("Doing now:  ", r.Doing)

	if len(r.Hops) > 0 {
		add("")
		add(driftIndent + dim + "How you got here:" + reset)
		for _, h := range r.Hops {
			addWrapped("• ", h)
		}
	}
	return L
}

// driftBanner is what the overlay shows while the model is thinking. The check
// takes a few seconds on-device, which is long enough to look hung.
func driftBanner(sessionID string) string {
	return fmt.Sprintf("\x1b[H\x1b[2J\n%sChecking %s against what you asked for…",
		driftIndent, shortID(sessionID))
}
