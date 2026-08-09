#!/bin/sh
# End-to-end check of the tap render path against the REAL binary:
# a live tail, a session that blocks on a question, and the preamble Claude Code
# withholds from the transcript arriving from the tap instead.
#
# Not part of `go test` (it drives the built binary + a live poll loop); run by
# hand: sh e2e_tap.sh
set -eu

BIN=${BIN:-./entire-tail}
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT

SID=aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee
PROJ="$ROOT/.claude/projects/-work-proj"
SIDECAR="$ROOT/.claude/entire-tail/tap/sessions"
mkdir -p "$PROJ" "$SIDECAR"
T="$PROJ/$SID.jsonl"

# A transcript that stops dead, exactly as one does while a question is pending.
cat > "$T" <<'JSONL'
{"type":"user","timestamp":"2026-08-10T09:00:00.000Z","message":{"role":"user","content":"which phase next?"}}
{"type":"assistant","timestamp":"2026-08-10T09:00:05.000Z","message":{"id":"msg_prev","role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"Checking the surfaces."}]}}
JSONL

HOME="$ROOT" $BIN --agent claude --no-pick --tool-style dots "$T" > "$ROOT/out.txt" 2>"$ROOT/err.txt" &
TAIL_PID=$!
sleep 1.5

# The agent streams its preamble and then blocks on AskUserQuestion. The
# transcript stays frozen; only the tap sees this.
cat >> "$SIDECAR/$SID.ndjson" <<JSONL
{"ts":1786318800000,"session":"$SID","kind":"text","msg":"msg_Q","idx":0,"text":"The plan's cost model was wrong."}
{"ts":1786318800100,"session":"$SID","kind":"tool_use","msg":"msg_Q","idx":1,"name":"AskUserQuestion","tool_id":"toolu_Q","input":{"questions":[{"question":"Which do I build next?","header":"Next phase","options":[{"label":"Phase 4a + 4c","description":"cheapest"},{"label":"Phase 3","description":"biggest"}]}]}}
{"ts":1786318800200,"session":"$SID","kind":"message_stop","msg":"msg_Q","stop_reason":"tool_use"}
JSONL
sleep 1.5

# The user answers -> Claude Code finally flushes the withheld message.
cat >> "$T" <<'JSONL'
{"type":"assistant","timestamp":"2026-08-10T09:01:00.000Z","message":{"id":"msg_Q","role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"The plan's cost model was wrong."}]}}
{"type":"assistant","timestamp":"2026-08-10T09:01:00.000Z","message":{"id":"msg_Q","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_Q","name":"AskUserQuestion","input":{"questions":[{"question":"Which do I build next?","header":"Next phase","options":[{"label":"Phase 4a + 4c","description":"cheapest"},{"label":"Phase 3","description":"biggest"}]}]}}]}}
{"type":"user","timestamp":"2026-08-10T09:01:01.000Z","message":{"role":"user","content":[{"type":"tool_result","content":"User has answered your questions: Phase 4a + 4c. You can now continue with the user's answers in mind."}]}}
JSONL
sleep 1.5
kill $TAIL_PID 2>/dev/null || true
wait $TAIL_PID 2>/dev/null || true

OUT=$(sed 's/\x1b\[[0-9;]*m//g' "$ROOT/out.txt")
printf '%s\n' "----- rendered -----" "$OUT" "--------------------"

fail=0
check() { # description, expected-count, pattern
	n=$(printf '%s\n' "$OUT" | grep -c "$3" || true)
	if [ "$n" != "$2" ]; then
		echo "FAIL: $1 (want $2 occurrence(s), got $n)"
		fail=1
	else
		echo "ok: $1"
	fi
}

check "preamble shown exactly once" 1 "cost model was wrong"
check "question card shown exactly once" 1 "Which do I build next"
check "the answer rendered" 1 "Phase 4a + 4c\$"

# Order: the preamble must precede the card.
pre=$(printf '%s\n' "$OUT" | grep -n "cost model" | head -1 | cut -d: -f1)
card=$(printf '%s\n' "$OUT" | grep -n "Which do I build next" | head -1 | cut -d: -f1)
if [ "$pre" -lt "$card" ]; then
	echo "ok: preamble precedes the question card (lines $pre < $card)"
else
	echo "FAIL: preamble at line $pre is not before the card at line $card"
	fail=1
fi

[ -s "$ROOT/err.txt" ] && { echo "--- stderr ---"; cat "$ROOT/err.txt"; }
exit $fail
