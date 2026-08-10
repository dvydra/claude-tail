#!/bin/sh
# End-to-end check of the tap render path against the REAL binary.
#
# The load-bearing assertion is STAGE 2: the preamble and the question card must
# be on screen while the transcript is still frozen. Checking only the final
# output proves nothing — a plain transcript flush (text record then tool_use
# record) renders preamble-then-card all by itself, so a tap that never fired
# would still look right at the end. This script failed to catch a nil watcher
# for exactly that reason once; don't "simplify" it back.
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

strip() { sed 's/\x1b\[[0-9;]*m//g' "$1"; }
fail=0
check() { # description, expected-count, pattern, file
	n=$(strip "$4" | grep -c "$3" || true)
	if [ "$n" != "$2" ]; then
		echo "FAIL: $1 (want $2, got $n)"
		fail=1
	else
		echo "ok: $1"
	fi
}

# ── stage 1: a transcript that stops dead, as one does while a question is pending
cat > "$T" <<'JSONL'
{"type":"user","timestamp":"2026-08-10T09:00:00.000Z","message":{"role":"user","content":"which phase next?"}}
{"type":"assistant","timestamp":"2026-08-10T09:00:05.000Z","message":{"id":"msg_prev","role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"Checking the surfaces."}]}}
JSONL

HOME="$ROOT" $BIN --agent claude --no-pick --tool-style dots "$T" > "$ROOT/out.txt" 2>"$ROOT/err.txt" &
TAIL_PID=$!
sleep 1.5

# ── stage 2: the agent streams its preamble, then blocks on AskUserQuestion.
# The transcript stays frozen — only the tap can see any of this.
cat >> "$SIDECAR/$SID.ndjson" <<JSONL
{"ts":1786352430000,"session":"$SID","kind":"text","msg":"msg_Q","idx":0,"text":"The plan's cost model was wrong."}
{"ts":1786352430100,"session":"$SID","kind":"tool_use","msg":"msg_Q","idx":1,"name":"AskUserQuestion","tool_id":"toolu_Q","input":{"questions":[{"question":"Which do I build next?","header":"Next phase","options":[{"label":"Phase 4a + 4c","description":"cheapest"},{"label":"Phase 3","description":"biggest"}]}]}}
{"ts":1786352430200,"session":"$SID","kind":"message_stop","msg":"msg_Q","stop_reason":"tool_use"}
JSONL
sleep 1.5
cp "$ROOT/out.txt" "$ROOT/pending.txt"

echo "----- STAGE 2: transcript frozen, tap only -----"
strip "$ROOT/pending.txt" | tail -8
echo "------------------------------------------------"
before=$(wc -c < "$T")
check "preamble visible BEFORE the transcript has it" 1 "cost model was wrong" "$ROOT/pending.txt"
check "card visible BEFORE the transcript has it" 1 "Which do I build next" "$ROOT/pending.txt"
if [ "$before" -eq "$(wc -c < "$T")" ]; then
	echo "ok: transcript untouched at this point ($before bytes)"
else
	echo "FAIL: transcript changed; the test no longer isolates the tap"
	fail=1
fi
pre=$(strip "$ROOT/pending.txt" | grep -n "cost model" | head -1 | cut -d: -f1)
card=$(strip "$ROOT/pending.txt" | grep -n "Which do I build next" | head -1 | cut -d: -f1)
if [ "$pre" -lt "$card" ]; then
	echo "ok: preamble precedes the card (lines $pre < $card)"
else
	echo "FAIL: preamble at $pre is not before the card at $card"
	fail=1
fi

# ── stage 3: the user answers -> Claude Code finally flushes the withheld message
cat >> "$T" <<'JSONL'
{"type":"assistant","timestamp":"2026-08-10T09:01:00.000Z","message":{"id":"msg_Q","role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"The plan's cost model was wrong."}]}}
{"type":"assistant","timestamp":"2026-08-10T09:01:00.000Z","message":{"id":"msg_Q","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_Q","name":"AskUserQuestion","input":{"questions":[{"question":"Which do I build next?","header":"Next phase","options":[{"label":"Phase 4a + 4c","description":"cheapest"},{"label":"Phase 3","description":"biggest"}]}]}}]}}
{"type":"user","timestamp":"2026-08-10T09:01:01.000Z","message":{"role":"user","content":[{"type":"tool_result","content":"User has answered your questions: Phase 4a + 4c. You can now continue with the user's answers in mind."}]}}
JSONL
sleep 1.5
kill $TAIL_PID 2>/dev/null || true
wait $TAIL_PID 2>/dev/null || true

echo "----- STAGE 3: after the flush -----"
strip "$ROOT/out.txt"
echo "------------------------------------"
check "preamble STILL shown once (transcript twin suppressed)" 1 "cost model was wrong" "$ROOT/out.txt"
check "card STILL shown once" 1 "Which do I build next" "$ROOT/out.txt"
check "the answer rendered" 1 "Phase 4a + 4c\$" "$ROOT/out.txt"

# A no-tap run must show the SAME content (from the transcript alone), proving the
# suppression isn't just hiding the transcript unconditionally.
HOME="$ROOT" timeout 3 $BIN --agent claude --no-pick --no-tap --tool-style dots "$T" > "$ROOT/notap.txt" 2>/dev/null || true
check "--no-tap still renders the preamble from the transcript" 1 "cost model was wrong" "$ROOT/notap.txt"
check "--no-tap still renders the card" 1 "Which do I build next" "$ROOT/notap.txt"

[ -s "$ROOT/err.txt" ] && { echo "--- stderr ---"; head -3 "$ROOT/err.txt"; }
exit $fail
