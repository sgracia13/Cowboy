package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/hooks"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

// The real sorting flow, with a receiver standing in for an orchestrator.
func TestEventsReachAReceiver(t *testing.T) {
	var mu sync.Mutex
	var events []hooks.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !hooks.Verify("k", b, r.Header.Get("X-Cowboy-Signature")) {
			t.Error("unsigned or wrongly signed event")
		}
		var e hooks.Event
		json.Unmarshal(b, &e)
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}))
	defer srv.Close()

	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	a.Hooks = hooks.New(hooks.Config{URLs: []string{srv.URL}, Secret: "k"})
	a.PRLink = func(repo string, n int) string { return fmt.Sprintf("https://github.com/%s/pull/%d", repo, n) }

	for _, n := range f.All() {
		a.HandlePR("acme/app", n)
	}
	a.Correct("acme/app", 512, score.Careful, "ann")
	a.Closed("acme/app", 507, true)
	a.emit("sorted", "acme/app", 498, "", map[string]string{"pen": "easy", "backfill": "true"}) // rating history: not news
	a.emit("merge-recorded", "acme/app", 507, "", nil)                                          // bookkeeping: not sent
	a.Hooks.Close(context.Background())

	byType := map[string][]hooks.Event{}
	for _, e := range events {
		byType[e.Type] = append(byType[e.Type], e)
	}
	if len(byType[hooks.PRSorted]) < 5 || len(byType[hooks.PRMoved]) != 1 || len(byType[hooks.PRMerged]) != 1 {
		t.Fatalf("events by type: sorted %d, moved %d, merged %d", len(byType[hooks.PRSorted]), len(byType[hooks.PRMoved]), len(byType[hooks.PRMerged]))
	}
	for _, e := range byType[hooks.PRSorted] {
		if e.Number == 498 && e.Data["backfill"] == "true" {
			t.Error("a backfilled sort was sent")
		}
	}
	var s507 *hooks.Event
	for i, e := range byType[hooks.PRSorted] {
		if e.Number == 507 {
			s507 = &byType[hooks.PRSorted][i]
		}
	}
	if s507 == nil || s507.PR == nil || s507.PR.Pen != "careful" || s507.PR.Agent != "codex" || len(s507.PR.Careful) == 0 ||
		s507.PR.URL != "https://github.com/acme/app/pull/507" || s507.V != hooks.Version || s507.Data["pen"] != "careful" {
		t.Errorf("#507 sorted: %+v", s507)
	}
	m := byType[hooks.PRMoved][0]
	if m.Number != 512 || m.Actor != "ann" || m.Data["to"] != "careful" || m.PR.Pen != "careful" || !m.PR.MovedBy || m.PR.RulesPen == "careful" {
		t.Errorf("moved: %+v %+v", m, m.PR)
	}
	if mg := byType[hooks.PRMerged][0]; mg.Number != 507 || mg.PR.State != "merged" {
		t.Errorf("merged: %+v %+v", mg, mg.PR)
	}
}

// No hooks configured: nothing changes.
func TestNoHooksNoEvents(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	for _, n := range f.All() {
		if _, _, err := a.HandlePR("acme/app", n); err != nil {
			t.Fatal(err)
		}
	}
	if a.PRInfo("acme/app", 507) == nil || a.PRInfo("acme/app", 99999) != nil {
		t.Error("PRInfo")
	}
}
