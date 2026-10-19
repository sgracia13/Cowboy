// Command cowboy-fitlab runs the walk-forward test of fitted rules
// (scripts/eval/evidence/CLAIMS-fit.md) on replayed history: for each test
// month, fit on the 90 days before it with internal/fit, then sort that
// month's PRs with the fitted rules. It writes one line per test PR and one
// per fit; compare_fit.py scores them.
//
//	cowboy-fitlab -sorted sorted.jsonl -replay DIR -collectlog LOG... -out OUT
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/fit"
)

type row struct {
	Repo     string   `json:"repo"`
	Number   int      `json:"number"`
	Merged   string   `json:"merged"`
	Pen      string   `json:"pen"`
	Lines    int      `json:"lines"`
	Facts    []string `json:"facts"`
	Reverted bool     `json:"reverted"` // the collector's label: decides the sampling weight
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

var windowRe = regexp.MustCompile(`^(\S+/\S+): (\d+) PRs in window, (\d+) later reverted`)

func main() {
	sorted := flag.String("sorted", "", "sorted PRs from cowboy-sortlab (with facts)")
	replay := flag.String("replay", "", "cowboy-replay output dir (outcomes.json per repo)")
	out := flag.String("out", "fitlab", "output prefix")
	first := flag.String("first", "2026-02", "first test month")
	last := flag.String("last", "2026-08", "last test month")
	var logs multi
	flag.Var(&logs, "collectlog", "collect_sort.py log (repeat), for the sampling weights")
	flag.Parse()

	window := map[string][2]int{}
	for _, p := range logs {
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatal(err)
		}
		for _, l := range strings.Split(string(b), "\n") {
			if m := windowRe.FindStringSubmatch(l); m != nil {
				a, _ := strconv.Atoi(m[2])
				r, _ := strconv.Atoi(m[3])
				window[m[1]] = [2]int{a, r}
			}
		}
	}

	f, err := os.Open(*sorted)
	if err != nil {
		log.Fatal(err)
	}
	byRepo := map[string][]row{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<20), 64<<20)
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			log.Fatal(err)
		}
		if r.Facts == nil {
			log.Fatalf("%s#%d has no facts: sort with a cowboy-sortlab that writes them", r.Repo, r.Number)
		}
		byRepo[r.Repo] = append(byRepo[r.Repo], r)
	}

	prs, _ := os.Create(*out + ".prs.jsonl")
	fits, _ := os.Create(*out + ".fits.jsonl")
	defer prs.Close()
	defer fits.Close()
	o := fit.Defaults()
	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		rows := byRepo[repo]
		trouble := reverts(*replay, repo)
		w, ok := window[repo]
		if !ok {
			log.Fatalf("no collect log line for %s", repo)
		}
		others := 0
		for _, r := range rows {
			if !r.Reverted {
				others++
			}
		}
		weight := float64(w[0]-w[1]) / float64(others)
		var ex []fit.Sized
		for _, r := range rows {
			m, err := time.Parse(time.RFC3339, r.Merged)
			if err != nil {
				log.Fatal(err)
			}
			e := fit.Sized{Example: fit.Example{Repo: repo, Number: r.Number, Merged: m.UTC(), Facts: r.Facts, W: weight, TroubleAt: trouble[r.Number]},
				DefaultCareful: r.Pen == "careful", Lines: r.Lines}
			if r.Reverted {
				e.W = 1
			}
			ex = append(ex, e)
		}
		start, _ := time.Parse("2006-01", *first)
		end, _ := time.Parse("2006-01", *last)
		for t := start; !t.After(end); t = t.AddDate(0, 1, 0) {
			next := t.AddDate(0, 1, 0)
			plain := make([]fit.Example, len(ex))
			for i := range ex {
				plain[i] = ex[i].Example
			}
			res := fit.Fit(repo, plain, t, o)
			b, _ := json.Marshal(map[string]any{"month": t.Format("2006-01"), "fit": res})
			fits.Write(append(b, '\n'))
			used := res.Enough && len(res.Rules) > 0
			for _, e := range ex {
				if e.Merged.Before(t) || !e.Merged.Before(next) {
					continue
				}
				bad := !e.TroubleAt.IsZero() && e.TroubleAt.Sub(e.Merged) <= 30*24*time.Hour
				fitted := e.DefaultCareful
				var hit []string
				if used {
					for _, x := range res.Careful(e.Facts) {
						hit = append(hit, x.Fact)
					}
					fitted = len(hit) > 0
				}
				b, _ := json.Marshal(map[string]any{"repo": repo, "month": t.Format("2006-01"), "number": e.Number, "w": e.W, "bad": bad,
					"lines": e.Lines, "default": e.DefaultCareful, "fitted": fitted, "used_fit": used, "rules": hit})
				prs.Write(append(b, '\n'))
			}
			fmt.Fprintf(os.Stderr, "%s %s: %d PRs with trouble in the fit window, %d rules, share %.1f%%%s\n", repo, t.Format("2006-01"),
				res.Trouble, len(res.Rules), res.Share*100, map[bool]string{true: "", false: " (defaults)"}[used])
		}
	}
}

// reverts reads the engine's counted reverts: PR number -> when.
func reverts(dir, repo string) map[int]time.Time {
	b, err := os.ReadFile(dir + "/" + strings.ReplaceAll(repo, "/", "_") + "/outcomes.json")
	if err != nil {
		log.Fatal(err)
	}
	var st struct {
		Outcomes []struct {
			Number int       `json:"number"`
			Kind   string    `json:"kind"`
			Status string    `json:"status"`
			At     time.Time `json:"at"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		log.Fatal(err)
	}
	out := map[int]time.Time{}
	for _, o := range st.Outcomes {
		if o.Kind != "revert" || (o.Status != "auto" && o.Status != "confirmed") {
			continue
		}
		if t, ok := out[o.Number]; !ok || o.At.Before(t) {
			out[o.Number] = o.At
		}
	}
	return out
}
