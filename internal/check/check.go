// Package check sorts the changes in a local git checkout the way Cowboy
// would sort them as a pull request: a self-check before the PR is opened,
// for people and for coding agents (`cowboy check`, and the MCP server's
// check_changes tool). It only reads: it never commits, pushes or writes.
//
// The rules come from the base branch's cowboy.json, not the working copy,
// so a change can't loosen its own rules (the same as on the server).
package check

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/graph"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/shortcuts"
)

// Options say what to check.
type Options struct {
	Dir   string // the checkout (default: the current folder)
	Base  string // compare against this ref (default: the default branch)
	Title string // what the change is for (default: the latest commit's subject)
	// NoGraph skips building the import graph (blast radius), which takes a
	// few seconds on big repos.
	NoGraph bool
}

// Result is what a self-check found.
type Result struct {
	Repo      string           `json:"repo"`
	Base      string           `json:"base"` // the commit compared against
	Pen       score.Pen        `json:"pen"`
	Files     int              `json:"files"`
	Lines     int              `json:"lines"`
	Reasons   []score.Reason   `json:"reasons"`
	Shortcuts []shortcuts.Flag `json:"shortcuts"`
	Owners    []string         `json:"owners,omitempty"`
	Rules     string           `json:"rules"` // where the rules came from
	Empty     bool             `json:"empty"` // nothing changed
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// defaultBase finds the branch a PR from here would go into.
func defaultBase(dir string) string {
	if s, err := git(dir, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(s), "refs/remotes/")
	}
	for _, b := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := git(dir, "rev-parse", "--verify", "--quiet", b); err == nil {
			return b
		}
	}
	return "HEAD"
}

// Changes returns the unified diff of everything on this branch and in the
// working copy since it left base, untracked new files included, and the
// commit it is measured from.
func Changes(dir, base string) (patch, from string, err error) {
	if base == "" {
		base = defaultBase(dir)
	}
	from, err = git(dir, "merge-base", base, "HEAD")
	if err != nil {
		if from, err = git(dir, "rev-parse", base); err != nil {
			return "", "", err
		}
	}
	from = strings.TrimSpace(from)
	patch, err = git(dir, "diff", "--no-color", "--no-renames", from)
	if err != nil {
		return "", "", err
	}
	// untracked files a PR would add
	list, err := git(dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	b.WriteString(patch)
	for _, f := range strings.Split(list, "\x00") {
		if f == "" {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, f))
		if rerr != nil || bytes.IndexByte(data, 0) >= 0 || len(data) > 1<<20 {
			continue // unreadable, binary or huge: left out
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", f, f, f, len(lines))
		for _, l := range lines {
			b.WriteString("+" + l + "\n")
		}
	}
	return b.String(), from, nil
}

// fileAt reads a file as it is at a commit ("" if it isn't there).
func fileAt(dir, commit, path string) string {
	s, err := git(dir, "show", commit+":"+path)
	if err != nil {
		return ""
	}
	return s
}

// RepoName is owner/name from the origin remote, else the folder's name.
func RepoName(dir string) string {
	if u, err := git(dir, "remote", "get-url", "origin"); err == nil {
		u = strings.TrimSuffix(strings.TrimSpace(u), ".git")
		u = strings.ReplaceAll(u, ":", "/")
		parts := strings.Split(u, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	abs, _ := filepath.Abs(dir)
	return "local/" + filepath.Base(abs)
}

// Rules loads the repo's rules as the base commit has them.
func Rules(dir, commit string) (config.Config, string, error) {
	cfg := config.Default()
	for _, p := range []string{"cowboy.json", ".cowboy.json"} {
		if s := fileAt(dir, commit, p); s != "" {
			if err := config.Merge(&cfg, []byte(s)); err != nil {
				return cfg, p, fmt.Errorf("%s: %v", p, err)
			}
			return cfg, p + " on the base branch", nil
		}
	}
	return cfg, "Cowboy's defaults (no cowboy.json on the base branch)", nil
}

// Run checks the changes in a checkout.
func Run(o Options) (Result, error) {
	if o.Dir == "" {
		o.Dir = "."
	}
	if _, err := git(o.Dir, "rev-parse", "--git-dir"); err != nil {
		return Result{}, fmt.Errorf("%s isn't a git checkout", o.Dir)
	}
	patch, from, err := Changes(o.Dir, o.Base)
	if err != nil {
		return Result{}, err
	}
	r := Result{Repo: RepoName(o.Dir), Base: from}
	cfg, src, err := Rules(o.Dir, from)
	if err != nil {
		return r, err
	}
	r.Rules = src
	files := diff.Parse(patch)
	if len(files) == 0 {
		r.Empty = true
		return r, nil
	}
	title := o.Title
	if title == "" {
		if s, err := git(o.Dir, "log", "-1", "--format=%s"); err == nil {
			title = strings.TrimSpace(s)
		}
	}
	msgs, _ := git(o.Dir, "log", "--format=%B%x00", from+"..HEAD")
	var commits []string
	for _, m := range strings.Split(msgs, "\x00") {
		if m = strings.TrimSpace(m); m != "" {
			commits = append(commits, m)
		}
	}
	owners := ""
	for _, p := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		if s := fileAt(o.Dir, from, p); s != "" {
			owners = s
			break
		}
	}
	var g *graph.Graph
	if !o.NoGraph {
		g, _ = graph.Build(o.Dir) // blast radius is optional: without it, those rules are skipped
	}
	meta := analyze.PRMeta{Repo: r.Repo, Title: title, Commits: commits}
	d, _ := pipeline.Run(pipeline.Input{Meta: meta, Files: files, Config: cfg, Graph: g,
		Codeowners: owners, Now: time.Now().UTC(), SortAll: true})
	r.Pen, r.Reasons, r.Shortcuts, r.Owners = d.Pen, d.Reasons, d.Signals.Shortcuts, d.Signals.Owners
	r.Files, r.Lines = d.Signals.Files, d.Signals.LinesChanged
	return r, nil
}

// Text is the result in plain words, for a person or an agent to act on.
func (r Result) Text() string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	if r.Empty {
		w("No changes against %s: nothing to check.\n", short(r.Base))
		return b.String()
	}
	w("%s: this change would be sorted %s (%d lines, %d files, compared with %s; rules from %s).\n", r.Repo, strings.ToUpper(string(r.Pen))[:1]+string(r.Pen)[1:], r.Lines, r.Files, short(r.Base), r.Rules)
	var careful, notes []string
	flagged := map[string]bool{}
	for _, s := range r.Shortcuts {
		flagged[s.Detail] = true
	}
	for _, x := range r.Reasons {
		if flagged[x.Detail] {
			continue // listed under shortcuts
		}
		switch x.Effect {
		case "careful":
			careful = append(careful, x.Detail)
		case "note":
			notes = append(notes, x.Detail)
		}
	}
	if len(careful) > 0 {
		w("\nWhy careful:\n")
		for _, c := range careful {
			w("- %s\n", c)
		}
	}
	if len(r.Shortcuts) > 0 {
		w("\nShortcuts found (a reviewer will see each of these; fix the cause, or say in the PR why it's right):\n")
		for _, s := range r.Shortcuts {
			w("- %s in %s: %s\n", s.Kind, s.File, s.Detail)
		}
	} else {
		w("\nNo shortcuts found (no skipped or deleted tests, silenced warnings, loosened CI or catch-alls).\n")
	}
	if len(notes) > 0 {
		w("\nWorth a look:\n")
		for _, n := range notes {
			w("- %s\n", n)
		}
	}
	if len(r.Owners) > 0 {
		w("\nCode owners who would be asked to review: %s\n", strings.Join(r.Owners, ", "))
	}
	return b.String()
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
