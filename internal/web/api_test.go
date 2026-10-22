package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

func getJSON(t *testing.T, h http.Handler, u string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	w := do(h, "GET", u, nil, hdr)
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestAPIPRs(t *testing.T) {
	mux, a := corralMux(t)
	a.Correct("acme/app", 512, score.Careful, "ann")
	code, m := getJSON(t, mux, "/api/v1/prs", nil)
	prs, _ := m["prs"].([]any)
	if code != 200 || m["v"].(float64) != APIVersion || len(prs) < 5 {
		t.Fatalf("%d %v", code, m)
	}
	_, m = getJSON(t, mux, "/api/v1/prs?pen=careful", nil)
	for _, p := range m["prs"].([]any) {
		if p.(map[string]any)["pen"] != "careful" {
			t.Errorf("pen filter: %v", p)
		}
	}
	_, m = getJSON(t, mux, "/api/v1/prs?agent=codex", nil)
	for _, p := range m["prs"].([]any) {
		if p.(map[string]any)["agent"] != "codex" {
			t.Errorf("agent filter: %v", p)
		}
	}
	if code, _ := getJSON(t, mux, "/api/v1/prs?state=sideways", nil); code != 400 {
		t.Errorf("bad state: %d", code)
	}
	code, m = getJSON(t, mux, "/api/v1/pr?repo=acme/app&n=512", nil)
	pr := m["pr"].(map[string]any)
	if code != 200 || pr["pen"] != "careful" || pr["moved_by_person"] != true || m["reasons"] == nil {
		t.Errorf("pr: %d %v", code, m)
	}
	if code, _ := getJSON(t, mux, "/api/v1/pr?repo=acme/app&n=4", nil); code != 404 {
		t.Errorf("unknown pr: %d", code)
	}
	if code, m := getJSON(t, mux, "/api/v1/rules?repo=acme/app", nil); code != 200 || m["rules"] == nil {
		t.Errorf("rules: %d %v", code, m)
	}
	if code, _ := getJSON(t, mux, "/api/v1/rules", nil); code != 400 {
		t.Errorf("rules without repo: %d", code)
	}
	if code, _ := getJSON(t, mux, "/api/v1/scorecards", nil); code != 404 {
		t.Errorf("scorecards with tracking off: %d", code)
	}
}

func TestAPIScorecards(t *testing.T) {
	_, a := corralMux(t)
	oc, _ := outcome.Open("")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var hist []pipeline.Decision
	for i := 1; i <= 60; i++ {
		agent := []string{"", "codex", "claude-code"}[i%3]
		hist = append(hist, pipeline.Decision{Repo: "o/r", Number: i, State: "merged", Agent: agent, Pen: score.Normal, Signals: score.Signals{Paths: []string{"pkg/a.go"}}})
		merged := now.Add(-time.Duration(40+i) * 24 * time.Hour)
		oc.PutMerge(outcome.Merge{Repo: "o/r", Number: i, MergedAt: merged})
		if agent == "codex" && i%2 == 0 {
			oc.Add(outcome.Outcome{Repo: "o/r", Number: i, Kind: outcome.Revert, Status: outcome.Auto, At: merged.Add(24 * time.Hour), Evidence: "r"})
		}
	}
	s := New(a)
	s.Outcomes = &OutcomeSettings{State: oc, Window: 30 * 24 * time.Hour, History: func() []pipeline.Decision { return hist }, Now: func() time.Time { return now }}
	mux := http.NewServeMux()
	s.Routes(mux)
	code, m := getJSON(t, mux, "/api/v1/scorecards?repo=o/r", nil)
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	got := map[string]map[string]any{}
	for _, c := range m["agents"].([]any) {
		c := c.(map[string]any)
		got[c["agent"].(string)] = c["rate"].(map[string]any)
	}
	if codex := got["codex"]; codex["n"].(float64) != 20 || codex["bad"].(float64) != 10 || codex["low"].(float64) <= 0 || codex["high"].(float64) >= 1 {
		t.Errorf("codex: %v", codex)
	}
	if p := got["person"]; p["n"].(float64) != 20 || p["bad"].(float64) != 0 {
		t.Errorf("person: %v", p)
	}
	if m["note"] == nil || m["window_days"].(float64) != 30 {
		t.Errorf("%v", m)
	}
}

// The API token reads the API and nothing else.
func TestAPIToken(t *testing.T) {
	mux, _ := corralMux(t)
	h := Auth{Password: "pw", Token: "tok-123"}.Wrap(mux)
	if code, _ := getJSON(t, h, "/api/v1/prs", map[string]string{"Authorization": "Bearer tok-123"}); code != 200 {
		t.Errorf("token read: %d", code)
	}
	if code, _ := getJSON(t, h, "/api/v1/prs", map[string]string{"Authorization": "Bearer wrong"}); code != 401 {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := getJSON(t, h, "/api/v1/prs", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
	w := do(h, "GET", "/", nil, map[string]string{"Authorization": "Bearer tok-123"})
	if w.Code == 200 {
		t.Error("the token must not open the dashboard")
	}
	w = do(h, "POST", "/pr/move", url.Values{"repo": {"acme/app"}, "n": {"512"}, "to": {"easy"}}, map[string]string{"Authorization": "Bearer tok-123", "X-Cowboy-Move": "1"})
	if w.Code == http.StatusNoContent {
		t.Error("the token must not move pull requests")
	}
}
