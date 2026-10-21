package habits

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/shortcuts"
)

var now = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

const window = 30 * 24 * time.Hour

// history: n merged PRs spread over the last 300 days. withTests decides
// which change tests; bad decides which had trouble.
func history(n int, withTests, bad func(i int) bool) ([]pipeline.Decision, *outcome.State) {
	st, _ := outcome.Open("")
	var ds []pipeline.Decision
	ok := true
	for i := 0; i < n; i++ {
		merged := now.Add(-time.Duration(i%300) * 24 * time.Hour)
		classes := map[string]int{"source": 1}
		if withTests(i) {
			classes["test"] = 1
		}
		sig := score.Signals{LinesChanged: 20 + (i*37)%300, SourceLines: 20, Classes: classes, ChecksOK: &ok}
		if i%9 == 0 {
			sig.Shortcuts = []shortcuts.Flag{{Kind: "suppressions", File: "a.go", Detail: "//nolint"}}
		}
		ds = append(ds, pipeline.Decision{Repo: "o/r", Number: i + 1, State: "merged", Pen: score.Normal, Signals: sig})
		st.PutMerge(outcome.Merge{Repo: "o/r", Number: i + 1, MergedAt: merged, ReviewsKnown: true, Approvers: []string{"ann"}[:i%2]})
		if bad(i) {
			st.Add(outcome.Outcome{Repo: "o/r", Number: i + 1, Kind: outcome.Revert, Status: outcome.Auto, At: merged.Add(48 * time.Hour), Evidence: fmt.Sprint(i)})
		}
	}
	return ds, st
}

func row(r Report, key string) Row {
	for _, x := range r.Rows {
		if x.Key == key {
			return x
		}
	}
	return Row{}
}

func TestSharesAndThinEvidence(t *testing.T) {
	ds, st := history(600, func(i int) bool { return i%4 != 0 }, func(i int) bool { return i%100 == 3 })
	r := Build("o/r", ds, st, config.Default(), now, window, 90)
	if r.Merged == 0 {
		t.Fatal("no PRs in the period")
	}
	tests := row(r, "tests")
	if tests.Share < 0.7 || tests.Share > 0.8 {
		t.Errorf("tests share %.2f, want about 0.75", tests.Share)
	}
	if tests.Before < 0 {
		t.Error("the earlier period should have a share")
	}
	if tests.Verdict != NotEnough {
		t.Errorf("6 PRs with trouble should be 'not enough evidence', got %q", tests.Verdict)
	}
	if rv := row(r, "reviewed"); rv.Share < 0.4 || rv.Share > 0.6 {
		t.Errorf("reviewed share %.2f", rv.Share)
	}
	if g := row(r, "green"); g.Share != 1 {
		t.Errorf("green share %.2f", g.Share)
	}
}

func TestVerdicts(t *testing.T) {
	// PRs without tests have trouble far more often: the habit holds here
	ds, st := history(1500, func(i int) bool { return i%3 != 0 }, func(i int) bool { return i%3 == 0 && i%5 == 0 || i%97 == 1 })
	r := Build("o/r", ds, st, config.Default(), now, window, 90)
	if v := row(r, "tests"); v.Verdict != Holds || v.RR >= 1 || v.High >= 1 {
		t.Errorf("a strong habit should hold: %+v", v)
	}
	// trouble unrelated to tests: no demonstrated difference
	ds, st = history(1500, func(i int) bool { return i%3 != 0 }, func(i int) bool { return (i*7919)%31 == 2 })
	r = Build("o/r", ds, st, config.Default(), now, window, 90)
	if v := row(r, "tests"); v.Verdict != NoDifference {
		t.Errorf("an unrelated habit shouldn't hold: %+v", v)
	}
}

func TestGuide(t *testing.T) {
	ds, st := history(600, func(i int) bool { return i%4 != 0 }, func(i int) bool { return false })
	cfg := config.Default()
	cfg.Shortcuts["skips"] = false
	g := Guide("acme/app", cfg, Build("o/r", ds, st, cfg, now, window, 90))
	for _, want := range []string{Start, End, "## Working in acme/app", "`auth/**`", "Don't silence warnings", "Co-authored-by:", "cowboy check", "This team's habits", "Tests with code"} {
		if !strings.Contains(g, want) {
			t.Errorf("guide lacks %q", want)
		}
	}
	if strings.Contains(g, "Don't skip tests") {
		t.Error("a shortcut check that's switched off shouldn't be in the guide")
	}
	if strings.Contains(g, "less trouble") || strings.Contains(g, "as often as those") {
		t.Error("no habit holds here, so the guide shouldn't claim one does")
	}
	// rows too thin to show: no empty habits heading
	thin := Report{Days: 90, Merged: 3, Rows: []Row{{Habit: All[0], Applies: 3, Share: 1}}}
	if g := Guide("acme/app", cfg, thin); strings.Contains(g, "This team's habits") {
		t.Error("no habit has 10 pull requests, so the habits heading shouldn't appear")
	}
	// no history yet: rules and shortcuts only
	if g := Guide("acme/app", config.Default(), Report{}); strings.Contains(g, "This team's habits") || !strings.Contains(g, "Don't skip tests") {
		t.Errorf("guide without history:\n%s", g)
	}
}

func TestApply(t *testing.T) {
	g := Guide("acme/app", config.Default(), Report{})
	if got := Apply("", g); got != g {
		t.Error("empty file should become the guide")
	}
	mine := "# Agents\n\nUse pnpm.\n"
	once := Apply(mine, g)
	if !strings.HasPrefix(once, mine[:len(mine)-1]) || !strings.Contains(once, Start) {
		t.Fatalf("appending:\n%s", once)
	}
	g2 := strings.Replace(g, "acme/app", "acme/web", 1)
	twice := Apply(once+"\nMore notes.\n", g2)
	if strings.Count(twice, Start) != 1 || !strings.Contains(twice, "acme/web") || strings.Contains(twice, "Working in acme/app") || !strings.Contains(twice, "More notes.") || !strings.Contains(twice, "Use pnpm.") {
		t.Errorf("replacing should keep the rest of the file and one section:\n%s", twice)
	}
}

func TestClassesFromPaths(t *testing.T) {
	st, _ := outcome.Open("")
	var ds []pipeline.Decision
	for i := 0; i < 40; i++ {
		paths := []string{"lib/a.go"}
		if i%2 == 0 {
			paths = append(paths, "lib/a_test.go")
		}
		ds = append(ds, pipeline.Decision{Repo: "o/r", Number: i + 1, State: "merged", Signals: score.Signals{LinesChanged: 30, Paths: paths}})
		st.PutMerge(outcome.Merge{Repo: "o/r", Number: i + 1, MergedAt: now.Add(-time.Duration(i) * 24 * time.Hour)})
	}
	r := Build("o/r", ds, st, config.Default(), now, window, 90)
	if v := row(r, "tests"); v.Applies != 40 || v.Share != 0.5 {
		t.Errorf("tests from paths: %+v", v)
	}
	if v := row(r, "reviewable"); v.Applies != 0 {
		t.Errorf("source lines unknown, so size can't be judged: %+v", v)
	}
}

func TestNarrow(t *testing.T) {
	if (Row{Verdict: NoDifference, Low: 0.54, High: 7.08}).Narrow() {
		t.Error("a wide range isn't evidence of no effect")
	}
	if !(Row{Verdict: NoDifference, Low: 0.9, High: 1.1}).Narrow() {
		t.Error("a tight range around 1 is")
	}
}
