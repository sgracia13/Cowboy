package outcome

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/reviewers"
)

// SummaryChars is how much of each AI finding is kept: enough to recognize
// it, not the whole comment (the full text stays on the git host).
const SummaryChars = 240

// FromBotReview keeps the AI reviewers' part of a host's bot reviews, and
// only what was said before the cutoff (merge time; zero = everything).
func FromBotReview(br forge.BotReview, before time.Time) ([]string, []AIFinding) {
	var names []string
	seen := map[string]bool{}
	for _, login := range br.Reviewers {
		if n := reviewers.Name(login); n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	var fs []AIFinding
	for _, note := range br.Notes {
		n := reviewers.Name(note.Login)
		if n == "" || (!before.IsZero() && note.At.After(before)) {
			continue
		}
		text := reviewers.Summary(note.Text, SummaryChars)
		if text == "" {
			continue
		}
		fs = append(fs, AIFinding{Reviewer: n, Path: note.Path, Line: note.Line, Text: text, At: note.At.UTC()})
	}
	sort.Strings(names)
	return names, fs
}

// ReadAIReview fetches a PR's AI reviews when the host can list them.
func ReadAIReview(f forge.Forge, repo string, n int, mergedAt time.Time) ([]string, []AIFinding, bool) {
	br, ok := f.(forge.BotReviewer)
	if !ok {
		return nil, nil, false
	}
	r, err := br.BotReviews(repo, n)
	if err != nil {
		log.Printf("ai reviews %s#%d: %v", repo, n, err)
		return nil, nil, false
	}
	names, fs := FromBotReview(r, mergedAt)
	return names, fs, true
}

// AIFillPerRun caps how many older merge records get their AI reviews read
// in one run, so an upgrade doesn't spend the API budget all at once.
const AIFillPerRun = 50

// fillAIReviews reads AI reviews for merges recorded before Cowboy did that.
func (w *Watcher) fillAIReviews(repo string) {
	if _, ok := w.Forge.(forge.BotReviewer); !ok {
		return
	}
	done := 0
	for _, m := range w.State.Merges(repo) {
		if m.AIKnown {
			continue
		}
		if done >= AIFillPerRun {
			return
		}
		done++
		names, fs, ok := ReadAIReview(w.Forge, repo, m.Number, m.MergedAt)
		if ok {
			w.State.SetAIReview(repo, m.Number, names, fs)
		}
	}
}

// ReviewerRecord is one AI reviewer's track record in a repo: of the merged
// PRs it reviewed whose trouble window has passed, how often the ones it
// flagged had trouble after merge, against the ones it passed quietly.
type ReviewerRecord struct {
	Reviewer string `json:"reviewer"`
	Reviewed int    `json:"reviewed"`
	Findings int    `json:"findings"`
	Flagged  Rate   `json:"flagged"` // PRs with at least one finding: how many had trouble
	Quiet    Rate   `json:"quiet"`   // PRs it reviewed without a finding: how many had trouble
	// TroubleSeen: of the PRs it reviewed that had trouble, how many it had
	// flagged (Flagged.Bad of Flagged.Bad + Quiet.Bad).
	TroubleSeen Rate `json:"trouble_seen"`
	// CalledIt: findings a person marked as naming what went wrong.
	CalledIt int    `json:"called_it"`
	Verdict  string `json:"verdict"` // too-early, signal, no-signal, inverted
	Says     string `json:"says"`
}

// MinForVerdict: below this many PRs on either side, or this few with
// trouble, the record says it's too early.
const (
	MinForVerdict     = 20
	MinTroubleVerdict = 5
)

// ReviewerRecords scores every AI reviewer seen on rated merges ("" = all repos).
func ReviewerRecords(st *State, repo string, now time.Time, window time.Duration) []ReviewerRecord {
	bad := map[string]bool{}
	merges := map[string]Merge{}
	for _, m := range st.Merges(repo) {
		merges[key(m.Repo, m.Number)] = m
	}
	for _, o := range st.Outcomes(repo, 0) {
		if m, ok := merges[key(o.Repo, o.Number)]; ok && o.Counts() && o.At.Sub(m.MergedAt) <= window {
			bad[key(o.Repo, o.Number)] = true
		}
	}
	recs := map[string]*ReviewerRecord{}
	get := func(name string) *ReviewerRecord {
		if recs[name] == nil {
			recs[name] = &ReviewerRecord{Reviewer: name}
		}
		return recs[name]
	}
	for k, m := range merges {
		if !m.AIKnown {
			continue
		}
		rated := now.Sub(m.MergedAt) >= window
		count := map[string]int{}
		for _, f := range m.AIFindings {
			count[f.Reviewer]++
			if f.CalledBy != "" {
				get(f.Reviewer).CalledIt++
			}
		}
		if !rated {
			continue
		}
		for _, name := range m.AIReviewers {
			r := get(name)
			r.Reviewed++
			r.Findings += count[name]
			if count[name] > 0 {
				r.Flagged.add(bad[k], 1)
			} else {
				r.Quiet.add(bad[k], 1)
			}
		}
	}
	var out []ReviewerRecord
	for _, r := range recs {
		r.Flagged.finish()
		r.Quiet.finish()
		r.TroubleSeen = Rate{N: r.Flagged.Bad + r.Quiet.Bad, Bad: r.Flagged.Bad}
		r.TroubleSeen.finish()
		r.Verdict, r.Says = verdict(*r)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Reviewed != out[j].Reviewed {
			return out[i].Reviewed > out[j].Reviewed
		}
		return out[i].Reviewer < out[j].Reviewer
	})
	return out
}

func pct(p float64) string { return fmt.Sprintf("%.0f%%", p*100) }

func itoa(n int) string { return strconv.Itoa(n) }

func verdict(r ReviewerRecord) (string, string) {
	trouble := r.Flagged.Bad + r.Quiet.Bad
	if r.Flagged.N < MinForVerdict || r.Quiet.N < MinForVerdict || trouble < MinTroubleVerdict {
		return "too-early", "Not enough history yet: " + itoa(r.Reviewed) + " reviewed and rated, " + itoa(trouble) + " had trouble."
	}
	switch {
	case r.Flagged.Low > r.Quiet.High:
		return "signal", "Its warnings mean something here: " + pct(r.Flagged.P) + " of the pull requests it flagged had trouble later, against " + pct(r.Quiet.P) + " of the ones it passed."
	case r.Flagged.High < r.Quiet.Low:
		return "inverted", "Trouble came more often where it stayed quiet (" + pct(r.Quiet.P) + ") than where it flagged something (" + pct(r.Flagged.P) + ")."
	default:
		return "no-signal", "No clear difference yet: trouble followed " + pct(r.Flagged.P) + " of the pull requests it flagged and " + pct(r.Quiet.P) + " of the ones it passed."
	}
}
