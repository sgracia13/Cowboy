package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/evaluate"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

// leaks are things that only show up on a page when a template or a
// format string went wrong.
var leaks = regexp.MustCompile(`template:|<no value>|%!|\{\{|NaN|\+Inf|map\[`)

func checkPage(t *testing.T, mux http.Handler, u string, want ...string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", u, nil))
	b := rec.Body.String()
	if rec.Code != 200 {
		t.Errorf("%s: status %d", u, rec.Code)
		return b
	}
	if m := leaks.FindString(b); m != "" {
		i := strings.Index(b, m)
		t.Errorf("%s: leaked %q near %q", u, m, b[max(0, i-80):min(len(b), i+80)])
	}
	for _, w := range want {
		if !strings.Contains(b, w) {
			t.Errorf("%s: missing %q", u, w)
		}
	}
	return b
}

// A fresh install: nothing sorted, nothing merged. Every page still reads
// sensibly and says what to do next.
func TestPagesWhenEmpty(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := app.New(f, st, nil)
	oc, _ := outcome.Open("")
	s := New(a)
	s.Outcomes = &OutcomeSettings{State: oc, Window: 30 * 24 * time.Hour, Backfill: func(int) {}}
	mux := http.NewServeMux()
	s.Routes(mux)
	checkPage(t, mux, "/", "0 pull requests in the corral", "Nothing here")
	checkPage(t, mux, "/outcomes", "Nothing to rate yet", "Rate the last 90 days")
	checkPage(t, mux, "/habits", "No repos yet")
	checkPage(t, mux, "/tally", "Nothing in the last 7 days")
	checkPage(t, mux, "/trail", "Nothing yet")
	checkPage(t, mux, "/rules", "No repos yet")
}

// Bad or missing input never shows a broken page.
func TestPagesWithBadInput(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := app.New(f, st, nil)
	mux := http.NewServeMux()
	New(a).Routes(mux)
	for _, u := range []string{"/pr", "/pr?repo=acme/app&n=abc", "/pr?repo=acme/app&n=-1", "/pr?repo=nope/nope&n=1", "/demo/host?n=zzz"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", u, nil))
		if rec.Code == 200 && leaks.MatchString(rec.Body.String()) || rec.Code >= 500 {
			t.Errorf("%s: %d", u, rec.Code)
		}
	}
	// the outcomes page is off without outcome tracking
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/outcomes", nil))
	if rec.Code == 200 {
		t.Error("/outcomes should not exist with outcomes off")
	}
}

// Every page with real-looking data: sorted PRs, merged PRs with trouble,
// a person's correction, a pending guess, an incident, a multi-repo setup.
func TestPagesWithData(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := app.New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	for _, n := range f.All() {
		a.HandlePR("acme/app", n)
	}
	a.Correct("acme/app", 506, score.Careful, "ann")
	oc, _ := outcome.Open("")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var hist []pipeline.Decision
	for i := 0; i < 400; i++ {
		repo := "o/one"
		if i%2 == 1 {
			repo = "o/two"
		}
		pen := []score.Pen{score.Easy, score.Normal, score.Careful, score.Normal}[i%4]
		merged := now.Add(-time.Duration(35+i%80) * 24 * time.Hour)
		reasons := []score.Reason{{Check: "size", Detail: "small", Effect: "info"}}
		if pen == score.Careful {
			reasons = append(reasons, score.Reason{Check: "sensitive", Detail: "touches a sensitive path: auth/x.go (auth/**)", Effect: "careful"})
		}
		hist = append(hist, pipeline.Decision{Repo: repo, Number: i + 1, Title: fmt.Sprintf("change %d with a long title that keeps going for a while to test wrapping", i), Pen: pen, State: "merged", Agent: []string{"", "claude-code", "copilot"}[i%3],
			Reasons: reasons, Signals: score.Signals{LinesChanged: 5 + i*13%900, Files: 1 + i%12, Paths: []string{"pkg/a.go"}}})
		oc.PutMerge(outcome.Merge{Repo: repo, Number: i + 1, MergedAt: merged, Title: "t"})
		if i%13 == 0 || (pen == score.Careful && i%7 == 0) {
			oc.Add(outcome.Outcome{Repo: repo, Number: i + 1, Kind: outcome.Revert, Status: outcome.Auto, At: merged.Add(48 * time.Hour), Evidence: fmt.Sprint("revert ", i)})
		}
		if i%29 == 0 {
			oc.Add(outcome.Outcome{Repo: repo, Number: i + 1, Kind: outcome.Fix, Status: outcome.Suggested, At: merged.Add(72 * time.Hour), Evidence: fmt.Sprint("fix ", i)})
		}
	}
	oc.AddIncident(outcome.IncidentRecord{ID: "inc-1", Title: "Checkout errors", Source: "sentry", Repo: "o/one", Started: now.Add(-40 * 24 * time.Hour), Received: now})
	s := New(a)
	s.Outcomes = &OutcomeSettings{State: oc, Window: 30 * 24 * time.Hour, History: func() []pipeline.Decision { return hist }, Now: func() time.Time { return now }}
	mux := http.NewServeMux()
	s.Routes(mux)
	checkPage(t, mux, "/", "pull requests in the corral", "Slow down. A code owner should look.")
	body := checkPage(t, mux, "/outcomes", "Do your rules earn their place?", "How each pen did", "What each careful rule caught", "Waiting for you", "Checkout errors", "Sensitive path")
	// the verdict sentence comes first, before any chart
	if i, j := strings.Index(body, `class="say"`), strings.Index(body, `class="bars"`); i < 0 || (j >= 0 && j < i) {
		t.Error("the answer should come before the charts")
	}
	// bar widths and dot positions stay inside their tracks
	for _, m := range regexp.MustCompile(`(?:width|left):(-?[0-9.]+)%`).FindAllStringSubmatch(body, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		if v < 0 || v > 100.01 {
			t.Errorf("chart geometry out of range: %s", m[0])
		}
	}
	checkPage(t, mux, "/pr?repo=acme/app&n=506", "A person moved it", "Why it’s careful")
	checkPage(t, mux, "/pr?repo=o/one&n=1", "After merge", "This pull request is from a replay")
	body = checkPage(t, mux, "/habits?repo=o/two", "o/two", "Tests with code", "Reviewable size", "Guide your agents", "cowboy:guide start", "Not enough evidence yet")
	if strings.Contains(body, "per person:") {
		t.Error("habits must stay team-wide")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/habits/guide.md?repo=o/one", nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "markdown") || !strings.Contains(rec.Body.String(), "## Working in o/one") {
		t.Errorf("guide download: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, u := range []string{"/tally", "/rules", "/trail", "/demo/host?n=507"} {
		checkPage(t, mux, u)
	}
}

func TestAnswerSentences(t *testing.T) {
	r := outcome.Report{WindowDays: 30}
	if a := makeAnswer(r, emptyResult(), 0); a.Say != "Nothing to rate yet." {
		t.Errorf("no history: %q", a.Say)
	}
	r.Matured = 100
	if a := makeAnswer(r, emptyResult(), 3); !strings.HasPrefix(a.Say, "Not enough trouble yet") || a.Waiting != 3 {
		t.Errorf("thin history: %+v", a)
	}
	if niceMax(0.26) != 0.3 || niceMax(0.9) != 1 || niceMax(0) != 0.01 {
		t.Errorf("niceMax: %v %v %v", niceMax(0.26), niceMax(0.9), niceMax(0))
	}
	if capFirst("ci loosened") != "CI loosened" || capFirst("") != "" || pctRate(0.016) != "1.6%" || pctRate(0.26) != "26%" {
		t.Error("labels")
	}
}

func emptyResult() evaluate.Result { return evaluate.Result{} }
