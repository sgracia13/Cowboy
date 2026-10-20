package evaluate

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
)

// CompareAI judges a model's risk scores against Cowboy's careful pen and
// the simple alternatives, on exactly the same PRs: those with a score.
// Unscored PRs are left out of every method and counted.
//
// B: the model's top-scored PRs, the same share as careful (and, second,
// the same lines read as careful), against careful, largest-first,
// most-files-first, chance, and smallest-first for the lines budget.
//
// D: the rules stay; the model adds an extra share of PRs (ExtraShare)
// from those careful didn't flag, against largest-first, most-files-first
// and a random pick adding the same share from the same PRs.
//
// "Bad" is a recorded outcome within the window. A PR with nothing recorded
// isn't proven safe; it's just not counted as trouble.

// ExtraShare is the extra review budget in D: 5% of PRs.
const ExtraShare = 0.05

// AIRepo is one repo's numbers.
type AIRepo struct {
	Repo     string `json:"repo"`
	PRs      int    `json:"prs"`      // scored PRs
	Unscored int    `json:"unscored"` // left out: no usable score
	// UnscoredBad is how many of the unscored had trouble; if failures
	// pile up on troubled PRs, the comparison is biased.
	UnscoredBad int                `json:"unscored_bad"`
	Weight      float64            `json:"weight"`
	Bad         float64            `json:"bad"`
	BadCount    int                `json:"bad_count"`
	Share       float64            `json:"share"` // share careful flagged (B's budget)
	Recall      map[string]float64 `json:"recall"`
	LinesRecall map[string]float64 `json:"lines_recall"` // at careful's lines read
	Extra       map[string]float64 `json:"extra"`        // D: share of all trouble caught by the extra picks
	Counted     bool               `json:"counted"`
}

// AIResult is the whole comparison.
type AIResult struct {
	Repos   []AIRepo           `json:"repos"`
	Counted []string           `json:"counted"`
	PRs     int                `json:"prs"`    // scored PRs in counted repos
	Events  int                `json:"events"` // PRs with trouble in counted repos
	Recall  map[string]float64 `json:"recall"`
	Lines   map[string]float64 `json:"lines_recall"`
	Extra   map[string]float64 `json:"extra"`
	// differences in recall (points as fractions), with 95% ranges
	Diffs   map[string]Range  `json:"diffs"`
	Verdict map[string]string `json:"verdict"` // per difference, plus "B" and "D"
	Draws   int               `json:"draws"`
}

// The differences reported, in order.
var AIDiffs = []string{
	"ai-rule", "ai-size", "ai-spread", "ai-chance", // B, same share as careful
	"ai-rule@lines", "ai-smallest@lines", // B, same lines as careful
	"d-ai-size", "d-ai-spread", "d-ai-chance", // D
}

type aiPrep struct {
	repo                             string
	items                            []Item
	byScore, byLines, byFiles, small []int
	restScore, restLines, restFiles  []int // not flagged by careful
	unscored, unscoredBad            int
}

func tieHash(it Item) uint32 {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s#%d", it.Repo, it.Number)
	return h.Sum32()
}

func prepareAI(repo string, all []Item) aiPrep {
	p := aiPrep{repo: repo}
	for _, it := range all {
		if !it.Scored {
			p.unscored++
			if it.Bad {
				p.unscoredBad++
			}
			continue
		}
		p.items = append(p.items, it)
	}
	base := prepare(repo, p.items)
	p.byLines, p.byFiles, p.small = base.byLines, base.byFiles, base.small
	p.byScore = make([]int, len(p.items))
	for i := range p.byScore {
		p.byScore[i] = i
	}
	// ties broken by a hash of the PR, so no size information leaks into
	// the model's ranking
	sort.SliceStable(p.byScore, func(i, j int) bool {
		a, b := p.items[p.byScore[i]], p.items[p.byScore[j]]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return tieHash(a) < tieHash(b)
	})
	rest := func(order []int) []int {
		var out []int
		for _, i := range order {
			if !p.items[i].Flag {
				out = append(out, i)
			}
		}
		return out
	}
	p.restScore, p.restLines, p.restFiles = rest(p.byScore), rest(p.byLines), rest(p.byFiles)
	return p
}

func (p aiPrep) measure(mult []float64) AIRepo {
	r := AIRepo{Repo: p.repo, PRs: len(p.items), Unscored: p.unscored, UnscoredBad: p.unscoredBad,
		Recall: map[string]float64{}, LinesRecall: map[string]float64{}, Extra: map[string]float64{}}
	w := func(i int) float64 {
		if mult == nil {
			return p.items[i].W
		}
		return p.items[i].W * mult[i]
	}
	var flagW, flagBad, flagLines, restW, restBad float64
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
			flagLines += x * float64(it.Lines)
			if it.Bad {
				flagBad += x
			}
		} else {
			restW += x
			if it.Bad {
				restBad += x
			}
		}
	}
	if r.Weight == 0 || r.Bad == 0 {
		return r
	}
	r.Share = flagW / r.Weight
	// first PRs in an order until they reach a budget, by weight or lines
	take := func(order []int, budget float64, byLines bool) (bad float64) {
		used := 0.0
		for _, i := range order {
			if used >= budget {
				break
			}
			x := w(i)
			if x == 0 {
				continue
			}
			if byLines {
				used += x * float64(p.items[i].Lines)
			} else {
				used += x
			}
			if p.items[i].Bad {
				bad += x
			}
		}
		return
	}
	share := r.Share * r.Weight
	r.Recall["rule"] = flagBad / r.Bad
	r.Recall["ai"] = take(p.byScore, share, false) / r.Bad
	r.Recall["size"] = take(p.byLines, share, false) / r.Bad
	r.Recall["spread"] = take(p.byFiles, share, false) / r.Bad
	r.Recall["chance"] = r.Share
	r.LinesRecall["rule"] = r.Recall["rule"]
	r.LinesRecall["ai"] = take(p.byScore, flagLines, true) / r.Bad
	r.LinesRecall["smallest"] = take(p.small, flagLines, true) / r.Bad
	extra := ExtraShare * r.Weight
	if extra > restW {
		extra = restW
	}
	r.Extra["ai"] = take(p.restScore, extra, false) / r.Bad
	r.Extra["size"] = take(p.restLines, extra, false) / r.Bad
	r.Extra["spread"] = take(p.restFiles, extra, false) / r.Bad
	if restW > 0 {
		r.Extra["chance"] = extra / restW * restBad / r.Bad
	}
	return r
}

type aiPooled struct {
	recall, lines, extra map[string]float64
	diffs                map[string]float64
}

func poolAI(rs []AIRepo) aiPooled {
	q := aiPooled{recall: map[string]float64{}, lines: map[string]float64{}, extra: map[string]float64{}, diffs: map[string]float64{}}
	var bad float64
	for _, r := range rs {
		bad += r.Bad
		for k, v := range r.Recall {
			q.recall[k] += v * r.Bad
		}
		for k, v := range r.LinesRecall {
			q.lines[k] += v * r.Bad
		}
		for k, v := range r.Extra {
			q.extra[k] += v * r.Bad
		}
	}
	if bad == 0 {
		return q
	}
	for _, m := range []map[string]float64{q.recall, q.lines, q.extra} {
		for k := range m {
			m[k] /= bad
		}
	}
	q.diffs["ai-rule"] = q.recall["ai"] - q.recall["rule"]
	q.diffs["ai-size"] = q.recall["ai"] - q.recall["size"]
	q.diffs["ai-spread"] = q.recall["ai"] - q.recall["spread"]
	q.diffs["ai-chance"] = q.recall["ai"] - q.recall["chance"]
	q.diffs["ai-rule@lines"] = q.lines["ai"] - q.lines["rule"]
	q.diffs["ai-smallest@lines"] = q.lines["ai"] - q.lines["smallest"]
	q.diffs["d-ai-size"] = q.extra["ai"] - q.extra["size"]
	q.diffs["d-ai-spread"] = q.extra["ai"] - q.extra["spread"]
	q.diffs["d-ai-chance"] = q.extra["ai"] - q.extra["chance"]
	return q
}

// CompareAI runs the comparison with the same bootstrap as Evaluate:
// resampling within repo × group × stratum, o.Draws times.
func CompareAI(items []Item, o Options) AIResult {
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
	res := AIResult{Diffs: map[string]Range{}, Verdict: map[string]string{}, Draws: o.Draws}
	preps := map[string]aiPrep{}
	var counted []AIRepo
	for _, n := range names {
		preps[n] = prepareAI(n, byRepo[n])
		r := preps[n].measure(nil)
		r.Counted = r.BadCount >= o.MinEvents
		res.Repos = append(res.Repos, r)
		if r.Counted {
			counted = append(counted, r)
			res.Counted = append(res.Counted, n)
			res.Events += r.BadCount
			res.PRs += r.PRs
		}
	}
	if len(counted) == 0 {
		for _, k := range append([]string{"B", "D"}, AIDiffs...) {
			res.Verdict[k] = Insufficient
		}
		return res
	}
	pt := poolAI(counted)
	res.Recall, res.Lines, res.Extra = pt.recall, pt.lines, pt.extra

	type key struct{ repo, group, stratum string }
	cells := map[key][]int{}
	var order []key
	for _, n := range res.Counted {
		for i, it := range preps[n].items {
			s := it.Stratum
			if s == "" {
				s = map[bool]string{true: "bad", false: "ok"}[it.Bad]
			}
			k := key{n, it.Group, s}
			if cells[k] == nil {
				order = append(order, k)
			}
			cells[k] = append(cells[k], i)
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
	draws := map[string][]float64{}
	for d := 0; d < o.Draws; d++ {
		for _, n := range res.Counted {
			for i := range mult[n] {
				mult[n][i] = 0
			}
		}
		for _, k := range order {
			idx := cells[k]
			m := mult[k.repo]
			for range idx {
				m[idx[rng.Intn(len(idx))]]++
			}
		}
		var rs []AIRepo
		for _, n := range res.Counted {
			rs = append(rs, preps[n].measure(mult[n]))
		}
		q := poolAI(rs)
		for _, k := range AIDiffs {
			draws[k] = append(draws[k], q.diffs[k])
		}
	}
	for _, k := range AIDiffs {
		r := Range{Value: pt.diffs[k]}
		if xs := draws[k]; len(xs) > 0 {
			sort.Float64s(xs)
			r.Low = xs[int(0.025*float64(len(xs)))]
			r.High = xs[int(0.975*float64(len(xs)))-1]
		}
		res.Diffs[k] = r
		res.Verdict[k] = verdict(r, 0, true)
	}
	// the pre-registered headlines (scripts/eval/evidence/CLAIMS-ai.md)
	switch {
	case res.Verdict["ai-rule"] == Outperformed && res.Verdict["ai-chance"] == Outperformed:
		res.Verdict["B"] = Outperformed
	case res.Verdict["ai-rule"] == Worse:
		res.Verdict["B"] = Worse
	default:
		res.Verdict["B"] = NoAdvantage
	}
	switch {
	case res.Verdict["d-ai-size"] == Outperformed && res.Verdict["d-ai-spread"] == Outperformed && res.Verdict["d-ai-chance"] == Outperformed:
		res.Verdict["D"] = Outperformed
	case res.Verdict["d-ai-chance"] == Worse:
		res.Verdict["D"] = Worse
	default:
		res.Verdict["D"] = NoAdvantage
	}
	return res
}
