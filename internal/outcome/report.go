package outcome

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

// Rate is a share with a 95% range, so small numbers don't look certain.
type Rate struct {
	N    int     `json:"n"`
	Bad  int     `json:"bad"`
	Cost int     `json:"cost"` // severity-weighted
	P    float64 `json:"p"`
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// Wilson is the 95% Wilson score interval for bad out of n.
func Wilson(bad, n int) (lo, hi float64) {
	if n == 0 {
		return 0, 1
	}
	const z = 1.96
	p := float64(bad) / float64(n)
	nn := float64(n)
	den := 1 + z*z/nn
	mid := (p + z*z/(2*nn)) / den
	half := z * math.Sqrt(p*(1-p)/nn+z*z/(4*nn*nn)) / den
	return math.Max(0, mid-half), math.Min(1, mid+half)
}

func (r *Rate) add(bad bool, cost int) {
	r.N++
	if bad {
		r.Bad++
		r.Cost += cost
	}
}

func (r *Rate) finish() {
	if r.N > 0 {
		r.P = float64(r.Bad) / float64(r.N)
	}
	r.Low, r.High = Wilson(r.Bad, r.N)
}

// String: "3 of 40 (8%, likely 3–20%)".
func (r Rate) String() string {
	if r.N == 0 {
		return "none yet"
	}
	return fmt.Sprintf("%d of %d (%.0f%%, likely %.0f–%.0f%%)", r.Bad, r.N, r.P*100, r.Low*100, r.High*100)
}

// PR is one merged PR as the report sees it.
type PR struct {
	Repo     string
	Number   int
	Title    string
	Pen      score.Pen
	Agent    string
	Area     string
	MergedAt time.Time
	Outcomes []Outcome // counted ones
}

// RuleRow is one rule's report card.
type RuleRow struct {
	Rule  string `json:"rule"`
	Label string `json:"label"`
	Rate  Rate   `json:"rate"`
}

// AgentRow is one agent in one area of the code.
type AgentRow struct {
	Agent string `json:"agent"`
	Area  string `json:"area"`
	Rate  Rate   `json:"rate"`
}

// MonthRow is one agent's month.
type MonthRow struct {
	Agent string `json:"agent"`
	Month string `json:"month"`
	Rate  Rate   `json:"rate"`
}

// Suggestion is a rule change the numbers point to. Cowboy never applies
// it; a person edits cowboy.json if they agree.
type Suggestion struct {
	Repo   string `json:"repo,omitempty"`
	Change string `json:"change"`
	Why    string `json:"why"`
}

// Report is the outcomes summary.
type Report struct {
	WindowDays  int          `json:"window_days"`
	Matured     int          `json:"matured"` // merged PRs whose window has passed
	Recent      int          `json:"recent"`  // merged PRs still inside the window
	ByPen       []PenRow     `json:"by_pen"`
	Rules       []RuleRow    `json:"rules"`
	Missed      []PR         `json:"missed"` // easy PRs that had trouble
	Reviewed    Rate         `json:"reviewed"`
	NotReviewed Rate         `json:"not_reviewed"`
	Agents      []AgentRow   `json:"agents"`
	Months      []MonthRow   `json:"months"`
	Survival    []SurvRow    `json:"survival"`
	Suggestions []Suggestion `json:"suggestions"`
	Pending     int          `json:"pending"` // suggested outcomes waiting for a person
}

// PenRow is one pen's rate.
type PenRow struct {
	Pen  score.Pen `json:"pen"`
	Rate Rate      `json:"rate"`
}

// SurvRow is the median share of code still there, per pen.
type SurvRow struct {
	Pen    score.Pen `json:"pen"`
	N      int       `json:"n"`
	Median float64   `json:"median"`
}

var ruleLabels = map[string]string{
	"sensitive":    "sensitive path",
	"blast_radius": "20+ files use it",
	"checks":       "checks failing",
	"size":         "big change",
}

// RuleLabel is a rule's plain name.
func RuleLabel(rule string) string {
	if l, ok := ruleLabels[rule]; ok {
		return l
	}
	if k, ok := strings.CutPrefix(rule, "shortcut:"); ok {
		return strings.ReplaceAll(k, "_", " ")
	}
	return rule
}

// rules lists the careful rules that fired for a decision.
func rules(d pipeline.Decision) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range d.Reasons {
		if r.Effect != "careful" {
			continue
		}
		k := r.Check
		if k == "shortcut" {
			for _, f := range d.Signals.Shortcuts {
				if f.Detail == r.Detail {
					k = "shortcut:" + f.Kind
					break
				}
			}
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// PenOf is the pen that counts: a person's move wins over Cowboy's.
func PenOf(d pipeline.Decision) score.Pen {
	if d.Corrected != "" {
		return d.Corrected
	}
	return d.Pen
}

// AgentName is the agent, or "people".
func AgentName(d pipeline.Decision) string {
	if d.Agent == "" {
		return "people"
	}
	return d.Agent
}

// Build makes the report. window is how long after merge an outcome counts
// (outcomes later than that are shown but not counted). Only PRs merged at
// least window ago go into the rates, so recent PRs don't look cleaner than
// they are just because there's been no time for trouble.
func Build(decisions []pipeline.Decision, st *State, now time.Time, window time.Duration, minFanIn map[string]int) Report {
	rep := Report{WindowDays: int(window.Hours() / 24)}
	merges := map[string]Merge{}
	for _, m := range st.Merges("") {
		merges[key(m.Repo, m.Number)] = m
	}
	counted := map[string][]Outcome{}
	for _, o := range st.Outcomes("", 0) {
		if o.Status == Suggested {
			rep.Pending++
		}
		if o.Counts() {
			counted[key(o.Repo, o.Number)] = append(counted[key(o.Repo, o.Number)], o)
		}
	}
	pens := map[score.Pen]*Rate{score.Easy: {}, score.Normal: {}, score.Careful: {}}
	ruleRates := map[string]*Rate{}
	agentArea := map[[2]string]*Rate{}
	months := map[[2]string]*Rate{}
	surv := map[score.Pen][]float64{}
	type fanRow struct {
		repo string
		fan  int
		bad  bool
	}
	var fans []fanRow
	areaEasy := map[[2]string]*Rate{}
	var easyAll Rate
	for _, d := range decisions {
		if d.State != "merged" {
			continue
		}
		m, ok := merges[key(d.Repo, d.Number)]
		if !ok {
			continue
		}
		if now.Sub(m.MergedAt) < window {
			rep.Recent++
			continue
		}
		rep.Matured++
		var outs []Outcome
		cost := 0
		for _, o := range counted[key(d.Repo, d.Number)] {
			if o.At.Sub(m.MergedAt) <= window {
				outs = append(outs, o)
				cost += o.Severity
			}
		}
		bad := len(outs) > 0
		pen := PenOf(d)
		if r := pens[pen]; r != nil {
			r.add(bad, cost)
		}
		for _, rule := range rules(d) {
			if ruleRates[rule] == nil {
				ruleRates[rule] = &Rate{}
			}
			ruleRates[rule].add(bad, cost)
		}
		area := Area(m.Files)
		k := [2]string{AgentName(d), area}
		if agentArea[k] == nil {
			agentArea[k] = &Rate{}
		}
		agentArea[k].add(bad, cost)
		mk := [2]string{AgentName(d), m.MergedAt.Format("2006-01")}
		if months[mk] == nil {
			months[mk] = &Rate{}
		}
		months[mk].add(bad, cost)
		if pen == score.Easy {
			easyAll.add(bad, cost)
			ak := [2]string{d.Repo, area}
			if areaEasy[ak] == nil {
				areaEasy[ak] = &Rate{}
			}
			areaEasy[ak].add(bad, cost)
			if bad {
				rep.Missed = append(rep.Missed, PR{Repo: d.Repo, Number: d.Number, Title: d.Title, Pen: pen, Agent: AgentName(d), Area: area, MergedAt: m.MergedAt, Outcomes: outs})
			}
		}
		if pen == score.Careful {
			if len(m.Approvers) > 0 {
				rep.Reviewed.add(bad, cost)
			} else {
				rep.NotReviewed.add(bad, cost)
			}
		}
		if s, ok := m.Survival["30d"]; ok {
			surv[pen] = append(surv[pen], s)
		}
		if d.Signals.FanInKnown {
			fans = append(fans, fanRow{d.Repo, d.Signals.FanIn, bad})
		}
	}
	for _, p := range []score.Pen{score.Easy, score.Normal, score.Careful} {
		pens[p].finish()
		rep.ByPen = append(rep.ByPen, PenRow{p, *pens[p]})
		if xs := surv[p]; len(xs) > 0 {
			sort.Float64s(xs)
			rep.Survival = append(rep.Survival, SurvRow{p, len(xs), xs[len(xs)/2]})
		}
	}
	for rule, r := range ruleRates {
		r.finish()
		rep.Rules = append(rep.Rules, RuleRow{rule, RuleLabel(rule), *r})
	}
	sort.Slice(rep.Rules, func(i, j int) bool {
		if rep.Rules[i].Rate.N != rep.Rules[j].Rate.N {
			return rep.Rules[i].Rate.N > rep.Rules[j].Rate.N
		}
		return rep.Rules[i].Rule < rep.Rules[j].Rule
	})
	rep.Reviewed.finish()
	rep.NotReviewed.finish()
	easyAll.finish()
	for k, r := range agentArea {
		r.finish()
		rep.Agents = append(rep.Agents, AgentRow{k[0], k[1], *r})
	}
	sort.Slice(rep.Agents, func(i, j int) bool {
		a, b := rep.Agents[i], rep.Agents[j]
		if a.Rate.N != b.Rate.N {
			return a.Rate.N > b.Rate.N
		}
		return a.Agent+a.Area < b.Agent+b.Area
	})
	for k, r := range months {
		r.finish()
		rep.Months = append(rep.Months, MonthRow{k[0], k[1], *r})
	}
	sort.Slice(rep.Months, func(i, j int) bool {
		if rep.Months[i].Agent != rep.Months[j].Agent {
			return rep.Months[i].Agent < rep.Months[j].Agent
		}
		return rep.Months[i].Month < rep.Months[j].Month
	})
	sort.Slice(rep.Missed, func(i, j int) bool { return rep.Missed[i].MergedAt.After(rep.Missed[j].MergedAt) })

	// ---- suggestions: only when the ranges don't overlap
	easy := pens[score.Easy]
	for _, row := range rep.Rules {
		r := row.Rate
		if r.N >= 30 && easy.N >= 30 && r.High < easy.Low {
			rep.Suggestions = append(rep.Suggestions, Suggestion{
				Change: fmt.Sprintf("Make %q a note instead of a careful rule", row.Label),
				Why:    fmt.Sprintf("PRs it sent to careful had trouble %s, less than easy PRs (%s).", r, *easy)})
		}
	}
	for k, r := range areaEasy {
		r.finish()
		if r.Bad >= 3 && easyAll.N >= 30 && r.Low > easyAll.High && k[1] != "(root)" {
			rep.Suggestions = append(rep.Suggestions, Suggestion{Repo: k[0],
				Change: fmt.Sprintf("Add %q to careful.paths", k[1]+"/**"),
				Why:    fmt.Sprintf("Easy PRs in %s/ had trouble %s, more than easy PRs overall (%s).", k[1], *r, easyAll)})
		}
	}
	byRepo := map[string][]fanRow{}
	for _, f := range fans {
		byRepo[f.repo] = append(byRepo[f.repo], f)
	}
	for repo, rows := range byRepo {
		cur := 20
		if v, ok := minFanIn[repo]; ok && v > 0 {
			cur = v
		}
		best, bestGap := 0, 0.0
		var bestAbove, bestBelow Rate
		for _, t := range []int{5, 10, 15, 20, 25, 30, 40, 50, 75, 100} {
			var above, below Rate
			for _, f := range rows {
				if f.fan >= t {
					above.add(f.bad, 0)
				} else {
					below.add(f.bad, 0)
				}
			}
			above.finish()
			below.finish()
			if above.N < 20 || below.N < 20 || above.Bad+below.Bad < 5 || above.Low <= below.High {
				continue
			}
			if gap := above.P - below.P; gap > bestGap {
				best, bestGap, bestAbove, bestBelow = t, gap, above, below
			}
		}
		if best != 0 && best != cur {
			rep.Suggestions = append(rep.Suggestions, Suggestion{Repo: repo,
				Change: fmt.Sprintf("Set careful.min_fan_in to %d (now %d)", best, cur),
				Why:    fmt.Sprintf("PRs changing a file %d+ files use had trouble %s; below that, %s.", best, bestAbove, bestBelow)})
		}
	}
	sort.Slice(rep.Suggestions, func(i, j int) bool { return rep.Suggestions[i].Change < rep.Suggestions[j].Change })
	return rep
}
