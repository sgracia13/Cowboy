package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/habits"
)

// habitsConfig is the rule book the guide describes: the repo's own, or
// the defaults for a replayed history (the demo).
func (s *Server) habitsConfig(repo string) config.Config {
	if s.Outcomes.History != nil {
		return config.Default()
	}
	return s.App.Config(repo)
}

// habitRepos lists the repos with merged history, and picks the one asked
// for (or the first).
func (s *Server) habitRepos(r *http.Request) ([]string, string) {
	seen := map[string]bool{}
	merged := map[string]int{}
	var repos []string
	for _, d := range s.history() {
		if d.State != "merged" {
			continue
		}
		merged[strings.ToLower(d.Repo)]++
		if !seen[strings.ToLower(d.Repo)] {
			seen[strings.ToLower(d.Repo)] = true
			repos = append(repos, d.Repo)
		}
	}
	for _, r := range s.App.Store.Repos() {
		if !seen[strings.ToLower(r)] {
			seen[strings.ToLower(r)] = true
			repos = append(repos, r)
		}
	}
	sort.Strings(repos)
	want := r.URL.Query().Get("repo")
	for _, x := range repos {
		if strings.EqualFold(x, want) {
			return repos, x
		}
	}
	// none asked for: open on the repo with the most merged history
	if len(repos) > 0 {
		best := repos[0]
		for _, x := range repos {
			if merged[strings.ToLower(x)] > merged[strings.ToLower(best)] {
				best = x
			}
		}
		return repos, best
	}
	return nil, ""
}

func (s *Server) habitReport(repo string) habits.Report {
	o := s.Outcomes
	return habits.Build(repo, s.history(), o.State, s.habitsConfig(repo), o.now(), o.Window, 90)
}

type habitsPage struct {
	Repos  []string
	Repo   string
	Report habits.Report
	Guide  string
	Window int
}

func (s *Server) habitsPage(w http.ResponseWriter, r *http.Request) {
	repos, repo := s.habitRepos(r)
	p := habitsPage{Repos: repos, Repo: repo, Window: int(s.Outcomes.Window.Hours() / 24)}
	if repo != "" {
		p.Report = s.habitReport(repo)
		p.Guide = habits.Guide(repo, s.habitsConfig(repo), p.Report)
	}
	s.render(w, "habits.html", "habits", p)
}

// habitsGuide downloads the agent guidance as a Markdown file.
func (s *Server) habitsGuide(w http.ResponseWriter, r *http.Request) {
	_, repo := s.habitRepos(r)
	if repo == "" {
		http.Error(w, "no repos yet", http.StatusNotFound)
		return
	}
	g := habits.Guide(repo, s.habitsConfig(repo), s.habitReport(repo))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="cowboy-agent-guide.md"`)
	w.Write([]byte(g))
}
