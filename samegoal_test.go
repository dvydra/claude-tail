package main

import "testing"

func TestSameGoal(t *testing.T) {
	tests := []struct {
		name         string
		asked, doing string
		want         bool
	}{
		{"identical", "raise a linear ticket", "raise a linear ticket", true},
		{"case and spacing differ", "Write A Letter", "write   a letter", true},
		{"trailing punctuation differs", "check the .zoekt size", "check the .zoekt size.", true},
		{"one word, identical", "test", "test", true},

		{"genuinely different", "why is my .zoekt 11gb", "check repo state after merge", false},
		{"same verb, different object", "delete the routing", "delete the cluster", false},
		{"substring is not sameness", "apply pr 722", "apply pr 722 and then run the migration", false},
		{"both empty is not a match", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameGoal(tc.asked, tc.doing); got != tc.want {
				t.Errorf("sameGoal(%q, %q) = %v, want %v", tc.asked, tc.doing, got, tc.want)
			}
		})
	}
}

// The invariant the model would not hold on its own: if it describes the opening
// and the current work with the same words, that is not drift, whatever verdict
// it picked.
func TestParseDriftJSONForcesOnTrackWhenAskedEqualsDoing(t *testing.T) {
	for _, verdict := range []string{verdictDrifted, verdictAdjacent, verdictOnTrack} {
		got, ok := parseDriftJSON([]byte(
			`{"verdict":"` + verdict + `","asked":"write a letter","doing":"Write a letter.","hops":[]}`))
		if !ok {
			t.Fatalf("%s: ok=false", verdict)
		}
		if got.Verdict != verdictOnTrack {
			t.Errorf("model said %s for identical asked/doing, got %s, want %s",
				verdict, got.Verdict, verdictOnTrack)
		}
	}
}

func TestParseDriftJSONKeepsVerdictWhenGoalsDiffer(t *testing.T) {
	got, ok := parseDriftJSON([]byte(
		`{"verdict":"DRIFTED","asked":"why is my .zoekt 11gb","doing":"delete the routing","hops":[]}`))
	if !ok {
		t.Fatal("ok=false")
	}
	if got.Verdict != verdictDrifted {
		t.Errorf("verdict = %q, want %q — different goals must keep the model's call", got.Verdict, verdictDrifted)
	}
}
