package graph

import (
	"os"
	"path/filepath"
	"strings"
)

// Import is one import as Cowboy read and resolved it (for the accuracy
// check in cmd/cowboy-graphlab).
type Import struct {
	File    string   `json:"file"`
	Lang    string   `json:"lang"`
	Spec    string   `json:"spec"`              // as written; python: "..mod:name1,name2"
	Targets []string `json:"targets,omitempty"` // files it resolved to
}

// Explain reads a checkout and lists every import with what it resolved to.
func Explain(root string) ([]Import, *Graph, error) {
	b := NewBuilder()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
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
		var c []byte
		if b.Wants(rel, info.Size()) {
			c, _ = os.ReadFile(p)
		}
		b.Add(rel, info.Size(), c)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	g := b.Graph()
	r := newResolver(b)
	var out []Import
	for f, specs := range b.jsImps {
		for _, s := range specs {
			im := Import{File: f, Lang: "js", Spec: s}
			if t := r.js(f, s); t != "" {
				im.Targets = []string{t}
			}
			out = append(out, im)
		}
	}
	for f, imps := range b.pyImps {
		for _, pi := range imps {
			out = append(out, Import{File: f, Lang: "py", Spec: strings.Repeat(".", pi.Level) + pi.Module + ":" + strings.Join(pi.Names, ","), Targets: r.py(f, pi)})
		}
	}
	for f, imps := range b.goImps {
		for _, p := range imps {
			im := Import{File: f, Lang: "go", Spec: p}
			if d, ok := r.goDir(p); ok {
				im.Targets = []string{"dir:" + d}
			}
			out = append(out, im)
		}
	}
	return out, g, nil
}
