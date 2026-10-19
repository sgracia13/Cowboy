package outcome

import (
	"time"

	"github.com/sgracia13/Cowboy/internal/evaluate"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

// Items turns merged PRs whose window has passed into evaluation items:
// trouble is any counted outcome within the window; the rule judged is
// the pen Cowboy gave (not a person's correction), careful as the flag
// and easy as the safe call. weight (nil on a live repo, where every PR is
// counted once) gives a sampled replay's weights and bootstrap strata.
func Items(decisions []pipeline.Decision, st *State, now time.Time, window time.Duration, weight func(repo string, n int) (float64, string)) []evaluate.Item {
	merges := map[string]Merge{}
	for _, m := range st.Merges("") {
		merges[key(m.Repo, m.Number)] = m
	}
	bad := map[string]bool{}
	for _, o := range st.Outcomes("", 0) {
		if !o.Counts() {
			continue
		}
		m, ok := merges[key(o.Repo, o.Number)]
		if ok && o.At.Sub(m.MergedAt) <= window {
			bad[key(o.Repo, o.Number)] = true
		}
	}
	var items []evaluate.Item
	for _, d := range decisions {
		if d.State != "merged" {
			continue
		}
		m, ok := merges[key(d.Repo, d.Number)]
		if !ok || now.Sub(m.MergedAt) < window {
			continue
		}
		it := evaluate.Item{Repo: d.Repo, Number: d.Number, W: 1, Bad: bad[key(d.Repo, d.Number)],
			Flag: d.Pen == score.Careful, Safe: d.Pen == score.Easy, Lines: d.Signals.LinesChanged, Files: d.Signals.Files}
		if weight != nil {
			it.W, it.Stratum = weight(d.Repo, d.Number)
		}
		items = append(items, it)
	}
	return items
}
