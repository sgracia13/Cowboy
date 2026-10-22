package web

// The versioned read API for other tools (docs/api.md). Everything here only
// reads. Tools sign in with API_TOKEN (Authorization: Bearer …); a browser
// session works too.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/hooks"
	"github.com/sgracia13/Cowboy/internal/outcome"
)

// APIVersion is in every response; fields are only added within a version.
const APIVersion = 1

type apiPR struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	DecidedAt time.Time `json:"decided_at"`
	*hooks.PR
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	e.Encode(v)
}

func apiError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"v": APIVersion, "error": msg})
}

// GET /api/v1/prs?repo=&state=open|merged|closed|all&pen=&agent=
func (s *Server) apiPRs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	switch state {
	case "", "open":
		state = "open"
	case "all":
		state = ""
	case "merged", "closed":
	default:
		apiError(w, 400, "state is open, merged, closed or all")
		return
	}
	pen, agent := q.Get("pen"), q.Get("agent")
	out := []apiPR{}
	for _, d := range s.App.Store.List(q.Get("repo"), state) {
		p := s.App.PRInfo(d.Repo, d.Number)
		if p == nil || (pen != "" && p.Pen != pen) {
			continue
		}
		if agent != "" && !(strings.EqualFold(agent, p.Agent) || (agent == "person" && p.Agent == "")) {
			continue
		}
		out = append(out, apiPR{Repo: d.Repo, Number: d.Number, DecidedAt: d.DecidedAt, PR: p})
	}
	writeJSON(w, 200, map[string]any{"v": APIVersion, "prs": out})
}

// GET /api/v1/pr?repo=&n=
func (s *Server) apiPR(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	d, ok := s.App.Store.Get(repo, n)
	if !ok {
		apiError(w, 404, "Cowboy hasn't seen that pull request")
		return
	}
	type apiOutcome struct {
		Kind     string    `json:"kind"`
		Status   string    `json:"status"`
		Counts   bool      `json:"counts"` // counted as trouble (found automatically, or confirmed)
		At       time.Time `json:"at"`
		Evidence string    `json:"evidence"`
		Link     string    `json:"link,omitempty"`
	}
	var outs []apiOutcome
	if s.Outcomes != nil {
		for _, o := range s.Outcomes.State.Outcomes(d.Repo, d.Number) {
			outs = append(outs, apiOutcome{o.Kind, o.Status, o.Counts(), o.At, o.Evidence, o.Link})
		}
	}
	ai := s.App.AIReviewOf(d.Repo, d.Number)
	type apiFinding struct {
		Reviewer string    `json:"reviewer"`
		Path     string    `json:"path,omitempty"`
		Line     int       `json:"line,omitempty"`
		Summary  string    `json:"summary"`
		At       time.Time `json:"at"`
		CalledIt bool      `json:"called_it"`
	}
	aiOut := map[string]any{"known": ai.Known, "reviewers": nonNil(ai.Reviewers), "findings": []apiFinding{}}
	var fs []apiFinding
	for _, f := range ai.Findings {
		fs = append(fs, apiFinding{f.Reviewer, f.Path, f.Line, f.Text, f.At, f.CalledBy != ""})
	}
	if fs != nil {
		aiOut["findings"] = fs
	}
	writeJSON(w, 200, map[string]any{"v": APIVersion, "repo": d.Repo, "number": d.Number, "decided_at": d.DecidedAt,
		"pr": s.App.PRInfo(d.Repo, d.Number), "reasons": d.Reasons, "outcomes": outs, "ai_review": aiOut})
}

// GET /api/v1/rules?repo=
func (s *Server) apiRules(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		apiError(w, 400, "repo is required")
		return
	}
	writeJSON(w, 200, map[string]any{"v": APIVersion, "repo": repo, "rules": s.App.Config(repo)})
}

// GET /api/v1/scorecards?repo=&days=
//
// For each agent (and "person"): merged pull requests whose trouble window
// has passed, how many had trouble, and a 95% range, overall and by area of
// the code. Small counts give wide ranges: compare ranges, not just rates.
func (s *Server) apiScorecards(w http.ResponseWriter, r *http.Request) {
	if s.Outcomes == nil {
		apiError(w, 404, "after-merge tracking is off (OUTCOMES=off)")
		return
	}
	repo := r.URL.Query().Get("repo")
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	o := s.Outcomes
	now := o.now()
	var ds = s.history()
	if repo != "" || days > 0 {
		var keep = ds[:0:0]
		for _, d := range ds {
			if repo != "" && !strings.EqualFold(d.Repo, repo) {
				continue
			}
			keep = append(keep, d)
		}
		ds = keep
	}
	merged := map[string]time.Time{}
	for _, m := range o.State.Merges("") {
		merged[strings.ToLower(m.Repo)+"#"+strconv.Itoa(m.Number)] = m.MergedAt
	}
	agentOf := map[string]string{}
	for _, d := range ds {
		a := d.Agent
		if a == "" {
			a = "person"
		}
		agentOf[strings.ToLower(d.Repo)+"#"+strconv.Itoa(d.Number)] = a
	}
	type card struct {
		Agent string       `json:"agent"`
		Rate  outcome.Rate `json:"rate"`
	}
	tot := map[string]*outcome.Rate{}
	for _, it := range outcome.Items(ds, o.State, now, o.Window, nil) {
		k := strings.ToLower(it.Repo) + "#" + strconv.Itoa(it.Number)
		if days > 0 && now.Sub(merged[k]) > time.Duration(days)*24*time.Hour+o.Window {
			continue
		}
		a := agentOf[k]
		if tot[a] == nil {
			tot[a] = &outcome.Rate{}
		}
		tot[a].N++
		if it.Bad {
			tot[a].Bad++
		}
	}
	cards := []card{}
	for a, rt := range tot {
		if rt.N > 0 {
			rt.P = float64(rt.Bad) / float64(rt.N)
		}
		rt.Low, rt.High = outcome.Wilson(rt.Bad, rt.N)
		cards = append(cards, card{a, *rt})
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].Rate.N > cards[j].Rate.N })
	areas := []outcome.AgentRow{}
	if days == 0 {
		areas = outcome.Build(ds, o.State, now, o.Window, nil).Agents
	}
	writeJSON(w, 200, map[string]any{"v": APIVersion, "repo": repo, "window_days": int(o.Window.Hours() / 24),
		"note":   "Rated pull requests only (merged at least the trouble window ago). Trouble = reverted, broke main, or a confirmed fix or incident. Compare the ranges: a small n gives a wide one.",
		"agents": cards, "by_area": areas})
}

func nonNil(x []string) []string {
	if x == nil {
		return []string{}
	}
	return x
}

// GET /api/v1/reviewers?repo=
//
// Each AI reviewer's track record: of the merged PRs it reviewed whose
// trouble window has passed, the trouble rate where it flagged something
// and where it stayed quiet, with 95% ranges, and how many of its warnings
// a person marked as naming what went wrong.
func (s *Server) apiReviewers(w http.ResponseWriter, r *http.Request) {
	if s.Outcomes == nil {
		apiError(w, 404, "after-merge tracking is off (OUTCOMES=off)")
		return
	}
	o := s.Outcomes
	recs := outcome.ReviewerRecords(o.State, r.URL.Query().Get("repo"), o.now(), o.Window)
	if recs == nil {
		recs = []outcome.ReviewerRecord{}
	}
	writeJSON(w, 200, map[string]any{"v": APIVersion, "repo": r.URL.Query().Get("repo"), "window_days": int(o.Window.Hours() / 24),
		"note":      "A reviewer worth listening to flags the pull requests that go wrong more often than the ones that don't: compare flagged with quiet, and the ranges, not just the rates. verdict is too-early until both sides have 20 rated PRs and 5 had trouble.",
		"reviewers": recs})
}
