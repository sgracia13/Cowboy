package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/audit"
	"github.com/sgracia13/Cowboy/internal/evaluate"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
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
	// Weight is set by a replay of a sampled history: each PR's sampling
	// weight and bootstrap stratum, so the comparison is weighted back to
	// the repo's real mix. nil on a live install.
	Weight func(repo string, n int) (float64, string)
	// History, when set, is where the page reads merged PRs from instead of
	// the store (the demo shows a replayed real history this way, apart from
	// its made-up corral). Note is shown at the top of the page.
	History func() []pipeline.Decision
	Note    string
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

// history is the merged PRs the outcomes page reads.
func (s *Server) history() []pipeline.Decision {
	if s.Outcomes.History != nil {
		return s.Outcomes.History()
	}
	return s.App.Store.List("", "")
}

func (s *Server) report() outcome.Report {
	o := s.Outcomes
	fan := map[string]int{}
	if o.MinFanIn != nil {
		for _, repo := range s.App.Store.Repos() {
			fan[repo] = o.MinFanIn(repo)
		}
	}
	return outcome.Build(s.history(), o.State, o.now(), o.Window, fan)
}

// check judges the careful and easy pens against simple alternatives with
// internal/evaluate, the same code the published evidence uses. The
// bootstrap takes a moment on big histories, so the result is kept until
// the inputs change.
func (s *Server) check() evaluate.Result {
	o := s.Outcomes
	items := outcome.Items(s.history(), o.State, o.now(), o.Window, o.Weight)
	bad := 0
	for _, it := range items {
		if it.Bad {
			bad++
		}
	}
	k := fmt.Sprintf("%d/%d/%s", len(items), bad, o.now().Format("2006-01-02"))
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if s.checkKey == k {
		return s.checkRes
	}
	s.checkRes, s.checkKey = evaluate.Evaluate(items, evaluate.Defaults()), k
	return s.checkRes
}

type outcomesPage struct {
	Answer      answerView
	Bars        barsView
	PenDots     dotsView
	RuleDots    dotsView
	Cards       []outcome.AgentRow // scorecards with enough PRs to read
	CardsRest   []outcome.AgentRow
	Note        string
	PendingMore int // suggestions past the first 10, not listed
	Report      outcome.Report
	Check       evaluate.Result
	Pending     []outcome.Outcome
	Recent      []outcome.Outcome
	Incidents   []outcome.IncidentRecord
	AuditN      int
	AuditFP     string
	Sources     string
	Error       string
	CanFill     bool
	Filling     string
}

func (s *Server) outcomes(w http.ResponseWriter, r *http.Request) {
	o := s.Outcomes
	p := outcomesPage{Note: o.Note, Report: s.report(), Check: s.check(), Sources: o.Sources, Error: r.URL.Query().Get("error")}
	all := o.State.Outcomes("", 0)
	for _, x := range all {
		if x.Status == outcome.Suggested {
			p.Pending = append(p.Pending, x)
		} else if len(p.Recent) < 50 {
			p.Recent = append(p.Recent, x)
		}
	}
	if len(p.Pending) > 6 {
		p.PendingMore = len(p.Pending) - 6
		p.Pending = p.Pending[:6]
	}
	p.Answer = makeAnswer(p.Report, p.Check, len(p.Pending)+p.PendingMore)
	p.Bars = makeBars(p.Check)
	var labels []string
	var rates []outcome.Rate
	bad, n := 0, 0
	for _, x := range p.Report.ByPen {
		labels = append(labels, capFirst(string(x.Pen)))
		rates = append(rates, x.Rate)
		bad += x.Rate.Bad
		n += x.Rate.N
	}
	avg := 0.0
	if n > 0 {
		avg = float64(bad) / float64(n)
	}
	p.PenDots = makeDots(labels, rates, avg)
	labels, rates = nil, nil
	for _, x := range p.Report.Rules {
		labels = append(labels, capFirst(x.Label))
		rates = append(rates, x.Rate)
	}
	p.RuleDots = makeDots(labels, rates, avg)
	for _, x := range p.Report.Agents {
		if x.Rate.N >= 20 {
			p.Cards = append(p.Cards, x)
		} else {
			p.CardsRest = append(p.CardsRest, x)
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
	b := outcome.Benchmark(s.history(), o.State, o.now(), o.Window, o.Version)
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
