// Package forge talks to git hosts. Every host looks the same to the rest of
// Cowboy through the Forge interface. Two implementations: GitHub, and Gitea
// (which also covers Forgejo, since Forgejo keeps Gitea's API).
package forge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/detect"
)

// Activity is what Cowboy needs to spot a stray.
type Activity struct {
	LastChangesRequested time.Time
	LastCommit           time.Time
}

// OpenPR is one row of an open-PR listing.
type OpenPR struct {
	Number  int
	HeadSHA string
}

// Forge is the small set of calls Cowboy makes.
type Forge interface {
	Name() string
	GetPR(repo string, n int) (analyze.PRMeta, string, error) // meta, state (open/closed/merged)
	GetDiff(repo string, n int) (string, error)
	GetFile(repo, path, ref string) (string, error) // "" if missing; ref "" = default branch
	ListOpenPRs(repo string) ([]OpenPR, error)      // with head SHAs, so a sweep can skip unchanged PRs
	ChecksOK(repo, sha string) (*bool, error)
	Activity(repo string, n int) (Activity, error)
	SetLabels(repo string, n int, add, remove []string) error
	// UpsertComment edits the comment holding marker, or posts a new one if
	// create is true (with create false and no comment, it does nothing).
	UpsertComment(repo string, n int, marker, body string, create bool) error
	RequestReview(repo string, n int, users []string) error
	// Snapshot gathers what the undisclosed-agent check needs: branch,
	// commit times, reviews and account facts. Fields the host doesn't
	// expose are left zero and marked unknown downstream.
	Snapshot(repo string, n int) (detect.PRSnapshot, error)
}

type client struct {
	base   string
	token  string
	scheme string // "Bearer" or "token"
	http   *http.Client
}

func (c *client) do(method, path string, body any, accept string, out any) ([]byte, error) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	newReq := func() (*http.Request, error) {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequest(method, c.base+path, rd)
		if err != nil {
			return nil, err
		}
		if c.token != "" {
			req.Header.Set("Authorization", c.scheme+" "+c.token)
		}
		if accept == "" {
			accept = "application/json"
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", "cowboy")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	}
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err = c.http.Do(req)
		if err != nil {
			if attempt >= maxRetries {
				return nil, err
			}
			sleep(backoff(attempt))
			continue
		}
		wait, again := retryAfter(resp, attempt)
		if !again || attempt >= maxRetries {
			break
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		sleep(wait)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		return nil, errNotFound
	}
	if resp.StatusCode >= 300 {
		return data, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return data, err
		}
	}
	return data, nil
}

var errNotFound = fmt.Errorf("not found")

const maxRetries = 3

// sleep is swapped out in tests.
var sleep = time.Sleep

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * 500 * time.Millisecond }

// retryAfter decides whether a response is worth retrying and how long to
// wait first. It covers server errors, 429s and GitHub's rate limits (a 403
// with Retry-After, or with no requests left until X-RateLimit-Reset).
// Waits are capped at a minute; past that Cowboy gives up on this call and
// the catch-up sweep tries again later.
func retryAfter(resp *http.Response, attempt int) (time.Duration, bool) {
	limited := resp.StatusCode == 429 ||
		(resp.StatusCode == 403 && (resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"))
	if !limited && resp.StatusCode < 500 {
		return 0, false
	}
	wait := backoff(attempt)
	if v, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && v >= 0 {
		wait = time.Duration(v) * time.Second
	} else if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			wait = time.Until(time.Unix(reset, 0)) + time.Second
		}
	}
	if wait > time.Minute {
		return 0, false
	}
	if wait < 0 {
		wait = 0
	}
	return wait, true
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool { return err == errNotFound }

func esc(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// refQ builds "?ref=x", or nothing for the default branch.
func refQ(ref string) string {
	if ref == "" {
		return ""
	}
	return "?ref=" + url.QueryEscape(ref)
}

func cleanUsers(users []string) []string {
	var out []string
	for _, u := range users {
		u = strings.TrimPrefix(u, "@")
		if u == "" || strings.Contains(u, "/") { // skip teams in the prototype
			continue
		}
		out = append(out, u)
	}
	return out
}

// New builds a Forge from settings. kind: github, gitea, forgejo.
func New(kind, baseURL, token string) (Forge, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	switch strings.ToLower(kind) {
	case "github":
		if baseURL == "" {
			baseURL = "https://api.github.com"
		}
		return &GitHub{client{strings.TrimRight(baseURL, "/"), token, "Bearer", hc}}, nil
	case "gitea", "forgejo":
		if baseURL == "" {
			return nil, fmt.Errorf("FORGE_URL is required for %s", kind)
		}
		return &Gitea{client{strings.TrimRight(baseURL, "/") + "/api/v1", token, "token", hc}, kind}, nil
	}
	return nil, fmt.Errorf("unknown forge %q (use github, gitea or forgejo)", kind)
}
