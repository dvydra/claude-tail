#!/usr/bin/env bash
# Install entire-tail.
#
# Strategy:
#   1. If the `entire` CLI is on $PATH, register entire-tail as a plugin via
#      `entire plugin install` — this makes it invokable as `entire tail`.
#   2. Always also drop a symlink in ~/.local/bin so the standalone command
#      `entire-tail` works regardless of whether the user has the entire CLI.
#
# The symlink resolves back to this directory, so editing files here picks
# up immediately for both invocation paths.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="$HERE/entire-tail"

chmod +x "$BIN"

# ── standalone install: symlink into ~/.local/bin ────────────────────────────
LOCAL_BIN="$HOME/.local/bin"
mkdir -p "$LOCAL_BIN"
ln -sf "$BIN" "$LOCAL_BIN/entire-tail"
echo "Linked: $LOCAL_BIN/entire-tail -> $BIN"

# ── entire plugin install (best-effort) ──────────────────────────────────────
if command -v entire >/dev/null 2>&1; then
  if entire plugin install "$BIN" 2>&1; then
    echo "Registered as entire plugin: invoke with 'entire tail'."
  else
    echo "warn: 'entire plugin install' failed — falling back to the ~/.local/bin symlink." >&2
  fi
else
  echo "note: 'entire' CLI not on \$PATH. Skipping plugin install."
  echo "      You can still run the standalone 'entire-tail' command."
fi
