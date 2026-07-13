#!/usr/bin/env bash
# Install entire-tail.
#
# Builds the Go binary in place, then:
#   1. If the `entire` CLI is on $PATH, registers the binary as a plugin via
#      `entire plugin install` — this makes it invokable as `entire tail`.
#   2. Always also drops a symlink in ~/.local/bin so the standalone command
#      `entire-tail` works regardless of whether the user has the entire CLI.
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

# ── Apple Intelligence summarizer (macOS, best-effort) ────────────────────────
# Compiles aisum.swift → entire-tail-aisum when the Swift toolchain + Foundation
# Models framework are present. Powers the on-device AI summary in the 'i' card;
# absent, the card just shows metadata. Kept beside the binary so entire-tail
# finds it next to itself.
AISUM="$HERE/entire-tail-aisum"
if [ "$(uname -s)" = "Darwin" ] && command -v swiftc >/dev/null 2>&1; then
  SDK="$(xcrun --sdk macosx --show-sdk-path 2>/dev/null || true)"
  if [ -n "$SDK" ] && [ -d "$SDK/System/Library/Frameworks/FoundationModels.framework" ]; then
    if ( cd "$HERE" && swiftc -O aisum.swift -o "$AISUM" ) 2>/dev/null; then
      ln -sf "$AISUM" "$LOCAL_BIN/entire-tail-aisum"
      echo "Built Apple Intelligence summarizer: $AISUM (powers the 'i' card)"
    else
      echo "note: aisum.swift failed to compile — 'i' shows metadata only." >&2
    fi
  else
    echo "note: FoundationModels not in the macOS SDK — 'i' shows metadata only."
  fi
fi

# ── entire plugin install (best-effort) ──────────────────────────────────────
if command -v entire >/dev/null 2>&1; then
  # --force so a re-install replaces the existing 'tail' plugin entry instead of
  # erroring out ("plugin already installed").
  if entire plugin install "$BIN" --force 2>&1; then
    echo "Registered as entire plugin: invoke with 'entire tail'."
  else
    echo "warn: 'entire plugin install' failed — falling back to the ~/.local/bin symlink." >&2
  fi
else
  echo "note: 'entire' CLI not on \$PATH. Skipping plugin install."
  echo "      You can still run the standalone 'entire-tail' command."
fi
