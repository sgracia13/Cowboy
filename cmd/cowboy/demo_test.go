package main

import (
	"strings"
	"testing"
	"time"
)

// The demo's outcomes page loads the replayed history built into the
// binary, and that history carries no author names.
func TestDemoHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	dir, err := unpackDemo()
	if err != nil {
		t.Fatal(err)
	}
	ui, err := demoHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	ds := ui.History()
	if len(ds) < 2000 {
		t.Fatalf("only %d PRs in the demo history", len(ds))
	}
	for _, d := range ds {
		if d.Author != "" || d.Body != "" {
			t.Fatalf("%s#%d keeps an author or body", d.Repo, d.Number)
		}
		if strings.Contains(strings.ToLower(d.Title), "co-authored-by") {
			t.Fatalf("%s#%d title names a co-author", d.Repo, d.Number)
		}
	}
	for _, m := range ui.State.Merges("") {
		if m.Author != "" || len(m.Approvers) > 0 {
			t.Fatalf("merge record for #%d keeps people", m.Number)
		}
	}
	if ui.Now().Before(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("history clock %v is too early", ui.Now())
	}
}
