package graph

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
)

type tsPaths struct {
	dir      string // where the config lives
	baseURL  string // "" if unset (resolved, repo-relative)
	pathsDir string // what "paths" targets are relative to
	paths    map[string][]string
}

type workspacePkg struct {
	dir     string
	entries []string            // main, module, types…: for a bare import, when there's no exports map
	exports map[string][]string // "./sub" or "./sub/*" -> targets, best condition first
}

type resolver struct {
	b        *Builder
	goMods   map[string]string // dir -> module path
	goDirs   map[string]bool   // dirs that hold .go files
	ts       map[string]*tsPaths
	pkgs     map[string]workspacePkg // package name -> where it lives
	pkgNames []string                // longest first
	pyRoots  []string
	fqcn     map[string]string   // java class -> file
	javaPkg  map[string][]string // java package -> files
}

func newResolver(b *Builder) *resolver {
	r := &resolver{b: b, goMods: map[string]string{}, goDirs: map[string]bool{}, ts: map[string]*tsPaths{},
		pkgs: map[string]workspacePkg{}, fqcn: map[string]string{}, javaPkg: map[string][]string{}}
	aliases := map[string]string{}
	// read configs in a fixed order, so duplicates always resolve the same way
	configNames := make([]string, 0, len(b.configs))
	for rel := range b.configs {
		configNames = append(configNames, rel)
	}
	sort.Strings(configNames)
	for _, rel := range configNames {
		raw := b.configs[rel]
		switch base := path.Base(rel); {
		case base == "go.mod":
			for _, l := range strings.Split(string(raw), "\n") {
				if m, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
					r.goMods[path.Dir(rel)] = strings.Trim(strings.TrimSpace(m), `"`)
				}
			}
		case base == "package.json":
			var p struct {
				Name                                 string
				Source, Types, Typings, Module, Main string
				Exports                              any
				Dependencies, DevDependencies        map[string]string
			}
			if json.Unmarshal(raw, &p) != nil {
				continue
			}
			// "sentry": "workspace:@sentry-internal/app@*" or "npm:@acme/ui@^2":
			// importing "sentry" means the workspace package @sentry-internal/app
			for _, deps := range []map[string]string{p.Dependencies, p.DevDependencies} {
				for alias, v := range deps {
					for _, pre := range []string{"workspace:", "npm:"} {
						if rest, ok := strings.CutPrefix(v, pre); ok {
							if i := strings.LastIndex(rest, "@"); i > 0 {
								if target := rest[:i]; target != alias {
									if _, seen := aliases[alias]; !seen {
										aliases[alias] = target
									}
								}
							}
						}
					}
				}
			}
			if _, dup := r.pkgs[p.Name]; p.Name != "" && !dup {
				dir := path.Dir(rel)
				var entries []string
				for _, e := range []string{p.Source, p.Module, p.Main, p.Types, p.Typings} {
					if e != "" {
						entries = append(entries, e)
					}
				}
				r.pkgs[p.Name] = workspacePkg{dir: dir, entries: entries, exports: exportsMap(p.Exports)}
			}
		}
	}
	aliasNames := make([]string, 0, len(aliases))
	for alias := range aliases {
		aliasNames = append(aliasNames, alias)
	}
	sort.Strings(aliasNames)
	for _, alias := range aliasNames {
		target := aliases[alias]
		if _, taken := r.pkgs[alias]; !taken {
			if p, ok := r.pkgs[target]; ok {
				r.pkgs[alias] = p
			}
		}
	}
	for rel := range b.goImps {
		r.goDirs[path.Dir(rel)] = true
	}
	for name := range r.pkgs {
		r.pkgNames = append(r.pkgNames, name)
	}
	sort.Slice(r.pkgNames, func(i, j int) bool {
		if len(r.pkgNames[i]) != len(r.pkgNames[j]) {
			return len(r.pkgNames[i]) > len(r.pkgNames[j])
		}
		return r.pkgNames[i] < r.pkgNames[j]
	})
	r.pyRoots = pyRoots(b.files)
	javaNames := make([]string, 0, len(b.javas))
	for rel := range b.javas {
		javaNames = append(javaNames, rel)
	}
	sort.Strings(javaNames)
	for _, rel := range javaNames {
		jf := b.javas[rel]
		name := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
		cls := name
		if jf.Package != "" {
			cls = jf.Package + "." + name
		}
		if _, dup := r.fqcn[cls]; !dup {
			r.fqcn[cls] = rel
		}
		r.javaPkg[jf.Package] = append(r.javaPkg[jf.Package], rel)
	}
	return r
}

// exportsMap reads package.json "exports": subpaths ("." for the package
// itself, "./x", "./x/*") and, for each, its targets with source-like
// conditions first (grafana's "@grafana-app/source", "source"), then
// import, require, default, and types last (types usually point at built
// .d.ts files that aren't in the repo).
func exportsMap(e any) map[string][]string {
	if e == nil {
		return nil
	}
	out := map[string][]string{}
	if m, ok := e.(map[string]any); ok {
		subpaths := false
		for k := range m {
			if strings.HasPrefix(k, ".") {
				subpaths = true
				break
			}
		}
		if subpaths {
			for k, v := range m {
				out[k] = conditionTargets(v)
			}
			return out
		}
	}
	out["."] = conditionTargets(e)
	return out
}

func conditionTargets(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, y := range x {
			out = append(out, conditionTargets(y)...)
		}
		return out
	case map[string]any:
		rank := func(k string) int {
			switch {
			case strings.Contains(k, "source"):
				return 0
			case k == "import" || k == "module":
				return 1
			case k == "require" || k == "node":
				return 2
			case k == "default" || k == "browser":
				return 3
			case k == "types" || k == "typings":
				return 5
			}
			return 4
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if rank(keys[i]) != rank(keys[j]) {
				return rank(keys[i]) < rank(keys[j])
			}
			return keys[i] < keys[j]
		})
		var out []string
		for _, k := range keys {
			out = append(out, conditionTargets(x[k])...)
		}
		return out
	}
	return nil
}

// ---- Go

func (r *resolver) goDir(imp string) (string, bool) {
	best, bestMod := "", ""
	for dir, mod := range r.goMods {
		if (imp == mod || strings.HasPrefix(imp, mod+"/")) && (len(mod) > len(bestMod) || (len(mod) == len(bestMod) && dir < best)) {
			best, bestMod = dir, mod
		}
	}
	if bestMod == "" {
		return "", false
	}
	d := path.Join(best, strings.TrimPrefix(imp, bestMod))
	return d, r.goDirs[d]
}

// ---- JavaScript / TypeScript

// js resolves one specifier to a file in the repo, or "".
func (r *resolver) js(from, spec string) string {
	if spec == "" {
		return ""
	}
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == "." || spec == ".." {
		return r.jsFile(path.Join(path.Dir(from), spec))
	}
	if strings.HasPrefix(spec, "/") {
		return ""
	}
	if c := r.tsConfigFor(from); c != nil {
		if f := r.tsPathMatch(c, spec); f != "" {
			return f
		}
		if c.baseURL != "" {
			if f := r.jsFile(path.Join(c.baseURL, spec)); f != "" {
				return f
			}
		}
	}
	return r.workspace(spec)
}

func (r *resolver) jsFile(p string) string {
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	if p == "." {
		p = ""
	}
	try := func(c string) string {
		if r.b.files[c] {
			return c
		}
		return ""
	}
	if lang(p) == "js" {
		if f := try(p); f != "" {
			return f
		}
		// ESM-style TypeScript: import './x.js' means x.ts
		for _, js := range []string{".js", ".jsx", ".mjs", ".cjs"} {
			if base, ok := strings.CutSuffix(p, js); ok {
				for _, e := range []string{".ts", ".tsx", ".mts", ".cts", ".d.ts"} {
					if f := try(base + e); f != "" {
						return f
					}
				}
			}
		}
	}
	for _, e := range jsExts {
		if f := try(p + e); f != "" {
			return f
		}
	}
	for _, e := range jsExts {
		if f := try(path.Join(p, "index"+e)); f != "" {
			return f
		}
	}
	return ""
}

// tsConfigFor finds the nearest tsconfig.json or jsconfig.json above a file.
func (r *resolver) tsConfigFor(rel string) *tsPaths {
	for dir := path.Dir(rel); ; dir = path.Dir(dir) {
		for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
			if c := r.loadTS(path.Join(dir, name), 0); c != nil {
				return c
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return nil
		}
	}
}

func (r *resolver) loadTS(rel string, depth int) *tsPaths {
	rel = path.Clean(rel)
	if c, ok := r.ts[rel]; ok {
		return c
	}
	raw, ok := r.b.configs[rel]
	if !ok || depth > 5 {
		return nil
	}
	r.ts[rel] = nil // break cycles
	var cfg struct {
		Extends         any `json:"extends"`
		CompilerOptions struct {
			BaseURL *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(jsonc(raw), &cfg) != nil {
		return nil
	}
	dir := path.Dir(rel)
	c := &tsPaths{dir: dir, pathsDir: dir}
	// inherit from a parent config in the repo
	var parents []string
	switch e := cfg.Extends.(type) {
	case string:
		parents = []string{e}
	case []any:
		for _, x := range e {
			if s, ok := x.(string); ok {
				parents = append(parents, s)
			}
		}
	}
	for _, p := range parents {
		if !strings.HasPrefix(p, ".") {
			continue // a package (like @tsconfig/node18): no paths for this repo
		}
		pp := path.Join(dir, p)
		if !strings.HasSuffix(pp, ".json") {
			pp += ".json"
		}
		if pc := r.loadTS(pp, depth+1); pc != nil {
			c.baseURL, c.pathsDir, c.paths = pc.baseURL, pc.pathsDir, pc.paths
		}
	}
	if cfg.CompilerOptions.BaseURL != nil {
		c.baseURL = path.Join(dir, *cfg.CompilerOptions.BaseURL)
		c.pathsDir = c.baseURL
	}
	if cfg.CompilerOptions.Paths != nil {
		c.paths = cfg.CompilerOptions.Paths
		if cfg.CompilerOptions.BaseURL == nil && c.baseURL == "" {
			c.pathsDir = dir
		}
	}
	r.ts[rel] = c
	return c
}

func (r *resolver) tsPathMatch(c *tsPaths, spec string) string {
	if len(c.paths) == 0 {
		return ""
	}
	// TypeScript picks the pattern with the longest prefix before the *
	keys := make([]string, 0, len(c.paths))
	for k := range c.paths {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, _, _ := strings.Cut(keys[i], "*")
		pj, _, _ := strings.Cut(keys[j], "*")
		if len(pi) != len(pj) {
			return len(pi) > len(pj)
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		pre, post, star := strings.Cut(k, "*")
		var hole string
		if star {
			if !strings.HasPrefix(spec, pre) || !strings.HasSuffix(spec, post) || len(spec) < len(pre)+len(post) {
				continue
			}
			hole = spec[len(pre) : len(spec)-len(post)]
		} else if spec != k {
			continue
		}
		for _, t := range c.paths[k] {
			if f := r.jsFile(path.Join(c.pathsDir, strings.Replace(t, "*", hole, 1))); f != "" {
				return f
			}
		}
	}
	return ""
}

// workspace resolves imports of packages that live in this repo (monorepos:
// npm, yarn and pnpm workspaces).
func (r *resolver) workspace(spec string) string {
	for _, name := range r.pkgNames {
		if spec != name && !strings.HasPrefix(spec, name+"/") {
			continue
		}
		p := r.pkgs[name]
		sub := strings.TrimPrefix(strings.TrimPrefix(spec, name), "/")
		if p.exports != nil {
			if f := r.viaExports(p, sub); f != "" {
				return f
			}
		}
		if sub != "" {
			for _, c := range []string{path.Join(p.dir, sub), path.Join(p.dir, "src", sub)} {
				if f := r.jsFile(c); f != "" {
					return f
				}
			}
			if f := r.builtToSource(p.dir, sub); f != "" {
				return f
			}
			return ""
		}
		for _, e := range p.entries {
			if f := r.jsFile(path.Join(p.dir, strings.TrimSuffix(e, ".d.ts"))); f != "" && !strings.HasSuffix(f, ".d.ts") {
				return f
			}
		}
		for _, c := range []string{"src/index", "index", "src/main", "lib/index"} {
			if f := r.jsFile(path.Join(p.dir, c)); f != "" {
				return f
			}
		}
		for _, e := range p.entries {
			if f := r.builtToSource(p.dir, strings.TrimSuffix(e, ".d.ts")); f != "" {
				return f
			}
		}
		return ""
	}
	return ""
}

// viaExports follows the package's exports map, like Node and TypeScript.
func (r *resolver) viaExports(p workspacePkg, sub string) string {
	key := "."
	if sub != "" {
		key = "./" + sub
	}
	var targets []string
	hole := ""
	if t, ok := p.exports[key]; ok {
		targets = t
	} else {
		// Node's rule: the longest part before the * wins, then the longest key
		best, bestPre := "", -1
		for k := range p.exports {
			pre, post, star := strings.Cut(k, "*")
			if !star || !strings.HasPrefix(key, pre) || !strings.HasSuffix(key, post) || len(key) < len(pre)+len(post) {
				continue
			}
			if len(pre) > bestPre || (len(pre) == bestPre && (len(k) > len(best) || (len(k) == len(best) && k < best))) {
				best, bestPre = k, len(pre)
			}
		}
		if best == "" {
			return ""
		}
		pre, post, _ := strings.Cut(best, "*")
		hole = key[len(pre) : len(key)-len(post)]
		targets = p.exports[best]
	}
	for _, t := range targets {
		t = strings.Replace(t, "*", hole, -1)
		if f := r.jsFile(path.Join(p.dir, strings.TrimSuffix(t, ".d.ts"))); f != "" && !strings.HasSuffix(f, ".d.ts") {
			return f
		}
	}
	for _, t := range targets {
		if f := r.builtToSource(p.dir, strings.TrimSuffix(strings.Replace(t, "*", hole, -1), ".d.ts")); f != "" {
			return f
		}
	}
	return ""
}

// builtToSource maps a build output path (dist/esm/x.mjs) to its source
// (src/x.ts), since built files aren't in the repo.
func (r *resolver) builtToSource(dir, p string) string {
	p = strings.TrimPrefix(p, "./")
	for _, out := range []string{"dist/", "lib/", "build/", "out/"} {
		rest, ok := strings.CutPrefix(p, out)
		if !ok {
			continue
		}
		cands := []string{rest}
		// dist/esm/x, dist/cjs/x, dist/types/x
		if i := strings.Index(rest, "/"); i > 0 {
			switch rest[:i] {
			case "esm", "cjs", "types", "es", "commonjs", "module", "src":
				cands = append([]string{rest[i+1:]}, cands...)
			}
		}
		for _, c := range cands {
			for _, js := range []string{".mjs", ".cjs", ".js"} {
				c = strings.TrimSuffix(c, js)
			}
			if f := r.jsFile(path.Join(dir, "src", c)); f != "" {
				return f
			}
		}
	}
	return ""
}

// jsonc removes comments and trailing commas (tsconfig allows both).
func jsonc(b []byte) []byte {
	var out []byte
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(b) {
				i++
				out = append(out, b[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		case c == ',':
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\n' || b[j] == '\t' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// ---- Python

// pyRoots are the folders Python would import top-level packages from: the
// repo root, plus the parent of every top-level package (a folder with an
// __init__.py whose own parent has none), like src/ or backend/.
func pyRoots(files map[string]bool) []string {
	set := map[string]bool{"": true}
	for f := range files {
		if path.Base(f) != "__init__.py" {
			continue
		}
		pkg := path.Dir(f)
		parent := path.Dir(pkg)
		if parent == "." {
			parent = ""
		}
		if !files[path.Join(parent, "__init__.py")] {
			set[parent] = true
		}
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// pyModule finds the file for a dotted module under base dirs.
func (r *resolver) pyModule(bases []string, dotted string, from string) string {
	rel := strings.ReplaceAll(dotted, ".", "/")
	var hits []string
	for _, b := range bases {
		for _, c := range []string{path.Join(b, rel+".py"), path.Join(b, rel, "__init__.py")} {
			if r.b.files[c] {
				hits = append(hits, c)
				break
			}
		}
	}
	if len(hits) <= 1 {
		if len(hits) == 1 {
			return hits[0]
		}
		return ""
	}
	// several roots have it (a vendored copy, tests): take the one nearest the importer
	best, bestN := "", -1
	for _, h := range hits {
		if n := commonPrefix(h, from); n > bestN {
			best, bestN = h, n
		}
	}
	return best
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// py resolves one import statement to the files it uses. `from x import y`
// points at x/y.py when y is a submodule, otherwise at x itself.
func (r *resolver) py(from string, im pyImport) []string {
	var bases []string
	if im.Level > 0 {
		d := path.Dir(from)
		for i := 1; i < im.Level; i++ {
			d = path.Dir(d)
		}
		if d == "." {
			d = ""
		}
		bases = []string{d}
	} else {
		bases = append(append([]string{}, r.pyRoots...), path.Dir(from)) // scripts import their neighbours
	}
	pkgFile := ""
	if im.Module != "" {
		pkgFile = r.pyModule(bases, im.Module, from)
	} else if len(bases) == 1 {
		if c := path.Join(bases[0], "__init__.py"); r.b.files[c] {
			pkgFile = c
		}
	}
	if len(im.Names) == 0 {
		if pkgFile == "" {
			return nil
		}
		return []string{pkgFile}
	}
	var out []string
	usedPkg := false
	for _, n := range im.Names {
		sub := n
		if im.Module != "" {
			sub = im.Module + "." + n
		}
		if f := r.pyModule(bases, sub, from); f != "" {
			out = append(out, f)
		} else if pkgFile != "" && !usedPkg {
			out = append(out, pkgFile)
			usedPkg = true
		}
	}
	return out
}

// ---- Java / Kotlin

func (r *resolver) java(im string) []string {
	if pkg, ok := strings.CutSuffix(im, ".*"); ok {
		if fs := r.javaPkg[pkg]; len(fs) > 0 {
			return fs
		}
		// import a.b.Outer.* (nested classes): the outer class's file
		if f := r.fqcn[pkg]; f != "" {
			return []string{f}
		}
		return nil
	}
	// a.b.C, a.b.C.Inner, a.b.C.staticMember
	for cls, i := im, 0; i < 4 && cls != ""; i++ {
		if f := r.fqcn[cls]; f != "" {
			return []string{f}
		}
		j := strings.LastIndex(cls, ".")
		if j < 0 {
			break
		}
		cls = cls[:j]
	}
	return nil
}
