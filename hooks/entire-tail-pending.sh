#!/usr/bin/env bash
# entire-tail pending-prompt hook.
# Writes/removes a per-session marker so entire-tail can surface an
# AskUserQuestion card or a permission prompt the instant it appears, before
# Claude Code's deferred transcript flush. Installed + wired by
# `entire-tail install-hooks`. Reads the Claude Code hook JSON on stdin.
#
# Usage: entire-tail-pending.sh <question-set|question-clear|perm-set|perm-clear>
set -euo pipefail

mode="${1:-}"
dir="${HOME}/.claude/entire-tail/pending"
in="$(cat)"

sid="$(printf '%s' "$in" | jq -r '.session_id // empty')"
[ -z "$sid" ] && exit 0
marker="${dir}/${sid}.json"

case "$mode" in
  question-set)
    mkdir -p "$dir"
    tmp="$(mktemp "${marker}.XXXXXX")"
    printf '%s' "$in" | jq -c '{kind:"question", payload:.tool_input, tool_use_id:(.tool_use_id // null), ts:(now|floor)}' > "$tmp"
    mv -f "$tmp" "$marker"
    ;;
  perm-set)
    mkdir -p "$dir"
    tmp="$(mktemp "${marker}.XXXXXX")"
    printf '%s' "$in" | jq -c '{kind:"permission", payload:{tool_name:.tool_name, tool_input:.tool_input}, tool_use_id:(.tool_use_id // null), ts:(now|floor)}' > "$tmp"
    mv -f "$tmp" "$marker"
    ;;
  question-clear|perm-clear)
    rm -f "$marker"
    ;;
  *)
    exit 0
    ;;
esac
exit 0
