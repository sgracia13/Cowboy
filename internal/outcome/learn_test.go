package outcome

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/fit"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

var testFacts FactsFunc = func(d pipeline.Decision) []string { return score.Facts(d.Signals, config.Default()) }

// history makes n merged PRs ending at end, one every 6 hours. Every 8th
// touches billing/ and has trouble when billingBad(i); others have trouble
// when otherBad(i).
func history(st *State, start int, n int, end time.Time, billingBad, otherBad func(i int) bool) []pipeline.Decision {
	var ds []pipeline.Decision
	for i := 0; i < n; i++ {
		num := start + i
		merged := end.Add(-time.Duration(n-i) * 6 * time.Hour)
		p := fmt.Sprintf("web/page%d.go", i%23)
		bad := otherBad(i)
		if i%8 == 0 {
			p, bad = "billing/charge.go", billingBad(i)
		}
		ds = append(ds, pipeline.Decision{Repo: "o/r", Number: num, State: "merged", Pen: score.Normal,
			Signals: score.Signals{Paths: []string{p}, Files: 1, LinesChanged: 20, Classes: map[string]int{"source": 1}}})
		st.PutMerge(Merge{Repo: "o/r", Number: num, MergedAt: merged})
		if bad {
			st.Add(Outcome{Repo: "o/r", Number: num, Kind: Revert, Status: Auto, At: merged.Add(48 * time.Hour), Evidence: fmt.Sprint(num)})
		}
	}
	return ds
}

func TestLearnFindsTheTroubledFolder(t *testing.T) {
	st, _ := Open("")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ds := history(st, 1, 360, now, func(i int) bool { return i%16 == 0 }, func(i int) bool { return i%50 == 7 })
	fits, already := Learn(ds, st, now, testFacts, fit.Defaults())
	r := fits["o/r"]
	if !r.Enough || len(r.Rules) == 0 {
		t.Fatalf("expected a rule: %+v", r)
	}
	if !strings.Contains(r.Rules[0].Fact, "billing") {
		t.Errorf("top rule %q, want billing", r.Rules[0].Fact)
	}
	if already["o/r"][r.Rules[0].Fact] != 0 {
		t.Errorf("no PR was careful, already = %v", already["o/r"][r.Rules[0].Fact])
	}

	// trouble unrelated to folders: nothing to suggest
	st2, _ := Open("")
	ds2 := history(st2, 1, 360, now, func(i int) bool { return i%40 == 0 }, func(i int) bool { return i%10 == 3 })
	if r := learnOne(ds2, st2, now); len(r.Rules) != 0 {
		t.Errorf("noise produced rules: %+v", r.Rules)
	}
}

func learnOne(ds []pipeline.Decision, st *State, now time.Time) fit.Result {
	f, _ := Learn(ds, st, now, testFacts, fit.Defaults())
	return f["o/r"]
}

func TestTrialCountsOnlyNewPRs(t *testing.T) {
	st, _ := Open("")
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// before the trial: billing always bad (must not count)
	old := history(st, 1, 200, start, func(int) bool { return true }, func(int) bool { return false })
	l := fit.Learned{Repo: "o/r", Fact: "path:billing/**", Status: fit.Shadow, ShadowSince: start}
	tr := Score(l, old, st, start.AddDate(0, 3, 0), 30*24*time.Hour, testFacts)
	if tr.Merged != 0 || tr.Fired != 0 {
		t.Fatalf("PRs merged before the trial counted: %+v", tr)
	}
	// a few weeks in: too early
	end := start.AddDate(0, 0, 10)
	few := history(st, 1000, 40, end, func(int) bool { return true }, func(int) bool { return false })
	tr = Score(l, append(old, few...), st, end, 30*24*time.Hour, testFacts)
	if tr.Merged != 40 || tr.Fired != 5 || tr.Rated != 0 || tr.Verdict != "too early" {
		t.Errorf("early trial: %+v", tr)
	}
	// months later, billing PRs keep having trouble: ahead
	end2 := start.AddDate(0, 4, 0)
	more := history(st, 2000, 400, end2, func(i int) bool { return i%16 == 0 }, func(i int) bool { return i%40 == 9 })
	all := append(append(old, few...), more...)
	tr = Score(l, all, st, end2, 30*24*time.Hour, testFacts)
	if tr.Verdict != "ahead" || tr.NewCatches == 0 || tr.TroubleFired == 0 {
		t.Errorf("strong trial: %+v", tr)
	}
	// a rule on an unremarkable folder isn't ahead
	l2 := l
	l2.Fact = "path:web/**"
	if tr := Score(l2, all, st, end2, 30*24*time.Hour, testFacts); tr.Verdict == "ahead" {
		t.Errorf("unremarkable rule ahead: %+v", tr)
	}
}
