package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type trailsCatalog struct {
	Version   int                          `json:"version"`
	UpdatedAt int64                        `json:"updatedAt"`
	Trails    map[string]trailsEntry       `json:"trails"`
	Sessions  map[string]wtfSession        `json:"sessions"`
	Cursors   map[string]trailsCursor      `json:"cursors,omitempty"`
	Branches  map[string]trailsBranchCache `json:"branches,omitempty"`
	Errors    []string                     `json:"errors,omitempty"`
}

type trailsEntry struct {
	URL           string                       `json:"url"`
	Repo          string                       `json:"repo"`
	Title         string                       `json:"title"`
	Status        string                       `json:"status"`
	Branch        string                       `json:"branch"`
	MetadataAt    int64                        `json:"metadataAt"`
	RetryAt       int64                        `json:"retryAt"`
	Attempts      int                          `json:"attempts"`
	MetadataError string                       `json:"metadataError,omitempty"`
	Associations  map[string]trailsAssociation `json:"associations"`
}

type trailsAssociation struct {
	FirstAt       int64  `json:"firstAt"`
	LastAt        int64  `json:"lastAt"`
	Branch        string `json:"branch"`
	Evidence      string `json:"evidence"`
	Source        string `json:"source"`
	BranchMatched bool   `json:"branchMatched"`
}

type trailsRow struct {
	Key         string
	Trail       trailsEntry
	SessionKeys []string
	Active      bool
	LastAt      int64
}

func newTrailsCatalog() trailsCatalog {
	return trailsCatalog{Version: 1, Trails: map[string]trailsEntry{}, Sessions: map[string]wtfSession{}}
}

func trailsDir(home string) string {
	return filepath.Join(home, "Library", "Application Support", "entire-tail", "trails")
}

func loadTrails(home string) (trailsCatalog, error) {
	data, err := os.ReadFile(filepath.Join(trailsDir(home), "catalog.json"))
	if os.IsNotExist(err) {
		return newTrailsCatalog(), nil
	}
	var c trailsCatalog
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	if err == nil && (c.Version != 1 || c.Trails == nil || c.Sessions == nil) {
		err = fmt.Errorf("invalid or unsupported trails catalog")
	}
	return c, err
}

func saveTrails(home string, catalog trailsCatalog) error {
	return writeTrailsJSON(filepath.Join(trailsDir(home), "catalog.json"), catalog)
}

func writeTrailsJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".trails-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func selectTrails(c trailsCatalog, now time.Time, query string) []trailsRow {
	var rows []trailsRow
	midnight := localMidnight(now.Unix(), now.Location())
	query = strings.ToLower(strings.TrimSpace(query))
	for key, trail := range c.Trails {
		row := trailsRow{Key: key, Trail: trail}
		search := trail.Title + " " + trail.Repo + " " + trail.URL
		for id, a := range trail.Associations {
			s := c.Sessions[id]
			row.LastAt = max(row.LastAt, a.LastAt)
			row.Active = row.Active || (a.BranchMatched && s.Active && a.Branch != "" && s.Branch == a.Branch && s.Repo == trail.Repo)
			row.SessionKeys = append(row.SessionKeys, id)
			search += " " + s.Name + " " + id
		}
		if !row.Active && row.LastAt < midnight {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(search), query) {
			continue
		}
		sort.Slice(row.SessionKeys, func(i, j int) bool {
			a, b := row.SessionKeys[i], row.SessionKeys[j]
			if c.Sessions[a].Active != c.Sessions[b].Active {
				return c.Sessions[a].Active
			}
			if trail.Associations[a].LastAt != trail.Associations[b].LastAt {
				return trail.Associations[a].LastAt > trail.Associations[b].LastAt
			}
			return a < b
		})
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Active != rows[j].Active {
			return rows[i].Active
		}
		if rows[i].LastAt != rows[j].LastAt {
			return rows[i].LastAt > rows[j].LastAt
		}
		return rows[i].Key < rows[j].Key
	})
	return rows
}
