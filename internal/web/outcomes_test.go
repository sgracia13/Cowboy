package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

// outcomesPageWith renders (the store saves on every Put, so keep n small)
// /outcomes for n merged PRs; pen and bad decide
// each PR's pen and whether it had trouble.
func outcomesPageWith(t *testing.T, n int, pen func(i int) score.Pen, bad func(i int) bool) string {
	t.Helper()
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := app.New(f, st, nil)
	oc, _ := outcome.Open("")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		merged := now.Add(-time.Duration(40+i%60) * 24 * time.Hour)
		d := pipeline.Decision{Repo: "o/r", Number: i + 1, Title: fmt.Sprintf("pr %d", i), Pen: pen(i), State: "merged",
			Signals: score.Signals{LinesChanged: 10 + (i*37)%400, Files: 1 + i%9}}
		st.Put(d)
		oc.PutMerge(outcome.Merge{Repo: "o/r", Number: i + 1, MergedAt: merged})
		if bad(i) {
			oc.Add(outcome.Outcome{Repo: "o/r", Number: i + 1, Kind: outcome.Revert, Status: outcome.Auto, At: merged.Add(48 * time.Hour), Evidence: fmt.Sprint(i)})
		}
	}
	s := New(a)
	s.Outcomes = &OutcomeSettings{State: oc, Window: 30 * 24 * time.Hour, Now: func() time.Time { return now }}
	mux := http.NewServeMux()
	s.Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/outcomes", nil))
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, "template:") {
		t.Fatalf("%d %s", rec.Code, body[:min(400, len(body))])
	}
	return body
}

func pens(i int) score.Pen {
	switch i % 10 {
	case 0:
		return score.Careful
	case 1, 2, 3:
		return score.Easy
	}
	return score.Normal
}

func TestOutcomesCheckInsufficient(t *testing.T) {
	body := outcomesPageWith(t, 300, pens, func(i int) bool { return i%60 == 0 })
	if !strings.Contains(body, "insufficient evidence") || !strings.Contains(body, "of 20 needed") {
		t.Error("few events should say insufficient evidence")
	}
	if strings.Contains(body, "outperformed") {
		t.Error("claimed a win on 5 events")
	}
}

func TestOutcomesCheckOutperformed(t *testing.T) {
	// careful PRs have trouble far more often
	body := outcomesPageWith(t, 1000, pens, func(i int) bool { return (i%10 == 0 && i%20 == 0) || i%300 == 7 })
	if !strings.Contains(body, "<b>outperformed</b>: careful prs had trouble") {
		t.Errorf("a strong rule should outperform chance:\n%s", section(body))
	}
}

func TestOutcomesCheckNoAdvantage(t *testing.T) {
	// trouble unrelated to the pen
	body := outcomesPageWith(t, 1000, pens, func(i int) bool { return (i*7919)%37 == 3 })
	s := section(body)
	if !strings.Contains(s, "no demonstrated advantage") || strings.Contains(s, "<b>outperformed</b>: careful") {
		t.Errorf("a coin-flip rule shouldn't outperform:\n%s", s)
	}
}

func section(body string) string {
	i := strings.Index(body, `id="check"`)
	if i < 0 {
		return "(no check section)"
	}
	j := strings.Index(body[i:], "</section>")
	return body[i : i+j]
}
