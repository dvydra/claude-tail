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
    trap 'rm -f "$tmp"' EXIT   # under set -e, a jq failure would otherwise orphan the temp
    printf '%s' "$in" | jq -c '{kind:"question", payload:.tool_input, tool_use_id:(.tool_use_id // null), ts:(now|floor)}' > "$tmp"
    mv -f "$tmp" "$marker"
    ;;
  perm-set)
    # AskUserQuestion already gets a richer question card from its Pre/Post
    # hooks; a permission notice for it is redundant noise and would clobber the
    # question marker (both write the same <sid>.json). Skip it — perm markers
    # are for genuine tool-permission prompts (Bash, etc.).
    tool="$(printf '%s' "$in" | jq -r '.tool_name // empty')"
    [ "$tool" = "AskUserQuestion" ] && exit 0
    mkdir -p "$dir"
    tmp="$(mktemp "${marker}.XXXXXX")"
    trap 'rm -f "$tmp"' EXIT   # under set -e, a jq failure would otherwise orphan the temp
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
