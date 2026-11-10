package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTrailNoteClosesOpenLine(t *testing.T) {
	var b bytes.Buffer
	r := newRendererWith(&b, testTheme(), "dots", 5, identityRender)
	r.toolUse("Bash", "ls")
	r.trailNote("X", false)
	if !strings.HasSuffix(b.String(), "]"+reset+"\nX\n") {
		t.Fatalf("open dots streak not closed before the note: %q", b.String())
	}
	b.Reset()
	r.live = true
	r.trailNote("Y", true)
	if b.String() != "\aY\n" {
		t.Fatalf("bell: %q", b.String())
	}
	b.Reset()
	r.live = false
	r.trailNote("Z", true)
	if b.String() != "Z\n" {
		t.Fatalf("no bell while not live: %q", b.String())
	}
}

func TestNotesUpTo(t *testing.T) {
	notes := []trailNoteRec{
		{Path: "a.jsonl", Offset: 10, Text: "a10"},
		{Path: "b.jsonl", Offset: 15, Text: "b15"},
		{Path: "a.jsonl", Offset: 20, Text: "a20"},
		{Path: "a.jsonl", Offset: 30, Text: "a30"},
	}
	due, next := notesUpTo(notes, "a.jsonl", 20, 0)
	if len(due) != 2 || due[0].Text != "a10" || due[1].Text != "a20" || next != 3 {
		t.Fatalf("due %+v next %d", due, next)
	}
	due, next = notesUpTo(notes, "a.jsonl", 100, next)
	if len(due) != 1 || due[0].Text != "a30" || next != 4 {
		t.Fatalf("rest %+v next %d", due, next)
	}
	if due, _ := notesUpTo(notes, "c.jsonl", 100, 0); len(due) != 0 {
		t.Fatalf("other file's notes returned: %+v", due)
	}
}

func TestConfigNoTrails(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, _, err := parseCLI([]string{"--no-trails"}, env(nil), savedPrefs{})
	if err != nil || !c.NoTrails {
		t.Fatalf("flag: %v %v", c.NoTrails, err)
	}
	c, _, err = parseCLI(nil, env(map[string]string{"ENTIRE_TAIL_NO_TRAILS": "1"}), savedPrefs{})
	if err != nil || !c.NoTrails {
		t.Fatalf("env: %v %v", c.NoTrails, err)
	}
	c, _, _ = parseCLI(nil, env(nil), savedPrefs{})
	if c.NoTrails {
		t.Fatal("on by default")
	}
}
