package evaluate

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// synth: n PRs per repo; about 3% have trouble. careful flags 20%, catching
// trouble at chance. score(i, bad) gives the model's score.
func synth(repos, n int, score func(rng *rand.Rand, bad bool) (float64, bool)) []Item {
	rng := rand.New(rand.NewSource(1))
	var items []Item
	for r := 0; r < repos; r++ {
		for i := 0; i < n; i++ {
			bad := rng.Float64() < 0.03
			s, ok := score(rng, bad)
			items = append(items, Item{Repo: fmt.Sprintf("o/r%d", r), Number: i + 1, W: 1, Bad: bad,
				Flag: rng.Float64() < 0.2, Safe: rng.Float64() < 0.3, Lines: 1 + rng.Intn(500), Files: 1 + rng.Intn(20),
				Score: s, Scored: ok})
		}
	}
	return items
}

var fast = Options{Draws: 300, Seed: 2026, MinEvents: MinEvents}

// A positive control: a model that sees the answer must clear every line.
func TestCompareAIKnowsTheAnswer(t *testing.T) {
	items := synth(3, 1500, func(rng *rand.Rand, bad bool) (float64, bool) {
		if bad {
			return 90, true
		}
		return float64(rng.Intn(50)), true
	})
	r := CompareAI(items, fast)
	if r.Verdict["B"] != Outperformed || r.Verdict["D"] != Outperformed {
		t.Fatalf("an oracle should win: %+v", r.Verdict)
	}
	if r.Recall["ai"] < 0.99 || r.Diffs["ai-rule"].Low <= 0 {
		t.Errorf("recall %+v diffs %+v", r.Recall, r.Diffs)
	}
}

// A negative control: scores unrelated to trouble must not "outperform".
func TestCompareAIKnowsNothing(t *testing.T) {
	items := synth(3, 1500, func(rng *rand.Rand, bad bool) (float64, bool) { return float64(rng.Intn(101)), true })
	r := CompareAI(items, fast)
	if r.Verdict["B"] == Outperformed || r.Verdict["D"] == Outperformed {
		t.Fatalf("a random scorer must not win: %+v", r.Verdict)
	}
	if r.Verdict["ai-chance"] != NoAdvantage {
		t.Errorf("a random scorer should be no different from chance: %+v", r.Diffs["ai-chance"])
	}
}

func TestCompareAINotEnoughEvidence(t *testing.T) {
	items := synth(2, 300, func(rng *rand.Rand, bad bool) (float64, bool) { return 50, true }) // ~9 events per repo
	r := CompareAI(items, fast)
	for _, k := range append([]string{"B", "D"}, AIDiffs...) {
		if r.Verdict[k] != Insufficient {
			t.Errorf("%s: %q, want %q", k, r.Verdict[k], Insufficient)
		}
	}
	if len(r.Counted) != 0 || r.Events != 0 {
		t.Errorf("nothing should be counted: %+v", r)
	}
}

// Unscored PRs are left out of every method, and counted, including how
// many of them had trouble.
func TestCompareAISamePopulation(t *testing.T) {
	items := synth(1, 2000, func(rng *rand.Rand, bad bool) (float64, bool) { return float64(rng.Intn(101)), rng.Float64() > 0.1 })
	r := CompareAI(items, fast)
	scored, unscored, unscoredBad := 0, 0, 0
	for _, it := range items {
		if it.Scored {
			scored++
		} else {
			unscored++
			if it.Bad {
				unscoredBad++
			}
		}
	}
	got := r.Repos[0]
	if got.PRs != scored || got.Unscored != unscored || got.UnscoredBad != unscoredBad {
		t.Fatalf("population: %+v, want %d scored, %d unscored (%d bad)", got, scored, unscored, unscoredBad)
	}
	// rules are judged on the scored PRs only: the same population as the model
	var onlyScored []Item
	for _, it := range items {
		if it.Scored {
			onlyScored = append(onlyScored, it)
		}
	}
	if CompareAI(onlyScored, fast).Recall["rule"] != r.Recall["rule"] {
		t.Error("the rule's recall must be measured on the same PRs as the model's")
	}
}

func TestCompareAIIsDeterministic(t *testing.T) {
	items := synth(2, 1500, func(rng *rand.Rand, bad bool) (float64, bool) { return float64(rng.Intn(5)), true }) // heavy ties
	a, b := CompareAI(items, fast), CompareAI(items, fast)
	if !reflect.DeepEqual(a, b) {
		t.Error("same input, same seed: results must match")
	}
	// reordering the input must not change anything either
	rev := append([]Item(nil), items...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if c := CompareAI(rev, fast); c.Recall["ai"] != a.Recall["ai"] {
		t.Errorf("input order changed the model's recall: %v vs %v", c.Recall["ai"], a.Recall["ai"])
	}
}

// The published Evaluate ignores scores.
func TestEvaluateIgnoresScores(t *testing.T) {
	items := synth(2, 1500, func(rng *rand.Rand, bad bool) (float64, bool) { return 0, false })
	a := Evaluate(items, fast)
	for i := range items {
		items[i].Score, items[i].Scored = 99, true
	}
	if b := Evaluate(items, fast); !reflect.DeepEqual(a, b) {
		t.Error("scores changed the rules' evaluation")
	}
}

// With every PR scored, the rules' and baselines' recall must match the
// published Evaluate exactly: the comparison adds the model, nothing else.
func TestCompareAIMatchesEvaluate(t *testing.T) {
	items := synth(3, 1500, func(rng *rand.Rand, bad bool) (float64, bool) { return float64(rng.Intn(101)), true })
	a, b := Evaluate(items, fast), CompareAI(items, fast)
	for _, k := range []string{"rule", "size", "spread", "chance"} {
		if a.Recall[k] != b.Recall[k] {
			t.Errorf("%s: Evaluate %v, CompareAI %v", k, a.Recall[k], b.Recall[k])
		}
	}
}
