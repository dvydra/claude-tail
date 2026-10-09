package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrailsLock(t *testing.T) {
	home := t.TempDir()
	release, acquired, err := acquireTrailsLock(home)
	if err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	defer release()
	_, acquired, err = acquireTrailsLock(home)
	if err != nil || acquired {
		t.Fatal("second writer", acquired, err)
	}
	release()
	release2, acquired, err := acquireTrailsLock(home)
	if err != nil || !acquired {
		t.Fatal("reacquire", acquired, err)
	}
	release2()
}

func TestTrailsCollectorPersistsAndAcknowledges(t *testing.T) {
	home := t.TempDir()
	request, err := requestTrailsScan(home)
	if err != nil {
		t.Fatal(err)
	}
	d := trailsScanDeps{Now: time.Now, Inventory: func(context.Context) ([]wtfSession, error) { return nil, nil }}
	c, err := collectTrailsOnce(context.Background(), home, newTrailsCatalog(), d, "foreground")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadTrails(home)
	if err != nil || stored.UpdatedAt != c.UpdatedAt || stored.UpdatedAt == 0 {
		t.Fatal(stored, err)
	}
	h := readTrailsHealth(home)
	if h.Request != request || h.Mode != "foreground" || h.LastScan != c.UpdatedAt {
		t.Fatal(h)
	}
	// An unsavable catalog must not acknowledge the next request.
	path := filepath.Join(trailsDir(home), "catalog.json")
	os.Remove(path)
	os.Mkdir(path, 0700)
	request2, _ := requestTrailsScan(home)
	_, err = collectTrailsOnce(context.Background(), home, c, d, "foreground")
	if err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	if readTrailsHealth(home).Request == request2 {
		t.Fatal("acknowledged failed save")
	}
}

func TestTrailsAgentPlist(t *testing.T) {
	p := trailsAgentPlist("/opt/bin/entire-tail", "/tmp/a&b.log", "/usr/bin:/opt/bin")
	for _, s := range []string{trailsAgentLabel, "<string>trails</string>", "<string>daemon</string>", "/usr/bin:/opt/bin", "a&amp;b.log"} {
		if !strings.Contains(p, s) {
			t.Fatalf("missing %s", s)
		}
	}
	if strings.Contains(p, "<string>wtf</string>") {
		t.Fatal("wrong daemon")
	}
}
