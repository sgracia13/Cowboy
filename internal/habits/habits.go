// Package habits measures how often a team keeps the habits that keep
// changes safe, per repo, and says whether a habit went with less trouble
// after merge in that repo's own history. It never scores people: every
// number is for the whole repo.
//
// Our test across nine public repos (scripts/eval/evidence/
// RESULTS-practices.md) found no habit that goes with fewer reverts
// everywhere, so a habit is only called "holds here" when this repo's own
// history shows it, with enough trouble to tell (MinTrouble).
package habits

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
)

// Habit is one thing a pull request can do to keep a change safe.
type Habit struct {
	Key  string
	Name string
	What string // what keeping it means, in plain words
}

// All are the habits Cowboy measures, in the order shown.
var All = []Habit{
	{"tests", "Tests with code", "changes tests alongside source code"},
	{"no_shortcuts", "No shortcuts", "no silenced warnings, skipped or deleted tests, loosened CI or catch-alls that hide errors"},
	{"reviewable", "Reviewable size", "under 400 lines of source, small enough to review in one sitting"},
	{"reviewed", "Reviewed before merge", "approved by someone other than the author before merging"},
	{"green", "Checks green", "checks were passing when Cowboy last sorted it"},
}

// Verdicts, in the same words as the rest of Cowboy.
const (
	Holds        = "holds here"
	Worse        = "did worse here"
	NoDifference = "no demonstrated difference"
	NotEnough    = "not enough evidence yet"
)

// MinTrouble is how many pull requests with trouble a habit needs before
// any verdict (the same bar as the rules comparison).
const MinTrouble = 20

// Row is one habit in one repo.
type Row struct {
	Habit
	Applies int     // merged PRs in the period the habit can be judged on
	Kept    int     // of those, PRs that kept it
	Share   float64 // Kept / Applies
	Before  float64 // the share in the period before (-1 if too few PRs)
	// verdict, from all rated history (merged at least the window ago)
	Trouble        int // rated PRs with trouble the habit applies to
	Verdict        string
	RR, Low, High  float64 // trouble with the habit ÷ without, size held fixed
	TroubleWith    float64
	TroubleWithout float64
}

// Narrow says a "no demonstrated difference" is close to no effect: the
// likely range sits within 0.8× to 1.25×. Only then is a required habit
// worth questioning; a wide range just means the history can't tell yet.
func (r Row) Narrow() bool {
	return r.Verdict == NoDifference && r.Low >= 0.8 && r.High <= 1.25
}

// Report is a repo's habits.
type Report struct {
	Repo   string
	Days   int // the period the shares cover
	Merged int // merged PRs in the period
	Rows   []Row
}

// fact is what one merged PR did.
type fact struct {
	at      time.Time
	lines   int
	bad     bool
	rated   bool
	applies map[string]bool
	kept    map[string]bool
}

func facts(d pipeline.Decision, m outcome.Merge, cfg config.Config) fact {
	s := d.Signals
	f := fact{at: m.MergedAt, lines: s.LinesChanged, applies: map[string]bool{}, kept: map[string]bool{}}
	big := cfg.Notes.BigSourceLines
	if big <= 0 {
		big = 400
	}
	classes, sizeKnown := s.Classes, len(s.Classes) > 0
	if !sizeKnown {
		// older records (and the demo's replay) keep paths but not classes:
		// classify the paths; source line counts can't be recovered
		classes = map[string]int{}
		for _, p := range s.Paths {
			classes[string(analyze.Classify(p))]++
		}
	}
	if classes["source"] > 0 {
		f.applies["tests"], f.kept["tests"] = true, classes["test"] > 0
		f.applies["no_shortcuts"], f.kept["no_shortcuts"] = true, len(s.Shortcuts) == 0
		if sizeKnown {
			f.applies["reviewable"], f.kept["reviewable"] = true, s.SourceLines < big
		}
	}
	if m.ReviewsKnown {
		f.applies["reviewed"], f.kept["reviewed"] = true, len(m.Approvers) > 0
	}
	if s.ChecksOK != nil {
		f.applies["green"], f.kept["green"] = true, *s.ChecksOK
	}
	return f
}

// Build measures a repo's habits: shares over the last `days`, verdicts
// over all rated history.
func Build(repo string, decisions []pipeline.Decision, st *outcome.State, cfg config.Config, now time.Time, window time.Duration, days int) Report {
	if days <= 0 {
		days = 90
	}
	r := Report{Repo: repo, Days: days}
	merges := map[int]outcome.Merge{}
	for _, m := range st.Merges(repo) {
		merges[m.Number] = m
	}
	firstTrouble := map[int]time.Time{}
	for _, o := range st.Outcomes(repo, 0) {
		if !o.Counts() {
			continue
		}
		if t, ok := firstTrouble[o.Number]; !ok || o.At.Before(t) {
			firstTrouble[o.Number] = o.At
		}
	}
	var all []fact
	for _, d := range decisions {
		if d.State != "merged" || !strings.EqualFold(d.Repo, repo) {
			continue
		}
		m, ok := merges[d.Number]
		if !ok {
			continue
		}
		f := facts(d, m, cfg)
		if t, ok := firstTrouble[d.Number]; ok && !t.Before(m.MergedAt) && t.Sub(m.MergedAt) <= window {
			f.bad = true
		}
		f.rated = now.Sub(m.MergedAt) >= window
		all = append(all, f)
	}
	from, before := now.AddDate(0, 0, -days), now.AddDate(0, 0, -2*days)
	for _, f := range all {
		if !f.at.Before(from) {
			r.Merged++
		}
	}
	for i, h := range All {
		row := Row{Habit: h, Before: -1}
		var bApplies, bKept int
		var rated []fact
		for _, f := range all {
			if !f.applies[h.Key] {
				continue
			}
			switch {
			case !f.at.Before(from):
				row.Applies++
				if f.kept[h.Key] {
					row.Kept++
				}
			case !f.at.Before(before):
				bApplies++
				if f.kept[h.Key] {
					bKept++
				}
			}
			if f.rated {
				rated = append(rated, f)
				if f.bad {
					row.Trouble++
				}
			}
		}
		if row.Applies > 0 {
			row.Share = float64(row.Kept) / float64(row.Applies)
		}
		if bApplies >= 10 {
			row.Before = float64(bKept) / float64(bApplies)
		}
		row.Verdict, row.RR, row.Low, row.High, row.TroubleWith, row.TroubleWithout = judge(h.Key, rated, row.Trouble, int64(i))
		r.Rows = append(r.Rows, row)
	}
	return r
}

var bands = []int{10, 50, 200, 1000}

func band(lines int) int {
	for i, b := range bands {
		if lines < b {
			return i
		}
	}
	return len(bands)
}

// mh is the Mantel-Haenszel risk ratio of trouble with the habit ÷
// without, over size bands (NaN when it can't be formed).
func mh(key string, fs []fact) float64 {
	type cell struct{ a, n1, c, n0 float64 }
	strata := map[int]*cell{}
	for _, f := range fs {
		s := strata[band(f.lines)]
		if s == nil {
			s = &cell{}
			strata[band(f.lines)] = s
		}
		b := 0.0
		if f.bad {
			b = 1
		}
		if f.kept[key] {
			s.a += b
			s.n1++
		} else {
			s.c += b
			s.n0++
		}
	}
	var num, den float64
	for _, s := range strata {
		t := s.n1 + s.n0
		if s.n1 == 0 || s.n0 == 0 {
			continue
		}
		num += s.a * s.n0 / t
		den += s.c * s.n1 / t
	}
	if den == 0 {
		return math.NaN()
	}
	return num / den
}

// judge gives a habit's verdict from rated history: a size-adjusted risk
// ratio with a 95% range from 400 bootstrap resamples (drawn within
// trouble / no trouble, fixed seed so the page is stable).
func judge(key string, fs []fact, trouble int, seed int64) (string, float64, float64, float64, float64, float64) {
	var with, without, bw, bwo float64
	for _, f := range fs {
		if f.kept[key] {
			with++
			if f.bad {
				bw++
			}
		} else {
			without++
			if f.bad {
				bwo++
			}
		}
	}
	rw, rwo := 0.0, 0.0
	if with > 0 {
		rw = bw / with
	}
	if without > 0 {
		rwo = bwo / without
	}
	if trouble < MinTrouble || with == 0 || without == 0 {
		return NotEnough, 0, 0, 0, rw, rwo
	}
	rr := mh(key, fs)
	if math.IsNaN(rr) || math.IsInf(rr, 0) {
		return NotEnough, 0, 0, 0, rw, rwo
	}
	var bad, good []fact
	for _, f := range fs {
		if f.bad {
			bad = append(bad, f)
		} else {
			good = append(good, f)
		}
	}
	rng := rand.New(rand.NewSource(2026 + seed))
	var draws []float64
	sample := make([]fact, 0, len(fs))
	for i := 0; i < 400; i++ {
		sample = sample[:0]
		for range bad {
			sample = append(sample, bad[rng.Intn(len(bad))])
		}
		for range good {
			sample = append(sample, good[rng.Intn(len(good))])
		}
		if v := mh(key, sample); !math.IsNaN(v) && !math.IsInf(v, 0) {
			draws = append(draws, v)
		}
	}
	if len(draws) < 100 {
		return NotEnough, rr, 0, 0, rw, rwo
	}
	sort.Float64s(draws)
	lo, hi := draws[int(0.025*float64(len(draws)))], draws[int(0.975*float64(len(draws)))-1]
	v := NoDifference
	switch {
	case hi < 1:
		v = Holds
	case lo > 1:
		v = Worse
	}
	return v, rr, lo, hi, rw, rwo
}
