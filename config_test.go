package main

import "testing"

// envFunc builds a getenv closure from a map.
func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseCLIDefaults(t *testing.T) {
	c, action, err := parseCLI(nil, envFunc(nil))
	if err != nil || action != ActionRun {
		t.Fatalf("err=%v action=%v", err, action)
	}
	if c.Agent != "auto" || c.Theme != "tokyo-night" || c.Backfill != "all" ||
		c.ToolStyle != "dots" || c.Collapse != "5" || c.Pick != "auto" {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestParseCLIEnvDefaults(t *testing.T) {
	env := map[string]string{
		"ENTIRE_TAIL_AGENT":    "codex",
		"CLAUDE_TAIL_THEME":    "dracula", // back-compat fallback
		"ENTIRE_TAIL_COLLAPSE": "off",     // → "0"
		"ENTIRE_TAIL_PICK":     "yes",     // → "always"
		"GLOW_STYLE":           "/x.json",
	}
	c, _, _ := parseCLI(nil, envFunc(env))
	if c.Agent != "codex" || c.Theme != "dracula" || c.Collapse != "0" ||
		c.Pick != "always" || c.GlowStyle != "/x.json" {
		t.Errorf("env not applied: %+v", c)
	}
}

func TestParseCLIEnvPrecedence(t *testing.T) {
	// ENTIRE_TAIL_THEME wins over CLAUDE_TAIL_THEME.
	env := map[string]string{"ENTIRE_TAIL_THEME": "nord", "CLAUDE_TAIL_THEME": "dracula"}
	c, _, _ := parseCLI(nil, envFunc(env))
	if c.Theme != "nord" {
		t.Errorf("got %q", c.Theme)
	}
}

func TestParseCLIFlagsOverrideEnv(t *testing.T) {
	env := map[string]string{"ENTIRE_TAIL_AGENT": "codex"}
	c, _, err := parseCLI([]string{"--agent", "claude", "-t", "nord", "--backfill", "50"}, envFunc(env))
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent != "claude" || c.Theme != "nord" || c.Backfill != "50" {
		t.Errorf("flags should override env: %+v", c)
	}
}

func TestParseCLIEqualsForm(t *testing.T) {
	c, _, err := parseCLI([]string{"--agent=agy", "--theme=nord", "--collapse=0"}, envFunc(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent != "agy" || c.Theme != "nord" || c.Collapse != "0" {
		t.Errorf("got %+v", c)
	}
}

func TestParseCLINegationFlags(t *testing.T) {
	c, _, _ := parseCLI([]string{"--no-backfill", "--no-collapse", "--no-pick", "--no-compact-tools"}, envFunc(nil))
	if c.Backfill != "0" || c.Collapse != "0" || c.Pick != "never" || c.ToolStyle != "lines" {
		t.Errorf("got %+v", c)
	}
}

func TestParseCLIPositional(t *testing.T) {
	c, _, err := parseCLI([]string{"-t", "nord", "/path/to/session.jsonl"}, envFunc(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Session != "/path/to/session.jsonl" {
		t.Errorf("got %q", c.Session)
	}
}

func TestParseCLIDashDash(t *testing.T) {
	c, _, err := parseCLI([]string{"--", "--weird-filename"}, envFunc(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Session != "--weird-filename" {
		t.Errorf("got %q", c.Session)
	}
}

func TestParseCLIErrors(t *testing.T) {
	cases := [][]string{
		{"--agent"},            // missing value
		{"--agent", ""},        // empty value
		{"--frobnicate"},       // unknown option
		{"a.jsonl", "b.jsonl"}, // too many positionals
		{"--", "a", "b"},       // too many after --
	}
	for _, args := range cases {
		if _, _, err := parseCLI(args, envFunc(nil)); err == nil {
			t.Errorf("expected error for args %v", args)
		}
	}
}

func TestParseCLIActions(t *testing.T) {
	for flag, want := range map[string]Action{
		"--help":        ActionHelp,
		"-h":            ActionHelp,
		"--version":     ActionVersion,
		"-V":            ActionVersion,
		"--list-themes": ActionListThemes,
		"-l":            ActionListThemes,
	} {
		_, action, err := parseCLI([]string{flag}, envFunc(nil))
		if err != nil || action != want {
			t.Errorf("%s: action=%v err=%v", flag, action, err)
		}
	}
}

func TestValidateAgent(t *testing.T) {
	for _, ok := range []string{"auto", "claude", "codex", "agy"} {
		if v, err := validateAgent(ok); err != nil || v != ok {
			t.Errorf("validateAgent(%q) = %q,%v", ok, v, err)
		}
	}
	if v, err := validateAgent("antigravity"); err != nil || v != "agy" {
		t.Errorf("antigravity should map to agy, got %q,%v", v, err)
	}
	if _, err := validateAgent("bogus"); err == nil {
		t.Error("expected error for bogus agent")
	}
}

func TestResolveBackfill(t *testing.T) {
	cases := []struct {
		s     string
		total int
		want  int
		err   bool
	}{
		{"all", 100, 1, false},
		{"full", 100, 1, false},
		{"0", 100, 0, false},
		{"30", 100, 71, false},
		{"500", 100, 1, false}, // clamp to 1
		{"abc", 100, 0, true},
	}
	for _, c := range cases {
		got, err := resolveBackfill(c.s, c.total)
		if (err != nil) != c.err || (err == nil && got != c.want) {
			t.Errorf("resolveBackfill(%q,%d) = %d,%v want %d,err=%v", c.s, c.total, got, err, c.want, c.err)
		}
	}
}

func TestResolveCollapse(t *testing.T) {
	if n, err := resolveCollapse("5"); err != nil || n != 5 {
		t.Errorf("got %d,%v", n, err)
	}
	if n, err := resolveCollapse("0"); err != nil || n != 0 {
		t.Errorf("got %d,%v", n, err)
	}
	if _, err := resolveCollapse("-1"); err == nil {
		t.Error("negative collapse should error")
	}
	if _, err := resolveCollapse("abc"); err == nil {
		t.Error("non-numeric collapse should error")
	}
}

func TestValidateToolStyle(t *testing.T) {
	for _, ok := range []string{"none", "dots", "lines"} {
		if err := validateToolStyle(ok); err != nil {
			t.Errorf("validateToolStyle(%q) error: %v", ok, err)
		}
	}
	if err := validateToolStyle("fancy"); err == nil {
		t.Error("expected error for invalid tool style")
	}
}
