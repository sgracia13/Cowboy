package app

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/model"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

// End to end against the demo forge: sort, label, comment, review, correct, sweep.
func TestLoop(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})

	nums := f.All()
	for _, n := range nums {
		if _, _, err := a.HandlePR("acme/app", n); err != nil {
			t.Fatal(n, err)
		}
	}
	if got := f.Labels[507]; !reflect.DeepEqual(got, []string{"agent:codex", "cowboy:careful"}) {
		t.Errorf("507 labels = %v", got)
	}
	if got := f.Reviews[507]; !reflect.DeepEqual(got, []string{"maria"}) {
		t.Errorf("507 review = %v, want maria (CODEOWNERS src/auth/)", got)
	}
	if _, ok := f.Labels[520]; ok {
		t.Errorf("human PR 520 should be untouched")
	}
	if _, ok := f.Comments[498]; !ok {
		t.Errorf("498 should have a comment")
	}

	// re-sorting the same code is idempotent: one comment, updated in place
	a.HandlePR("acme/app", 498)
	if len(f.Comments) != 9 {
		t.Errorf("want 9 comments (one per agent PR), got %d", len(f.Comments))
	}

	// a human moves 506 to careful
	if err := a.Correct("acme/app", 506, score.Careful, "sam"); err != nil {
		t.Fatal(err)
	}
	if got := f.Labels[506]; !reflect.DeepEqual(got, []string{"agent:devin", "cowboy:careful"}) {
		t.Errorf("506 after move = %v", got)
	}
	// a re-sort of the same head keeps the human's pen
	a.HandlePR("acme/app", 506)
	if d, _ := st.Get("acme/app", 506); d.Corrected != score.Careful || f.Labels[506][1] != "cowboy:careful" {
		t.Errorf("correction lost on re-sort: %+v %v", d.Corrected, f.Labels[506])
	}

	// 503 has changes requested 8 days ago and nothing since
	a.Sweep()
	if d, _ := st.Get("acme/app", 503); !d.Stray {
		t.Errorf("503 should be a stray")
	}

	rows := a.Tally(st.List("", "")[0].DecidedAt.AddDate(0, 0, -1))
	total := 0
	for _, r := range rows {
		total += r.Total
	}
	if total != 9 || len(st.Corrections()) != 1 {
		t.Errorf("tally total %d corrections %d", total, len(st.Corrections()))
	}
}

// The model adds notes to the comment but never changes a pen.
func TestModelNeverChangesThePen(t *testing.T) {
	td := "../../testdata"
	run := func(withModel bool) map[int]string {
		f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
		st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
		a := New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
		if withModel {
			a.Model = model.Demo{Dir: filepath.Join(td, "prs")}
		}
		nums := f.All()
		pens := map[int]string{}
		for _, n := range nums {
			if d, ok, _ := a.HandlePR("acme/app", n); ok {
				pens[n] = string(d.Pen)
				if withModel && (d.Notes == nil || !strings.Contains(f.Comments[n], "local model notes")) {
					t.Errorf("#%d: notes missing from comment", n)
				}
			}
		}
		if withModel {
			if d, _ := st.Get("acme/app", 506); !d.Notes.Drift() {
				t.Errorf("506 should be flagged off task (keepalive change)")
			}
		}
		return pens
	}
	if a, b := run(false), run(true); !reflect.DeepEqual(a, b) {
		t.Fatalf("pens changed with the model on:\n%v\n%v", a, b)
	}
}

// The catch-up sweep sorts PRs whose webhooks never arrived, skips human PRs
// without refetching them, re-sorts new commits, and notices closes.
func TestCatchUp(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	st.AddRepo("acme/app")
	events, stop := a.Subscribe()
	defer stop()

	// no webhooks at all: the sweep finds every agent PR
	caught := a.Reconcile()
	if len(caught) != 9 || len(st.List("acme/app", "open")) != 9 {
		t.Fatalf("caught %d: %v", len(caught), caught)
	}
	if _, ok := st.Get("acme/app", 520); ok {
		t.Fatal("human PR 520 should not be stored")
	}
	// nothing changed: a second sweep does nothing and writes nothing
	writes := len(f.Log)
	if again := a.Reconcile(); len(again) != 0 || len(f.Log) != writes {
		t.Fatalf("second sweep should be a no-op, got %v", again)
	}
	// a PR closed while Cowboy wasn't listening
	f.Close(498, true)
	a.Reconcile()
	if d, _ := st.Get("acme/app", 498); d.State != "merged" {
		t.Fatalf("498 state = %q, want merged", d.State)
	}
	// events were published for the live dashboard
	kinds := map[string]int{}
	for len(events) > 0 {
		kinds[(<-events).Kind]++
	}
	if kinds["sorted"] != 9 || kinds["caught-up"] != 9 || kinds["closed"] != 1 {
		t.Fatalf("events = %v", kinds)
	}
}
