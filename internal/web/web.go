// Package web is the corral: a small server-rendered UI over the store.
// No JavaScript framework, no build step: html/template and one stylesheet.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/detect"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

//go:embed templates/*.html static/*
var files embed.FS

// Server serves the corral.
type Server struct {
	App  *app.App
	tmpl *template.Template
	// SignOut shows a sign-out link (password login is on).
	SignOut bool
	// TallyNote says where the weekly tally is sent.
	TallyNote string
	// Setup is the "Connect GitHub" flow, when Cowboy signs in as a GitHub App.
	Setup *GitHubSetup
}

// New parses templates.
func New(a *app.App) *Server {
	funcs := template.FuncMap{
		"pen": func(d pipeline.Decision) string {
			if d.Corrected != "" {
				return string(d.Corrected)
			}
			return string(d.Pen)
		},
		"short": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n-1] + "…"
		},
		"top": func(d pipeline.Decision) string {
			for _, r := range d.Reasons {
				if r.Effect == "careful" {
					return r.Detail
				}
			}
			if d.DoubleOf != 0 {
				return "same work as #" + strconv.Itoa(d.DoubleOf)
			}
			return ""
		},
		"ago": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return strconv.Itoa(int(d.Minutes())) + "m ago"
			case d < 48*time.Hour:
				return strconv.Itoa(int(d.Hours())) + "h ago"
			}
			return strconv.Itoa(int(d.Hours()/24)) + "d ago"
		},
		"dict3": func(name, sub string, items []pipeline.Decision) map[string]any {
			return map[string]any{"Name": name, "Sub": sub, "Items": items}
		},
		"join": strings.Join,
		"likely": func(d pipeline.Decision) bool {
			return d.Detection != nil && d.Detection.Label == "likely agent" && d.AgentCleared == ""
		},
		"agentTag": func(d pipeline.Decision) string {
			switch {
			case d.Detection != nil && d.Detection.Label == "likely agent" && d.AgentCleared == "":
				return "likely " + strings.TrimSuffix(d.Agent, "?")
			case d.Agent == "":
				return "person"
			}
			return d.Agent
		},
		"pct":  func(n int) int { return n },
		"isub": func(a, b int) int { return a - b },
		"modname": func(m string) string {
			return map[string]string{"disclosure": "deterministic", "time": "behaviour", "text": "text", "code": "code", "baseline": "history"}[m]
		},
		"mulf": func(a, b float64) float64 { return a * b },
		"modsum": func(rs []detect.Contribution, mod string) float64 {
			t := 0.0
			for _, r := range rs {
				if r.Module == mod {
					t += r.Effect
				}
			}
			return t
		},
		"trimprefix": strings.TrimPrefix,
		"lower":      strings.ToLower,
		"json": func(v any) string {
			b, _ := json.MarshalIndent(v, "", "  ")
			return string(b)
		},
		"checks": func(p *bool) string {
			if p == nil {
				return "unknown"
			}
			if *p {
				return "passing"
			}
			return "failing"
		},
	}
	t := template.Must(template.New("").Funcs(funcs).ParseFS(files, "templates/*.html"))
	return &Server{App: a, tmpl: t}
}

// Routes registers the UI on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.Handle("GET /static/", http.FileServerFS(files))
	if s.Setup != nil {
		mux.HandleFunc("GET /setup", s.setupHome)
		mux.HandleFunc("POST /setup/github/start", s.setupStart)
		mux.HandleFunc("GET /setup/github/callback", s.setupCallback)
		mux.HandleFunc("GET /setup/github/installed", s.setupInstalled)
	}
	mux.HandleFunc("GET /{$}", s.corral)
	mux.HandleFunc("GET /pr", s.pr)
	mux.HandleFunc("POST /pr/move", s.move)
	mux.HandleFunc("POST /pr/notagent", s.notAgent)
	mux.HandleFunc("GET /tally", s.tally)
	mux.HandleFunc("GET /rules", s.rules)
	mux.HandleFunc("GET /trail", s.trail)
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("GET /demo/host", s.host)
	mux.HandleFunc("POST /sweep", s.sweep)
	mux.HandleFunc("GET /api/decisions", s.apiDecisions)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
}

type page struct {
	Title   string
	Nav     string
	Forge   string
	Model   string
	DryRun  bool
	SignOut bool
	// NeedsSetup shows the "connect GitHub" banner; Setup links the setup page.
	NeedsSetup, Setup bool
	Data              any
}

func (s *Server) render(w http.ResponseWriter, name, nav string, data any) {
	p := page{Title: nav, Nav: nav, Forge: s.App.Forge.Name(), Model: s.App.ModelName(), DryRun: s.App.DryRun, SignOut: s.SignOut, Data: data}
	if s.Setup != nil {
		p.Setup = true
		p.NeedsSetup = nav != "setup" && !s.Setup.Status().Connected
	}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, p); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// fragment renders just one named template (for live updates).
func (s *Server) fragment(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

type corralData struct {
	Repo                  string
	Repos                 []string
	Easy, Normal, Careful []pipeline.Decision
	Doubles, Strays       []pipeline.Decision
	Total                 int
}

func (s *Server) corral(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	d := corralData{Repo: repo, Repos: s.App.Store.Repos()}
	for _, x := range s.App.Store.List(repo, "open") {
		p := x.Pen
		if x.Corrected != "" {
			p = x.Corrected
		}
		switch p {
		case score.Easy:
			d.Easy = append(d.Easy, x)
		case score.Normal:
			d.Normal = append(d.Normal, x)
		default:
			d.Careful = append(d.Careful, x)
		}
		if x.DoubleOf != 0 {
			d.Doubles = append(d.Doubles, x)
		}
		if x.Stray {
			d.Strays = append(d.Strays, x)
		}
		d.Total++
	}
	if r.Header.Get("X-Cowboy-Partial") == "1" {
		s.fragment(w, "corral-live", d)
		return
	}
	s.render(w, "corral.html", "corral", d)
}

type prData struct {
	D       pipeline.Decision
	Comment string
	Reviews []string
	OnForge []string
	Pens    []score.Pen
	Moved   bool
}

func (s *Server) pr(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	d, ok := s.App.Store.Get(repo, n)
	if !ok {
		http.NotFound(w, r)
		return
	}
	pd := prData{D: d, Comment: pipeline.FullComment(d), Pens: []score.Pen{score.Easy, score.Normal, score.Careful}, Moved: r.URL.Query().Get("moved") != ""}
	if f, ok := s.App.Forge.(*forge.Fake); ok {
		pd.OnForge = f.Labels[n]
		pd.Reviews = f.Reviews[n]
	}
	s.render(w, "pr.html", "corral", pd)
}

func (s *Server) move(w http.ResponseWriter, r *http.Request) {
	repo := r.FormValue("repo")
	n, _ := strconv.Atoi(r.FormValue("n"))
	to := score.Pen(r.FormValue("to"))
	if to != score.Easy && to != score.Normal && to != score.Careful {
		http.Error(w, "bad pen", 400)
		return
	}
	by := r.Header.Get("X-Forwarded-User")
	if by == "" {
		by = "you"
	}
	if err := s.App.Correct(repo, n, to, by); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/pr?repo="+repo+"&n="+strconv.Itoa(n)+"&moved=1", http.StatusSeeOther)
}

func (s *Server) tally(w http.ResponseWriter, r *http.Request) {
	rows := s.App.Tally(time.Now().AddDate(0, 0, -7))
	s.render(w, "tally.html", "tally", map[string]any{"Rows": rows, "Markdown": app.TallyMarkdown(rows), "Corrections": s.App.Store.Corrections(), "TallyNote": s.TallyNote})
}

func (s *Server) rules(w http.ResponseWriter, r *http.Request) {
	type repoRules struct {
		Repo  string
		JSON  string
		Blast string
	}
	var out []repoRules
	for _, repo := range s.App.Store.Repos() {
		b, _ := json.MarshalIndent(s.App.Config(repo), "", "  ")
		out = append(out, repoRules{repo, string(b), s.App.GraphStatus(repo)})
	}
	type det struct {
		Repo    string
		On      bool
		SortAll bool
		LabelAt int
		Prior   float64
	}
	var dets []det
	for _, repo := range s.App.Store.Repos() {
		c := s.App.Config(repo)
		dets = append(dets, det{repo, c.DetectOn() && s.App.Baselines != nil, c.SortAllOn(), c.Detect.LabelAt, c.Detect.Prior})
	}
	s.render(w, "rules.html", "rules", map[string]any{"Repos": out, "Model": s.App.ModelName(), "Detect": dets})
}

func (s *Server) trail(w http.ResponseWriter, r *http.Request) {
	var writes []string
	if f, ok := s.App.Forge.(*forge.Fake); ok {
		writes = append(writes, f.Log...)
		for i, j := 0, len(writes)-1; i < j; i, j = i+1, j-1 {
			writes[i], writes[j] = writes[j], writes[i]
		}
	}
	s.render(w, "trail.html", "trail", map[string]any{"Events": s.App.Recent(), "Writes": writes})
}

func (s *Server) sweep(w http.ResponseWriter, r *http.Request) {
	s.App.Sweep()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) apiDecisions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.App.Store.List(r.URL.Query().Get("repo"), r.URL.Query().Get("state")))
}

// events streams Cowboy's activity to the browser (Server-Sent Events), so
// the corral updates the moment a PR is sorted. No polling, no websocket
// library: one long HTTP response.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // let nginx pass it through
	ch, stop := s.App.Subscribe()
	defer stop()
	fmt.Fprint(w, "retry: 3000\n: hello\n\n")
	fl.Flush()
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-beat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) notAgent(w http.ResponseWriter, r *http.Request) {
	repo := r.FormValue("repo")
	n, _ := strconv.Atoi(r.FormValue("n"))
	by := r.Header.Get("X-Forwarded-User")
	if by == "" {
		by = "you"
	}
	if err := s.App.ClearAgent(repo, n, by); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/pr?repo="+repo+"&n="+strconv.Itoa(n), http.StatusSeeOther)
}
