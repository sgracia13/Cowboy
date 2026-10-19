package web

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/forge"
)

// GitHubSetup is the "Connect GitHub" flow: Cowboy asks GitHub to create a
// private GitHub App for this server (GitHub's app-manifest flow), keeps
// its credentials, and sends the person on to pick repos. No token, no
// webhook to paste, no secret to invent.
type GitHubSetup struct {
	WebBase   string // https://github.com, or a GitHub Enterprise address
	PublicURL string // PUBLIC_URL, if set; otherwise taken from the request

	// Status is cheap (no network): is an app connected, and which.
	Status func() SetupStatus
	// Finish trades GitHub's one-time code for the app and starts using it.
	Finish func(code string) (forge.AppCreds, error)
	// Sync tracks every repo the app is installed on and sorts their open
	// pull requests. It returns how many repos it found.
	Sync func() (int, error)

	mu       sync.Mutex
	states   map[string]time.Time
	lastSync time.Time
}

// SetupStatus is what the setup page and the banner show.
type SetupStatus struct {
	Connected  bool
	AppName    string
	AppURL     string
	InstallURL string
	Repos      []string
}

func (g *GitHubSetup) newState() string {
	var b [24]byte
	rand.Read(b[:])
	s := hex.EncodeToString(b[:])
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.states == nil {
		g.states = map[string]time.Time{}
	}
	for k, exp := range g.states {
		if time.Now().After(exp) {
			delete(g.states, k)
		}
	}
	g.states[s] = time.Now().Add(time.Hour)
	return s
}

// useState checks and consumes a state value (single use, one hour).
func (g *GitHubSetup) useState(s string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.states[s]
	delete(g.states, s)
	return ok && time.Now().Before(exp)
}

// publicBase is where GitHub should send people (and webhooks) back to,
// and whether GitHub can reach it for webhooks.
func (g *GitHubSetup) publicBase(r *http.Request) (base string, hooks bool) {
	base = strings.TrimRight(g.PublicURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		host := r.Host
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			host = fh
		}
		base = scheme + "://" + host
	}
	u, err := url.Parse(base)
	if err != nil {
		return base, false
	}
	return base, u.Scheme == "https" && publicHost(u.Hostname())
}

func publicHost(h string) bool {
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
	}
	return strings.Contains(h, ".")
}

var appNameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,33}$`)
var ownerOK = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

func defaultAppName() string {
	var b [2]byte
	rand.Read(b[:])
	return "cowboy-" + hex.EncodeToString(b[:])
}

type setupPage struct {
	SetupStatus
	Base, DefaultName, Error string
	Hooks                    bool
}

func (s *Server) setupHome(w http.ResponseWriter, r *http.Request) {
	g := s.Setup
	base, hooks := g.publicBase(r)
	s.render(w, "setup-home", "setup", setupPage{SetupStatus: g.Status(), Base: base, Hooks: hooks, DefaultName: defaultAppName(), Error: r.URL.Query().Get("error")})
}

type setupGo struct {
	Action, Manifest string
}

// setupStart builds the manifest and hands the browser to GitHub.
func (s *Server) setupStart(w http.ResponseWriter, r *http.Request) {
	g := s.Setup
	if g.Status().Connected {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = defaultAppName()
	}
	if !appNameOK.MatchString(name) {
		http.Redirect(w, r, "/setup?error="+url.QueryEscape("App names can use letters, numbers, spaces, dots, dashes and underscores, up to 34 characters."), http.StatusSeeOther)
		return
	}
	org := strings.TrimSpace(strings.TrimPrefix(r.FormValue("org"), "@"))
	if org != "" && !ownerOK.MatchString(org) {
		http.Redirect(w, r, "/setup?error="+url.QueryEscape("That doesn't look like a GitHub organization name."), http.StatusSeeOther)
		return
	}
	base, hooks := g.publicBase(r)
	web := strings.TrimRight(g.WebBase, "/")
	action := web + "/settings/apps/new"
	if org != "" {
		action = web + "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	action += "?state=" + g.newState()
	// This one page posts a form to GitHub, so widen form-action for it only.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self' "+web)
	s.render(w, "setup-go", "setup", setupGo{Action: action, Manifest: string(forge.Manifest(name, base, hooks))})
}

// setupCallback is where GitHub sends the browser after the app is made.
// It's outside the login (a sign-in cookie isn't sent on a redirect from
// another site), so it trusts only a valid, single-use state value.
func (s *Server) setupCallback(w http.ResponseWriter, r *http.Request) {
	g := s.Setup
	if !g.useState(r.URL.Query().Get("state")) {
		http.Error(w, "This setup link has expired or was already used. Go back to /setup and start again.", http.StatusBadRequest)
		return
	}
	if g.Status().Connected {
		http.Error(w, "Cowboy is already connected to a GitHub App.", http.StatusConflict)
		return
	}
	creds, err := g.Finish(r.URL.Query().Get("code"))
	if err != nil {
		log.Printf("github setup: %v", err)
		http.Error(w, "Couldn't finish connecting to GitHub: "+err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("github setup: connected as the app %q (%s)", creds.Name, creds.HTMLURL)
	http.Redirect(w, r, creds.InstallURL(), http.StatusSeeOther)
}

// setupInstalled is where GitHub sends the browser after repos are picked
// (or changed). Also outside the login, so it shows no repo names and only
// starts a sync, at most every 10 seconds.
func (s *Server) setupInstalled(w http.ResponseWriter, r *http.Request) {
	g := s.Setup
	if !g.Status().Connected {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	g.mu.Lock()
	run := time.Since(g.lastSync) > 10*time.Second
	if run {
		g.lastSync = time.Now()
	}
	g.mu.Unlock()
	if run {
		go func() {
			n, err := g.Sync()
			if err != nil {
				log.Printf("github setup: listing repos: %v", err)
				return
			}
			log.Printf("github setup: the app can use %d repo(s)", n)
		}()
	}
	s.render(w, "setup-done", "setup", nil)
}
