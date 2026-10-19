package forge

import (
	"bytes"
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
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AppCreds is what GitHub hands back when Cowboy creates its GitHub App
// (or what an admin copies from an app they made by hand).
type AppCreds struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	Owner         string `json:"owner"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
	ClientID      string `json:"client_id,omitempty"`
	ClientSecret  string `json:"client_secret,omitempty"`
}

// InstallURL is where a person picks the repos the app may use.
func (c AppCreds) InstallURL() string {
	if c.HTMLURL == "" {
		return ""
	}
	return strings.TrimRight(c.HTMLURL, "/") + "/installations/new"
}

// LoadAppCreds reads saved credentials. A missing file is (zero, false, nil).
func LoadAppCreds(path string) (AppCreds, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return AppCreds{}, false, nil
	}
	if err != nil {
		return AppCreds{}, false, err
	}
	var c AppCreds
	if err := json.Unmarshal(b, &c); err != nil {
		return AppCreds{}, false, fmt.Errorf("%s: %v", path, err)
	}
	if c.ID == 0 || c.PEM == "" {
		return AppCreds{}, false, fmt.Errorf("%s is missing the app id or private key", path)
	}
	return c, true, nil
}

// SaveAppCreds writes credentials readable by this user only.
func SaveAppCreds(path string, c AppCreds) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ConvertManifest finishes GitHub's app-manifest flow: it trades the
// one-time code from the redirect for the new app's credentials.
func ConvertManifest(apiBase, code string) (AppCreds, error) {
	if code == "" || strings.ContainsAny(code, "/?#") {
		return AppCreds{}, errors.New("bad code from GitHub")
	}
	req, _ := http.NewRequest("POST", strings.TrimRight(apiBase, "/")+"/app-manifests/"+code+"/conversions", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cowboy")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return AppCreds{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return AppCreds{}, fmt.Errorf("GitHub said %d: %s (the code is single-use and expires after an hour; start again)", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var r struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		Name          string `json:"name"`
		HTMLURL       string `json:"html_url"`
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return AppCreds{}, err
	}
	c := AppCreds{ID: r.ID, Slug: r.Slug, Name: r.Name, HTMLURL: r.HTMLURL, Owner: r.Owner.Login, PEM: r.PEM,
		WebhookSecret: r.WebhookSecret, ClientID: r.ClientID, ClientSecret: r.ClientSecret}
	if c.ID == 0 || c.PEM == "" {
		return AppCreds{}, errors.New("GitHub's answer had no app id or private key")
	}
	return c, nil
}

// AppAuth signs in as a GitHub App: a short JWT for app-level calls, and a
// one-hour token per installation for everything on a repo.
type AppAuth struct {
	Creds AppCreds
	base  string
	key   *rsa.PrivateKey
	http  *http.Client
	now   func() time.Time

	mu     sync.Mutex
	tokens map[int64]instToken // installation id → token
	inst   map[string]int64    // lower(owner/repo) → installation id
}

type instToken struct {
	token string
	exp   time.Time
}

// ErrNotInstalled means the app isn't installed on that repo.
var ErrNotInstalled = errors.New("the GitHub App isn't installed on this repo")

// NewAppAuth parses the private key. apiBase is the REST API address.
func NewAppAuth(apiBase string, c AppCreds) (*AppAuth, error) {
	block, _ := pem.Decode([]byte(c.PEM))
	if block == nil {
		return nil, errors.New("the app's private key isn't PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k8, err8 := x509.ParsePKCS8PrivateKey(block.Bytes); err8 == nil {
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("the app's private key isn't an RSA key")
		}
		key = rk
	} else {
		return nil, fmt.Errorf("can't read the app's private key: %v", err)
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &AppAuth{Creds: c, base: strings.TrimRight(apiBase, "/"), key: key,
		http: &http.Client{Timeout: 30 * time.Second}, now: time.Now,
		tokens: map[int64]instToken{}, inst: map[string]int64{}}, nil
}

// JWT is the app's own short-lived sign-in (RS256, 9 minutes).
func (a *AppAuth) JWT() (string, error) {
	now := a.now()
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(), // allow for clock drift
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(a.Creds.ID, 10),
	})
	signing := head + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (a *AppAuth) call(method, path, bearer string, out any) error {
	req, _ := http.NewRequest(method, a.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cowboy")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == 404 {
		return errNotFound
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// installation finds (and remembers) which installation covers a repo.
func (a *AppAuth) installation(repo string) (int64, error) {
	k := strings.ToLower(repo)
	a.mu.Lock()
	id, ok := a.inst[k]
	a.mu.Unlock()
	if ok {
		return id, nil
	}
	jwt, err := a.JWT()
	if err != nil {
		return 0, err
	}
	var r struct {
		ID int64 `json:"id"`
	}
	if err := a.call("GET", "/repos/"+repo+"/installation", jwt, &r); err != nil {
		if IsNotFound(err) {
			return 0, ErrNotInstalled
		}
		return 0, err
	}
	a.mu.Lock()
	a.inst[k] = r.ID
	a.mu.Unlock()
	return r.ID, nil
}

// Token returns a valid installation token, refreshing it 5 minutes early.
func (a *AppAuth) Token(installation int64) (string, error) {
	a.mu.Lock()
	t, ok := a.tokens[installation]
	a.mu.Unlock()
	if ok && a.now().Before(t.exp.Add(-5*time.Minute)) {
		return t.token, nil
	}
	jwt, err := a.JWT()
	if err != nil {
		return "", err
	}
	var r struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := a.call("POST", fmt.Sprintf("/app/installations/%d/access_tokens", installation), jwt, &r); err != nil {
		if IsNotFound(err) {
			a.forgetInstallation(installation)
			return "", ErrNotInstalled
		}
		return "", err
	}
	a.mu.Lock()
	a.tokens[installation] = instToken{r.Token, r.ExpiresAt}
	a.mu.Unlock()
	return r.Token, nil
}

// header is the Authorization header for a REST path. Repo paths use that
// repo's installation token; anything else (like /users/x) uses any
// installation, since a token is needed for the higher rate limit.
func (a *AppAuth) header(path string) (string, error) {
	repo := repoOf(path)
	var id int64
	var err error
	if repo != "" {
		id, err = a.installation(repo)
	} else {
		id, err = a.anyInstallation()
	}
	if err != nil {
		return "", err
	}
	tok, err := a.Token(id)
	if err != nil {
		return "", err
	}
	return "Bearer " + tok, nil
}

func (a *AppAuth) anyInstallation() (int64, error) {
	a.mu.Lock()
	for _, id := range a.inst {
		a.mu.Unlock()
		return id, nil
	}
	a.mu.Unlock()
	ids, err := a.Installations()
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, ErrNotInstalled
	}
	return ids[0], nil
}

// drop forgets a cached token after a 401, so the next call gets a new one.
func (a *AppAuth) drop(path string) {
	repo := repoOf(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, ok := a.inst[strings.ToLower(repo)]; ok {
		delete(a.tokens, id)
		return
	}
	a.tokens = map[int64]instToken{}
}

// Forget drops what Cowboy knows about a repo's installation (it was
// uninstalled or removed from the app).
func (a *AppAuth) Forget(repo string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.inst, strings.ToLower(repo))
}

func (a *AppAuth) forgetInstallation(id int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.tokens, id)
	for k, v := range a.inst {
		if v == id {
			delete(a.inst, k)
		}
	}
}

// Installations lists where the app is installed.
func (a *AppAuth) Installations() ([]int64, error) {
	jwt, err := a.JWT()
	if err != nil {
		return nil, err
	}
	var ids []int64
	for page := 1; page <= 10; page++ {
		var r []struct {
			ID int64 `json:"id"`
		}
		if err := a.call("GET", fmt.Sprintf("/app/installations?per_page=100&page=%d", page), jwt, &r); err != nil {
			return nil, err
		}
		for _, x := range r {
			ids = append(ids, x.ID)
		}
		if len(r) < 100 {
			break
		}
	}
	return ids, nil
}

// Repos lists every repo the app can use, across its installations.
func (a *AppAuth) Repos() ([]string, error) {
	ids, err := a.Installations()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		tok, err := a.Token(id)
		if err != nil {
			return nil, err
		}
		for page := 1; page <= 50; page++ {
			var r struct {
				Repositories []struct {
					FullName string `json:"full_name"`
					Archived bool   `json:"archived"`
				} `json:"repositories"`
			}
			if err := a.call("GET", fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), tok, &r); err != nil {
				return nil, err
			}
			for _, x := range r.Repositories {
				a.mu.Lock()
				a.inst[strings.ToLower(x.FullName)] = id
				a.mu.Unlock()
				if !x.Archived {
					out = append(out, x.FullName)
				}
			}
			if len(r.Repositories) < 100 {
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// repoOf pulls owner/repo out of a /repos/owner/repo/... path.
func repoOf(path string) string {
	rest, ok := strings.CutPrefix(path, "/repos/")
	if !ok {
		return ""
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	name, _, _ := strings.Cut(parts[1], "?")
	return parts[0] + "/" + name
}

// Manifest is the GitHub App Cowboy asks GitHub to create.
// publicURL is where this Cowboy can be reached; hooks says whether GitHub
// can deliver webhooks there (it can't reach localhost).
func Manifest(name, publicURL string, hooks bool) []byte {
	base := strings.TrimRight(publicURL, "/")
	m := map[string]any{
		"name":            name,
		"url":             "https://github.com/sgracia13/Cowboy",
		"description":     "Sorts pull requests into easy, normal and careful, and tags the ones AI agents opened. Self-hosted.",
		"redirect_url":    base + "/setup/github/callback",
		"setup_url":       base + "/setup/github/installed",
		"setup_on_update": true,
		"public":          false,
		"hook_attributes": map[string]any{"url": base + "/webhook/github", "active": hooks},
		"default_permissions": map[string]string{
			"pull_requests": "write", // read prs, diffs, reviews; ask code owners for a review
			"issues":        "write", // labels and the one comment
			"contents":      "read",  // cowboy.json and CODEOWNERS
			"checks":        "read",  // are checks passing
			"metadata":      "read",
		},
		"default_events": []string{"pull_request", "check_suite", "check_run"},
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(m)
	return bytes.TrimSpace(b.Bytes())
}
