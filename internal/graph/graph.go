// Package graph builds a per-repo import graph and answers "how many files
// use this one?" (blast radius as import fan-in).
//
// The product plan uses tree-sitter. This prototype extracts imports with
// line-level patterns for Go, JavaScript/TypeScript, Python and Java, which is
// enough to show the idea; tree-sitter replaces extractImports later without
// changing the rest of the package.
package graph

import (
	"bufio"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Graph maps a module key to the set of files that import it.
type Graph struct {
	importers map[string]map[string]bool
	goModule  string
	files     map[string]bool
}

var (
	jsImport  = regexp.MustCompile(`(?:import\s[^'"]*from\s*|import\s*\(\s*|require\(\s*|import\s+)['"]([^'"]+)['"]`)
	pyFrom    = regexp.MustCompile(`^\s*from\s+([.\w]+)\s+import\s`)
	pyImport  = regexp.MustCompile(`^\s*import\s+([\w.]+)`)
	goImport  = regexp.MustCompile(`^\s*(?:import\s+)?(?:[\w.]+\s+)?"([^"]+)"`)
	javaImprt = regexp.MustCompile(`^\s*import\s+(?:static\s+)?([\w.]+)\s*;`)
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, ".venv": true, "venv": true, "__pycache__": true, "target": true}

// Build walks a local checkout and records every import it finds.
func Build(root string) (*Graph, error) {
	g := &Graph{importers: map[string]map[string]bool{}, files: map[string]bool{}}
	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "module ") {
				g.goModule = strings.TrimSpace(strings.TrimPrefix(l, "module "))
			}
		}
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		g.files[rel] = true
		for _, key := range g.extractImports(root, rel) {
			if g.importers[key] == nil {
				g.importers[key] = map[string]bool{}
			}
			g.importers[key][rel] = true
		}
		return nil
	})
	return g, err
}

// extractImports returns module keys imported by one file, normalized so they
// can be compared with ModuleKey of a changed file.
func (g *Graph) extractImports(root, rel string) []string {
	ext := path.Ext(rel)
	switch ext {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".py", ".java":
	default:
		return nil
	}
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return nil
	}
	defer f.Close()
	var keys []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	inGoBlock := false
	for sc.Scan() {
		line := sc.Text()
		switch ext {
		case ".go":
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "import (") {
				inGoBlock = true
				continue
			}
			if inGoBlock && t == ")" {
				inGoBlock = false
				continue
			}
			if inGoBlock || strings.HasPrefix(t, "import ") {
				if m := goImport.FindStringSubmatch(t); m != nil && g.goModule != "" && strings.HasPrefix(m[1], g.goModule+"/") {
					keys = append(keys, "go:"+strings.TrimPrefix(m[1], g.goModule+"/"))
				}
			}
		case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
			for _, m := range jsImport.FindAllStringSubmatch(line, -1) {
				spec := m[1]
				if strings.HasPrefix(spec, ".") {
					keys = append(keys, "js:"+stripJSExt(path.Clean(path.Join(path.Dir(rel), spec))))
				}
			}
		case ".py":
			if m := pyFrom.FindStringSubmatch(line); m != nil {
				keys = append(keys, pyKey(rel, m[1]))
			} else if m := pyImport.FindStringSubmatch(line); m != nil {
				keys = append(keys, "py:"+m[1])
			}
		case ".java":
			if m := javaImprt.FindStringSubmatch(line); m != nil {
				keys = append(keys, "java:"+m[1])
			}
		}
	}
	return keys
}

func stripJSExt(p string) string {
	for _, e := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
		p = strings.TrimSuffix(p, e)
	}
	return strings.TrimSuffix(p, "/index")
}

func pyKey(rel, mod string) string {
	if strings.HasPrefix(mod, ".") {
		dots := len(mod) - len(strings.TrimLeft(mod, "."))
		base := path.Dir(rel)
		for i := 1; i < dots; i++ {
			base = path.Dir(base)
		}
		rest := strings.TrimLeft(mod, ".")
		p := base
		if rest != "" {
			p = path.Join(base, strings.ReplaceAll(rest, ".", "/"))
		}
		return "py:" + strings.ReplaceAll(strings.TrimPrefix(p, "./"), "/", ".")
	}
	return "py:" + mod
}

// ModuleKeys returns the keys other files would use to import this file.
func (g *Graph) ModuleKeys(rel string) []string {
	ext := path.Ext(rel)
	switch ext {
	case ".go":
		return []string{"go:" + path.Dir(rel)}
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return []string{"js:" + stripJSExt(rel)}
	case ".py":
		mod := strings.TrimSuffix(rel, ".py")
		mod = strings.TrimSuffix(mod, "/__init__")
		dotted := strings.ReplaceAll(mod, "/", ".")
		keys := []string{"py:" + dotted}
		// also match when the repo uses a src/ layout
		if strings.HasPrefix(dotted, "src.") {
			keys = append(keys, "py:"+strings.TrimPrefix(dotted, "src."))
		}
		return keys
	case ".java":
		p := rel
		for _, pre := range []string{"src/main/java/", "src/test/java/", "src/"} {
			if i := strings.Index(p, pre); i >= 0 {
				p = p[i+len(pre):]
				break
			}
		}
		return []string{"java:" + strings.ReplaceAll(strings.TrimSuffix(p, ".java"), "/", ".")}
	}
	return nil
}

// FanIn returns how many distinct files import rel (excluding rel itself and
// files in the same Go package), plus a few example importers.
func (g *Graph) FanIn(rel string) (int, []string) {
	set := map[string]bool{}
	for _, k := range g.ModuleKeys(rel) {
		for f := range g.importers[k] {
			if f == rel {
				continue
			}
			if path.Ext(rel) == ".go" && path.Dir(f) == path.Dir(rel) {
				continue
			}
			set[f] = true
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return len(out), out
}

// Has reports whether the graph saw this file.
func (g *Graph) Has(rel string) bool { return g.files[rel] }
