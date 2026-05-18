#!/usr/bin/env bash
# Symlink claude-tail into ~/.local/bin so it's on $PATH.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
DEST="${HOME}/.local/bin/claude-tail"

mkdir -p "$(dirname "$DEST")"
chmod +x "$HERE/claude-tail"
ln -sf "$HERE/claude-tail" "$DEST"
echo "Linked: $DEST -> $HERE/claude-tail"
