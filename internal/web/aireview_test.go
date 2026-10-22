package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/reviewers"
)

func init() { reviewers.Add("demo-reviewer[bot]=Demo reviewer") }

// An open PR's AI findings are read live from the git host and shown with
// the reviewer's (still empty) track record.
func TestPRShowsAIFindings(t *testing.T) {
	mux, _ := corralMux(t)
	checkPage(t, mux, "/pr?repo=acme/app&n=515", `id="ai"`, "Demo reviewer", "billing/totals.py", "hides every error",
		"read from the pull request just now", "No track record yet")
	body := checkPage(t, mux, "/pr?repo=acme/app&n=512", "Demo reviewer", "reviewed, flagged nothing")
	if strings.Contains(body, "Called it</button>") {
		t.Error("an open PR has nothing to call")
	}
}

// After merge with trouble, a person marks the warning that called it; the
// mark is saved, counted, shown, and in the API.
func TestCalledIt(t *testing.T) {
	_, a := corralMux(t)
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	oc, _ := outcome.Open(filepath.Join(t.TempDir(), "o.json"))
	a.Outcomes = oc
	merged := now.Add(-40 * 24 * time.Hour)
	oc.PutMerge(outcome.Merge{Repo: "acme/app", Number: 507, MergedAt: merged, AIKnown: true, AIReviewers: []string{"Demo reviewer"},
		AIFindings: []outcome.AIFinding{{Reviewer: "Demo reviewer", Path: "src/auth/session.ts", Line: 7, Text: "falls back to the old store", At: merged}}})
	oc.Add(outcome.Outcome{Repo: "acme/app", Number: 507, Kind: outcome.Revert, Status: outcome.Auto, At: merged.Add(48 * time.Hour), Evidence: "reverted"})
	s := New(a)
	s.Outcomes = &OutcomeSettings{State: oc, Window: 30 * 24 * time.Hour, Now: func() time.Time { return now }}
	mux := http.NewServeMux()
	s.Routes(mux)
	checkPage(t, mux, "/pr?repo=acme/app&n=507", "had trouble after merge", "Called it</button>", "falls back to the old store")
	w := do(mux, "POST", "/pr/calledit", url.Values{"repo": {"acme/app"}, "n": {"507"}, "i": {"0"}, "on": {"yes"}}, map[string]string{"X-Forwarded-User": "ann"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("called it: %d %s", w.Code, w.Body)
	}
	checkPage(t, mux, "/pr?repo=acme/app&n=507&called=1", "<b>called it</b>, marked by ann", "Undo</button>", "Called it 1 time")
	checkPage(t, mux, "/outcomes", `id="ai-reviewers"`, "Demo reviewer", "Not enough history yet")
	code, m := getJSON(t, mux, "/api/v1/reviewers?repo=acme/app", nil)
	recs, _ := m["reviewers"].([]any)
	if code != 200 || len(recs) != 1 || recs[0].(map[string]any)["called_it"].(float64) != 1 || recs[0].(map[string]any)["verdict"] != "too-early" {
		t.Errorf("api reviewers: %d %v", code, m)
	}
	_, m = getJSON(t, mux, "/api/v1/pr?repo=acme/app&n=507", nil)
	ai := m["ai_review"].(map[string]any)
	fs := ai["findings"].([]any)
	if ai["known"] != true || len(fs) != 1 || fs[0].(map[string]any)["called_it"] != true {
		t.Errorf("api pr ai_review: %v", ai)
	}
	w = do(mux, "POST", "/pr/calledit", url.Values{"repo": {"acme/app"}, "n": {"507"}, "i": {"5"}}, nil)
	if w.Code != 400 {
		t.Errorf("a finding that isn't there: %d", w.Code)
	}
	if code, _ := getJSON(t, http.Handler(func() *http.ServeMux { m, _ := corralMux(t); return m }()), "/api/v1/reviewers", nil); code != 404 {
		t.Errorf("reviewers with tracking off: %d", code)
	}
}
