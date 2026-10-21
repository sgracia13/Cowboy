package main

// cowboy check and cowboy mcp: Cowboy for the person or agent writing the
// change, before there is a pull request. Both only read.

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/check"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/habits"
	"github.com/sgracia13/Cowboy/internal/mcp"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/store"
)

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	dir := fs.String("repo", ".", "the checkout to check")
	base := fs.String("base", "", "compare against this branch or commit (default: the default branch)")
	title := fs.String("title", "", "what the change is for (default: the latest commit's subject)")
	noGraph := fs.Bool("no-blast-radius", false, "skip building the import graph (faster on big repos)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	strict := fs.Bool("strict", false, "exit 1 when shortcuts are found (for a git hook or CI)")
	fs.Parse(args)
	r, err := check.Run(check.Options{Dir: *dir, Base: *base, Title: *title, NoGraph: *noGraph})
	if err != nil {
		log.Fatal(err)
	}
	if *asJSON {
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		e.Encode(r)
	} else {
		fmt.Print(r.Text())
	}
	if *strict && len(r.Shortcuts) > 0 {
		os.Exit(1)
	}
}

// localData opens this machine's Cowboy data, read for history, if there is
// any. It never creates a database where none exists.
func localData() (*store.Store, *outcome.State, bool) {
	path := os.Getenv("DATA")
	if path == "" {
		if _, err := os.Stat("data/cowboy.db"); err == nil {
			path = "data/cowboy.json"
		} else if dir, err := os.UserConfigDir(); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cowboy", "cowboy.db")); err == nil {
				path = filepath.Join(dir, "cowboy", "cowboy.json")
			}
		}
	}
	if path == "" {
		return nil, nil, false
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "outcomes.db")); err != nil {
		return nil, nil, false
	}
	st, err := store.Open(path)
	if err != nil {
		return nil, nil, false
	}
	oc, err := outcome.Open(filepath.Join(filepath.Dir(path), "outcomes.json"))
	if err != nil {
		return nil, nil, false
	}
	return st, oc, true
}

type pathArg struct {
	Path string `json:"path"`
}

func (p pathArg) dir() string {
	if p.Path == "" {
		return "."
	}
	return p.Path
}

// baseRules: the repo's rules as its default branch has them.
func baseRules(dir string) (config.Config, string, string, error) {
	_, from, err := check.Changes(dir, "")
	if err != nil {
		return config.Config{}, "", "", err
	}
	cfg, src, err := check.Rules(dir, from)
	return cfg, src, check.RepoName(dir), err
}

func sameRepo(a, b string) bool { return strings.EqualFold(a, b) }

// history: merged PRs in this machine's data that touched path (a file, or
// a folder ending in /), and what happened to them.
func history(repo, path string, st *store.Store, oc *outcome.State) (string, any) {
	type row struct {
		Number  int      `json:"number"`
		Title   string   `json:"title"`
		Merged  string   `json:"merged"`
		Trouble []string `json:"trouble,omitempty"`
	}
	trouble := map[int][]string{}
	for _, o := range oc.Outcomes("", 0) {
		if sameRepo(o.Repo, repo) && o.Counts() {
			trouble[o.Number] = append(trouble[o.Number], o.Kind)
		}
	}
	var rows []row
	bad := 0
	for _, m := range oc.Merges("") {
		if !sameRepo(m.Repo, repo) {
			continue
		}
		hit := false
		for _, f := range m.Files {
			if f == path || (strings.HasSuffix(path, "/") && strings.HasPrefix(f, path)) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		r := row{Number: m.Number, Title: m.Title, Merged: m.MergedAt.Format("2006-01-02"), Trouble: trouble[m.Number]}
		if len(r.Trouble) > 0 {
			bad++
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Merged > rows[j].Merged })
	var b strings.Builder
	if len(rows) == 0 {
		fmt.Fprintf(&b, "No merged pull requests in Cowboy's history here touched %s.\n", path)
	} else {
		fmt.Fprintf(&b, "%d merged pull requests touched %s; %d had trouble after merge (reverted, broke main, or a confirmed fix or incident).\n", len(rows), path, bad)
		for i, r := range rows {
			if i == 20 {
				fmt.Fprintf(&b, "… and %d more\n", len(rows)-20)
				break
			}
			t := ""
			if len(r.Trouble) > 0 {
				t = " — trouble: " + strings.Join(r.Trouble, ", ")
			}
			fmt.Fprintf(&b, "- #%d %s (merged %s)%s\n", r.Number, r.Title, r.Merged, t)
		}
	}
	b.WriteString("A file with past trouble isn't proven risky, and one without isn't proven safe: use this to decide where to add tests or explain more, not as a verdict.\n")
	return b.String(), map[string]any{"repo": repo, "path": path, "prs": rows, "with_trouble": bad}
}

func agentTools() []mcp.Tool {
	readOnly := map[string]any{"readOnlyHint": true, "openWorldHint": false}
	pathProp := map[string]any{"type": "string", "description": "path to the git checkout (default: the current folder)"}
	return []mcp.Tool{
		{
			Name:        "check_changes",
			Title:       "Check my changes",
			Description: "Sort the changes in a git checkout (committed, uncommitted and new files since the default branch) the way Cowboy would sort the pull request: easy, normal or careful, with the reasons, and any shortcuts it found (skipped or deleted tests, silenced warnings, loosened CI, catch-alls). Call this before opening a pull request and fix what it flags, or explain in the PR why it is right. Read-only.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"path":         pathProp,
				"base":         map[string]any{"type": "string", "description": "branch or commit to compare against (default: the default branch)"},
				"title":        map[string]any{"type": "string", "description": "what the change is for (default: the latest commit's subject)"},
				"blast_radius": map[string]any{"type": "boolean", "description": "count how many files use each changed file (slower on big repos; default true)"},
			}},
			Annotations: readOnly,
			Call: func(a json.RawMessage) (string, any, error) {
				var p struct {
					pathArg
					Base        string `json:"base"`
					Title       string `json:"title"`
					BlastRadius *bool  `json:"blast_radius"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", nil, err
				}
				r, err := check.Run(check.Options{Dir: p.dir(), Base: p.Base, Title: p.Title, NoGraph: p.BlastRadius != nil && !*p.BlastRadius})
				if err != nil {
					return "", nil, err
				}
				return r.Text(), r, nil
			},
		},
		{
			Name:        "repo_rules",
			Title:       "This repo's review rules",
			Description: "The review rules this repo's pull requests are sorted by, as its default branch has them (cowboy.json over Cowboy's defaults): which paths and files get a careful review, the size that gets the quickest review, and which shortcut checks are on. Read-only.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": pathProp}},
			Annotations: readOnly,
			Call: func(a json.RawMessage) (string, any, error) {
				var p pathArg
				json.Unmarshal(a, &p)
				cfg, src, repo, err := baseRules(p.dir())
				if err != nil {
					return "", nil, err
				}
				b, _ := json.MarshalIndent(cfg, "", "  ")
				return fmt.Sprintf("Rules for %s, from %s:\n\n%s\n", repo, src, b), cfg, nil
			},
		},
		{
			Name:        "agent_guide",
			Title:       "Guidance for this repo",
			Description: "Plain instructions for working in this repo, built from its review rules, the shortcuts Cowboy flags and, when this machine has Cowboy history for it, the team's habits. The same text `cowboy guide` writes into AGENTS.md. Read-only.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": pathProp}},
			Annotations: readOnly,
			Call: func(a json.RawMessage) (string, any, error) {
				var p pathArg
				json.Unmarshal(a, &p)
				cfg, _, repo, err := baseRules(p.dir())
				if err != nil {
					return "", nil, err
				}
				rep := habits.Report{}
				if st, oc, ok := localData(); ok {
					rep = habits.Build(repo, st.List(repo, ""), oc, cfg, time.Now().UTC(), outcomeWindow(), 90)
				}
				return habits.Guide(repo, cfg, rep), nil, nil
			},
		},
		{
			Name:        "file_history",
			Title:       "Trouble history for a file",
			Description: "Merged pull requests in this machine's Cowboy history that touched a file (or a folder, ending in /), and which had trouble after merge: reverted, broke main, or a confirmed fix or incident. Useful before changing code that has broken before. Needs Cowboy to have run on this repo (it can rate the last 90 days on day one). Read-only.",
			InputSchema: map[string]any{"type": "object", "required": []string{"file"}, "properties": map[string]any{
				"path": pathProp,
				"file": map[string]any{"type": "string", "description": "file path in the repo, e.g. src/auth/session.ts, or a folder ending in /"},
			}},
			Annotations: readOnly,
			Call: func(a json.RawMessage) (string, any, error) {
				var p struct {
					pathArg
					File string `json:"file"`
				}
				json.Unmarshal(a, &p)
				if strings.TrimSpace(p.File) == "" {
					return "", nil, fmt.Errorf("file is required")
				}
				st, oc, ok := localData()
				if !ok {
					return "No Cowboy history on this machine yet. Run `cowboy`, connect the repo, and press \"Rate the last 90 days\" on the After merge page; then this tool can answer.", nil, nil
				}
				text, data := history(check.RepoName(p.dir()), strings.TrimPrefix(p.File, "./"), st, oc)
				return text, data, nil
			},
		},
	}
}

func mcpCmd(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "no log lines on stderr")
	fs.Parse(args)
	s := &mcp.Server{Name: "cowboy", Version: version, Tools: agentTools()}
	if !*quiet {
		s.Log = log.New(os.Stderr, "cowboy mcp: ", log.LstdFlags)
	}
	if err := s.Serve(os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
