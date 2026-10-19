package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/fit"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

// A rule a person promoted sorts the next pull request through the whole
// path (fetch, sort, label); one still on trial doesn't.
func TestPromotedRuleSortsLivePRs(t *testing.T) {
	td := "../../testdata"
	newApp := func(book *fit.Book) (*App, *forge.Fake) {
		f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
		st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
		a := New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
		a.Learned = book
		return a, f
	}
	// find a PR the default rules don't send to careful
	a, f := newApp(nil)
	var n int
	var fact string
	for _, x := range f.All() {
		d, ok, err := a.HandlePR("acme/app", x)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || d.Pen == score.Careful {
			continue
		}
		for _, ft := range score.Facts(d.Signals, a.Config("acme/app")) {
			if strings.HasPrefix(ft, "file:") {
				n, fact = x, ft
				break
			}
		}
		if n != 0 {
			break
		}
	}
	if n == 0 {
		t.Fatal("no non-careful sample PR with a source file")
	}
	now := time.Now()
	book, _ := fit.OpenBook("")
	book.Suggest(fit.Result{Repo: "acme/app", At: now, From: now.AddDate(0, 0, -90), Base: 0.05, Enough: true,
		Rules: []fit.Rule{{Fact: fact, N: 20, K: 6, Rate: 0.3, LiftLow: 3}}}, nil, now)
	book.Move("acme/app", fact, fit.Shadow, "ann", now)

	a, f = newApp(book)
	d, _, _ := a.HandlePR("acme/app", n)
	if d.Pen == score.Careful {
		t.Fatalf("#%d went careful while its rule was only on trial", n)
	}
	book.Move("acme/app", fact, fit.Active, "ann", now)
	a, f = newApp(book)
	d, _, err := a.HandlePR("acme/app", n)
	if err != nil {
		t.Fatal(err)
	}
	if d.Pen != score.Careful {
		t.Fatalf("#%d: pen %s with the promoted rule %s", n, d.Pen, fact)
	}
	if !strings.Contains(strings.Join(f.Labels[n], ","), "cowboy:careful") {
		t.Errorf("#%d labels %v", n, f.Labels[n])
	}
	if !strings.Contains(d.Comment, "learned from this repo's history") {
		t.Errorf("comment doesn't say why:\n%s", d.Comment)
	}
}
