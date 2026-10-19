package web

import (
	"html"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/sgracia13/Cowboy/internal/forge"
)

// The demo git host: a plain stand-in for a PR page on GitHub or Forgejo,
// shown only in demo mode, so people can see what Cowboy leaves on a PR
// (labels and one comment) without connecting a real host.

type hostPR struct {
	Repo, Title, Author string
	Number              int
	Labels              []string
	Reviewers           []string
	Comment             template.HTML // rendered HTML
	Files               []string
}

func (s *Server) host(w http.ResponseWriter, r *http.Request) {
	f, ok := s.App.Forge.(*forge.Fake)
	if !ok {
		http.NotFound(w, r)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	meta, _, err := f.GetPR("acme/app", n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, _ := s.App.Store.Get("acme/app", n)
	p := hostPR{Repo: "acme/app", Number: n, Title: meta.Title, Author: meta.Author,
		Labels: f.Labels[n], Reviewers: f.Reviews[n], Comment: template.HTML(renderMarkdown(f.Comments[n])), Files: d.Signals.Paths}
	s.fragment(w, "host.html", p)
}

var (
	reBold = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reCode = regexp.MustCompile("`([^`]+)`")
)

// renderMarkdown handles the small subset Cowboy's comment uses.
func renderMarkdown(md string) string {
	if md == "" {
		return ""
	}
	inline := func(s string) string {
		s = html.EscapeString(s)
		s = reBold.ReplaceAllString(s, "<b>$1</b>")
		s = reCode.ReplaceAllString(s, "<code>$1</code>")
		s = strings.ReplaceAll(s, "&lt;sub&gt;", "<sub>")
		s = strings.ReplaceAll(s, "&lt;/sub&gt;", "</sub>")
		return s
	}
	var b strings.Builder
	depth := 0
	closeTo := func(d int) {
		for depth > d {
			b.WriteString("</ul>")
			depth--
		}
	}
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimRight(line, " ")
		switch {
		case strings.HasPrefix(t, "<!--"):
			continue
		case strings.HasPrefix(t, "  - "):
			if depth < 2 {
				b.WriteString("<ul>")
				depth = 2
			}
			b.WriteString("<li>" + inline(t[4:]) + "</li>")
		case strings.HasPrefix(t, "- "):
			closeTo(1)
			if depth < 1 {
				b.WriteString("<ul>")
				depth = 1
			}
			b.WriteString("<li>" + inline(t[2:]) + "</li>")
		case t == "":
			closeTo(0)
		default:
			closeTo(0)
			b.WriteString("<p>" + inline(t) + "</p>")
		}
	}
	closeTo(0)
	return b.String()
}
