package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/store"
)

type fakeConnect struct {
	st       ConnectStatus
	chosen   []string
	label    bool
	canWrite bool
}

func (f *fakeConnect) web() *Connect {
	return &Connect{
		ReadLink: "https://github.com/settings/personal-access-tokens/new?contents=read", WriteLink: "w",
		GHLogin: func() bool { return true },
		Status:  func() ConnectStatus { return f.st },
		UseGH:   func() (string, error) { f.st.Connected, f.st.Via, f.st.User = true, "gh", "me"; return "me", nil },
		UseToken: func(t string) (string, error) {
			if t == "bad" {
				return "", errors.New("401 Bad credentials")
			}
			f.st.Connected, f.st.Via, f.st.User = true, "token", "me"
			return "me", nil
		},
		Repos: func() ([]string, error) { return []string{"me/a", "me/b"}, nil },
		Choose: func(r []string, label bool) error {
			f.chosen, f.label, f.st.Repos = r, label, r
			return nil
		},
		SetLabelling: func(on bool) error {
			if on && !f.canWrite {
				return errors.New("this login can't add labels")
			}
			f.st.Labelling = on
			return nil
		},
		Disconnect: func() error { f.st = ConnectStatus{}; return nil },
	}
}

func connectServer(t *testing.T, f *fakeConnect) http.Handler {
	td := "../../testdata"
	fk := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	s := New(app.New(fk, st, nil))
	s.Connect = f.web()
	mux := http.NewServeMux()
	s.Routes(mux)
	return Auth{Open: true}.Wrap(mux)
}

func call(h http.Handler, method, path string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://example.com")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestConnectFlow(t *testing.T) {
	f := &fakeConnect{}
	h := connectServer(t, f)
	// not connected: the corral says so, the setup page offers gh and a token
	if b := call(h, "GET", "/", nil).Body.String(); !strings.Contains(b, "isn't connected to GitHub yet") {
		t.Error("corral doesn't point to setup")
	}
	b := call(h, "GET", "/setup", nil).Body.String()
	for _, want := range []string{"use my gh login", "open github's token page", "personal-access-tokens/new"} {
		if !strings.Contains(b, want) {
			t.Errorf("setup page lacks %q", want)
		}
	}
	// a bad token goes back with GitHub's reason
	if w := call(h, "POST", "/setup/token", url.Values{"token": {"bad"}}); !strings.Contains(w.Header().Get("Location"), "Bad+credentials") {
		t.Errorf("bad token: %d %s", w.Code, w.Header().Get("Location"))
	}
	// gh login, then the repo picker
	if w := call(h, "POST", "/setup/gh", url.Values{}); w.Header().Get("Location") != "/setup/repos" {
		t.Fatalf("gh login: %d %s", w.Code, w.Header().Get("Location"))
	}
	b = call(h, "GET", "/setup/repos", nil).Body.String()
	if !strings.Contains(b, `value="me/a"`) || !strings.Contains(b, "just look") {
		t.Error("repo picker missing repos or modes")
	}
	// nothing picked is refused; a malformed extra repo is refused
	if w := call(h, "POST", "/setup/repos", url.Values{"mode": {"look"}}); !strings.Contains(w.Header().Get("Location"), "error=") {
		t.Error("empty choice accepted")
	}
	if w := call(h, "POST", "/setup/repos", url.Values{"other": {"not a repo"}}); !strings.Contains(w.Header().Get("Location"), "error=") {
		t.Error("malformed repo accepted")
	}
	w := call(h, "POST", "/setup/repos", url.Values{"repo": {"me/b"}, "other": {"https://github.com/me/c"}, "mode": {"look"}})
	if w.Header().Get("Location") != "/" || strings.Join(f.chosen, ",") != "me/b,me/c" || f.label {
		t.Errorf("choose: %s %v label=%v", w.Header().Get("Location"), f.chosen, f.label)
	}
	// labelling is refused without write access, then allowed
	if w := call(h, "POST", "/setup/mode", url.Values{"label": {"on"}}); !strings.Contains(w.Header().Get("Location"), "error=") || f.st.Labelling {
		t.Error("labelling turned on without write access")
	}
	f.canWrite = true
	if call(h, "POST", "/setup/mode", url.Values{"label": {"on"}}); !f.st.Labelling {
		t.Error("labelling didn't turn on")
	}
	if b := call(h, "GET", "/setup", nil).Body.String(); !strings.Contains(b, "labelling is on") || !strings.Contains(b, "me/b") {
		t.Error("connected page doesn't show the mode and repos")
	}
	// posts from another site are refused
	r := httptest.NewRequest("POST", "/setup/token", strings.NewReader("token=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site token post: %d", rec.Code)
	}
}
