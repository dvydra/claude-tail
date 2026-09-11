package main

import (
	"strings"
	"testing"
)

func TestStripCodeFences(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"fenced block removed", "before\n```go\nfunc main() {}\n```\nafter", "before\nafter"},
		{"two blocks removed", "a\n```\nx\n```\nb\n```\ny\n```\nc", "a\nb\nc"},
		{"unterminated fence drops the tail", "keep this\n```\nnever closed", "keep this"},
		{"inline backticks survive", "use `go test` here", "use `go test` here"},
		{"no fence unchanged", "plain words", "plain words"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripCodeFences(tc.in); got != tc.want {
				t.Errorf("stripCodeFences(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func userTurns(bodies ...string) []turn {
	out := make([]turn, 0, len(bodies))
	for _, b := range bodies {
		out = append(out, turn{user: true, body: b})
	}
	return out
}

func TestDriftSampleSplitsOpeningFromNow(t *testing.T) {
	in := userTurns("one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten")
	got := driftSample(in)

	open, now, ok := strings.Cut(got, driftNowMarker)
	if !ok {
		t.Fatalf("no %q marker in:\n%s", driftNowMarker, got)
	}
	if !strings.Contains(open, driftOpenMarker) {
		t.Errorf("no %q marker in:\n%s", driftOpenMarker, open)
	}
	if !strings.Contains(open, "one") || !strings.Contains(open, "three") {
		t.Errorf("opening section missing the first turns:\n%s", open)
	}
	if strings.Contains(open, "ten") {
		t.Errorf("opening section leaked a recent turn:\n%s", open)
	}
	if !strings.Contains(now, "ten") || !strings.Contains(now, "six") {
		t.Errorf("now section missing the recent turns:\n%s", now)
	}
	if strings.Contains(now, "one") {
		t.Errorf("now section leaked an opening turn:\n%s", now)
	}
}

func TestDriftSampleShortSessionUsesEveryTurnOnce(t *testing.T) {
	in := userTurns("build a widget", "make it blue", "now add sound")
	got := driftSample(in)
	for _, want := range []string{"build a widget", "make it blue", "now add sound"} {
		if strings.Count(got, want) != 1 {
			t.Errorf("want %q exactly once, got %d:\n%s", want, strings.Count(got, want), got)
		}
	}
}

func TestDriftSampleDropsAssistantTurns(t *testing.T) {
	in := []turn{
		{user: true, body: "the human asked this"},
		{user: false, body: "ASSISTANT_NOISE"},
		{user: true, body: "and then this"},
		{user: false, body: "MORE_NOISE"},
		{user: true, body: "and finally this"},
	}
	got := driftSample(in)
	if strings.Contains(got, "ASSISTANT_NOISE") || strings.Contains(got, "MORE_NOISE") {
		t.Errorf("assistant turns leaked into the sample:\n%s", got)
	}
	if !strings.Contains(got, "the human asked this") {
		t.Errorf("dropped a human turn:\n%s", got)
	}
}

func TestDriftSampleStripsCodeFromTurns(t *testing.T) {
	in := userTurns("here is my code\n```\nSECRET_SNIPPET\n```\nplease fix", "second", "third")
	got := driftSample(in)
	if strings.Contains(got, "SECRET_SNIPPET") {
		t.Errorf("code fence survived into the sample:\n%s", got)
	}
	if !strings.Contains(got, "please fix") {
		t.Errorf("prose around the fence was lost:\n%s", got)
	}
}

func TestDriftSampleTooShortToJudge(t *testing.T) {
	for _, in := range [][]turn{
		{},
		userTurns("only one turn"),
		userTurns("one", "two"),
	} {
		if got := driftSample(in); got != "" {
			t.Errorf("driftSample(%d turns) = %q, want \"\" (nothing to compare yet)", len(in), got)
		}
	}
}

func TestDriftSampleCapsAGiantTurn(t *testing.T) {
	huge := strings.Repeat("x", driftTurnCap*3)
	got := driftSample(userTurns(huge, "two", "three"))
	if len(got) > driftTurnCap*2 {
		t.Errorf("sample is %d chars, want a giant turn capped near %d", len(got), driftTurnCap)
	}
}

func TestParseDriftJSON(t *testing.T) {
	t.Run("plain object", func(t *testing.T) {
		got, ok := parseDriftJSON([]byte(`{"verdict":"DRIFTED","asked":"a","doing":"b","hops":["x","y"]}`))
		if !ok {
			t.Fatal("ok=false, want true")
		}
		if got.Verdict != "DRIFTED" || got.Asked != "a" || got.Doing != "b" || len(got.Hops) != 2 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("spinner and ANSI chrome around it", func(t *testing.T) {
		raw := []byte("\x1b[2K⠋ thinking\n{\"verdict\":\"ON TRACK\",\"asked\":\"a\",\"doing\":\"b\",\"hops\":[]}\ndone\n")
		got, ok := parseDriftJSON(raw)
		if !ok || got.Verdict != "ON TRACK" {
			t.Errorf("got %+v ok=%v", got, ok)
		}
	})

	t.Run("unknown verdict falls back rather than rendering garbage", func(t *testing.T) {
		got, ok := parseDriftJSON([]byte(`{"verdict":"banana","asked":"a","doing":"b","hops":[]}`))
		if !ok {
			t.Fatal("ok=false, want true")
		}
		if got.Verdict != verdictAdjacent {
			t.Errorf("verdict = %q, want %q", got.Verdict, verdictAdjacent)
		}
	})

	t.Run("hops capped", func(t *testing.T) {
		got, _ := parseDriftJSON([]byte(`{"verdict":"DRIFTED","asked":"a","doing":"b","hops":["1","2","3","4","5","6","7"]}`))
		if len(got.Hops) > driftMaxHops {
			t.Errorf("got %d hops, want <= %d", len(got.Hops), driftMaxHops)
		}
	})

	for _, bad := range []string{"", "not json at all", "{", `{"verdict":"DRIFTED"`} {
		t.Run("garbage: "+bad, func(t *testing.T) {
			if _, ok := parseDriftJSON([]byte(bad)); ok {
				t.Errorf("parseDriftJSON(%q) ok=true, want false", bad)
			}
		})
	}

	t.Run("missing asked is a failure, not a blank card", func(t *testing.T) {
		if _, ok := parseDriftJSON([]byte(`{"verdict":"DRIFTED","doing":"b","hops":[]}`)); ok {
			t.Error("ok=true for a report with no asked field, want false")
		}
	})
}
