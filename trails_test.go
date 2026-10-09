package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestTrailsCatalogRoundTrip(t *testing.T) {
	home := t.TempDir()
	want := newTrailsCatalog()
	want.Sessions["claude:one"] = wtfSession{ID: "one", Name: "retained"}
	if err := saveTrails(home, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadTrails(home)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%#v %v", got, err)
	}
	path := filepath.Join(trailsDir(home), "catalog.json")
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	for _, bad := range []string{`{`, `{"version":99}`, `{"version":1}`} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTrails(home); err == nil {
			t.Fatalf("accepted %s", bad)
		}
		data, _ := os.ReadFile(path)
		if string(data) != bad {
			t.Fatal("corrupt data changed")
		}
	}
}

func TestSelectTrailsToday(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 23, 59, 0, 0, loc)
	c := newTrailsCatalog()
	for i, url := range []string{"https://entire.io/gh/acme/api/trails/7", "https://entire.io/et/acme/api/trails/7"} {
		id := []string{"claude:one", "amp:two"}[i]
		c.Sessions[id] = wtfSession{Name: "Fix login", Repo: "acme/api", Branch: "login", Active: true}
		c.Trails[url] = trailsEntry{URL: url, Repo: "acme/api", Title: "Auth", Status: "merged", Branch: "login", Associations: map[string]trailsAssociation{id: {FirstAt: now.Unix() - 100, LastAt: now.Unix() - 10, Branch: "login", BranchMatched: i == 0}}}
	}
	rows := selectTrails(c, now, "LOGIN")
	if len(rows) != 2 || !rows[0].Active || rows[1].Active {
		t.Fatalf("%+v", rows)
	}
	if len(selectTrails(c, now, "missing")) != 0 {
		t.Fatal("search ignored")
	}
	tomorrow := time.Date(2026, 10, 5, 0, 0, 0, 0, loc)
	if got := selectTrails(c, tomorrow, ""); len(got) != 1 || !got[0].Active {
		t.Fatalf("midnight: %+v", got)
	}
	s := c.Sessions["claude:one"]
	s.Branch = "elsewhere"
	c.Sessions["claude:one"] = s
	if got := selectTrails(c, tomorrow, ""); len(got) != 0 {
		t.Fatalf("old branch active: %+v", got)
	}
	if got := selectTrails(c, now, ""); len(got) != 2 {
		t.Fatal("lost today's stopped trails")
	}
}
