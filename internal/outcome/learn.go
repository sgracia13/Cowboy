package outcome

import (
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/fit"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

// FactsFunc lists a stored decision's facts (see score.Facts) under its
// repo's current settings.
type FactsFunc func(d pipeline.Decision) []string

// troubleAt is, per merged PR, the earliest outcome that counts against it.
func troubleAt(st *State) map[string]time.Time {
	out := map[string]time.Time{}
	for _, o := range st.Outcomes("", 0) {
		if !o.Counts() {
			continue
		}
		k := key(o.Repo, o.Number)
		if t, ok := out[k]; !ok || o.At.Before(t) {
			out[k] = o.At
		}
	}
	return out
}

// carefulWithout reports whether the rules sent a PR to careful for some
// reason other than a learned rule: what the careful pen would have done
// without the learned rules.
func carefulWithout(d pipeline.Decision) bool {
	if d.Pen != score.Careful {
		return false
	}
	for _, r := range d.Reasons {
		if r.Effect == "careful" && r.Check != "learned" {
			return true
		}
	}
	return false
}

// Learn fits careful rules for each repo from the merged PRs in the
// opts.Days before now, with the tested method in internal/fit. already
// gives, per repo and fact, the share of that fact's PRs the other rules
// already sent to careful (a rule that adds nothing new isn't worth a
// trial).
func Learn(decisions []pipeline.Decision, st *State, now time.Time, facts FactsFunc, opts fit.Options) (fits map[string]fit.Result, already map[string]map[string]float64) {
	merges := map[string]Merge{}
	for _, m := range st.Merges("") {
		merges[key(m.Repo, m.Number)] = m
	}
	trouble := troubleAt(st)
	byRepo := map[string][]fit.Example{}
	type tally struct{ n, careful int }
	counts := map[string]map[string]*tally{}
	from := now.Add(-time.Duration(opts.Days) * 24 * time.Hour)
	for _, d := range decisions {
		if d.State != "merged" {
			continue
		}
		m, ok := merges[key(d.Repo, d.Number)]
		if !ok {
			continue
		}
		fs := facts(d)
		byRepo[d.Repo] = append(byRepo[d.Repo], fit.Example{Repo: d.Repo, Number: d.Number, Merged: m.MergedAt,
			Facts: fs, TroubleAt: trouble[key(d.Repo, d.Number)], W: 1})
		if m.MergedAt.Before(from) || !m.MergedAt.Before(now) {
			continue
		}
		if counts[d.Repo] == nil {
			counts[d.Repo] = map[string]*tally{}
		}
		c := carefulWithout(d)
		for _, f := range fs {
			t := counts[d.Repo][f]
			if t == nil {
				t = &tally{}
				counts[d.Repo][f] = t
			}
			t.n++
			if c {
				t.careful++
			}
		}
	}
	fits = map[string]fit.Result{}
	already = map[string]map[string]float64{}
	for repo, ex := range byRepo {
		fits[repo] = fit.Fit(repo, ex, now, opts)
		already[repo] = map[string]float64{}
		for f, t := range counts[repo] {
			already[repo][f] = float64(t.careful) / float64(t.n)
		}
	}
	return fits, already
}

// Trial is a learned rule's record on pull requests merged since its
// shadow trial started: PRs it never saw when it was learned.
type Trial struct {
	Merged int // PRs merged since the trial started
	Fired  int // of those, PRs the rule matches
	// Rated: merged at least the window ago, so trouble is known.
	Rated, RatedFired     int
	Trouble, TroubleFired int
	// NewCatches: PRs with trouble the rule matched that the other rules
	// didn't send to careful. NewFlags: every matched PR they didn't.
	NewCatches, NewFlags int
	// Low is the cautious (one-sided 95%) trouble rate of matched PRs.
	Rate, Low, Base float64
	Verdict         string // "too early", "ahead", "not ahead"
	Why             string
}

// Trial minimums: fewer matched rated PRs, or fewer rated PRs with
// trouble, than these and the record is too thin to read.
const (
	TrialMinFired   = 10
	TrialMinTrouble = 5
)

// Score is a learned rule's trial record.
func Score(l fit.Learned, decisions []pipeline.Decision, st *State, now time.Time, window time.Duration, facts FactsFunc) Trial {
	var t Trial
	if l.ShadowSince.IsZero() {
		return t
	}
	merges := map[string]Merge{}
	for _, m := range st.Merges(l.Repo) {
		merges[key(m.Repo, m.Number)] = m
	}
	bad := map[string]bool{}
	tr := troubleAt(st)
	for k, at := range tr {
		if m, ok := merges[k]; ok && at.Sub(m.MergedAt) <= window {
			bad[k] = true
		}
	}
	for _, d := range decisions {
		if d.State != "merged" || !strings.EqualFold(d.Repo, l.Repo) {
			continue
		}
		m, ok := merges[key(d.Repo, d.Number)]
		if !ok || m.MergedAt.Before(l.ShadowSince) {
			continue
		}
		fired := false
		for _, f := range facts(d) {
			if f == l.Fact {
				fired = true
				break
			}
		}
		t.Merged++
		other := carefulWithout(d)
		if fired {
			t.Fired++
			if !other {
				t.NewFlags++
			}
		}
		if now.Sub(m.MergedAt) < window {
			continue
		}
		b := bad[key(d.Repo, d.Number)]
		t.Rated++
		if b {
			t.Trouble++
		}
		if fired {
			t.RatedFired++
			if b {
				t.TroubleFired++
				if !other {
					t.NewCatches++
				}
			}
		}
	}
	if t.Rated > 0 {
		t.Base = float64(t.Trouble) / float64(t.Rated)
	}
	if t.RatedFired > 0 {
		t.Rate = float64(t.TroubleFired) / float64(t.RatedFired)
		t.Low = fit.WilsonLow(t.TroubleFired, float64(t.RatedFired), 1.645)
	}
	switch {
	case t.RatedFired < TrialMinFired || t.Trouble < TrialMinTrouble:
		t.Verdict = "too early"
		t.Why = "needs at least 10 matching pull requests past the window, and 5 with trouble among all of them, before the record says anything"
	case t.Low > t.Base:
		t.Verdict = "ahead"
		t.Why = "matching pull requests had trouble more often than average, by more than chance would explain"
	default:
		t.Verdict = "not ahead"
		t.Why = "so far, matching pull requests haven't had trouble clearly more often than average"
	}
	return t
}
