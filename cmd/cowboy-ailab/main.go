// Command cowboy-ailab is the offline experiment from
// scripts/eval/evidence/CLAIMS-ai.md: does a local model's risk score find
// PRs that later had trouble better than Cowboy's rules and simple
// alternatives, on the same PRs and the same review budget?
//
//	cowboy-ailab score   -in prs.jsonl -out ai.jsonl -model-url http://localhost:11434 -model qwen2.5-coder:7b
//	cowboy-ailab compare -sorted sorted.jsonl -replay replay/ -logs a.log,b.log -ai ai.jsonl -model-cutoff 2024-09-30
//
// score reads PRs from scripts/eval/collect_sort.py and only ever decodes
// their title, message and diff (plus repo, number and merge time to label
// the output). Outcome fields in the same file are never read. compare
// joins the scores to the replayed outcomes (cmd/cowboy-replay) and the
// rules' pens (cmd/cowboy-sortlab). Nothing here touches a live install.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/model"
)

// Scored is one line of ai.jsonl: the PR, and the model's risk with its
// provenance.
type Scored struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	Merged time.Time `json:"merged"`
	model.Risk
}

// prIn is everything score reads from a collected PR. Fields such as
// "reverted" or "followups" sit in the same file; they aren't listed here,
// so they're never decoded.
type prIn struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	Merged time.Time `json:"merged"`
	Title  string    `json:"title"`
	Msg    string    `json:"msg"`
	Patch  string    `json:"patch"`
}

// Ask builds what the model sees: the title, the rest of the merge message
// as the description, and the diff.
func (p prIn) Ask() model.Ask {
	body := ""
	if _, rest, ok := strings.Cut(p.Msg, "\n"); ok {
		body = rest
	}
	return model.Ask{Title: p.Title, Body: body, Files: diff.Parse(p.Patch)}
}

func key(repo string, n int) string { return fmt.Sprintf("%s#%d", strings.ToLower(repo), n) }

// Risker is the model (a *model.Client in use, a fake in tests).
type Risker interface {
	Risk(model.Ask) model.Risk
}

// score scores every PR in `in` that `done` doesn't already hold, appending
// a line per PR to out. limit 0 = all. It returns how many it scored.
func score(in io.Reader, out io.Writer, done map[string]bool, m Risker, limit int, progress func(Scored)) (int, error) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<20), 64<<20)
	enc := json.NewEncoder(out)
	n := 0
	for sc.Scan() {
		if limit > 0 && n >= limit {
			break
		}
		var p prIn
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			return n, err
		}
		if done[key(p.Repo, p.Number)] {
			continue
		}
		s := Scored{Repo: p.Repo, Number: p.Number, Merged: p.Merged, Risk: m.Risk(p.Ask())}
		if err := enc.Encode(s); err != nil {
			return n, err
		}
		done[key(p.Repo, p.Number)] = true
		n++
		if progress != nil {
			progress(s)
		}
	}
	return n, sc.Err()
}

// readScores reads ai.jsonl. For each PR the last line wins (a rerun after
// errors appends). It returns the lines kept, and the runs (model + prompt
// version) they come from.
func readScores(r io.Reader) (map[string]Scored, map[string]int, error) {
	out := map[string]Scored{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 16<<20), 16<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var s Scored
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return nil, nil, err
		}
		out[key(s.Repo, s.Number)] = s
	}
	runs := map[string]int{}
	for _, s := range out {
		runs[s.Model+" | "+s.PromptVersion]++
	}
	return out, runs, sc.Err()
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "score":
		scoreCmd(os.Args[2:])
	case "compare":
		compareCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  cowboy-ailab score   -in prs.jsonl -out ai.jsonl -model-url URL -model NAME [-limit N] [-retry-errors]
  cowboy-ailab compare -sorted sorted.jsonl -replay DIR -logs a.log,b.log -ai ai.jsonl -model-cutoff YYYY-MM-DD [-json]`)
	os.Exit(2)
}

func scoreCmd(args []string) {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	in := fs.String("in", "", "PRs from scripts/eval/collect_sort.py")
	outPath := fs.String("out", "ai.jsonl", "scores are appended here")
	url := fs.String("model-url", os.Getenv("MODEL_URL"), "OpenAI-compatible server, e.g. http://localhost:11434")
	name := fs.String("model", os.Getenv("MODEL_NAME"), "model name, e.g. qwen2.5-coder:7b")
	maxDiff := fs.Int("max-diff", 12000, "characters of diff sent")
	limit := fs.Int("limit", 0, "score at most this many PRs (0 = all), for a pilot")
	retry := fs.Bool("retry-errors", false, "score again PRs whose last attempt failed")
	fs.Parse(args)
	if *in == "" || *url == "" || *name == "" {
		log.Fatal("score needs -in, -model-url and -model")
	}
	done := map[string]bool{}
	if f, err := os.Open(*outPath); err == nil {
		prev, runs, err := readScores(f)
		f.Close()
		if err != nil {
			log.Fatalf("%s: %v", *outPath, err)
		}
		want := *name + " | " + model.RiskPromptVersion
		for run := range runs {
			if run != want {
				log.Fatalf("%s holds scores from another run (%s); use a new -out for %s", *outPath, run, want)
			}
		}
		for k, s := range prev {
			if s.Error == "" || !*retry {
				done[k] = true
			}
		}
	}
	src, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	defer src.Close()
	out, err := os.OpenFile(*outPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	c := model.New(*url, *name, os.Getenv("MODEL_KEY"))
	c.MaxDiff = *maxDiff
	n, err := score(src, out, done, c, *limit, func(s Scored) {
		v := "no score: " + s.Error
		if s.Score != nil {
			v = fmt.Sprint(*s.Score)
		}
		fmt.Fprintf(os.Stderr, "%s#%d %s (%.1fs)\n", s.Repo, s.Number, v, s.Seconds)
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "scored %d PRs with %s, prompt %s, into %s\n", n, *name, model.RiskPromptVersion, *outPath)
}
