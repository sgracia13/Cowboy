// Command cowboy-sortlab sorts real, already-merged PRs with Cowboy's own
// rules (score.Measure + score.Decide, default config) so the pens can be
// compared with what happened to those PRs afterwards.
//
//	cowboy-sortlab -in prs.jsonl -checkout owner/name=/path/to/checkout,... -out sorted.jsonl
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/graph"
	"github.com/sgracia13/Cowboy/internal/score"
)

type rec struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Author string `json:"author"`
	Title  string `json:"title"`
	Msg    string `json:"msg"`
	Patch  string `json:"patch"`
	// passed through untouched
	Rest map[string]any `json:"-"`
}

func main() {
	in := flag.String("in", "", "input jsonl from collect_sort.py")
	out := flag.String("out", "sorted.jsonl", "output jsonl")
	checkouts := flag.String("checkout", "", "owner/name=/path,... checkouts for blast radius (optional)")
	flag.Parse()

	graphs := map[string]*graph.Graph{}
	for _, kv := range strings.Split(*checkouts, ",") {
		if repo, p, ok := strings.Cut(kv, "="); ok {
			g, err := graph.Build(p)
			if err != nil {
				log.Printf("%s: graph: %v", repo, err)
				continue
			}
			graphs[repo] = g
			fmt.Fprintf(os.Stderr, "built import graph for %s\n", repo)
		}
	}

	f, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	o, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer o.Close()
	cfg := config.Default()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256<<20), 256<<20)
	n := 0
	for sc.Scan() {
		var all map[string]any
		if err := json.Unmarshal(sc.Bytes(), &all); err != nil {
			log.Fatal(err)
		}
		var r rec
		json.Unmarshal(sc.Bytes(), &r)
		body := ""
		if _, b, ok := strings.Cut(r.Msg, "\n"); ok {
			body = b
		}
		meta := analyze.PRMeta{Repo: r.Repo, Number: r.Number, Title: r.Title, Author: r.Author, Body: body, Commits: []string{r.Msg}}
		files := diff.Parse(r.Patch)
		s := score.Measure(meta, files, cfg, graphs[r.Repo])
		pen, reasons := score.Decide(s, cfg)
		delete(all, "patch")
		all["pen"] = pen
		all["reasons"] = reasons
		all["lines"] = s.LinesChanged
		all["source_lines"] = s.SourceLines
		all["dirs"] = s.Dirs
		all["fan_in"] = s.FanIn
		all["fan_in_known"] = s.FanInKnown
		all["sensitive"] = len(s.Sensitive) > 0
		all["shortcuts"] = s.Shortcuts
		all["classes"] = s.Classes
		all["facts"] = score.Facts(s, cfg)
		all["agent"] = s.Agent
		var triggers []string
		for _, x := range reasons {
			if x.Effect == "careful" {
				triggers = append(triggers, x.Check)
			}
		}
		all["triggers"] = triggers
		b, _ := json.Marshal(all)
		o.Write(append(b, '\n'))
		n++
	}
	if err := sc.Err(); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "sorted %d PRs -> %s\n", n, *out)
}
