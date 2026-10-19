package web

import (
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Connect is the setup page's token route: sign in with the GitHub CLI's
// login or a pasted token, pick repos, and choose whether Cowboy only looks
// or also labels. Starting in "just look" mode writes nothing to GitHub.
type Connect struct {
	// ReadLink and WriteLink open GitHub's new-token page pre-filled with
	// the permissions each mode needs.
	ReadLink, WriteLink string
	// GHLogin reports whether the GitHub CLI (gh) is signed in here.
	GHLogin func() bool
	Status  func() ConnectStatus
	// UseGH and UseToken switch Cowboy to that login and save it. They
	// return the GitHub account it belongs to.
	UseGH    func() (string, error)
	UseToken func(token string) (string, error)
	// Repos lists the repos the login can see.
	Repos func() ([]string, error)
	// Choose saves the repos to sort and the mode, and starts sorting and
	// rating their last 90 days.
	Choose func(repos []string, label bool) error
	// SetLabelling switches labelling on (after checking the login can
	// write labels) or off.
	SetLabelling func(on bool) error
	// Disconnect forgets the saved login.
	Disconnect func() error
}

// ConnectStatus is what the setup page shows about the token route.
type ConnectStatus struct {
	Connected bool
	Via       string // "gh" or "token"
	User      string
	Repos     []string
	Labelling bool
	Locked    string // why the mode can't be changed here, if it can't
}

type connectPage struct {
	App      SetupStatus // the GitHub App route, if offered
	HasApp   bool
	C        ConnectStatus
	GH       bool
	Read     string
	Write    string
	Error    string
	Note     string
	Base     string
	Hooks    bool
	AppName  string
	Choosing bool
	Repos    []repoChoice
	ReposErr string
}

type repoChoice struct {
	Name    string
	Checked bool
}

func (s *Server) connected() bool {
	if s.Setup != nil && s.Setup.Status().Connected {
		return true
	}
	return s.Connect != nil && s.Connect.Status().Connected
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request, choosing bool) {
	p := connectPage{Error: r.URL.Query().Get("error"), Note: r.URL.Query().Get("note"), Choosing: choosing}
	if s.Setup != nil {
		p.HasApp = true
		p.App = s.Setup.Status()
		p.Base, p.Hooks = s.Setup.publicBase(r)
		p.AppName = defaultAppName()
	}
	if c := s.Connect; c != nil {
		p.C = c.Status()
		p.GH = c.GHLogin != nil && c.GHLogin()
		p.Read, p.Write = c.ReadLink, c.WriteLink
		if choosing || (p.C.Connected && len(p.C.Repos) == 0) {
			p.Choosing = true
			names, err := c.Repos()
			if err != nil {
				p.ReposErr = err.Error()
			}
			have := map[string]bool{}
			for _, r := range p.C.Repos {
				have[strings.ToLower(r)] = true
			}
			seen := map[string]bool{}
			for _, n := range names {
				p.Repos = append(p.Repos, repoChoice{Name: n, Checked: have[strings.ToLower(n)]})
				seen[strings.ToLower(n)] = true
			}
			// repos already chosen that the list didn't return stay listed
			for _, r := range p.C.Repos {
				if !seen[strings.ToLower(r)] {
					p.Repos = append(p.Repos, repoChoice{Name: r, Checked: true})
				}
			}
		}
	}
	s.render(w, "setup-home", "setup", p)
}

func (s *Server) setupHome(w http.ResponseWriter, r *http.Request) { s.setupPage(w, r, false) }

func (s *Server) setupRepos(w http.ResponseWriter, r *http.Request) { s.setupPage(w, r, true) }

func back(w http.ResponseWriter, r *http.Request, path, key, msg string) {
	if msg != "" {
		path += "?" + key + "=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (s *Server) setupGH(w http.ResponseWriter, r *http.Request) {
	user, err := s.Connect.UseGH()
	if err != nil {
		back(w, r, "/setup", "error", "Couldn't use the GitHub CLI's login: "+err.Error())
		return
	}
	log.Printf("setup: signed in as %s with the GitHub CLI's login", user)
	http.Redirect(w, r, "/setup/repos", http.StatusSeeOther)
}

func (s *Server) setupToken(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(r.FormValue("token"))
	if tok == "" {
		back(w, r, "/setup", "error", "Paste the token first.")
		return
	}
	user, err := s.Connect.UseToken(tok)
	if err != nil {
		back(w, r, "/setup", "error", "GitHub didn't accept that token: "+err.Error())
		return
	}
	log.Printf("setup: signed in as %s with a token", user)
	if len(s.Connect.Status().Repos) > 0 {
		back(w, r, "/setup", "note", "Signed in as "+user+" with the new token.")
		return
	}
	http.Redirect(w, r, "/setup/repos", http.StatusSeeOther)
}

func (s *Server) setupChoose(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	repos := r.Form["repo"]
	if extra := strings.TrimSpace(r.FormValue("other")); extra != "" {
		repos = append(repos, strings.FieldsFunc(extra, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' })...)
	}
	seen := map[string]bool{}
	var clean []string
	for _, x := range repos {
		x = strings.Trim(strings.TrimSpace(x), "/")
		x = strings.TrimPrefix(x, "https://github.com/")
		if x == "" || seen[strings.ToLower(x)] {
			continue
		}
		if parts := strings.Split(x, "/"); len(parts) != 2 || !ownerOK.MatchString(parts[0]) {
			back(w, r, "/setup/repos", "error", x+" doesn't look like owner/repo.")
			return
		}
		seen[strings.ToLower(x)] = true
		clean = append(clean, x)
	}
	sort.Strings(clean)
	if len(clean) == 0 {
		back(w, r, "/setup/repos", "error", "Pick at least one repo.")
		return
	}
	if err := s.Connect.Choose(clean, r.FormValue("mode") == "label"); err != nil {
		back(w, r, "/setup/repos", "error", err.Error())
		return
	}
	if s.Outcomes != nil {
		http.Redirect(w, r, "/outcomes", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) setupMode(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("label") == "on"
	if err := s.Connect.SetLabelling(on); err != nil {
		back(w, r, "/setup", "error", err.Error())
		return
	}
	if on {
		back(w, r, "/setup", "note", "Labelling is on. New and updated pull requests get a cowboy label from now on.")
		return
	}
	back(w, r, "/setup", "note", "Labelling is off. Cowboy keeps sorting but writes nothing to GitHub.")
}

func (s *Server) setupDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.Connect.Disconnect(); err != nil {
		back(w, r, "/setup", "error", err.Error())
		return
	}
	back(w, r, "/setup", "note", "Signed out. Cowboy forgot the saved login; the data it collected stays.")
}
