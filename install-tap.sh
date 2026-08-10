#!/usr/bin/env bash
# Enable the entire-tail API tap and keep it alive.
#
# The tap is a local reverse proxy that agents launched from the tree are pointed
# at (ANTHROPIC_BASE_URL), so entire-tail can render a blocked question's preamble
# — the text Claude Code withholds from the transcript until you answer — and know
# exactly which session is generating.
#
# This script:
#   1. Makes sure entire-tail is installed at a STABLE path (./install.sh), because
#      a LaunchAgent outlives the shell that wrote it: pinning it to a git worktree
#      or temp build would leave you with a daemon that quietly stops returning.
#   2. Installs + loads a KeepAlive LaunchAgent (entire-tail tap install), so the
#      daemon starts at login and comes back if it crashes.
#   3. Verifies something is actually listening, and prints how to turn it off.
#
# Turn it off with ./disable-tap.sh. Nothing here is required to use entire-tail:
# with no daemon, agents launch unrouted and the tail works as it always has.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
PORT="${ENTIRE_TAIL_TAP_PORT:-47391}"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "error: the tap's KeepAlive uses launchd, so this installer is macOS-only." >&2
  echo "       On other systems run 'entire-tail tap start' yourself (e.g. under systemd --user)." >&2
  exit 1
fi

# ── 1. a stable binary ────────────────────────────────────────────────────────
# Prefer an already-installed entire-tail; otherwise build+link this checkout via
# install.sh. Either way the plist gets a path that outlives this shell.
if command -v entire-tail >/dev/null 2>&1; then
  BIN="$(command -v entire-tail)"
  echo "Using installed entire-tail: $BIN"
else
  echo "entire-tail is not on \$PATH — installing it first..."
  "$HERE/install.sh"
  BIN="$HOME/.local/bin/entire-tail"
fi

# A symlink into a worktree is the trap this guards: resolve it and say so.
REAL="$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$BIN" 2>/dev/null || echo "$BIN")"
case "$REAL" in
  */.claude/worktrees/*|/tmp/*|/private/tmp/*|/var/folders/*)
    echo
    echo "WARNING: $BIN resolves to $REAL"
    echo "         That path is temporary (worktree or temp dir). When it goes away the"
    echo "         daemon stops coming back, and routed sessions lose their endpoint."
    echo "         Run ./install.sh from a normal checkout first, or pass a stable path:"
    echo "           entire-tail tap install --binary /usr/local/bin/entire-tail"
    echo
    read -r -p "Continue anyway? [y/N] " reply
    [[ "$reply" == [yY]* ]] || { echo "Aborted."; exit 1; }
    ;;
esac

# ── 2. install + load the KeepAlive agent ─────────────────────────────────────
echo "Installing the LaunchAgent (port $PORT)..."
"$BIN" tap install --binary "$BIN" --port "$PORT"

# ── 3. report ─────────────────────────────────────────────────────────────────
echo
"$BIN" tap status
cat <<EOF

The tap is on. From now on:
  • Sessions entire-tail launches (tree ⏎ / n) route through 127.0.0.1:$PORT
    automatically, with ENABLE_TOOL_SEARCH=true so request composition is
    unchanged.
  • To route a session you start by hand:
      ANTHROPIC_BASE_URL=http://127.0.0.1:$PORT ENABLE_TOOL_SEARCH=true claude
  • Logs: $HOME/.claude/entire-tail/tap/daemon.log
  • Turn it off:  $HERE/disable-tap.sh
EOF
