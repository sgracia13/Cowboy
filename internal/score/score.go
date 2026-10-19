// Package score turns a pull request into a pen (easy, normal or careful)
// plus the reasons, using fixed rules. Same input, same answer, always.
package score

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/graph"
	"github.com/sgracia13/Cowboy/internal/shortcuts"
)

// Pen is where a PR goes.
type Pen string

const (
	Easy    Pen = "easy"
	Normal  Pen = "normal"
	Careful Pen = "careful"
)

// Reason is one line of "why".
type Reason struct {
	Check  string `json:"check"`  // size, sensitive, blast_radius, shortcut, checks, file_class
	Detail string `json:"detail"` // plain words
	Effect string `json:"effect"` // careful, easy-ok, info
}

// Signals are the facts Cowboy measured. Kept separate from the decision so
// they can be stored, shown and re-scored with new rules.
type Signals struct {
	Agent        string         `json:"agent"`
	AgentHow     string         `json:"agent_how"`
	Files        int            `json:"files"`
	LinesChanged int            `json:"lines_changed"`
	SourceLines  int            `json:"source_lines"`
	Dirs         int            `json:"dirs"`
	Classes      map[string]int `json:"classes"`
	Sensitive    []string       `json:"sensitive"` // "path (glob)"
	FanIn        int            `json:"fan_in"`
	FanInFile    string         `json:"fan_in_file"`
	FanInKnown   bool           `json:"fan_in_known"`
	// FanInTop is the fan-in of the repo's top TopFanInPercent of files
	// (0 when not measured; older stored signals have none).
	FanInTop    int              `json:"fan_in_top,omitempty"`
	FanInTopPct float64          `json:"fan_in_top_pct,omitempty"`
	Shortcuts   []shortcuts.Flag `json:"shortcuts"`
	ChecksOK    *bool            `json:"checks_ok,omitempty"`
	Paths       []string         `json:"paths"`
	Owners      []string         `json:"owners,omitempty"`
}

// Measure computes signals from a diff. g may be nil (blast radius then unknown).
func Measure(meta analyze.PRMeta, files []diff.File, cfg config.Config, g *graph.Graph) Signals {
	s := Signals{Classes: map[string]int{}, ChecksOK: meta.ChecksOK}
	s.Agent, s.AgentHow = analyze.DetectAgent(meta, lower(cfg.Agents))
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		c := analyze.Classify(f.Path)
		s.Classes[string(c)]++
		s.LinesChanged += f.Changes()
		if c == analyze.Source {
			s.SourceLines += f.Changes()
		}
		for _, glob := range cfg.Careful.Paths {
			if config.MatchGlob(glob, f.Path) {
				s.Sensitive = append(s.Sensitive, fmt.Sprintf("%s (%s)", f.Path, glob))
				break
			}
		}
	}
	s.Files = len(files)
	s.Paths = paths
	s.Dirs = len(analyze.Dirs(paths))
	if g != nil {
		s.FanInKnown = true
		if p := cfg.Careful.TopFanInPercent; p > 0 && p < 100 {
			s.FanInTop, s.FanInTopPct = g.TopFanIn(p), p
		}
		ignore := map[string]bool{}
		for _, p := range cfg.FanInIgnore {
			ignore[p] = true
		}
		for _, f := range files {
			if f.Status == "added" || analyze.Classify(f.Path) != analyze.Source || ignore[f.Path] {
				continue
			}
			if n, _ := g.FanIn(f.Path); n > s.FanIn {
				s.FanIn, s.FanInFile = n, f.Path
			}
		}
	}
	s.Shortcuts = shortcuts.Find(files, cfg.Shortcuts)
	return s
}

func lower(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// Decide applies the rules. It is a pure function.
func Decide(s Signals, cfg config.Config) (Pen, []Reason) {
	var reasons []Reason
	careful := false
	add := func(check, detail, effect string) { reasons = append(reasons, Reason{check, detail, effect}) }

	// careful triggers
	for _, p := range s.Sensitive {
		add("sensitive", "touches a sensitive path: "+p, "careful")
		careful = true
	}
	bar := blastBar(s, cfg)
	if s.FanInKnown && s.FanIn >= bar {
		detail := fmt.Sprintf("changes %s, which %d files use", s.FanInFile, s.FanIn)
		if bar > cfg.Careful.MinFanIn {
			detail += fmt.Sprintf(" (among this repo's %s most-used files)", pctWord(s.FanInTopPct))
		}
		add("blast_radius", detail, "careful")
		careful = true
	}
	carefulKinds := map[string]bool{}
	for _, k := range cfg.Careful.Shortcuts {
		carefulKinds[k] = true
	}
	var notes []Reason
	for _, f := range s.Shortcuts {
		if carefulKinds[f.Kind] {
			add("shortcut", f.Detail, "careful")
			careful = true
		} else {
			notes = append(notes, Reason{"shortcut", f.Detail, "note"})
		}
	}
	if cfg.Careful.MinSourceLines > 0 && s.SourceLines >= cfg.Careful.MinSourceLines {
		add("size", fmt.Sprintf("%d lines of source changed", s.SourceLines), "careful")
		careful = true
	} else if cfg.Notes.BigSourceLines > 0 && s.SourceLines >= cfg.Notes.BigSourceLines {
		notes = append(notes, Reason{"size", fmt.Sprintf("%d lines of source: too big to review well in one sitting; consider asking for it in smaller parts", s.SourceLines), "note"})
	}
	reasons = append(reasons, notes...)
	if s.ChecksOK != nil && !*s.ChecksOK {
		add("checks", "checks are failing", "careful")
		careful = true
	}

	// context lines, always shown
	add("size", fmt.Sprintf("%d lines across %d files in %d folders", s.LinesChanged, s.Files, s.Dirs), "info")
	if s.FanInKnown && s.FanIn < bar && s.FanInFile != "" {
		if s.FanIn >= cfg.Careful.MinFanIn {
			add("blast_radius", fmt.Sprintf("%s is used by %d files; careful starts at %d here (this repo's %s most-used files)", s.FanInFile, s.FanIn, bar, pctWord(s.FanInTopPct)), "info")
		} else {
			add("blast_radius", fmt.Sprintf("%s is used by %d files", s.FanInFile, s.FanIn), "info")
		}
	}
	if !s.FanInKnown {
		add("blast_radius", "not measured: the import graph for this repo isn't available (see the rules page)", "info")
	}
	add("file_class", classSummary(s.Classes), "info")

	if careful {
		return Careful, reasons
	}

	easy := s.LinesChanged <= cfg.Easy.MaxLines &&
		s.Dirs <= cfg.Easy.MaxDirs &&
		(!s.FanInKnown || s.FanIn <= cfg.Easy.MaxFanIn) &&
		(s.ChecksOK == nil || *s.ChecksOK)
	// big PRs that are only tests/docs/config are still easy to review
	onlySafe := s.Classes["source"] == 0 && s.Classes["generated"] == 0
	if onlySafe && s.LinesChanged <= cfg.Easy.MaxLines*12 {
		add("file_class", "no source code changed", "easy-ok")
		return Easy, reasons
	}
	if easy {
		add("size", "small, few folders, little depends on it", "easy-ok")
		return Easy, reasons
	}
	return Normal, reasons
}

// blastBar is the fan-in that sends a PR to careful: MinFanIn, raised to
// the repo's own top files when that was measured and is enabled.
func blastBar(s Signals, cfg config.Config) int {
	bar := cfg.Careful.MinFanIn
	p := cfg.Careful.TopFanInPercent
	if p > 0 && p < 100 && s.FanInTop > bar {
		bar = s.FanInTop
	}
	return bar
}

func pctWord(p float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", p), "0"), ".") + "%"
}

func classSummary(c map[string]int) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", c[k], k))
	}
	return "files: " + strings.Join(parts, ", ")
}

// Overlap returns the share of files two PRs have in common (0..1).
func Overlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, p := range a {
		set[p] = true
	}
	common := 0
	for _, p := range b {
		if set[p] {
			common++
		}
	}
	min := len(a)
	if len(b) < min {
		min = len(b)
	}
	return float64(common) / float64(min)
}

// Comment renders the text Cowboy posts on the PR.
func Comment(pen Pen, s Signals, reasons []Reason, extra []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- cowboy -->\n**cowboy: %s**", pen)
	if s.Agent != "" {
		fmt.Fprintf(&b, " · agent: %s (%s)", s.Agent, s.AgentHow)
	}
	b.WriteString("\n\n")
	for _, r := range reasons {
		if r.Effect == "careful" {
			fmt.Fprintf(&b, "- %s\n", r.Detail)
		}
	}
	for _, e := range extra {
		fmt.Fprintf(&b, "- %s\n", e)
	}
	for _, r := range reasons {
		if r.Effect == "note" {
			fmt.Fprintf(&b, "- worth a look: %s\n", r.Detail)
		}
	}
	for _, r := range reasons {
		if r.Effect != "careful" && r.Effect != "note" {
			fmt.Fprintf(&b, "- %s\n", r.Detail)
		}
	}
	if len(s.Owners) > 0 && pen == Careful {
		fmt.Fprintf(&b, "\nReview requested from %s.\n", strings.Join(s.Owners, ", "))
	}
	b.WriteString("\n<sub>Pen set by fixed rules: same PR, same pen. Wrong pen? Change the label and Cowboy will remember.</sub>")
	return b.String()
}

// OwnersFor returns CODEOWNERS entries matching any of the paths (last match wins per path).
func OwnersFor(codeowners string, paths []string) []string {
	type rule struct {
		pat    string
		owners []string
	}
	var rules []rule
	for _, l := range strings.Split(codeowners, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Fields(l)
		if len(f) >= 2 {
			rules = append(rules, rule{f[0], f[1:]})
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		var match []string
		for _, r := range rules {
			pat := strings.TrimPrefix(r.pat, "/")
			ok := false
			switch {
			case pat == "*":
				ok = true
			case strings.HasSuffix(pat, "/"):
				ok = strings.HasPrefix(p, pat) || strings.Contains(p, "/"+pat)
			case strings.Contains(pat, "*"):
				ok = config.MatchGlob(pat, p)
				if !ok {
					ok, _ = path.Match(pat, path.Base(p))
				}
			default:
				ok = p == pat || strings.HasPrefix(p, pat+"/")
			}
			if ok {
				match = r.owners
			}
		}
		for _, o := range match {
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	return out
}
