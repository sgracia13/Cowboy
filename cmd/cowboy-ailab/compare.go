package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/evaluate"
)

// Population is what happened to one repo's PRs before the comparison.
type Population struct {
	Repo         string `json:"repo"`
	Rows         int    `json:"rows"`          // sorted PRs in the replay
	BeforeCut    int    `json:"before_cutoff"` // merged on or before the model's training cutoff: left out of every method
	BeforeCutBad int    `json:"before_cutoff_bad"`
	NotScored    int    `json:"not_scored"` // no line in ai.jsonl yet
	Failed       int    `json:"failed"`     // a line, but no usable score
	FailedBad    int    `json:"failed_bad"`
	Scored       int    `json:"scored"`
}

// Cost is what the scoring took, over the PRs attempted.
type Cost struct {
	Attempted   int     `json:"attempted"`
	FailureRate float64 `json:"failure_rate"`
	Truncated   float64 `json:"truncated_share"`
	MedianSec   float64 `json:"median_seconds"`
	P95Sec      float64 `json:"p95_seconds"`
	TotalHours  float64 `json:"total_hours"`
}

// Report is the whole comparison.
type Report struct {
	Run        string            `json:"run"` // model | prompt version
	Cutoff     string            `json:"model_cutoff"`
	Population []Population      `json:"population"`
	Cost       Cost              `json:"cost"`
	Bar        string            `json:"practical_bar"` // under 1% failures: met / not met
	Result     evaluate.AIResult `json:"result"`
	Complete   bool              `json:"complete"` // every eligible PR has a line
}

// buildItems marks each replay item with its score, leaving out PRs merged
// on or before the cutoff entirely (from every method).
func buildItems(items []evaluate.Item, merged map[string]time.Time, scores map[string]Scored, cutoff time.Time) ([]evaluate.Item, []Population, Cost, error) {
	pops := map[string]*Population{}
	var order []string
	var out []evaluate.Item
	var secs []float64
	cost := Cost{}
	trunc := 0
	for _, it := range items {
		p := pops[it.Repo]
		if p == nil {
			p = &Population{Repo: it.Repo}
			pops[it.Repo] = p
			order = append(order, it.Repo)
		}
		p.Rows++
		k := key(it.Repo, it.Number)
		m, ok := merged[k]
		if !ok {
			return nil, nil, cost, fmt.Errorf("%s has no merge time in the sorted file", k)
		}
		if !m.After(cutoff) {
			p.BeforeCut++
			if it.Bad {
				p.BeforeCutBad++
			}
			continue
		}
		s, ok := scores[k]
		switch {
		case !ok:
			p.NotScored++
		case s.Score == nil:
			p.Failed++
			if it.Bad {
				p.FailedBad++
			}
		default:
			it.Score, it.Scored = float64(*s.Score), true
			p.Scored++
		}
		if ok {
			cost.Attempted++
			secs = append(secs, s.Seconds)
			if s.Truncated {
				trunc++
			}
		}
		out = append(out, it)
	}
	var pl []Population
	failed := 0
	for _, r := range order {
		pl = append(pl, *pops[r])
		failed += pops[r].Failed
	}
	if cost.Attempted > 0 {
		cost.FailureRate = float64(failed) / float64(cost.Attempted)
		cost.Truncated = float64(trunc) / float64(cost.Attempted)
		sort.Float64s(secs)
		cost.MedianSec = secs[len(secs)/2]
		cost.P95Sec = secs[int(0.95*float64(len(secs)-1))]
		for _, x := range secs {
			cost.TotalHours += x / 3600
		}
	}
	return out, pl, cost, nil
}

// mergedTimes reads each sorted PR's merge time.
func mergedTimes(r io.Reader) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<20), 64<<20)
	for sc.Scan() {
		var row struct {
			Repo   string    `json:"repo"`
			Number int       `json:"number"`
			Merged time.Time `json:"merged"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			return nil, err
		}
		if !row.Merged.IsZero() {
			out[key(row.Repo, row.Number)] = row.Merged
		}
	}
	return out, sc.Err()
}

func runReport(items []evaluate.Item, merged map[string]time.Time, scores map[string]Scored, runs map[string]int, cutoff time.Time, o evaluate.Options) (Report, error) {
	if len(runs) > 1 {
		var rs []string
		for r := range runs {
			rs = append(rs, r)
		}
		sort.Strings(rs)
		return Report{}, fmt.Errorf("ai.jsonl mixes runs (%s); compare one model and prompt at a time", strings.Join(rs, "; "))
	}
	rep := Report{Cutoff: cutoff.Format("2006-01-02")}
	for r := range runs {
		rep.Run = r
	}
	its, pops, cost, err := buildItems(items, merged, scores, cutoff)
	if err != nil {
		return rep, err
	}
	rep.Population, rep.Cost = pops, cost
	rep.Complete = true
	for _, p := range pops {
		if p.NotScored > 0 {
			rep.Complete = false
		}
	}
	rep.Bar = "met"
	if cost.Attempted == 0 || cost.FailureRate >= 0.01 {
		rep.Bar = "not met"
	}
	rep.Result = evaluate.CompareAI(its, o)
	return rep, nil
}

func compareCmd(args []string) {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	sorted := fs.String("sorted", "", "rules' pens, from cmd/cowboy-sortlab")
	replay := fs.String("replay", "", "replayed outcomes, from cmd/cowboy-replay (one folder per repo)")
	logs := fs.String("logs", "", "collector logs (comma-separated), for the sampling weights")
	aiPath := fs.String("ai", "ai.jsonl", "scores, from cowboy-ailab score")
	cut := fs.String("model-cutoff", "", "the model's training cutoff (YYYY-MM-DD); PRs merged on or before it are left out")
	draws := fs.Int("draws", 2000, "bootstrap draws")
	asJSON := fs.Bool("json", false, "print every number as JSON")
	fs.Parse(args)
	if *sorted == "" || *replay == "" || *logs == "" {
		log.Fatal("compare needs -sorted, -replay and -logs")
	}
	if *cut == "" {
		log.Fatal("compare needs -model-cutoff: a model may have seen PRs merged before its training cutoff, and their reverts")
	}
	cutoff, err := time.Parse("2006-01-02", *cut)
	if err != nil {
		log.Fatalf("-model-cutoff: %v", err)
	}
	cutoff = cutoff.Add(24*time.Hour - time.Nanosecond) // the whole day
	items, err := evaluate.LoadReplay(*sorted, *replay, strings.Split(*logs, ","))
	if err != nil {
		log.Fatal(err)
	}
	sf, err := os.Open(*sorted)
	if err != nil {
		log.Fatal(err)
	}
	merged, err := mergedTimes(sf)
	sf.Close()
	if err != nil {
		log.Fatal(err)
	}
	af, err := os.Open(*aiPath)
	if err != nil {
		log.Fatal(err)
	}
	scores, runs, err := readScores(af)
	af.Close()
	if err != nil {
		log.Fatal(err)
	}
	o := evaluate.Defaults()
	o.Draws = *draws
	rep, err := runReport(items, merged, scores, runs, cutoff, o)
	if err != nil {
		log.Fatal(err)
	}
	if *asJSON {
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		e.Encode(rep)
		return
	}
	fmt.Print(text(rep))
}

func pct(x float64) string { return fmt.Sprintf("%.1f%%", 100*x) }
func pts(r evaluate.Range) string {
	return fmt.Sprintf("%+.1f pts (likely %+.1f to %+.1f)", 100*r.Value, 100*r.Low, 100*r.High)
}

// text is the report in words.
func text(r Report) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	res := r.Result
	w("AI risk scores vs Cowboy's rules vs simple alternatives\n")
	w("run: %s\nmodel training cutoff: %s (PRs merged on or before it are left out)\n\n", r.Run, r.Cutoff)
	w("population\n")
	w("  %-28s %7s %10s %10s %8s %8s\n", "repo", "rows", "pre-cutoff", "not scored", "failed", "scored")
	for _, p := range r.Population {
		w("  %-28s %7d %10d %10d %8d %8d\n", p.Repo, p.Rows, p.BeforeCut, p.NotScored, p.Failed, p.Scored)
	}
	for _, p := range r.Population {
		if p.FailedBad > 0 {
			w("  note: %d of %s's failed PRs had trouble; failures left out can bias the comparison\n", p.FailedBad, p.Repo)
		}
	}
	if !r.Complete {
		w("  the scoring run is incomplete: PRs without a score are left out of every method\n")
	}
	c := r.Cost
	w("\ncost: %d PRs attempted, %s failed, %s with the diff cut short, %.1fs median, %.1fs p95, %.1f hours in all\n", c.Attempted, pct(c.FailureRate), pct(c.Truncated), c.MedianSec, c.P95Sec, c.TotalHours)
	w("practical bar (under 1%% failures): %s\n\n", r.Bar)
	w("counted repos (20+ PRs with trouble among scored PRs): %d · %d PRs · %d with trouble\n", len(res.Counted), res.PRs, res.Events)
	for _, rp := range res.Repos {
		if !rp.Counted {
			w("  %s: %d PRs with trouble among scored PRs, not counted\n", rp.Repo, rp.BadCount)
		}
	}
	if len(res.Counted) == 0 {
		w("\nverdicts: not enough evidence yet. No repo has 20 PRs with trouble among scored, eligible PRs.\n")
		w("\n%s", limits)
		return b.String()
	}
	w("\nB. same share of PRs as careful: share of later-troubled PRs each method flags (weighted)\n")
	for _, k := range []string{"ai", "rule", "size", "spread", "chance"} {
		w("  %-10s %s\n", map[string]string{"ai": "model", "rule": "careful", "size": "largest", "spread": "most files", "chance": "chance"}[k], pct(res.Recall[k]))
	}
	for _, k := range []string{"ai-rule", "ai-size", "ai-spread", "ai-chance"} {
		w("  %-22s %s: %s\n", strings.Replace(strings.Replace(k, "ai-", "model vs ", 1), "rule", "careful", 1), pts(res.Diffs[k]), res.Verdict[k])
	}
	w("\nB. same lines read as careful\n")
	w("  model %s · careful %s · smallest first %s\n", pct(res.Lines["ai"]), pct(res.Lines["rule"]), pct(res.Lines["smallest"]))
	w("  %-22s %s: %s\n", "model vs careful", pts(res.Diffs["ai-rule@lines"]), res.Verdict["ai-rule@lines"])
	w("  %-22s %s: %s\n", "model vs smallest", pts(res.Diffs["ai-smallest@lines"]), res.Verdict["ai-smallest@lines"])
	w("\nD. rules unchanged, plus %.0f%% more PRs from those careful didn't flag: share of all trouble the extra picks catch\n", 100*evaluate.ExtraShare)
	w("  model %s · largest %s · most files %s · random %s\n", pct(res.Extra["ai"]), pct(res.Extra["size"]), pct(res.Extra["spread"]), pct(res.Extra["chance"]))
	for _, k := range []string{"d-ai-size", "d-ai-spread", "d-ai-chance"} {
		w("  %-22s %s: %s\n", strings.Replace(k, "d-ai-", "model vs ", 1), pts(res.Diffs[k]), res.Verdict[k])
	}
	w("\npre-registered verdicts (CLAIMS-ai.md)\n")
	w("  B, the model ranks instead of the rules: %s\n", res.Verdict["B"])
	w("  D, the model adds to the rules: %s\n", res.Verdict["D"])
	w("\n%s", limits)
	return b.String()
}

const limits = `limits
  trouble = a revert within 30 days of merge, found in git history. A PR with no
  revert isn't proven safe: fixes forward, incidents and slow breakage aren't counted.
  This shows what went together on these PRs, not what caused what.
  Ranges come from a bootstrap that doesn't cluster repeated PRs, so they're a
  little narrow. One large repo can decide pooled numbers; read the per-repo lines.
`
