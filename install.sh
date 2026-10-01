#!/usr/bin/env bash
# Install entire-tail.
#
# Builds the Go binary in place, then:
#   1. If the `entire` CLI is on $PATH, registers the binary as both `entire
#      tail` and `entire wtf` plugins.
#   2. Always also drops `entire-tail` and `entire-wtf` symlinks in
#      ~/.local/bin. The latter selects the dashboard from argv[0].
#   3. Links amp-plugin/entire-tail.ts into ~/.config/amp/plugins.
#
# The binary embeds its themes (go:embed), so it is self-contained — the
# symlink works from anywhere and editing themes/ requires a rebuild.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="$HERE/entire-tail"

# ── build ─────────────────────────────────────────────────────────────────────
if ! command -v go >/dev/null 2>&1; then
  echo "error: Go toolchain not found. Install Go (https://go.dev/dl/) and re-run." >&2
  exit 1
fi
echo "Building entire-tail..."
( cd "$HERE" && go build -o "$BIN" . )
echo "Built: $BIN"

# ── standalone install: symlink into ~/.local/bin ────────────────────────────
LOCAL_BIN="$HOME/.local/bin"
mkdir -p "$LOCAL_BIN"
ln -sf "$BIN" "$LOCAL_BIN/entire-tail"
echo "Linked: $LOCAL_BIN/entire-tail -> $BIN"
ln -sf "$BIN" "$LOCAL_BIN/entire-wtf"
echo "Linked: $LOCAL_BIN/entire-wtf -> $BIN"

# ── Amp plugin: live feed so Amp tails skip `amp threads export` polling ─────
AMP_PLUGINS="$HOME/.config/amp/plugins"
mkdir -p "$AMP_PLUGINS"
ln -sf "$HERE/amp-plugin/entire-tail.ts" "$AMP_PLUGINS/entire-tail.ts"
echo "Linked: $AMP_PLUGINS/entire-tail.ts (loads in Amp sessions started from now on)"

# ── entire plugin install (best-effort) ──────────────────────────────────────
if command -v entire >/dev/null 2>&1; then
  # --force so a re-install replaces existing entries instead of erroring.
  if entire plugin install "$LOCAL_BIN/entire-tail" --force 2>&1 &&
     entire plugin install "$LOCAL_BIN/entire-wtf" --force 2>&1; then
    echo "Registered as entire plugins: invoke with 'entire tail' or 'entire wtf'."
  else
    echo "warn: 'entire plugin install' failed — falling back to the ~/.local/bin symlink." >&2
  fi
else
  echo "note: 'entire' CLI not on \$PATH. Skipping plugin install."
  echo "      You can still run the standalone 'entire-tail' command."
fi
