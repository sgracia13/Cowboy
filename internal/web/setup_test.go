package web

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/store"
)

type setupRig struct {
	h         http.Handler
	connected atomic.Bool
	finished  atomic.Int32
	synced    atomic.Int32
}

func newSetupRig(t *testing.T, auth Auth) *setupRig {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	rig := &setupRig{}
	s := New(app.New(f, st, nil))
	s.Setup = &GitHubSetup{
		WebBase: "https://github.com",
		Status: func() SetupStatus {
			if !rig.connected.Load() {
				return SetupStatus{}
			}
			return SetupStatus{Connected: true, AppName: "cowboy-ab12", AppURL: "https://github.com/apps/cowboy-ab12", InstallURL: "https://github.com/apps/cowboy-ab12/installations/new", Repos: []string{"acme/web"}}
		},
		Finish: func(code string) (forge.AppCreds, error) {
			if code != "good" {
				return forge.AppCreds{}, errors.New("bad code")
			}
			rig.finished.Add(1)
			rig.connected.Store(true)
			return forge.AppCreds{Name: "cowboy-ab12", HTMLURL: "https://github.com/apps/cowboy-ab12"}, nil
		},
		Sync: func() (int, error) { rig.synced.Add(1); return 1, nil },
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	rig.h = auth.Wrap(mux)
	return rig
}

var stateRe = regexp.MustCompile(`state=([0-9a-f]+)`)

func TestSetupFlow(t *testing.T) {
	rig := newSetupRig(t, Auth{Open: true})
	same := map[string]string{"Origin": "http://example.com"}

	// Not connected: every page says so.
	if w := do(rig.h, "GET", "/", nil, nil); !strings.Contains(w.Body.String(), "isn't connected to GitHub yet") {
		t.Fatal("the corral should show the connect banner")
	}
	w := do(rig.h, "GET", "/setup", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "create the github app") {
		t.Fatalf("setup page: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "webhooks off") {
		t.Error("on an http address GitHub can't reach, the page should say webhooks are off")
	}

	// Start: a form that posts the manifest to GitHub.
	w = do(rig.h, "POST", "/setup/github/start", url.Values{"name": {"cowboy-ab12"}, "org": {"acme"}}, same)
	body := html.UnescapeString(w.Body.String())
	if !strings.Contains(body, `action="https://github.com/organizations/acme/settings/apps/new?state=`) {
		t.Fatalf("form action wrong:\n%s", body)
	}
	if !strings.Contains(body, `"redirect_url":"http://example.com/setup/github/callback"`) || !strings.Contains(body, `"active":false`) {
		t.Errorf("manifest wrong:\n%s", body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://github.com") {
		t.Errorf("csp %q", csp)
	}
	state := stateRe.FindStringSubmatch(body)[1]

	// A forged or reused state is refused.
	if w := do(rig.h, "GET", "/setup/github/callback?code=good&state=deadbeef", nil, nil); w.Code != 400 {
		t.Fatalf("forged state: %d", w.Code)
	}
	// The real one finishes and sends the browser to pick repos.
	w = do(rig.h, "GET", "/setup/github/callback?code=good&state="+state, nil, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://github.com/apps/cowboy-ab12/installations/new" {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do(rig.h, "GET", "/setup/github/callback?code=good&state="+state, nil, nil); w.Code != 400 || rig.finished.Load() != 1 {
		t.Fatalf("a state must work once: %d, finished %d", w.Code, rig.finished.Load())
	}

	// Back from picking repos: a sync starts.
	if w := do(rig.h, "GET", "/setup/github/installed?installation_id=7&setup_action=install", nil, nil); w.Code != 200 {
		t.Fatalf("installed: %d", w.Code)
	}
	waitFor(t, func() bool { return rig.synced.Load() == 1 })
	do(rig.h, "GET", "/setup/github/installed", nil, nil)
	time.Sleep(50 * time.Millisecond)
	if rig.synced.Load() != 1 {
		t.Error("repeat visits within 10 seconds shouldn't start another sync")
	}

	// Connected: no banner, and /setup shows the app and its repos.
	if w := do(rig.h, "GET", "/", nil, nil); strings.Contains(w.Body.String(), "isn't connected") {
		t.Error("banner still showing after connecting")
	}
	if w := do(rig.h, "GET", "/setup", nil, nil); !strings.Contains(w.Body.String(), "acme/web") || !strings.Contains(w.Body.String(), "add or remove repos") {
		t.Error("setup page should show the connected app")
	}
	if w := do(rig.h, "POST", "/setup/github/start", url.Values{"name": {"x"}}, same); w.Code != http.StatusSeeOther {
		t.Errorf("starting again once connected should just go back to /setup, got %d", w.Code)
	}
}

func TestSetupInputChecks(t *testing.T) {
	rig := newSetupRig(t, Auth{Open: true})
	same := map[string]string{"Origin": "http://example.com"}
	for _, f := range []url.Values{
		{"name": {"<script>"}},
		{"name": {"ok"}, "org": {"acme/../evil"}},
	} {
		if w := do(rig.h, "POST", "/setup/github/start", f, same); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "error=") {
			t.Errorf("%v: %d %s", f, w.Code, w.Header().Get("Location"))
		}
	}
	if w := do(rig.h, "POST", "/setup/github/start", url.Values{"name": {"x"}}, map[string]string{"Origin": "https://evil.example"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-site start: %d", w.Code)
	}
}

func TestSetupNeedsLogin(t *testing.T) {
	rig := newSetupRig(t, Auth{Password: "hunter2"})
	if w := do(rig.h, "GET", "/setup", nil, nil); w.Code != http.StatusSeeOther {
		t.Errorf("/setup signed out: %d, want a redirect to login", w.Code)
	}
	// GitHub's redirect back arrives without the strict cookie; the state guards it.
	if w := do(rig.h, "GET", "/setup/github/callback?code=good&state=nope", nil, nil); w.Code != 400 {
		t.Errorf("callback signed out: %d, want 400 (state check), not a login redirect", w.Code)
	}
	if rig.finished.Load() != 0 {
		t.Error("finished without a valid state")
	}
}

func TestPublicBase(t *testing.T) {
	g := &GitHubSetup{}
	for host, want := range map[string]bool{"cowboy.acme.com": true, "localhost:8080": false, "192.168.1.5": false, "10.0.0.2:8080": false, "cowboy.local": false} {
		r := httptestReq(host, "https")
		if _, hooks := g.publicBase(r); hooks != want {
			t.Errorf("%s: hooks=%v want %v", host, hooks, want)
		}
	}
	if _, hooks := g.publicBase(httptestReq("cowboy.acme.com", "http")); hooks {
		t.Error("plain http shouldn't get webhooks")
	}
	g.PublicURL = "https://cowboy.acme.com/"
	if base, hooks := g.publicBase(httptestReq("127.0.0.1:8080", "")); base != "https://cowboy.acme.com" || !hooks {
		t.Errorf("PUBLIC_URL should win: %s %v", base, hooks)
	}
}

func httptestReq(host, proto string) *http.Request {
	r, _ := http.NewRequest("GET", "http://"+host+"/setup", nil)
	r.Host = host
	if proto != "" {
		r.Header.Set("X-Forwarded-Proto", proto)
	}
	return r
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
