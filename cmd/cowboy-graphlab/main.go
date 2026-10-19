// Command cowboy-graphlab prints how Cowboy reads a repo's imports, to check
// blast radius against the TypeScript compiler and Python's ast. Not part of
// the product.
//
//	cowboy-graphlab imports DIR               one JSON line per import
//	cowboy-graphlab fanin DIR                 one JSON line per file: its fan-in
//	cowboy-graphlab remote OWNER/REPO[@SHA]   download from GitHub as serve does; fan-in
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/graph"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: cowboy-graphlab imports|fanin|remote PATH")
	}
	enc := json.NewEncoder(os.Stdout)
	switch os.Args[1] {
	case "imports":
		imps, g, err := graph.Explain(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		sort.Slice(imps, func(i, j int) bool { return imps[i].File < imps[j].File })
		for _, im := range imps {
			enc.Encode(im)
		}
		fmt.Fprintf(os.Stderr, "%+v\n", g.Stats)
	case "fanin":
		g, err := graph.Build(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		printFanIn(enc, g)
		fmt.Fprintf(os.Stderr, "%+v\n", g.Stats)
	case "remote":
		repo, sha, _ := strings.Cut(os.Args[2], "@")
		f, err := forge.New("github", os.Getenv("FORGE_URL"), os.Getenv("FORGE_TOKEN"))
		if err != nil {
			log.Fatal(err)
		}
		a := f.(forge.Archiver)
		if sha == "" {
			if sha, err = a.Head(repo); err != nil {
				log.Fatal(err)
			}
		}
		start := time.Now()
		rc, err := a.Archive(context.Background(), repo, sha)
		if err != nil {
			log.Fatal(err)
		}
		defer rc.Close()
		cr := &counter{r: rc}
		g, err := graph.FromTarGz(cr, 300<<20)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(os.Stderr, "%s@%.12s: downloaded %d MB, built in %s: %+v\n", repo, sha, cr.n>>20, time.Since(start).Round(time.Millisecond), g.Stats)
		printFanIn(enc, g)
	case "archive":
		f, err := os.Open(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		start := time.Now()
		g, err := graph.FromTarGz(f, 300<<20)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(os.Stderr, "built in %s: %+v\n", time.Since(start).Round(time.Millisecond), g.Stats)
		printFanIn(enc, g)
	default:
		log.Fatal("usage: cowboy-graphlab imports|fanin|remote|archive PATH")
	}
}

type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func printFanIn(enc *json.Encoder, g *graph.Graph) {
	files := map[string]bool{}
	for from, targets := range g.Edges() {
		files[from] = true
		for _, t := range targets {
			files[t] = true
		}
	}
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		if n, _ := g.FanIn(f); n > 0 {
			enc.Encode(map[string]any{"file": f, "fanin": n})
		}
	}
}
