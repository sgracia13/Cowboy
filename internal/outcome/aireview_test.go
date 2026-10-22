package outcome

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/forge"
)

func TestFromBotReview(t *testing.T) {
	merged := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	br := forge.BotReview{
		Reviewers: []string{"greptile-apps[bot]", "github-actions[bot]", "cursor[bot]"},
		Notes: []forge.BotNote{
			{Login: "greptile-apps[bot]", Path: "a.go", Line: 3, Text: "**Bug:** nil map write", At: merged.Add(-time.Hour)},
			{Login: "github-actions[bot]", Path: "a.go", Text: "lint", At: merged.Add(-time.Hour)},
			{Login: "cursor[bot]", Path: "b.go", Text: "said after merge", At: merged.Add(time.Hour)},
		},
	}
	names, fs := FromBotReview(br, merged)
	if len(names) != 2 || names[0] != "Cursor Bugbot" || names[1] != "Greptile" {
		t.Errorf("reviewers: %v", names)
	}
	if len(fs) != 1 || fs[0].Reviewer != "Greptile" || fs[0].Text != "nil map write" || fs[0].Line != 3 {
		t.Errorf("findings: %+v", fs)
	}
}

// A reviewer whose flagged PRs go wrong far more often earns "signal"; one
// that flags at random earns "no-signal"; a thin history is "too-early".
func TestReviewerRecords(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	st, _ := Open(filepath.Join(dir, "o.json"))
	n := 0
	put := func(reviewers []string, flags map[string]bool, bad bool, age time.Duration) {
		n++
		m := Merge{Repo: "o/r", Number: n, MergedAt: now.Add(-age), AIKnown: true, AIReviewers: reviewers}
		for r, f := range flags {
			if f {
				m.AIFindings = append(m.AIFindings, AIFinding{Reviewer: r, Text: "x", At: m.MergedAt})
			}
		}
		st.PutMerge(m)
		if bad {
			st.Add(Outcome{Repo: "o/r", Number: n, Kind: Revert, Status: Auto, At: m.MergedAt.Add(24 * time.Hour), Evidence: "r"})
		}
	}
	old := 60 * 24 * time.Hour
	for i := 0; i < 60; i++ {
		// Sharp flags 1 in 2; 22 of its 30 flagged go bad, 1 of 30 quiet do.
		sharp := i%2 == 0
		bad := (sharp && i%8 != 0) || (!sharp && i == 1)
		// Noisy flags every third PR regardless.
		put([]string{"Sharp", "Noisy"}, map[string]bool{"Sharp": sharp, "Noisy": i%3 == 0}, bad, old)
	}
	put([]string{"Sharp"}, map[string]bool{"Sharp": true}, true, 5*24*time.Hour) // not rated yet
	put([]string{"Rare"}, map[string]bool{"Rare": true}, false, old)
	st.PutMerge(Merge{Repo: "o/r", Number: 999, MergedAt: now.Add(-old)}) // AI reviews never read: ignored
	recs := map[string]ReviewerRecord{}
	for _, r := range ReviewerRecords(st, "o/r", now, 30*24*time.Hour) {
		recs[r.Reviewer] = r
	}
	s := recs["Sharp"]
	if s.Reviewed != 60 || s.Flagged.N != 30 || s.Flagged.Bad != 22 || s.Quiet.Bad != 1 || s.Verdict != "signal" {
		t.Errorf("sharp: %+v", s)
	}
	if v := recs["Noisy"].Verdict; v != "no-signal" {
		t.Errorf("noisy: %s (%s)", v, recs["Noisy"].Says)
	}
	if v := recs["Rare"].Verdict; v != "too-early" {
		t.Errorf("rare: %s", v)
	}
	// called it: marked, saved, counted, undone
	f, err := st.CallIt("o/r", 1, 0, "ann", true, now)
	if err != nil || f.CalledBy != "ann" {
		t.Fatalf("%+v %v", f, err)
	}
	st2, _ := Open(filepath.Join(dir, "o.json"))
	if m, _ := st2.Merge("o/r", 1); len(m.AIFindings) == 0 || m.AIFindings[0].CalledBy != "ann" {
		t.Errorf("called it wasn't saved: %+v", m.AIFindings)
	}
	for _, r := range ReviewerRecords(st, "o/r", now, 30*24*time.Hour) {
		if r.Reviewer == "Sharp" && r.CalledIt != 1 {
			t.Error("called it not counted")
		}
	}
	if _, err := st.CallIt("o/r", 1, 9, "ann", true, now); err == nil {
		t.Error("a finding that isn't there")
	}
}

type aiHost struct {
	*forge.Fake
	calls int
}

func (h *aiHost) BotReviews(repo string, n int) (forge.BotReview, error) {
	h.calls++
	return forge.BotReview{Reviewers: []string{"coderabbitai[bot]"},
		Notes: []forge.BotNote{{Login: "coderabbitai[bot]", Path: "x.go", Text: "**Bug:** off by one", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}}, nil
}

// Merges recorded before Cowboy read AI reviews get them filled in, a
// capped number per run.
func TestFillAIReviews(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "o.json"))
	for i := 1; i <= AIFillPerRun+5; i++ {
		st.PutMerge(Merge{Repo: "o/r", Number: i, MergedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)})
	}
	h := &aiHost{Fake: forge.NewFake("", "")}
	w := &Watcher{Forge: h, State: st}
	w.fillAIReviews("o/r")
	if h.calls != AIFillPerRun {
		t.Errorf("first run read %d, want the cap %d", h.calls, AIFillPerRun)
	}
	w.fillAIReviews("o/r")
	if h.calls != AIFillPerRun+5 {
		t.Errorf("second run should finish the rest: %d", h.calls)
	}
	m, _ := st.Merge("o/r", 1)
	if !m.AIKnown || len(m.AIFindings) != 1 || m.AIFindings[0].Reviewer != "CodeRabbit" || m.AIFindings[0].Text != "off by one" {
		t.Errorf("%+v", m)
	}
}
