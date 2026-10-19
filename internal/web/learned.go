package web

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/fit"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

// learnedRow is one learned rule on the outcomes page.
type learnedRow struct {
	fit.Learned
	Says  string // the fact in plain words
	Trial outcome.Trial
}

// facts lists a stored decision's facts under its repo's settings (the
// blast-radius bar is the only setting facts depend on).
func (s *Server) facts() outcome.FactsFunc {
	cfgs := map[string]config.Config{}
	return func(d pipeline.Decision) []string {
		k := strings.ToLower(d.Repo)
		cfg, ok := cfgs[k]
		if !ok {
			cfg = config.Default()
			if s.Outcomes.MinFanIn != nil {
				cfg.Careful.MinFanIn = s.Outcomes.MinFanIn(d.Repo)
			}
			cfgs[k] = cfg
		}
		return score.Facts(d.Signals, cfg)
	}
}

func (s *Server) fitOptions() fit.Options {
	o := fit.Defaults()
	if w := int(s.Outcomes.Window.Hours() / 24); w > 0 {
		o.Window = w
	}
	if s.Outcomes.FitDays > 0 {
		o.Days = s.Outcomes.FitDays
	}
	return o
}

// learnedRows lists the rule book with each shadow or active rule's trial.
func (s *Server) learnedRows() []learnedRow {
	o := s.Outcomes
	if o.Learned == nil {
		return nil
	}
	hist := s.history()
	facts := s.facts()
	var rows []learnedRow
	for _, l := range o.Learned.List("") {
		r := learnedRow{Learned: l, Says: fit.Describe(l.Fact)}
		if l.Status == fit.Shadow || l.Status == fit.Active {
			r.Trial = outcome.Score(l, hist, o.State, o.now(), o.Window, facts)
		}
		rows = append(rows, r)
	}
	return rows
}

// learnRules fits each repo's history now and records what it finds as
// suggestions. It changes no sorting.
func (s *Server) learnRules(w http.ResponseWriter, r *http.Request) {
	o := s.Outcomes
	if o.Learned == nil {
		http.Error(w, "learning rules isn't available here", 400)
		return
	}
	fits, already := outcome.Learn(s.history(), o.State, o.now(), s.facts(), s.fitOptions())
	var msgs []string
	for _, repo := range sortedKeys(fits) {
		res := fits[repo]
		// a rule the other rules already cover adds nothing: leave it out
		var keep []fit.Rule
		skipped := 0
		for _, x := range res.Rules {
			if already[repo][x.Fact] >= 0.95 {
				skipped++
				continue
			}
			keep = append(keep, x)
		}
		res.Rules = keep
		added, err := o.Learned.Suggest(res, already[repo], o.now())
		if err != nil {
			back(w, r, "/outcomes", "error", err.Error())
			return
		}
		switch {
		case !res.Enough:
			msgs = append(msgs, repo+": "+res.Note)
		case len(res.Rules) == 0 && skipped > 0:
			msgs = append(msgs, fmt.Sprintf("%s: what stood out is already sent to careful by the default rules, so nothing new to try", repo))
		case len(res.Rules) == 0:
			msgs = append(msgs, repo+": "+res.Note)
		default:
			msgs = append(msgs, fmt.Sprintf("%s: %d rule%s worth a trial (%d new)", repo, len(res.Rules), plural(len(res.Rules)), added))
		}
		if o.Audit != nil {
			o.Audit.Append("rules_learned", repo, 0, actor(r), map[string]string{
				"suggested": fmt.Sprint(len(res.Rules)), "trouble": fmt.Sprint(res.Trouble), "prs": fmt.Sprint(int(res.PRs)), "fit_at": res.At.Format("2006-01-02")})
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, "no merged pull requests to learn from yet")
	}
	http.Redirect(w, r, "/outcomes?msg="+url.QueryEscape(strings.Join(msgs, ". ")+".")+"#learned", http.StatusSeeOther)
}

// moveRule is a person moving a learned rule: start a trial, promote,
// switch off or reject.
func (s *Server) moveRule(w http.ResponseWriter, r *http.Request) {
	o := s.Outcomes
	if o.Learned == nil {
		http.Error(w, "learning rules isn't available here", 400)
		return
	}
	repo, fact, to := r.FormValue("repo"), r.FormValue("fact"), r.FormValue("to")
	_, err := o.Learned.Move(repo, fact, to, actor(r), o.now())
	if err != nil {
		back(w, r, "/outcomes", "error", err.Error())
		return
	}
	if o.Audit != nil {
		o.Audit.Append("rule_"+to, repo, 0, actor(r), map[string]string{"fact": fact, "rule": fit.Describe(fact)})
	}
	msg := map[string]string{
		fit.Shadow:   "Trial started: \"" + fit.Describe(fact) + "\" is scored on pull requests merged from now on. It doesn't sort anything.",
		fit.Active:   "\"" + fit.Describe(fact) + "\" now sends matching pull requests to careful, next to the default rules. It keeps its record here.",
		fit.Rejected: "\"" + fit.Describe(fact) + "\" is set aside. Cowboy won't suggest it again.",
	}[to]
	if to == fit.Shadow && r.FormValue("from") == fit.Active {
		msg = "\"" + fit.Describe(fact) + "\" no longer sorts. It's back on trial and keeps its record."
	}
	http.Redirect(w, r, "/outcomes?msg="+url.QueryEscape(msg)+"#learned", http.StatusSeeOther)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
