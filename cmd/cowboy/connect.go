package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/web"
)

// settings is what the setup page saves (settings.json next to the data
// file, readable by its owner only). Environment variables still win.
type settings struct {
	Token         string   `json:"token,omitempty"`
	Via           string   `json:"via,omitempty"` // "gh" or "token"
	User          string   `json:"user,omitempty"`
	Repos         []string `json:"repos,omitempty"`
	Labelling     bool     `json:"labelling,omitempty"`
	WebhookSecret string   `json:"webhook_secret,omitempty"`
}

func settingsPath() string { return filepath.Join(dataDir(), "settings.json") }

func loadSettings() settings {
	var s settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			log.Printf("%s: %v (ignoring it)", settingsPath(), err)
			return settings{}
		}
	}
	return s
}

func (s settings) save() error {
	b, _ := json.MarshalIndent(s, "", " ")
	os.MkdirAll(dataDir(), 0o700)
	tmp := settingsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath())
}

func randomSecret() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ghLogin asks the GitHub CLI for its token, if it's installed and signed in.
func ghLogin() (string, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("the GitHub CLI (gh) isn't installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "auth", "token").Output()
	tok := strings.TrimSpace(string(out))
	if err != nil || tok == "" {
		return "", fmt.Errorf("the GitHub CLI isn't signed in (run gh auth login)")
	}
	return tok, nil
}

// tokenLinks are GitHub's new-token page, pre-filled for each mode.
func tokenLinks() (read, write string) {
	webBase := "https://github.com"
	if api := os.Getenv("FORGE_URL"); api != "" {
		webBase = strings.TrimSuffix(strings.TrimRight(api, "/"), "/api/v3")
	}
	base := func(desc string) url.Values {
		v := url.Values{}
		v.Set("name", "cowboy "+time.Now().Format("2006-01-02"))
		v.Set("description", desc)
		v.Set("expires_in", "90")
		v.Set("contents", "read")
		v.Set("statuses", "read")
		return v
	}
	r := base("Cowboy: read pull requests to sort them and check what happened after merge")
	r.Set("pull_requests", "read")
	w := base("Cowboy: read pull requests, and add its labels and one comment")
	w.Set("pull_requests", "write")
	w.Set("issues", "write")
	page := webBase + "/settings/personal-access-tokens/new?"
	return page + r.Encode(), page + w.Encode()
}

// connector backs the setup page's token route.
type connector struct {
	a   *app.App
	gh  *forge.GitHub
	mu  sync.Mutex
	s   settings
	env bool // signed in with FORGE_TOKEN from the environment
	// envRepos came from REPOS; the setup page never removes them.
	envRepos map[string]bool
	ghOK     struct {
		at time.Time
		ok bool
	}
}

func (c *connector) web() *web.Connect {
	read, write := tokenLinks()
	return &web.Connect{
		ReadLink: read, WriteLink: write,
		GHLogin:      c.ghAvailable,
		Status:       c.status,
		UseGH:        c.useGH,
		UseToken:     func(t string) (string, error) { return c.use(t, "token") },
		Repos:        func() ([]string, error) { return c.gh.MyRepos(300) },
		Choose:       c.choose,
		SetLabelling: func(on bool) error { return c.setLabelling(on) },
		Disconnect:   c.disconnect,
	}
}

func (c *connector) ghAvailable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.ghOK.at) < time.Minute {
		return c.ghOK.ok
	}
	_, err := ghLogin()
	c.ghOK.at, c.ghOK.ok = time.Now(), err == nil
	return c.ghOK.ok
}

func (c *connector) status() web.ConnectStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := web.ConnectStatus{Connected: c.env || c.s.Token != "", Via: c.s.Via, User: c.s.User, Labelling: !c.a.DryRun()}
	if c.env {
		st.Via = "env"
		st.Locked = "Cowboy signs in with FORGE_TOKEN from its environment, and DRY_RUN decides whether it labels. Change those where Cowboy is started."
	} else if os.Getenv("DRY_RUN") == "1" {
		st.Locked = "DRY_RUN=1 is set where Cowboy was started, so it writes nothing to GitHub. Start it without DRY_RUN to label."
	}
	st.Repos = c.a.Store.Repos()
	return st
}

func (c *connector) useGH() (string, error) {
	tok, err := ghLogin()
	if err != nil {
		return "", err
	}
	return c.use(tok, "gh")
}

// use switches to the token, checks GitHub accepts it, and saves it.
func (c *connector) use(tok, via string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.env {
		return "", fmt.Errorf("Cowboy signs in with FORGE_TOKEN from its environment; change it there")
	}
	prev := c.s.Token
	c.gh.SetToken(tok)
	user, err := c.gh.WhoAmI()
	if err != nil {
		c.gh.SetToken(prev)
		return "", err
	}
	c.s.Token, c.s.Via, c.s.User = tok, via, user
	if c.s.WebhookSecret == "" {
		c.s.WebhookSecret = randomSecret()
	}
	if hooks != nil {
		hooks.SetSecret(c.s.WebhookSecret)
	}
	return user, c.s.save()
}

func (c *connector) choose(repos []string, label bool) error {
	if label {
		if err := c.setLabelling(true, repos...); err != nil {
			return err
		}
	} else if err := c.setLabelling(false); err != nil {
		return err
	}
	c.mu.Lock()
	keep := map[string]bool{}
	for _, r := range repos {
		keep[strings.ToLower(r)] = true
	}
	var gone []string
	for _, r := range c.s.Repos {
		if !keep[strings.ToLower(r)] && !c.envRepos[strings.ToLower(r)] {
			gone = append(gone, r)
		}
	}
	c.s.Repos = repos
	err := c.s.save()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	for _, r := range gone {
		c.a.Store.RemoveRepo(r)
	}
	for _, r := range repos {
		c.a.Store.AddRepo(r)
	}
	log.Printf("setup: sorting %s", strings.Join(repos, ", "))
	go safely("setup sort", func() {
		for _, m := range c.a.Reconcile() {
			log.Print(m)
		}
		if outcomeUI != nil && outcomeUI.Backfill != nil {
			outcomeUI.Backfill(90)
		}
	})
	return nil
}

// setLabelling turns labelling on (after checking the login can write
// labels on every repo) or off. repos defaults to the ones being sorted.
func (c *connector) setLabelling(on bool, repos ...string) error {
	if c.env || os.Getenv("DRY_RUN") == "1" {
		if on != !c.a.DryRun() {
			return fmt.Errorf("%s", c.status().Locked)
		}
		return nil
	}
	if on {
		if len(repos) == 0 {
			repos = c.a.Store.Repos()
		}
		for _, r := range repos {
			if err := c.gh.CanLabel(r); err != nil {
				return fmt.Errorf("this login can't add labels on %s, so labelling stays off. Use a token that can write pull requests and issues on it (GitHub said: %v)", r, err)
			}
		}
	}
	c.a.SetDryRun(!on)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.s.Labelling = on
	return c.s.save()
}

func (c *connector) disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.env {
		return fmt.Errorf("Cowboy signs in with FORGE_TOKEN from its environment; remove it there")
	}
	c.s.Token, c.s.Via, c.s.User, c.s.Labelling = "", "", "", false
	c.gh.SetToken("")
	c.a.SetDryRun(true)
	return c.s.save()
}

// start is `cowboy` with no arguments: run on this machine only, keep the
// data in the user's config folder, and open the browser on the setup page
// (or the corral, once set up).
func start() {
	if os.Getenv("DATA") == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			dir, _ = os.UserHomeDir()
		}
		os.Setenv("DATA", filepath.Join(dir, "cowboy", "cowboy.json"))
	}
	if os.Getenv("ADDR") == "" {
		os.Setenv("ADDR", "127.0.0.1:8080")
	}
	addr := os.Getenv("ADDR")
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	set := loadSettings()
	_, appErr := os.Stat(filepath.Join(dataDir(), "github-app.json"))
	page := "/setup"
	if set.Token != "" || os.Getenv("FORGE_TOKEN") != "" || appErr == nil {
		page = "/"
	}
	u := "http://" + host + page
	fmt.Printf("\n  cowboy %s\n\n  open %s\n  data: %s\n  press Ctrl+C to stop\n\n", version, u, dataDir())
	go func() {
		for i := 0; i < 50; i++ {
			if resp, err := http.Get("http://" + host + "/healthz"); err == nil {
				resp.Body.Close()
				openBrowser(u)
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	serve()
}

func openBrowser(u string) {
	if os.Getenv("COWBOY_NO_BROWSER") != "" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("couldn't open a browser (%v): open %s yourself", err, u)
	}
}
