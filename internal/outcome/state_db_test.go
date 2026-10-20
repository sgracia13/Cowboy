package outcome

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/db"
)

func reopen(t *testing.T, path string) *State {
	t.Helper()
	d, err := db.Open(db.PathFor(path))
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// An outcomes.json from before SQLite is imported once; every kind of
// change after that survives a restart, including the watcher's batched
// ones once flushed.
func TestOutcomesImportAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outcomes.json")
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	old, _ := Open("")
	old.PutMerge(Merge{Repo: "o/r", Number: 1, MergedAt: at, Title: "one"})
	old.Add(Outcome{Repo: "o/r", Number: 1, Kind: Revert, Status: Auto, At: at.Add(time.Hour), Evidence: "revert"})
	old.AddIncident(IncidentRecord{ID: "inc-1", Title: "down", Repo: "o/r", Started: at})
	old.setScan("o/r", Scan{Commits: at})
	if err := old.ExportJSON(path); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Merge("o/r", 1); !ok || len(s.Outcomes("", 0)) != 1 || len(s.Incidents()) != 1 || s.scan("o/r").Commits.IsZero() {
		t.Fatal("old outcomes.json not imported")
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Error("old file should be kept as .migrated")
	}

	// changes of every kind
	s.PutMerge(Merge{Repo: "o/r", Number: 2, MergedAt: at, Title: "two"})
	s.update("o/r", 1, func(m *Merge) { m.Release = "v1" })
	o, _, _ := s.Add(Outcome{Repo: "o/r", Number: 2, Kind: Fix, Status: Suggested, At: at.Add(2 * time.Hour), Evidence: "fix"})
	s.Decide(o.ID, true, "ann")
	s.setScan("o/r", Scan{Commits: at.Add(48 * time.Hour)})
	s.Rewind("o/r", at.Add(24*time.Hour))
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	s2 := reopen(t, path)
	if m, ok := s2.Merge("o/r", 2); !ok || m.Title != "two" {
		t.Error("new merge lost")
	}
	if m, _ := s2.Merge("o/r", 1); m.Release != "v1" {
		t.Error("merge update lost")
	}
	if os := s2.Outcomes("o/r", 2); len(os) != 1 || os[0].Status != Confirmed || os[0].By != "ann" {
		t.Errorf("decision lost: %+v", os)
	}
	if got := s2.scan("o/r").Commits; !got.Equal(at.Add(24 * time.Hour)) {
		t.Errorf("rewind lost: %v", got)
	}
	// unflushed batch changes are written by the next save
	s2.PutMerge(Merge{Repo: "o/r", Number: 3, MergedAt: at})
	s2.Add(Outcome{Repo: "o/r", Number: 3, Kind: Revert, Status: Auto, At: at, Evidence: "r3"})
	s3 := reopen(t, path)
	if _, ok := s3.Merge("o/r", 3); !ok {
		t.Error("a merge recorded before the next save was lost")
	}
}

func TestLoadIsMemoryOnly(t *testing.T) {
	s, err := Load([]byte(`{"merges":{"o/r#1":{"repo":"o/r","number":1}},"outcomes":[],"incidents":[],"scans":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Merge("o/r", 1); !ok {
		t.Fatal("not loaded")
	}
	if _, _, err := s.Add(Outcome{Repo: "o/r", Number: 1, Kind: Revert, Status: Auto, Evidence: "x"}); err != nil {
		t.Fatal(err)
	}
}
