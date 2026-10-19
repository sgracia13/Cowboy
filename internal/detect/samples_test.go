package detect_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/detect"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/forge"
)

var now = time.Date(2026, 10, 18, 18, 0, 0, 0, time.UTC)

func assess(t *testing.T, n int) (detect.DetectionMatrix, detect.Assessment) {
	t.Helper()
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	bs, _ := detect.OpenBaselines("", 90)
	bs.Now = func() time.Time { return now }
	if err := detect.SeedDir(bs, "demo", filepath.Join(td, "history"), now); err != nil {
		t.Fatal(err)
	}
	snap, err := f.Snapshot("acme/app", n)
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := f.GetDiff("acme/app", n)
	for _, fl := range diff.Parse(patch) {
		snap.Files = append(snap.Files, detect.FileChange{Path: fl.Path, Status: fl.Status, Added: fl.Added, Removed: fl.Removed})
	}
	base, _ := bs.Get("demo", snap.Author)
	m := detect.Build(snap, base, now)
	return m, detect.NewScorer().Score(m)
}

func show(a detect.Assessment) string {
	s := fmt.Sprintf("score %d [%d-%d] label=%q abstain=%v\n", a.Score, a.Low, a.High, a.Label, a.Abstained)
	for _, r := range a.Reasons {
		s += fmt.Sprintf("   %+.2f %-8s %s\n", r.Effect, r.Module, r.Detail)
	}
	return s
}

// Both directions: masked agent PRs are caught, humans (including a very
// fast one) are not.
func TestSamplePRs(t *testing.T) {
	cases := []struct {
		n     int
		label string
	}{
		{540, "likely agent"}, // codex/ branch, 3 a.m. burst, template text
		{542, "likely agent"}, // no fingerprint at all: behaviour + text + code
		{541, ""},             // sam's codemod: fast, but normal for sam
		{520, ""},             // alice's small change
	}
	for _, c := range cases {
		_, a := assess(t, c.n)
		t.Logf("#%d %s", c.n, show(a))
		if a.Label != c.label {
			t.Errorf("#%d: label %q, want %q\n%s", c.n, a.Label, c.label, show(a))
		}
	}
}

// Speed alone can never earn the label, however extreme.
func TestSpeedAloneNeverLabels(t *testing.T) {
	m, _ := assess(t, 541)
	m.Time.LinesPerMinute = detect.Observed[float64]{Value: 5000, Known: true}
	m.Time.BurstMaxCommits = detect.Observed[int]{Value: 9, Known: true}
	m.Time.FirstPushToPRSec = detect.Observed[float64]{Value: 1, Known: true}
	m.Time.OffHoursShare = detect.Observed[float64]{Value: 1, Known: true}
	m.Text = detect.TextualTypology{} // nothing from the words
	m.Code = detect.CodeEntropy{LinesChanged: m.Code.LinesChanged}
	a := detect.NewScorer().Score(m)
	if a.Label != "" {
		t.Fatalf("speed alone labelled the PR:\n%s", show(a))
	}
}

// Unknown is not evidence: an empty matrix abstains at the prior.
func TestUnknownIsNotEvidence(t *testing.T) {
	a := detect.NewScorer().Score(detect.DetectionMatrix{Baseline: detect.DeveloperBaseline{ColdStart: true}})
	if !a.Abstained || a.Score != 8 || a.Label != "" {
		t.Fatalf("empty matrix: %s", show(a))
	}
}

func TestDisclosedIs100(t *testing.T) {
	a := detect.NewScorer().Score(detect.DetectionMatrix{Disclosure: detect.Disclosure{Agent: "codex", How: []string{"bot account"}}})
	if a.Score != 100 || a.Label != "disclosed agent" {
		t.Fatal(show(a))
	}
}

// A person correcting the label makes the engine ask for more next time.
func TestCorrectionsRaiseTheBar(t *testing.T) {
	m, before := assess(t, 542)
	m.Baseline.CorrectionsAgainst = 3
	after := detect.NewScorer().Score(m)
	if after.Score >= before.Score {
		t.Fatalf("corrections didn't lower the score: %d -> %d", before.Score, after.Score)
	}
}

// Same matrix, same answer.
func TestScoreDeterministic(t *testing.T) {
	m, first := assess(t, 540)
	for i := 0; i < 100; i++ {
		if a := detect.NewScorer().Score(m); fmt.Sprint(a) != fmt.Sprint(first) {
			t.Fatal("score changed between runs")
		}
	}
}

// A brand-new author with no history gets half-weight behaviour and a wider
// range, so the bar is higher. Logged for inspection.
func TestColdStartIsCautious(t *testing.T) {
	m, warm := assess(t, 542)
	m.Baseline = detect.DeveloperBaseline{Author: "newbie", ColdStart: true, Verification: m.Baseline.Verification}
	cold := detect.NewScorer().Score(m)
	t.Logf("warm %s\ncold %s", show(warm), show(cold))
	if cold.Low > warm.Low {
		t.Fatal("cold start should never be more confident")
	}
}
