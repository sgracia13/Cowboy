package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sgracia13/Cowboy/internal/db"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

// An install from before SQLite keeps everything: the old cowboy.json is
// imported once and kept as cowboy.json.migrated.
func TestImportsTheOldJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cowboy.json")
	old := data{
		Decisions: map[string]pipeline.Decision{
			"acme/app#1": {Repo: "acme/app", Number: 1, Title: "one", Pen: score.Easy, State: "open"},
			"acme/app#2": {Repo: "acme/app", Number: 2, Title: "two", Pen: score.Careful, Corrected: score.Normal, State: "merged"},
		},
		Corrections: []Correction{{Repo: "acme/app", Number: 2, From: score.Careful, To: score.Normal, By: "ann"}},
		Repos:       []string{"acme/app", "acme/web"},
	}
	b, _ := json.Marshal(old)
	os.WriteFile(path, b, 0o600)

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := s.Get("acme/app", 2); !ok || d.Corrected != score.Normal || d.Title != "two" {
		t.Fatalf("decision not imported: %+v", d)
	}
	if len(s.Corrections()) != 1 || len(s.Repos()) != 2 {
		t.Fatalf("corrections %v repos %v", s.Corrections(), s.Repos())
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Error("the old file should be kept as .migrated")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the old file should be moved aside, so it isn't imported again")
	}
	if _, err := os.Stat(filepath.Join(dir, "cowboy.db")); err != nil {
		t.Error("cowboy.db should sit next to the data path")
	}

	// changes persist across a restart, and the import doesn't run twice
	s.Put(pipeline.Decision{Repo: "acme/app", Number: 3, Title: "three", Pen: score.Normal})
	s.SetState("acme/app", 1, "merged")
	s.Correct("acme/app", 3, score.Careful, "bo")
	s.RemoveRepo("acme/web")
	reopen(t, path)
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := s2.Get("acme/app", 1); d.State != "merged" {
		t.Error("state change lost")
	}
	if d, _ := s2.Get("acme/app", 3); d.Corrected != score.Careful {
		t.Error("correction lost")
	}
	if cs := s2.Corrections(); len(cs) != 2 || cs[1].By != "bo" {
		t.Errorf("corrections after restart: %+v", cs)
	}
	if r := s2.Repos(); len(r) != 1 || r[0] != "acme/app" {
		t.Errorf("repos after restart: %v", r)
	}
	if len(s2.List("", "")) != 3 {
		t.Errorf("decisions after restart: %d", len(s2.List("", "")))
	}
}

// reopen simulates a restart: close the shared connection so Open reads
// the file again.
func reopen(t *testing.T, path string) {
	t.Helper()
	d, err := db.Open(db.PathFor(path))
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
}

func TestExportJSONRoundTrips(t *testing.T) {
	s, _ := Open("")
	s.Put(pipeline.Decision{Repo: "o/r", Number: 7, Title: "x", Pen: score.Easy})
	out := filepath.Join(t.TempDir(), "export.json")
	if err := s.ExportJSON(out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	var d data
	if err := json.Unmarshal(b, &d); err != nil || d.Decisions["o/r#7"].Title != "x" {
		t.Fatalf("export: %v %+v", err, d)
	}
}

// Thousands of decisions save quickly: each Put writes one row, not the
// whole store.
func TestManyPutsStayFast(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cowboy.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		if err := s.Put(pipeline.Decision{Repo: "o/r", Number: i, Title: "pull request with a reasonably long title", Pen: score.Normal,
			Signals: score.Signals{Paths: []string{"a/b/c.go", "a/b/d.go", "e/f.go"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(s.List("o/r", "")); n != 2000 {
		t.Fatal(n)
	}
}
