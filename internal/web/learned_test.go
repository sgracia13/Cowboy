package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/audit"
	"github.com/sgracia13/Cowboy/internal/fit"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

// The whole loop through the page: look for rules, start a trial, promote,
// and the promoted rule sorts the next PR. Every move is audited.
func TestLearnedRulesThroughThePage(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := app.New(f, st, nil)
	oc, _ := outcome.Open("")
	book, _ := fit.OpenBook(filepath.Join(t.TempDir(), "learned.json"))
	a.Learned = book
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var hist []pipeline.Decision
	for i := 0; i < 360; i++ {
		merged := now.Add(-time.Duration(360-i) * 6 * time.Hour)
		p, bad := fmt.Sprintf("web/page%d.go", i%23), i%50 == 7
		if i%8 == 0 {
			p, bad = "billing/charge.go", i%16 == 0
		}
		hist = append(hist, pipeline.Decision{Repo: "acme/app", Number: i + 1, State: "merged", Pen: score.Normal,
			Signals: score.Signals{Paths: []string{p}, Files: 1, LinesChanged: 20, Classes: map[string]int{"source": 1}}})
		oc.PutMerge(outcome.Merge{Repo: "acme/app", Number: i + 1, MergedAt: merged})
		if bad {
			oc.Add(outcome.Outcome{Repo: "acme/app", Number: i + 1, Kind: outcome.Revert, Status: outcome.Auto, At: merged.Add(48 * time.Hour), Evidence: fmt.Sprint(i)})
		}
	}
	s := New(a)
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	al, _ := audit.Open(auditPath)
	s.Outcomes = &OutcomeSettings{State: oc, Window: 30 * 24 * time.Hour, Learned: book, Audit: al,
		History: func() []pipeline.Decision { return hist }, Now: func() time.Time { return now }}
	mux := http.NewServeMux()
	s.Routes(mux)
	do := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	page := func(q string) string {
		rec := do("GET", "/outcomes"+q, nil)
		b := rec.Body.String()
		if rec.Code != 200 || strings.Contains(b, "template:") {
			t.Fatalf("%d %s", rec.Code, b[:min(500, len(b))])
		}
		i := strings.Index(b, `id="learned"`)
		if i < 0 {
			t.Fatal("no learned section")
		}
		return b[i : i+strings.Index(b[i:], "</section>")]
	}
	if sec := page(""); !strings.Contains(sec, "nothing learned yet") {
		t.Errorf("empty book:\n%s", sec)
	}
	rec := do("POST", "/outcomes/learn", url.Values{})
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.Contains(loc, "worth+a+trial") {
		t.Fatalf("learn: %d %s", rec.Code, loc)
	}
	sec := page("")
	if !strings.Contains(sec, "touches billing/**") && !strings.Contains(sec, "changes billing/charge.go") {
		t.Fatalf("no billing suggestion:\n%s", sec)
	}
	if !strings.Contains(sec, "start trial") || strings.Contains(sec, "use for sorting") {
		t.Errorf("a suggestion should offer only a trial:\n%s", sec)
	}
	fact := book.List("acme/app")[0].Fact
	if len(a.Config("acme/app").Learned) != 0 {
		t.Fatal("a suggestion changed the sorting")
	}
	// skipping the trial is refused
	rec = do("POST", "/outcomes/rule", url.Values{"repo": {"acme/app"}, "fact": {fact}, "to": {"active"}})
	if !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Errorf("suggested -> active allowed: %s", rec.Header().Get("Location"))
	}
	do("POST", "/outcomes/rule", url.Values{"repo": {"acme/app"}, "fact": {fact}, "to": {"shadow"}})
	sec = page("")
	if !strings.Contains(sec, "on trial since") || !strings.Contains(sec, "too early") || !strings.Contains(sec, "use for sorting") {
		t.Errorf("trial row:\n%s", sec)
	}
	if len(a.Config("acme/app").Learned) != 0 {
		t.Fatal("a rule on trial changed the sorting")
	}
	do("POST", "/outcomes/rule", url.Values{"repo": {"acme/app"}, "fact": {fact}, "to": {"active"}})
	cfg := a.Config("acme/app")
	if len(cfg.Learned) != 1 {
		t.Fatalf("promoted rule not in the config: %+v", cfg.Learned)
	}
	pen, reasons := score.Decide(score.Signals{Paths: []string{"billing/charge.go"}, Files: 1, LinesChanged: 3, Dirs: 1, Classes: map[string]int{"source": 1}}, cfg)
	if pen != score.Careful || !strings.Contains(fmt.Sprint(reasons), "learned") {
		t.Errorf("promoted rule didn't sort: %s %+v", pen, reasons)
	}
	if sec := page(""); !strings.Contains(sec, "in use") || !strings.Contains(sec, "stop using") {
		t.Errorf("active row:\n%s", sec)
	}
	do("POST", "/outcomes/rule", url.Values{"repo": {"acme/app"}, "fact": {fact}, "to": {"shadow"}, "from": {"active"}})
	if len(a.Config("acme/app").Learned) != 0 {
		t.Error("stop using left the rule sorting")
	}
	es, _ := audit.Read(auditPath)
	var kinds []string
	for _, e := range es {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); got != "rules_learned,rule_shadow,rule_active,rule_shadow" {
		t.Errorf("audit record: %s", got)
	}
}
