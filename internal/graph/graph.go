// Package graph builds a per-repo import graph and answers "how many files
// use this one?" (blast radius as import fan-in).
//
// It works in two steps. Reading: each file's imports are pulled out by a
// parser or scanner for its language (Go's own parser; scanners for
// JavaScript/TypeScript, Python and Java/Kotlin that skip comments and
// strings). Resolving: once every file has been seen, each import is matched
// to the file it points at, using go.mod, tsconfig/jsconfig paths and
// baseUrl, workspace package.json names, and Python package roots.
//
// Files can come from a local checkout (Build) or straight from a repo
// archive streamed from the git host (FromTarGz), so no local copy is needed.
package graph

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Graph maps each file (or Go package directory) to the files that import it.
type Graph struct {
	files     map[string]bool
	importers map[string]map[string]bool
	Stats     Stats

	topMu sync.Mutex
	top   map[float64]int
}

// Stats describes how a graph was built, for the rules page and the log.
type Stats struct {
	Source   string    `json:"source"` // "checkout" or "archive"
	Ref      string    `json:"ref,omitempty"`
	BuiltAt  time.Time `json:"built_at"`
	Files    int       `json:"files"`    // files seen
	Parsed   int       `json:"parsed"`   // source files read for imports
	Imports  int       `json:"imports"`  // imports found
	Resolved int       `json:"resolved"` // imports matched to a file in the repo
	TooBig   int       `json:"too_big"`  // source files skipped for size
	Took     string    `json:"took"`
}

// MaxFileBytes: bigger source files (bundles, generated code) are counted as
// files but not read for imports.
const MaxFileBytes = 2 << 20

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, ".venv": true, "venv": true, "__pycache__": true, "target": true, ".next": true, "coverage": true, "bower_components": true}

var jsExts = []string{".ts", ".tsx", ".d.ts", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"}

func lang(rel string) string {
	switch path.Ext(rel) {
	case ".go":
		return "go"
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts":
		return "js"
	case ".py":
		return "py"
	case ".java", ".kt":
		return "java"
	}
	return ""
}

func isConfig(rel string) bool {
	b := path.Base(rel)
	return b == "go.mod" || b == "package.json" || b == "jsconfig.json" || (strings.HasPrefix(b, "tsconfig") && strings.HasSuffix(b, ".json"))
}

// skip says whether a path is in a directory Cowboy never reads.
func skip(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

// Builder collects files, then resolves imports in Graph.
type Builder struct {
	files   map[string]bool
	configs map[string][]byte
	goImps  map[string][]string
	jsImps  map[string][]string
	pyImps  map[string][]pyImport
	javas   map[string]javaFile
	stats   Stats
}

func NewBuilder() *Builder {
	return &Builder{files: map[string]bool{}, configs: map[string][]byte{}, goImps: map[string][]string{},
		jsImps: map[string][]string{}, pyImps: map[string][]pyImport{}, javas: map[string]javaFile{}}
}

// Wants says whether a file should be read (its contents passed to Add).
func (b *Builder) Wants(rel string, size int64) bool {
	if skip(rel) {
		return false
	}
	if isConfig(rel) {
		return size <= MaxFileBytes
	}
	return lang(rel) != "" && size <= MaxFileBytes
}

// Add records a file. content may be nil for files that aren't read.
func (b *Builder) Add(rel string, size int64, content []byte) {
	if skip(rel) {
		return
	}
	b.files[rel] = true
	if isConfig(rel) {
		if content != nil {
			b.configs[rel] = content
		}
		return
	}
	l := lang(rel)
	if l == "" {
		return
	}
	if content == nil {
		if size > MaxFileBytes {
			b.stats.TooBig++
		}
		return
	}
	b.stats.Parsed++
	switch l {
	case "go":
		b.goImps[rel] = goImports(rel, content)
	case "js":
		b.jsImps[rel] = jsImports(content)
	case "py":
		b.pyImps[rel] = pyImports(content)
	case "java":
		b.javas[rel] = javaImports(content)
	}
}

// Build reads a local checkout.
func Build(root string) (*Graph, error) {
	start := time.Now()
	b := NewBuilder()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		var content []byte
		if b.Wants(rel, info.Size()) {
			content, _ = os.ReadFile(p)
		}
		b.Add(rel, info.Size(), content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	g := b.Graph()
	g.Stats.Source, g.Stats.BuiltAt, g.Stats.Took = "checkout", time.Now(), time.Since(start).Round(time.Millisecond).String()
	return g, nil
}

// ErrTooBig means the repo archive was bigger than the limit.
var ErrTooBig = errors.New("repo archive is bigger than the limit")

type countingReader struct {
	r     io.Reader
	n     int64
	limit int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.limit > 0 && c.n > c.limit {
		return n, ErrTooBig
	}
	return n, err
}

// FromTarGz reads a repo archive (.tar.gz, as GitHub, Forgejo and Gitea
// serve them) as it streams in. Only import lists are kept, never the code.
// limit caps the compressed bytes read (0 = no cap).
func FromTarGz(r io.Reader, limit int64) (*Graph, error) {
	start := time.Now()
	cr := &countingReader{r: r, limit: limit}
	zr, err := gzip.NewReader(cr)
	if err != nil {
		return nil, fmt.Errorf("not a .tar.gz archive: %w", err)
	}
	tr := tar.NewReader(zr)
	b := NewBuilder()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if errors.Is(err, ErrTooBig) || errors.Is(cr.lastErr(), ErrTooBig) {
				return nil, ErrTooBig
			}
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		// archives put everything under one top folder (owner-repo-sha/)
		_, rel, ok := strings.Cut(h.Name, "/")
		if !ok || rel == "" {
			continue
		}
		var content []byte
		if b.Wants(rel, h.Size) {
			content, err = io.ReadAll(tr)
			if err != nil {
				if errors.Is(err, ErrTooBig) {
					return nil, ErrTooBig
				}
				return nil, err
			}
		}
		b.Add(rel, h.Size, content)
	}
	g := b.Graph()
	g.Stats.Source, g.Stats.BuiltAt, g.Stats.Took = "archive", time.Now(), time.Since(start).Round(time.Millisecond).String()
	return g, nil
}

func (c *countingReader) lastErr() error {
	if c.limit > 0 && c.n > c.limit {
		return ErrTooBig
	}
	return nil
}

// Graph resolves every import and returns the graph.
func (b *Builder) Graph() *Graph {
	g := &Graph{files: b.files, importers: map[string]map[string]bool{}, Stats: b.stats}
	g.Stats.Files = len(b.files)
	edge := func(target, from string) {
		if target == "" || target == from {
			return
		}
		if g.importers[target] == nil {
			g.importers[target] = map[string]bool{}
		}
		g.importers[target][from] = true
		g.Stats.Resolved++
	}
	r := newResolver(b)
	for rel, imps := range b.goImps {
		for _, p := range imps {
			g.Stats.Imports++
			if d, ok := r.goDir(p); ok {
				edge("dir:"+d, rel)
			}
		}
	}
	for rel, specs := range b.jsImps {
		for _, s := range specs {
			g.Stats.Imports++
			edge(r.js(rel, s), rel)
		}
	}
	for rel, imps := range b.pyImps {
		for _, im := range imps {
			targets := r.py(rel, im)
			if len(im.Names) == 0 || len(targets) == 0 {
				g.Stats.Imports++
			} else {
				g.Stats.Imports += len(im.Names)
			}
			for _, t := range targets {
				edge(t, rel)
			}
		}
	}
	for rel, jf := range b.javas {
		for _, im := range jf.Imports {
			g.Stats.Imports++
			for _, t := range r.java(im) {
				edge(t, rel)
			}
		}
	}
	return g
}

// FanIn returns how many distinct files import rel (excluding rel itself and,
// for Go, files in the same package), plus their paths.
func (g *Graph) FanIn(rel string) (int, []string) {
	set := map[string]bool{}
	if lang(rel) == "go" {
		dir := path.Dir(rel)
		for f := range g.importers["dir:"+dir] {
			if path.Dir(f) != dir {
				set[f] = true
			}
		}
	} else {
		for f := range g.importers[rel] {
			if f != rel {
				set[f] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return len(out), out
}

// TopFanIn returns the fan-in a file needs to be in the top pct percent of
// the files anything imports (at least 1). Each pct is worked out once.
func (g *Graph) TopFanIn(pct float64) int {
	g.topMu.Lock()
	defer g.topMu.Unlock()
	if v, ok := g.top[pct]; ok {
		return v
	}
	seen := map[string]bool{}
	var counts []int
	for target, froms := range g.importers {
		var name string
		if strings.HasPrefix(target, "dir:") {
			// a Go package counts once, by its importers outside the package
			dir := strings.TrimPrefix(target, "dir:")
			name = target
			n := 0
			for f := range froms {
				if path.Dir(f) != dir {
					n++
				}
			}
			if n > 0 && !seen[name] {
				seen[name] = true
				counts = append(counts, n)
			}
			continue
		}
		name = target
		n := 0
		for f := range froms {
			if f != target {
				n++
			}
		}
		if n > 0 && !seen[name] {
			seen[name] = true
			counts = append(counts, n)
		}
	}
	v := 1
	if len(counts) > 0 && pct > 0 && pct < 100 {
		sort.Sort(sort.Reverse(sort.IntSlice(counts)))
		i := int(float64(len(counts)) * pct / 100)
		if i >= len(counts) {
			i = len(counts) - 1
		}
		v = counts[i]
	}
	if g.top == nil {
		g.top = map[float64]int{}
	}
	g.top[pct] = v
	return v
}

// Has reports whether the graph saw this file.
func (g *Graph) Has(rel string) bool { return g.files[rel] }

// Edges lists every resolved import as importer → target (for tests and the
// accuracy check). Go targets are package directories, written "dir:<path>".
func (g *Graph) Edges() map[string][]string {
	out := map[string][]string{}
	for target, froms := range g.importers {
		for f := range froms {
			out[f] = append(out[f], target)
		}
	}
	for f := range out {
		sort.Strings(out[f])
	}
	return out
}
