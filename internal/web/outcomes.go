package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/audit"
	"github.com/sgracia13/Cowboy/internal/outcome"
)

// OutcomeSettings turns on the outcomes page.
type OutcomeSettings struct {
	State    *outcome.State
	Window   time.Duration
	Audit    *audit.Log
	Version  string
	MinFanIn func(repo string) int
	Sources  string // which incident tools are connected, for the page
	// Backfill starts rating the last few days of merged PRs in the
	// background; BackfillStatus says how it's going ("" = not running).
	Backfill       func(days int)
	BackfillStatus func() string
	// Now is the clock for the report (nil = the real time; a replay of
	// past history sets it to the end of that history).
	Now func() time.Time
}

func (o *OutcomeSettings) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func actor(r *http.Request) string {
	if u := r.Header.Get("X-Forwarded-User"); u != "" {
		return u
	}
	return "team"
}

func (s *Server) report() outcome.Report {
	o := s.Outcomes
	fan := map[string]int{}
	if o.MinFanIn != nil {
		for _, repo := range s.App.Store.Repos() {
			fan[repo] = o.MinFanIn(repo)
		}
	}
	return outcome.Build(s.App.Store.List("", ""), o.State, o.now(), o.Window, fan)
}

type outcomesPage struct {
	Report    outcome.Report
	Pending   []outcome.Outcome
	Recent    []outcome.Outcome
	Incidents []outcome.IncidentRecord
	AuditN    int
	AuditFP   string
	Sources   string
	Error     string
	CanFill   bool
	Filling   string
}

func (s *Server) outcomes(w http.ResponseWriter, r *http.Request) {
	o := s.Outcomes
	p := outcomesPage{Report: s.report(), Sources: o.Sources, Error: r.URL.Query().Get("error")}
	all := o.State.Outcomes("", 0)
	for _, x := range all {
		if x.Status == outcome.Suggested {
			p.Pending = append(p.Pending, x)
		} else if len(p.Recent) < 50 {
			p.Recent = append(p.Recent, x)
		}
	}
	p.Incidents = o.State.Incidents()
	if len(p.Incidents) > 20 {
		p.Incidents = p.Incidents[:20]
	}
	p.AuditN, p.AuditFP = o.Audit.Fingerprint()
	p.CanFill = o.Backfill != nil
	if o.BackfillStatus != nil {
		p.Filling = o.BackfillStatus()
	}
	s.render(w, "outcomes.html", "outcomes", p)
}

func (s *Server) decideOutcome(w http.ResponseWriter, r *http.Request) {
	confirm := r.FormValue("confirm") == "yes"
	if err := s.App.DecideOutcome(r.FormValue("id"), confirm, actor(r)); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/outcomes"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) linkIncident(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.FormValue("pr")) // "acme/web#12" or "12"
	repo, num, ok := strings.Cut(ref, "#")
	if !ok {
		repo, num = r.FormValue("repo"), ref
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		http.Redirect(w, r, "/outcomes?error=Write+the+pull+request+as+owner/repo%2312", http.StatusSeeOther)
		return
	}
	if err := s.App.LinkIncident(r.FormValue("incident"), strings.TrimSpace(repo), n, actor(r)); err != nil {
		http.Redirect(w, r, "/outcomes?error="+strings.ReplaceAll(err.Error(), " ", "+"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/outcomes", http.StatusSeeOther)
}

func (s *Server) auditCSV(w http.ResponseWriter, r *http.Request) {
	path := s.Outcomes.Audit.Path()
	es, err := audit.Read(path)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	since, _ := time.Parse("2006-01-02", r.URL.Query().Get("since"))
	es = audit.Filter(es, since, r.URL.Query().Get("repo"))
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="cowboy-audit.json"`)
		json.NewEncoder(w).Encode(es)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="cowboy-audit.csv"`)
	audit.WriteCSV(w, es)
}

func (s *Server) benchmark(w http.ResponseWriter, r *http.Request) {
	o := s.Outcomes
	b := outcome.Benchmark(s.App.Store.List("", ""), o.State, o.now(), o.Window, o.Version)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="cowboy-benchmark.json"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(b)
}

// CSS is the dashboard's stylesheet (for pages rendered outside the server).
func CSS() string {
	b, _ := files.ReadFile("static/cowboy.css")
	return string(b)
}

func (s *Server) startBackfill(w http.ResponseWriter, r *http.Request) {
	if s.Outcomes.Backfill == nil {
		http.Error(w, "backfill isn't available here", 400)
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	if days <= 0 || days > 365 {
		days = 90
	}
	s.Outcomes.Backfill(days)
	http.Redirect(w, r, "/outcomes", http.StatusSeeOther)
}
