package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// profile.go — which Claude *account* a session belongs to.
//
// macOS keeps subscription logins in the shared system Keychain, not in the
// config dir, so a second account can't simply `/login`: whichever logged in
// last flips every session, running ones included. The way around it is to pin
// the second account with a long-lived OAuth token (`claude setup-token`) and
// give it its own CLAUDE_CONFIG_DIR — by convention ~/.claude-personal. Because
// Claude Code stores transcripts under $CLAUDE_CONFIG_DIR/projects, that account
// writes a SECOND, parallel projects tree that everything else in entire-tail
// (discovery, the picker, search, auto-adopt) would otherwise never look at.
//
// This file makes that tree a first-class citizen: discovered next to the
// default one, marked in the picker with a pink @, and — the part that can't be
// faked — launched back under the account that owns it, since resuming a
// personal session with the work account's credentials is not a resume at all.
//
// Only two profiles exist on purpose. A third account is a config-format
// question (which dirs? which Keychain entry?) that nobody has yet; the shape
// here (an ordered []claudeProfile everything iterates) is what a third would
// slot into.

const (
	// personalProfile is the profile Name for the second account. The default
	// (work) account's Name is "" — it is the absence of a marker everywhere.
	personalProfile = "personal"

	// mixedProfile is not a real account: it is what a folder reports when it
	// holds sessions from both. Only ever a display state.
	mixedProfile = "mixed"

	personalConfigDir = ".claude-personal"

	// personalKeychainService matches setup-claude-personal.sh. The token itself
	// is never read by entire-tail — the launched shell looks it up, so it never
	// passes through our memory, argv, or environment.
	personalKeychainService = "claude-personal-token"
)

// claudeProfile is one Claude Code config dir and the account behind it.
type claudeProfile struct {
	Name string // "" for the default (work) account, "personal" for the second
	Dir  string // the CLAUDE_CONFIG_DIR
}

func (p claudeProfile) projects() string { return filepath.Join(p.Dir, "projects") }

// claudeProfiles lists the config dirs to search, DEFAULT FIRST. Order is
// load-bearing: callers that resolve a single session (findSessionClaude,
// resolveClaudeSession) treat earlier profiles as the tiebreak when two roots
// offer an equally good match, so the work account keeps winning ties exactly as
// it did before this file existed.
//
// The personal profile appears only when its projects/ dir is really there, so a
// machine that never ran setup-claude-personal.sh does strictly one root's worth
// of work and behaves byte-identically to before.
func claudeProfiles(home string) []claudeProfile {
	out := []claudeProfile{{Dir: filepath.Join(home, ".claude")}}
	if p := (claudeProfile{Name: personalProfile, Dir: filepath.Join(home, personalConfigDir)}); isDir(p.projects()) {
		out = append(out, p)
	}
	return out
}

// claudeProjectsRoots is claudeProfiles reduced to the projects dirs, for the
// callers that only need somewhere to glob.
func claudeProjectsRoots(home string) []string {
	profs := claudeProfiles(home)
	out := make([]string, 0, len(profs))
	for _, p := range profs {
		out = append(out, p.projects())
	}
	return out
}

// profileForPath names the account owning a session file, by which projects root
// contains it. "" covers both "the default account" and "not under any root at
// all" (a fixture, a reconstructed transcript) — neither gets a marker or an
// account-specific launch, which is the correct handling for both.
func profileForPath(home, path string) string {
	for _, p := range claudeProfiles(home) {
		if strings.HasPrefix(path, p.projects()+string(filepath.Separator)) {
			return p.Name
		}
	}
	return ""
}

// projectsRootOf returns the projects root containing a session file, derived
// from the path's own shape (<root>/<slug>/<id>.jsonl) rather than from home.
// Anything that follows a session across the disk has to use this: which account
// owns the file is already encoded in the path we're holding, and re-deriving it
// from home would always answer "the default one".
func projectsRootOf(sessionPath string) string {
	return filepath.Dir(filepath.Dir(sessionPath))
}

// profileByName resolves a Name back to its profile. The zero value (default
// account) is returned for "" and for anything unknown, so a stale/garbled name
// degrades to the work account rather than to a broken launch.
func profileByName(home, name string) claudeProfile {
	for _, p := range claudeProfiles(home) {
		if p.Name == name {
			return p
		}
	}
	return claudeProfile{Dir: filepath.Join(home, ".claude")}
}

// ── the pink @ ───────────────────────────────────────────────────────────────

const (
	// Hot pink, in the same truecolor register as the recency tiers.
	pinkANSI = "\x1b[38;2;255;105;180m"
	// A muted pink for a folder holding BOTH accounts — visibly the same mark,
	// visibly not the confident one.
	dimPinkANSI = "\x1b[38;2;168;108;136m"
)

// profileMark renders the account marker as a fixed two-column cell: "@ " for
// personal, "  " for work, so session rows in a mixed folder still line up.
// restore is the ANSI the rest of the row should return to (the row's recency
// color); "" means uncolored output (a piped --list), where the @ still shows
// as plain text.
func profileMark(prof, restore string) string {
	switch prof {
	case personalProfile:
		return colorize(pinkANSI, "@", restore) + " "
	case mixedProfile:
		return colorize(dimPinkANSI, "@", restore) + " "
	default:
		return "  "
	}
}

// profileTag is profileMark without the reserved column — for rows that have no
// columns to align (folder headers, whose paths vary in length anyway). Work
// folders therefore render exactly as they always have.
func profileTag(prof, restore string) string {
	if m := strings.TrimRight(profileMark(prof, restore), " "); m != "" {
		return m + " "
	}
	return ""
}

func colorize(color, s, restore string) string {
	if restore == "" {
		return s
	}
	return color + s + restore
}

// folderProfile reports the account a folder belongs to: personal when EVERY
// session in it is personal, "" when every session is work, and mixedProfile
// when it holds both. Deliberately not "any session is personal" — a collapsed
// folder must not claim to be personal when most of it isn't.
func folderProfile(sessions []treeSession) string {
	var personal, work int
	for _, s := range sessions {
		if s.Profile == personalProfile {
			personal++
		} else {
			work++
		}
	}
	switch {
	case personal == 0:
		return ""
	case work == 0:
		return personalProfile
	default:
		return mixedProfile
	}
}

// ── launching under the right account ────────────────────────────────────────

// accountEnvPrefix is the shell assignment that makes a launched agent run as
// the profile's account, or "" for the default account (whose launch command
// stays byte-identical to the pre-profiles one).
//
// Both halves are needed and neither is sufficient. CLAUDE_CONFIG_DIR alone
// moves the transcripts and history but NOT the credentials — the Keychain login
// still decides the account, so a "personal" resume would quietly run as work.
// CLAUDE_CODE_OAUTH_TOKEN alone authenticates the right account but leaves it
// writing into the other account's projects tree.
//
// The token is read at launch time by the shell, from the Keychain, in a command
// substitution — so it never enters entire-tail's memory, never lands in argv
// (visible to every process via `ps`), and never reaches a log. Double quotes
// around the substitution are required: single quotes would pass it literally.
func accountEnvPrefix(p claudeProfile) string {
	if p.Name != personalProfile {
		return ""
	}
	return "CLAUDE_CONFIG_DIR=" + shQuote(p.Dir) +
		` CLAUDE_CODE_OAUTH_TOKEN="$(security find-generic-password -s ` +
		personalKeychainService + ` -w)" `
}

// warnMissingToken prints a note when a personal launch is about to happen with
// no token in the Keychain. It never blocks the launch: the failure it predicts
// is recoverable (the agent opens on a login screen) and the check itself can be
// wrong — a Keychain that prompts for access is a slow success, not a failure.
// But launching silently into the WRONG account is the one outcome this whole
// file exists to prevent, so it is worth one line on stderr.
func warnMissingToken(p claudeProfile) {
	if p.Name != personalProfile || personalTokenAvailable() {
		return
	}
	fmt.Fprintf(os.Stderr,
		"entire-tail: no %q token in the Keychain — the personal session may open unauthenticated.\n"+
			"             fix with: claude setup-token   (personal account), then re-run setup-claude-personal.sh\n",
		personalKeychainService)
}

// personalTokenAvailable reports whether the personal account's token is
// actually in the Keychain.
func personalTokenAvailable() bool {
	return exec.Command("security", "find-generic-password", "-s", personalKeychainService, "-w").Run() == nil
}
