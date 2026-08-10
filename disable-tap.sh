#!/usr/bin/env bash
# Turn the entire-tail API tap off.
#
# Unloads and removes the KeepAlive LaunchAgent, stops a daemon started by hand,
# and verifies nothing is left listening.
#
# entire-tail keeps working without the tap — that's the designed fallback.
# New sessions simply launch unrouted (no ANTHROPIC_BASE_URL), the tail renders
# from the transcript as it always has, and a blocked question still alerts via
# the opt-in hooks; you just lose the question's preamble until you answer.
#
# The one thing to know: a session ALREADY routed through the tap has the
# daemon's address baked in from launch, so it will fail on its next API call
# once the daemon is gone. Restart those sessions.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
PORT="${ENTIRE_TAIL_TAP_PORT:-47391}"

# Prefer the installed binary; fall back to this checkout's build. Only used to
# read state and unload — if neither exists we still clean up by hand below.
BIN=""
if command -v entire-tail >/dev/null 2>&1; then
  BIN="$(command -v entire-tail)"
elif [[ -x "$HERE/entire-tail" ]]; then
  BIN="$HERE/entire-tail"
fi

# Warn about live routed sessions BEFORE pulling the rug, so the choice is informed.
ACTIVE="$HOME/.claude/entire-tail/tap/active.json"
if [[ -f "$ACTIVE" ]] && command -v python3 >/dev/null 2>&1; then
  python3 - "$ACTIVE" <<'PY' || true
import json, sys, time
try:
    a = json.load(open(sys.argv[1]))
except Exception:
    sys.exit(0)
now = time.time() * 1000
recent = [s for s, v in (a.get("sessions") or {}).items()
          if now - max(v.get("last_event", 0), v.get("last_end", 0), v.get("last_start", 0)) < 3600_000]
if recent:
    print(f"note: {len(recent)} session(s) used the tap in the last hour:")
    for s in recent[:5]:
        print(f"        {s}")
    print("      If any are still open, they were launched with the tap's address and")
    print("      will fail on their next request. Restart them after this.")
PY
fi

if [[ -n "$BIN" ]]; then
  echo "Removing the LaunchAgent and stopping the daemon..."
  "$BIN" tap uninstall --port "$PORT"
else
  # No binary available — do the same two things directly.
  PLIST="$HOME/Library/LaunchAgents/io.entire.entire-tail.tap.plist"
  echo "entire-tail not found; unloading $PLIST directly..."
  launchctl bootout "gui/$(id -u)/io.entire.entire-tail.tap" 2>/dev/null || launchctl unload "$PLIST" 2>/dev/null || true
  rm -f "$PLIST"
fi

# ── verify ────────────────────────────────────────────────────────────────────
sleep 1
if command -v curl >/dev/null 2>&1 &&
   curl -fsS -m 2 "http://127.0.0.1:$PORT/-/entire-tail-tap/health" >/dev/null 2>&1; then
  echo
  echo "WARNING: something is still answering on 127.0.0.1:$PORT."
  echo "         If you started a daemon by hand in another pane, stop it there (Ctrl-C)."
  echo "         Check:  launchctl list | grep entire-tail"
  exit 1
fi

echo "The tap is off. Nothing is listening on 127.0.0.1:$PORT."
echo "entire-tail still works: new sessions launch unrouted and tail from the transcript."
echo "Re-enable with: $HERE/install-tap.sh"
