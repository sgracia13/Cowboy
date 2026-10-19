package forge

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	return k, string(p)
}

// checkJWT verifies an app JWT the way GitHub does.
func checkJWT(t *testing.T, pub *rsa.PublicKey, tok string, appID int64) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iat, Exp int64
		Iss      string
	}
	json.Unmarshal(raw, &c)
	now := time.Now().Unix()
	return c.Iss == strconv.FormatInt(appID, 10) && c.Iat <= now && c.Exp > now && c.Exp-c.Iat <= 600
}

// fakeGitHub is the slice of the GitHub API the app flow uses.
type fakeGitHub struct {
	t        *testing.T
	pub      *rsa.PublicKey
	mu       sync.Mutex
	issued   int    // installation tokens handed out
	revoke   string // a token that now answers 401
	auths    []string
	srv      *httptest.Server
	notThere bool // the app isn't installed on acme/web
}

func newFakeGitHub(t *testing.T, pub *rsa.PublicKey) *fakeGitHub {
	f := &fakeGitHub{t: t, pub: pub}
	mux := http.NewServeMux()
	jwtOK := func(r *http.Request) bool {
		return checkJWT(t, f.pub, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), 42)
	}
	tokOK := func(r *http.Request) bool {
		h := r.Header.Get("Authorization")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auths = append(f.auths, h)
		return strings.HasPrefix(h, "Bearer ghs_") && h != "Bearer "+f.revoke
	}
	mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") != "good-code" {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		fmt.Fprintf(w, `{"id":42,"slug":"cowboy-ab12","name":"cowboy-ab12","html_url":"https://github.com/apps/cowboy-ab12","owner":{"login":"acme"},"pem":%q,"webhook_secret":"whsec","client_id":"Iv1.x","client_secret":"cs"}`, "PEM")
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", func(w http.ResponseWriter, r *http.Request) {
		if !jwtOK(r) {
			http.Error(w, "bad jwt", 401)
			return
		}
		if f.notThere && r.PathValue("repo") == "web" {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		io := `{"id":7}`
		if r.PathValue("owner") == "other" {
			io = `{"id":8}`
		}
		w.Write([]byte(io))
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if !jwtOK(r) {
			http.Error(w, "bad jwt", 401)
			return
		}
		f.mu.Lock()
		f.issued++
		n := f.issued
		f.mu.Unlock()
		fmt.Fprintf(w, `{"token":"ghs_%s_%d","expires_at":%q}`, r.PathValue("id"), n, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	})
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, r *http.Request) {
		if !jwtOK(r) {
			http.Error(w, "bad jwt", 401)
			return
		}
		w.Write([]byte(`[{"id":7},{"id":8}]`))
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		if !tokOK(r) {
			http.Error(w, "bad token", 401)
			return
		}
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_7_") {
			w.Write([]byte(`{"repositories":[{"full_name":"acme/web"},{"full_name":"acme/old","archived":true}]}`))
			return
		}
		w.Write([]byte(`{"repositories":[{"full_name":"other/api"}]}`))
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{n}", func(w http.ResponseWriter, r *http.Request) {
		if !tokOK(r) {
			http.Error(w, `{"message":"Bad credentials"}`, 401)
			return
		}
		w.Write([]byte(`{"title":"t","state":"open","user":{"login":"sam"},"head":{"sha":"abc"}}`))
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{n}/commits", func(w http.ResponseWriter, r *http.Request) {
		tokOK(r)
		w.Write([]byte(`[]`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestAppJWT(t *testing.T) {
	k, p := testKey(t)
	a, err := NewAppAuth("", AppCreds{ID: 42, PEM: p})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.JWT()
	if err != nil {
		t.Fatal(err)
	}
	if !checkJWT(t, &k.PublicKey, tok, 42) {
		t.Fatalf("GitHub would reject this JWT: %s", tok)
	}
	// PKCS#8 keys (what some tools export) work too.
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	p8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := NewAppAuth("", AppCreds{ID: 42, PEM: string(p8)}); err != nil {
		t.Errorf("pkcs8 key: %v", err)
	}
	if _, err := NewAppAuth("", AppCreds{ID: 42, PEM: "nope"}); err == nil {
		t.Error("a broken key should fail")
	}
}

func TestConvertManifest(t *testing.T) {
	k, _ := testKey(t)
	f := newFakeGitHub(t, &k.PublicKey)
	c, err := ConvertManifest(f.srv.URL, "good-code")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != 42 || c.WebhookSecret != "whsec" || c.Owner != "acme" || c.InstallURL() != "https://github.com/apps/cowboy-ab12/installations/new" {
		t.Errorf("creds %+v", c)
	}
	if _, err := ConvertManifest(f.srv.URL, "used-code"); err == nil || !strings.Contains(err.Error(), "single-use") {
		t.Errorf("a used code should explain itself, got %v", err)
	}
	if _, err := ConvertManifest(f.srv.URL, "../x"); err == nil {
		t.Error("a code with a slash should be refused")
	}
}

func TestAppSignsInPerRepo(t *testing.T) {
	k, p := testKey(t)
	f := newFakeGitHub(t, &k.PublicKey)
	fg, _ := New("github", f.srv.URL, "")
	gh := fg.(*GitHub)
	a, _ := NewAppAuth(f.srv.URL, AppCreds{ID: 42, PEM: p})
	gh.UseApp(a)

	for i := 0; i < 3; i++ {
		if _, _, err := gh.GetPR("acme/web", 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := gh.GetPR("other/api", 2); err != nil {
		t.Fatal(err)
	}
	if f.issued != 2 {
		t.Errorf("issued %d installation tokens, want 2 (one per installation, then cached)", f.issued)
	}
	if !strings.HasPrefix(f.auths[0], "Bearer ghs_7_") || !strings.HasPrefix(f.auths[len(f.auths)-1], "Bearer ghs_8_") {
		t.Errorf("each repo should use its own installation's token: %v", f.auths)
	}

	// A token near expiry is replaced.
	a.mu.Lock()
	old := a.tokens[7]
	a.tokens[7] = instToken{old.token, time.Now().Add(4 * time.Minute)}
	a.mu.Unlock()
	if _, _, err := gh.GetPR("acme/web", 1); err != nil {
		t.Fatal(err)
	}
	if f.issued != 3 {
		t.Errorf("issued %d, want a fresh token 5 minutes before expiry", f.issued)
	}

	// A revoked token gets one retry with a new one.
	f.mu.Lock()
	f.revoke = "ghs_7_3"
	f.mu.Unlock()
	if _, _, err := gh.GetPR("acme/web", 1); err != nil {
		t.Fatalf("after a 401 it should fetch a new token and retry: %v", err)
	}
	if f.issued != 4 {
		t.Errorf("issued %d, want 4", f.issued)
	}
}

func TestAppNotInstalled(t *testing.T) {
	k, p := testKey(t)
	f := newFakeGitHub(t, &k.PublicKey)
	f.notThere = true
	fg, _ := New("github", f.srv.URL, "")
	a, _ := NewAppAuth(f.srv.URL, AppCreds{ID: 42, PEM: p})
	fg.(*GitHub).UseApp(a)
	if _, _, err := fg.GetPR("acme/web", 1); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("err = %v, want ErrNotInstalled", err)
	}
}

func TestAppRepos(t *testing.T) {
	k, p := testKey(t)
	f := newFakeGitHub(t, &k.PublicKey)
	a, _ := NewAppAuth(f.srv.URL, AppCreds{ID: 42, PEM: p})
	repos, err := a.Repos()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(repos, ",") != "acme/web,other/api" {
		t.Errorf("repos %v (archived ones are skipped)", repos)
	}
	// Repos also teaches it which installation owns each repo: no lookups needed.
	before := f.issued
	if _, err := a.header("/repos/acme/web/pulls/1"); err != nil || f.issued != before {
		t.Errorf("header after Repos: err=%v, new tokens=%d", err, f.issued-before)
	}
}

func TestCredsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "github-app.json")
	if _, ok, err := LoadAppCreds(path); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	_, p := testKey(t)
	if err := SaveAppCreds(path, AppCreds{ID: 42, PEM: p, Name: "cowboy-ab12"}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := LoadAppCreds(path)
	if !ok || err != nil || c.Name != "cowboy-ab12" {
		t.Fatalf("load: %+v ok=%v err=%v", c, ok, err)
	}
	if st, _ := osStat(path); st&0o077 != 0 {
		t.Errorf("credentials file mode %o, want owner-only", st)
	}
}

func TestRepoOf(t *testing.T) {
	for path, want := range map[string]string{
		"/repos/acme/web/pulls/1":               "acme/web",
		"/repos/acme/web":                       "acme/web",
		"/repos/acme/web?x=1":                   "acme/web",
		"/repos/acme/web/contents/a/b?ref=main": "acme/web",
		"/users/sam":                            "",
	} {
		if got := repoOf(path); got != want {
			t.Errorf("repoOf(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestManifest(t *testing.T) {
	var m struct {
		Name, RedirectURL, SetupURL string
		Hook                        struct {
			URL    string
			Active bool
		} `json:"hook_attributes"`
		Perms  map[string]string `json:"default_permissions"`
		Events []string          `json:"default_events"`
		Public bool
	}
	b := Manifest("cowboy-ab12", "https://cowboy.acme.com/", true)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(b, &raw)
	if raw["redirect_url"] != "https://cowboy.acme.com/setup/github/callback" || raw["setup_url"] != "https://cowboy.acme.com/setup/github/installed" {
		t.Errorf("urls: %v %v", raw["redirect_url"], raw["setup_url"])
	}
	if m.Hook.URL != "https://cowboy.acme.com/webhook/github" || !m.Hook.Active || m.Public {
		t.Errorf("hook %+v public=%v", m.Hook, m.Public)
	}
	if m.Perms["pull_requests"] != "write" || m.Perms["contents"] != "read" || m.Perms["issues"] != "write" || len(m.Perms) != 5 {
		t.Errorf("permissions %v", m.Perms)
	}
	json.Unmarshal(Manifest("x", "http://localhost:8080", false), &m)
	if m.Hook.Active {
		t.Error("webhooks should be off when GitHub can't reach Cowboy")
	}
}

func osStat(path string) (uint32, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return uint32(st.Mode().Perm()), nil
}
