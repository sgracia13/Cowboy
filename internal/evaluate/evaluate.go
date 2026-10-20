// Package evaluate is Cowboy's one way of judging a rule: did the PRs it
// picked have trouble after merge more often than simple alternatives
// picking the same share of PRs (the largest, the most files touched, or
// at random)? The outcomes page, `cowboy evaluate` and the published
// evidence all use it, so the product never shows a number the evidence
// scripts wouldn't.
//
// The method is the one in scripts/eval/evidence/CLAIMS.md (weighted, as
// in RESULTS.md); TestMatchesCompareScript checks it against compare.py.
package evaluate

import (
	"math/rand"
	"sort"
)

// Item is one merged PR whose trouble window has passed.
type Item struct {
	Repo   string
	Number int
	Group  string  // bootstrap group within a repo (e.g. a month); "" = one group
	W      float64 // weight: 1 on a live repo; sampling weight in replays
	// Stratum keeps the bootstrap's mix fixed. Replays use the sampling
	// label ("reverted" vs not); live repos leave it "" and Bad is used.
	Stratum string
	Bad     bool // had trouble within the window
	Flag    bool // the rule picked it (e.g. careful)
	Safe    bool // the rule called it safe (e.g. easy)
	Lines   int
	Files   int
	// Score is a model's risk score for the PR (higher = riskier), for
	// CompareAI; Scored says one exists. Evaluate ignores both.
	Score  float64
	Scored bool
}

// Verdict words. No pass/fail badges: a result is one of these.
const (
	Outperformed = "outperformed"
	NoAdvantage  = "no demonstrated advantage"
	Worse        = "did worse"
	Insufficient = "insufficient evidence"
)

// MinEvents is how many PRs with trouble a comparison needs before it says
// anything but "insufficient evidence" (CLAIMS.md: 20 per repo).
const MinEvents = 20

// Range is an estimate with its 95% bootstrap range.
type Range struct {
	Value float64 `json:"value"`
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
}

// Repo is one repo's numbers (no ranges: those are pooled).
type Repo struct {
	Repo      string             `json:"repo"`
	PRs       int                `json:"prs"`
	Weight    float64            `json:"weight"`
	Bad       float64            `json:"bad"`       // weighted PRs with trouble
	BadCount  int                `json:"bad_count"` // PRs with trouble, unweighted
	Share     float64            `json:"share"`     // share the rule flagged
	Base      float64            `json:"base"`      // overall trouble rate
	Recall    map[string]float64 `json:"recall"`    // rule, size, spread, chance
	Lift      float64            `json:"lift"`
	SafeShare float64            `json:"safe_share"` // share the rule called safe
	SafeRate  float64            `json:"safe_rate"`
	Smallest  float64            `json:"smallest_rate"`
	Counted   bool               `json:"counted"`
}

// Result is a whole evaluation.
type Result struct {
	Repos   []Repo             `json:"repos"`
	Counted []string           `json:"counted"`
	Events  int                `json:"events"` // PRs with trouble in counted repos
	Recall  map[string]float64 `json:"recall"`
	VsSize  Range              `json:"vs_size"`  // rule recall − largest-first recall
	VsFiles Range              `json:"vs_files"` // rule recall − most-files recall
	Lift    Range              `json:"lift"`     // rule's rate ÷ overall rate
	SafeVs  Range              `json:"safe_vs"`  // safe rate − smallest rate
	Verdict map[string]string  `json:"verdict"`  // size, files, chance, safe
	Draws   int                `json:"draws"`
}

// Options control an evaluation.
type Options struct {
	Draws     int   // bootstrap draws (2000)
	Seed      int64 // 2026
	MinEvents int   // per repo
}

// Defaults are the published settings.
func Defaults() Options { return Options{Draws: 2000, Seed: 2026, MinEvents: MinEvents} }

// prep is one repo's items with the orders the baselines need, sorted
// once: the bootstrap reweights items instead of re-sorting copies.
type prep struct {
	repo                    string
	items                   []Item
	byLines, byFiles, small []int
}

func prepare(repo string, items []Item) prep {
	p := prep{repo: repo, items: items}
	idx := func() []int {
		o := make([]int, len(items))
		for i := range o {
			o[i] = i
		}
		return o
	}
	p.byLines, p.byFiles, p.small = idx(), idx(), idx()
	sort.SliceStable(p.byLines, func(i, j int) bool {
		a, b := items[p.byLines[i]], items[p.byLines[j]]
		if a.Lines != b.Lines {
			return a.Lines > b.Lines
		}
		return a.Number < b.Number
	})
	sort.SliceStable(p.byFiles, func(i, j int) bool {
		a, b := items[p.byFiles[i]], items[p.byFiles[j]]
		if a.Files != b.Files {
			return a.Files > b.Files
		}
		if a.Lines != b.Lines {
			return a.Lines > b.Lines
		}
		return a.Number < b.Number
	})
	sort.SliceStable(p.small, func(i, j int) bool {
		a, b := items[p.small[i]], items[p.small[j]]
		if a.Lines != b.Lines {
			return a.Lines < b.Lines
		}
		return a.Number < b.Number
	})
	return p
}

// measure computes a repo's numbers. mult (nil = 1 each) is how many times
// each item was drawn in a bootstrap sample.
func (p prep) measure(mult []float64) Repo {
	r := Repo{Repo: p.repo, PRs: len(p.items), Recall: map[string]float64{}}
	w := func(i int) float64 {
		if mult == nil {
			return p.items[i].W
		}
		return p.items[i].W * mult[i]
	}
	var flagW, flagBad, safeW, safeBad float64
	for i, it := range p.items {
		x := w(i)
		r.Weight += x
		if it.Bad {
			r.Bad += x
			if mult == nil {
				r.BadCount++
			}
		}
		if it.Flag {
			flagW += x
			if it.Bad {
				flagBad += x
			}
		}
		if it.Safe {
			safeW += x
			if it.Bad {
				safeBad += x
			}
		}
	}
	if r.Weight == 0 {
		return r
	}
	r.Share = flagW / r.Weight
	r.Base = r.Bad / r.Weight
	// the first PRs in an order until they make up `share` of the weight
	top := func(order []int, share float64) (tw, tb float64) {
		for _, i := range order {
			if tw >= share*r.Weight {
				break
			}
			x := w(i)
			if x == 0 {
				continue
			}
			tw += x
			if p.items[i].Bad {
				tb += x
			}
		}
		return
	}
	if r.Bad > 0 {
		r.Recall["rule"] = flagBad / r.Bad
		_, b := top(p.byLines, r.Share)
		r.Recall["size"] = b / r.Bad
		_, b = top(p.byFiles, r.Share)
		r.Recall["spread"] = b / r.Bad
		if flagW > 0 {
			r.Lift = (flagBad / flagW) / r.Base
		}
	}
	r.Recall["chance"] = r.Share
	r.SafeShare = safeW / r.Weight
	if safeW > 0 {
		r.SafeRate = safeBad / safeW
	}
	if tw, tb := top(p.small, r.SafeShare); tw > 0 {
		r.Smallest = tb / tw
	}
	return r
}

type pooledNums struct {
	recall                       map[string]float64
	vsSize, vsFiles, lift, safeV float64
}

func pool(rs []Repo) pooledNums {
	var bad, w, carW, carBad, safeW, safeBad, smallBad float64
	rec := map[string]float64{}
	for _, r := range rs {
		bad += r.Bad
		w += r.Weight
		for _, k := range []string{"rule", "size", "spread", "chance"} {
			rec[k] += r.Recall[k] * r.Bad
		}
		carW += r.Share * r.Weight
		carBad += r.Recall["rule"] * r.Bad
		sw := r.SafeShare * r.Weight
		safeW += sw
		safeBad += r.SafeRate * sw
		smallBad += r.Smallest * sw
	}
	p := pooledNums{recall: map[string]float64{}}
	if bad > 0 {
		for k, v := range rec {
			p.recall[k] = v / bad
		}
		p.vsSize = p.recall["rule"] - p.recall["size"]
		p.vsFiles = p.recall["rule"] - p.recall["spread"]
		if carW > 0 {
			p.lift = (carBad / carW) / (bad / w)
		}
	}
	if safeW > 0 {
		p.safeV = (safeBad - smallBad) / safeW
	}
	return p
}

// Evaluate judges the rule on these items.
func Evaluate(items []Item, o Options) Result {
	byRepo := map[string][]Item{}
	for _, it := range items {
		if it.W == 0 {
			it.W = 1
		}
		byRepo[it.Repo] = append(byRepo[it.Repo], it)
	}
	names := make([]string, 0, len(byRepo))
	for k := range byRepo {
		names = append(names, k)
	}
	sort.Strings(names)
	res := Result{Verdict: map[string]string{}, Draws: o.Draws}
	var counted []Repo
	preps := map[string]prep{}
	for _, n := range names {
		preps[n] = prepare(n, byRepo[n])
		r := preps[n].measure(nil)
		r.Counted = r.BadCount >= o.MinEvents
		res.Repos = append(res.Repos, r)
		if r.Counted {
			counted = append(counted, r)
			res.Counted = append(res.Counted, n)
			res.Events += r.BadCount
		}
	}
	if len(counted) == 0 {
		for _, k := range []string{"size", "files", "chance", "safe"} {
			res.Verdict[k] = Insufficient
		}
		return res
	}
	p := pool(counted)
	res.Recall = p.recall
	res.VsSize.Value, res.VsFiles.Value, res.Lift.Value, res.SafeVs.Value = p.vsSize, p.vsFiles, p.lift, p.safeV

	// bootstrap: resample within repo × group × stratum, as draw counts
	type cell struct {
		repo string
		idx  []int
	}
	type key struct{ repo, group, stratum string }
	cells := map[key]*cell{}
	var order []key
	for _, n := range res.Counted {
		for i, it := range preps[n].items {
			s := it.Stratum
			if s == "" {
				s = map[bool]string{true: "bad", false: "ok"}[it.Bad]
			}
			k := key{n, it.Group, s}
			if cells[k] == nil {
				cells[k] = &cell{repo: n}
				order = append(order, k)
			}
			cells[k].idx = append(cells[k].idx, i)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.repo != b.repo {
			return a.repo < b.repo
		}
		if a.group != b.group {
			return a.group < b.group
		}
		return a.stratum < b.stratum
	})
	rng := rand.New(rand.NewSource(o.Seed))
	mult := map[string][]float64{}
	for _, n := range res.Counted {
		mult[n] = make([]float64, len(preps[n].items))
	}
	var dSize, dFiles, lift, safe []float64
	for d := 0; d < o.Draws; d++ {
		for _, n := range res.Counted {
			m := mult[n]
			for i := range m {
				m[i] = 0
			}
		}
		for _, k := range order {
			c := cells[k]
			m := mult[c.repo]
			for range c.idx {
				m[c.idx[rng.Intn(len(c.idx))]]++
			}
		}
		var rs []Repo
		for _, n := range res.Counted {
			rs = append(rs, preps[n].measure(mult[n]))
		}
		q := pool(rs)
		dSize = append(dSize, q.vsSize)
		dFiles = append(dFiles, q.vsFiles)
		lift = append(lift, q.lift)
		safe = append(safe, q.safeV)
	}
	ci := func(xs []float64, r *Range) {
		sort.Float64s(xs)
		r.Low = xs[int(0.025*float64(len(xs)))]
		r.High = xs[int(0.975*float64(len(xs)))-1]
	}
	if o.Draws > 0 {
		ci(dSize, &res.VsSize)
		ci(dFiles, &res.VsFiles)
		ci(lift, &res.Lift)
		ci(safe, &res.SafeVs)
	}
	res.Verdict["size"] = verdict(res.VsSize, 0, true)
	res.Verdict["files"] = verdict(res.VsFiles, 0, true)
	res.Verdict["chance"] = verdict(res.Lift, 1, true)
	res.Verdict["safe"] = verdict(res.SafeVs, 0, false) // lower is better
	return res
}

// verdict reads a range against the line it has to clear.
func verdict(r Range, line float64, higherBetter bool) string {
	switch {
	case higherBetter && r.Low > line, !higherBetter && r.High < line:
		return Outperformed
	case higherBetter && r.High < line, !higherBetter && r.Low > line:
		return Worse
	}
	return NoAdvantage
}
