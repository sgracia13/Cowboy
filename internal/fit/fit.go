// Package fit picks careful rules from a repo's own history: the few facts
// (folders, files, default triggers) that went with trouble after merge in
// that repo, more than chance would explain. The method is written down in
// scripts/eval/evidence/CLAIMS-fit.md and tested there; change it only
// with a new test.
package fit

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/config"
)

// Options are the fitting settings. Defaults() holds the tested values.
type Options struct {
	Days       int     // history to fit on, before the fit date
	Window     int     // days after merge that trouble counts
	MinTrouble int     // fewer PRs with trouble than this: not enough history
	MinK       int     // a fact needs at least this many PRs with trouble
	MinLift    float64 // cautious lift a fact needs
	MaxShare   float64 // stop adding rules past this share of PRs
	MaxRules   int
	Z          float64 // one-sided Wilson z (1.2816 = 90%)
}

// Defaults are the settings written down in CLAIMS-fit.md.
func Defaults() Options {
	return Options{Days: 90, Window: 30, MinTrouble: 8, MinK: 3, MinLift: 1.5, MaxShare: 0.15, MaxRules: 10, Z: 1.2816}
}

// Example is one merged PR.
type Example struct {
	Repo      string
	Number    int
	Merged    time.Time
	Facts     []string
	TroubleAt time.Time // first trouble that counts; zero if none
	W         float64   // weight (1 on a live repo; sampling weight in tests)
}

// Rule is one fitted rule with the numbers behind it.
type Rule struct {
	Fact    string  `json:"fact"`
	N       float64 `json:"n"` // PRs with the fact
	K       int     `json:"k"` // of those, PRs with trouble
	Rate    float64 `json:"rate"`
	Low     float64 `json:"low"` // cautious rate
	LiftLow float64 `json:"lift_low"`
}

// Result is a fit for one repo.
type Result struct {
	Repo    string    `json:"repo"`
	At      time.Time `json:"at"`   // fit date: history before it was used
	From    time.Time `json:"from"` // start of the history used
	PRs     float64   `json:"prs"`
	Trouble int       `json:"trouble"`
	Base    float64   `json:"base"`
	Enough  bool      `json:"enough"`
	Rules   []Rule    `json:"rules"`
	Share   float64   `json:"share"` // share of the history's PRs the rules cover
	Note    string    `json:"note"`
}

func (o Options) troubled(e Example, at time.Time) bool {
	t := e.TroubleAt
	return !t.IsZero() && t.Before(at) && t.Sub(e.Merged) <= time.Duration(o.Window)*24*time.Hour
}

// WilsonLow is the low end of a one-sided Wilson interval for k of n.
func WilsonLow(k int, n, z float64) float64 {
	if n <= 0 {
		return 0
	}
	p := float64(k) / n
	if p > 1 {
		p = 1
	}
	den := 1 + z*z/n
	mid := (p + z*z/(2*n)) / den
	half := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / den
	return math.Max(0, mid-half)
}

// Fit fits rules from the examples merged in the Days before at.
func Fit(repo string, ex []Example, at time.Time, o Options) Result {
	from := at.Add(-time.Duration(o.Days) * 24 * time.Hour)
	r := Result{Repo: repo, At: at, From: from}
	var hist []Example
	for _, e := range ex {
		if !e.Merged.Before(from) && e.Merged.Before(at) {
			hist = append(hist, e)
		}
	}
	type acc struct {
		n   float64
		k   int
		idx []int
	}
	facts := map[string]*acc{}
	bad := make([]bool, len(hist))
	for i, e := range hist {
		w := e.W
		if w == 0 {
			w = 1
		}
		r.PRs += w
		bad[i] = o.troubled(e, at)
		if bad[i] {
			r.Trouble++
		}
		for _, f := range e.Facts {
			a := facts[f]
			if a == nil {
				a = &acc{}
				facts[f] = a
			}
			a.n += w
			a.idx = append(a.idx, i)
			if bad[i] {
				a.k++
			}
		}
	}
	if r.PRs > 0 {
		r.Base = float64(r.Trouble) / r.PRs
	}
	if r.Trouble < o.MinTrouble {
		r.Note = fmt.Sprintf("not enough history: %d PRs with trouble in %d days (needs %d); using the default rules", r.Trouble, o.Days, o.MinTrouble)
		return r
	}
	r.Enough = true
	var cands []Rule
	for f, a := range facts {
		if a.k < o.MinK {
			continue
		}
		low := WilsonLow(a.k, a.n, o.Z)
		lift := low / r.Base
		if lift >= o.MinLift {
			cands = append(cands, Rule{Fact: f, N: a.n, K: a.k, Rate: float64(a.k) / a.n, Low: low, LiftLow: lift})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].LiftLow != cands[j].LiftLow {
			return cands[i].LiftLow > cands[j].LiftLow
		}
		if cands[i].K != cands[j].K {
			return cands[i].K > cands[j].K
		}
		return cands[i].Fact < cands[j].Fact
	})
	covered := map[int]bool{}
	var coveredW float64
	for _, c := range cands {
		if len(r.Rules) >= o.MaxRules {
			break
		}
		var add []int
		addW := 0.0
		for _, i := range facts[c.Fact].idx {
			if !covered[i] {
				add = append(add, i)
				w := hist[i].W
				if w == 0 {
					w = 1
				}
				addW += w
			}
		}
		if len(add) == 0 {
			continue
		}
		if (coveredW+addW)/r.PRs > o.MaxShare {
			break
		}
		for _, i := range add {
			covered[i] = true
		}
		coveredW += addW
		r.Rules = append(r.Rules, c)
	}
	r.Share = coveredW / r.PRs
	if len(r.Rules) == 0 {
		r.Note = "nothing in this repo's history stood out clearly enough; using the default rules"
	}
	return r
}

// Careful reports whether a PR with these facts meets any fitted rule, and
// which.
func (r Result) Careful(facts []string) []Rule {
	have := map[string]bool{}
	for _, f := range facts {
		have[f] = true
	}
	var out []Rule
	for _, x := range r.Rules {
		if have[x.Fact] {
			out = append(out, x)
		}
	}
	return out
}

// Describe is a fact in plain words.
func Describe(fact string) string {
	switch {
	case strings.HasPrefix(fact, "path:"):
		return "touches " + strings.TrimPrefix(fact, "path:")
	case strings.HasPrefix(fact, "file:"):
		return "changes " + strings.TrimPrefix(fact, "file:")
	case strings.HasPrefix(fact, "shortcut:"):
		return "has the " + strings.ReplaceAll(strings.TrimPrefix(fact, "shortcut:"), "_", " ") + " shortcut"
	}
	switch fact {
	case "sensitive":
		return "touches a sensitive path"
	case "blast":
		return "changes one of the repo's most-imported files"
	case "agent":
		return "written by an agent"
	case "big":
		return "400+ lines of source"
	}
	return fact
}

// Label is the reason shown on a PR the rule sends to careful.
func (x Rule) Label(r Result) string {
	return fmt.Sprintf("%s: in this repo's last %d days, %d of %s such PRs had trouble after merge (%.1f× the usual rate; fitted %s)",
		Describe(x.Fact), int(r.At.Sub(r.From).Hours()/24+0.5), x.K, trimNum(x.N), x.Rate/r.Base, r.At.Format("Jan 2"))
}

func trimNum(f float64) string {
	if f == math.Trunc(f) {
		return fmt.Sprintf("%d", int(f))
	}
	return fmt.Sprintf("%.0f", f)
}

// Config turns a fit into the rules score.Decide uses.
func (r Result) Config() config.Fitted {
	f := config.Fitted{At: r.At}
	for _, x := range r.Rules {
		f.Rules = append(f.Rules, config.FittedRule{Fact: x.Fact, Label: x.Label(r)})
	}
	return f
}
