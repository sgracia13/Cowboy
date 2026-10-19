// Command cowboy-eval runs the stripped-label test: real PRs whose agent
// labels were removed are scored by the agent check exactly as Cowboy would
// score them, replaying each author's PRs in order so every PR is judged
// only against that author's earlier work.
//
//	cowboy-eval -in prs.jsonl -out results.jsonl [-hider always|sometimes] [-history 2022]
//
// Input records come from scripts/eval/collect.py. Agent markers are already
// removed from the text; the "label" field is the ground truth.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/sgracia13/Cowboy/internal/detect"
)

type rec struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Period   string `json:"period"`
	Author   string `json:"author"`
	Merged   string `json:"merged"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	BodyKind string `json:"body_kind"`
	Label    string `json:"label"`
	Commits  []struct {
		SHA     string `json:"sha"`
		Author  string `json:"author"`
		Message string `json:"message"`
		AD      string `json:"author_date"`
		CD      string `json:"committer_date"`
	} `json:"commits"`
	Files []struct {
		Path    string   `json:"path"`
		Added   []string `json:"added"`
		Removed []string `json:"removed"`
	} `json:"files"`
	merged time.Time
}

type result struct {
	Repo      string                    `json:"repo"`
	Number    int                       `json:"number"`
	Period    string                    `json:"period"`
	Author    string                    `json:"author"`
	Truth     string                    `json:"truth"` // agent (label stripped) | human-2022 | unlabelled
	Agent     string                    `json:"agent,omitempty"`
	Score     int                       `json:"score"`
	Low       int                       `json:"low"`
	High      int                       `json:"high"`
	Flagged   bool                      `json:"flagged"`
	Abstained bool                      `json:"abstained"`
	Cold      bool                      `json:"cold"`
	Samples   int                       `json:"samples"`
	Commits   int                       `json:"commits"`
	Lines     int                       `json:"lines"`
	BodyKind  string                    `json:"body_kind"`
	Modules   []detect.ModuleTotal      `json:"modules"`
	Reasons   []detect.Contribution     `json:"reasons"`
	Matrix    *detect.DetectionMatrix   `json:"matrix,omitempty"`
	Baseline  *detect.DeveloperBaseline `json:"baseline,omitempty"`
}

func main() {
	in := flag.String("in", "", "input jsonl (one or more files, comma separated not supported: cat them)")
	out := flag.String("out", "results.jsonl", "output jsonl")
	hider := flag.String("hider", "sometimes", "sometimes: the author's other agent PRs are known (excluded from their baseline); always: they never label, so agent PRs count as their normal")
	window := flag.Int("window", 90, "baseline window in days")
	prior := flag.Float64("prior", 0.08, "scorer prior")
	labelAt := flag.Float64("label-at", 0.80, "label line for the low end of the range")
	withMatrix := flag.Bool("matrix", false, "include the full matrix and baseline in each result (for fitting)")
	authorDates := flag.Bool("author-dates", false, "time commits by author date instead of committer date (survives rebases)")
	flag.Parse()

	f, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	var recs []*rec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<20), 64<<20)
	for sc.Scan() {
		var r rec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			log.Fatal(err)
		}
		r.merged, _ = time.Parse(time.RFC3339, r.Merged)
		recs = append(recs, &r)
	}
	if err := sc.Err(); err != nil {
		log.Fatal(err)
	}

	// replay per repo + author + period, oldest first
	groups := map[string][]*rec{}
	for _, r := range recs {
		k := r.Repo + "|" + r.Period + "|" + r.Author
		groups[k] = append(groups[k], r)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	scorer := detect.ScorerV0{Prior: *prior, LabelAt: *labelAt}
	o, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer o.Close()
	enc := json.NewEncoder(o)
	n := 0
	for _, k := range keys {
		g := groups[k]
		sort.Slice(g, func(i, j int) bool { return g[i].merged.Before(g[j].merged) })
		var past []detect.PastPR
		for _, r := range g {
			s := snapshot(r)
			if *authorDates {
				for i := range s.Commits {
					s.Commits[i].CommitterDate = s.Commits[i].AuthorDate
				}
			}
			base := detect.BuildBaseline("github", r.Author, past, r.merged, *window, 0)
			m := detect.Build(s, base, r.merged)
			a := scorer.Score(m)

			truth := "unlabelled"
			switch {
			case r.Label != "":
				truth = "agent"
			case r.Period == "2022":
				truth = "human-2022"
			}
			lines := 0
			for _, fc := range r.Files {
				lines += len(fc.Added) + len(fc.Removed)
			}
			res := result{Repo: r.Repo, Number: r.Number, Period: r.Period, Author: r.Author, Truth: truth, Agent: r.Label,
				Score: a.Score, Low: a.Low, High: a.High, Flagged: a.Label == "likely agent", Abstained: a.Abstained,
				Cold: base.ColdStart, Samples: base.SamplePRs, Commits: len(r.Commits), Lines: lines, BodyKind: r.BodyKind,
				Modules: a.Modules, Reasons: a.Reasons}
			if *withMatrix {
				mm, bb := m, base
				res.Matrix, res.Baseline = &mm, &bb
			}
			enc.Encode(res)
			n++

			// what Cowboy would remember about this PR once it closes
			var hours []int
			for _, c := range s.Commits {
				hours = append(hours, c.AuthorDate.UTC().Hour())
			}
			p := detect.Summarize(m, r.merged, hours)
			if *hider == "sometimes" && r.Label != "" {
				p.DisclosedAgent = r.Label
			}
			past = append(past, p)
		}
	}
	fmt.Fprintf(os.Stderr, "scored %d PRs in %d author groups -> %s\n", n, len(keys), *out)
}

func snapshot(r *rec) detect.PRSnapshot {
	s := detect.PRSnapshot{Repo: r.Repo, Number: r.Number, Author: r.Author, Forge: "github",
		Title: r.Title, Body: r.Body}
	for _, c := range r.Commits {
		ad, _ := time.Parse(time.RFC3339, c.AD)
		cd, _ := time.Parse(time.RFC3339, c.CD)
		s.Commits = append(s.Commits, detect.CommitInfo{SHA: c.SHA, Message: c.Message, AuthorDate: ad, CommitterDate: cd})
	}
	s.CommitsKnown = len(s.Commits) > 0
	if s.CommitsKnown {
		s.HeadSHA = r.Commits[0].SHA
	}
	for _, fc := range r.Files {
		s.Files = append(s.Files, detect.FileChange{Path: fc.Path, Status: "modified", Added: fc.Added, Removed: fc.Removed})
	}
	// Not available from git alone, so left unknown: branch name, PR open
	// time, first push, reviews, force pushes, account age, PR template.
	return s
}
