// Package webhook receives pull request events from GitHub, GitLab and
// Gitea/Forgejo, checks the signature or token, and queues the PR for
// sorting.
// It answers fast (202) and does the work in the background.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// Job is one PR to (re)sort, or close.
type Job struct {
	Repo   string
	Number int
	Closed bool
	Merged bool
	Repen  string // a human put a cowboy:<pen> label on the PR
	By     string
	Unflag bool // a human removed the agent:likely label

	// The GitHub App was installed on, or removed from, these repos.
	Track, Untrack []string
}

// Handler turns webhook deliveries into Jobs.
type Handler struct {
	Secret string
	Queue  chan<- Job
	// Self is Cowboy's own account on hosts that don't mark bots in
	// webhooks (GitLab), so its own label changes aren't read as people's.
	Self string

	live atomic.Pointer[string] // SetSecret: replaces Secret once set

	mu    sync.Mutex
	seen  map[string]bool
	order []string
}

// SetSecret changes the webhook secret while running (GitHub App setup
// finishing after start).
func (h *Handler) SetSecret(s string) { h.live.Store(&s) }

func (h *Handler) secret() string {
	if s := h.live.Load(); s != nil {
		return *s
	}
	return h.Secret
}

// duplicate reports whether this delivery ID was already handled. Hosts
// redeliver on timeouts and people press "redeliver"; doing the work twice
// is harmless but noisy, so recent IDs are remembered.
func (h *Handler) duplicate(id string) bool {
	if id == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.seen == nil {
		h.seen = map[string]bool{}
	}
	if h.seen[id] {
		return true
	}
	h.seen[id] = true
	h.order = append(h.order, id)
	if len(h.order) > 2000 {
		delete(h.seen, h.order[0])
		h.order = h.order[1:]
	}
	return false
}

// Verify checks an HMAC-SHA256 signature (hex, optional "sha256=" prefix).
func Verify(secret string, body []byte, sig string) bool {
	if secret == "" {
		return false
	}
	sig = strings.TrimPrefix(sig, "sha256=")
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hmac.Equal(m.Sum(nil), want)
}

// Sign returns the GitHub-style signature header value for body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

type prEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number int  `json:"number"`
		Merged bool `json:"merged"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Label struct {
		Name string `json:"name"`
	} `json:"label"`
	Sender struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"sender"`
	// installation / installation_repositories (GitHub App)
	Repositories []struct {
		FullName string `json:"full_name"`
	} `json:"repositories"`
	RepositoriesAdded []struct {
		FullName string `json:"full_name"`
	} `json:"repositories_added"`
	RepositoriesRemoved []struct {
		FullName string `json:"full_name"`
	} `json:"repositories_removed"`
	// check_suite / check_run (GitHub)
	CheckSuite struct {
		PullRequests []struct{ Number int } `json:"pull_requests"`
	} `json:"check_suite"`
	CheckRun struct {
		PullRequests []struct{ Number int } `json:"pull_requests"`
	} `json:"check_run"`
}

func read(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// GitHub handles /webhook/github.
func (h *Handler) GitHub(w http.ResponseWriter, r *http.Request) {
	body, ok := read(w, r)
	if !ok {
		return
	}
	if !Verify(h.secret(), body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if h.duplicate(r.Header.Get("X-GitHub-Delivery")) {
		w.Write([]byte("duplicate delivery, ignored\n"))
		return
	}
	h.dispatch(w, r.Header.Get("X-GitHub-Event"), body)
}

// Gitea handles /webhook/gitea (Gitea and Forgejo).
func (h *Handler) Gitea(w http.ResponseWriter, r *http.Request) {
	body, ok := read(w, r)
	if !ok {
		return
	}
	sig := r.Header.Get("X-Forgejo-Signature")
	if sig == "" {
		sig = r.Header.Get("X-Gitea-Signature")
	}
	if !Verify(h.secret(), body, sig) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	id := r.Header.Get("X-Forgejo-Delivery")
	if id == "" {
		id = r.Header.Get("X-Gitea-Delivery")
	}
	if h.duplicate(id) {
		w.Write([]byte("duplicate delivery, ignored\n"))
		return
	}
	ev := r.Header.Get("X-Forgejo-Event")
	if ev == "" {
		ev = r.Header.Get("X-Gitea-Event")
	}
	h.dispatch(w, ev, body)
}

func (h *Handler) dispatch(w http.ResponseWriter, event string, body []byte) {
	var e prEvent
	if err := json.Unmarshal(body, &e); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	repo := e.Repository.FullName
	n := e.Number
	if n == 0 {
		n = e.PullRequest.Number
	}
	var jobs []Job
	switch event {
	case "pull_request", "pull_request_sync", "pull_request_label":
		switch e.Action {
		case "opened", "reopened", "synchronize", "synchronized", "edited", "ready_for_review":
			jobs = append(jobs, Job{Repo: repo, Number: n})
		case "labeled":
			if e.Sender.Type == "Bot" {
				break // cowboy's own label changes (and other bots) aren't corrections
			}
			if pen, ok := strings.CutPrefix(e.Label.Name, "cowboy:"); ok && (pen == "easy" || pen == "normal" || pen == "careful") {
				jobs = append(jobs, Job{Repo: repo, Number: n, Repen: pen, By: e.Sender.Login})
			}
		case "unlabeled":
			if e.Sender.Type == "Bot" {
				break
			}
			if e.Label.Name == "agent:likely" {
				jobs = append(jobs, Job{Repo: repo, Number: n, Unflag: true, By: e.Sender.Login})
			}
		case "closed":
			jobs = append(jobs, Job{Repo: repo, Number: n, Closed: true, Merged: e.PullRequest.Merged})
		}
	case "check_suite", "check_run":
		if e.Action == "completed" {
			for _, p := range append(e.CheckSuite.PullRequests, e.CheckRun.PullRequests...) {
				jobs = append(jobs, Job{Repo: repo, Number: p.Number})
			}
		}
	case "ping":
		w.Write([]byte("pong\n"))
		return
	case "installation", "installation_repositories":
		names := func(rs []struct {
			FullName string `json:"full_name"`
		}) []string {
			var out []string
			for _, r := range rs {
				out = append(out, r.FullName)
			}
			return out
		}
		var j Job
		switch e.Action {
		case "created", "unsuspend", "new_permissions_accepted":
			j.Track = names(e.Repositories)
		case "deleted", "suspend":
			j.Untrack = names(e.Repositories)
		case "added":
			j.Track = names(e.RepositoriesAdded)
		case "removed":
			j.Untrack = names(e.RepositoriesRemoved)
		}
		if len(j.Track)+len(j.Untrack) > 0 {
			h.Queue <- j
		}
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("queued\n"))
		return
	}
	for _, j := range jobs {
		if j.Repo == "" || j.Number == 0 {
			continue
		}
		select {
		case h.Queue <- j:
		default:
			log.Printf("queue full, dropped %s#%d (the catch-up sweep will get it)", j.Repo, j.Number)
		}
	}
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte("queued\n"))
}

// gitlabEvent is the part of a GitLab merge request or pipeline hook
// Cowboy reads.
type gitlabEvent struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		IID    int    `json:"iid"`
		Action string `json:"action"`
		Status string `json:"status"` // pipelines
	} `json:"object_attributes"`
	MergeRequest *struct {
		IID int `json:"iid"`
	} `json:"merge_request"` // pipelines
	Changes struct {
		Labels *struct {
			Previous []struct{ Title string } `json:"previous"`
			Current  []struct{ Title string } `json:"current"`
		} `json:"labels"`
	} `json:"changes"`
}

// GitLab handles /webhook/gitlab. GitLab sends the secret token as is in
// X-Gitlab-Token (no signature); it's compared in constant time. Label
// changes made by Cowboy's own account (Self) or by GitLab bot users are
// not corrections.
func (h *Handler) GitLab(w http.ResponseWriter, r *http.Request) {
	body, ok := read(w, r)
	if !ok {
		return
	}
	sec := h.secret()
	if sec == "" || !hmac.Equal([]byte(r.Header.Get("X-Gitlab-Token")), []byte(sec)) {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	id := r.Header.Get("X-Gitlab-Event-UUID")
	if id == "" {
		id = r.Header.Get("X-Gitlab-Webhook-UUID")
	}
	if h.duplicate(id) {
		w.Write([]byte("duplicate delivery, ignored\n"))
		return
	}
	var e gitlabEvent
	if err := json.Unmarshal(body, &e); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	repo := e.Project.PathWithNamespace
	by := e.User.Username
	self := strings.EqualFold(by, h.Self) || strings.Contains(by, "_bot_") || strings.HasPrefix(by, "project_") && strings.HasSuffix(by, "_bot")
	var jobs []Job
	switch e.ObjectKind {
	case "merge_request":
		n := e.ObjectAttributes.IID
		switch e.ObjectAttributes.Action {
		case "open", "reopen":
			jobs = append(jobs, Job{Repo: repo, Number: n})
		case "update":
			if l := e.Changes.Labels; l != nil && !self {
				prev, cur := map[string]bool{}, map[string]bool{}
				for _, x := range l.Previous {
					prev[x.Title] = true
				}
				for _, x := range l.Current {
					cur[x.Title] = true
				}
				for t := range cur {
					if pen, ok := strings.CutPrefix(t, "cowboy:"); ok && !prev[t] && (pen == "easy" || pen == "normal" || pen == "careful") {
						jobs = append(jobs, Job{Repo: repo, Number: n, Repen: pen, By: by})
					}
				}
				if prev["agent:likely"] && !cur["agent:likely"] {
					jobs = append(jobs, Job{Repo: repo, Number: n, Unflag: true, By: by})
				}
			}
			if len(jobs) == 0 {
				jobs = append(jobs, Job{Repo: repo, Number: n}) // new commits or an edit
			}
		case "merge":
			jobs = append(jobs, Job{Repo: repo, Number: n, Closed: true, Merged: true})
		case "close":
			jobs = append(jobs, Job{Repo: repo, Number: n, Closed: true})
		}
	case "pipeline":
		if e.MergeRequest != nil && (e.ObjectAttributes.Status == "success" || e.ObjectAttributes.Status == "failed") {
			jobs = append(jobs, Job{Repo: repo, Number: e.MergeRequest.IID})
		}
	}
	for _, j := range jobs {
		if j.Repo == "" || j.Number == 0 {
			continue
		}
		select {
		case h.Queue <- j:
		default:
			log.Printf("queue full, dropped %s!%d (the catch-up sweep will get it)", j.Repo, j.Number)
		}
	}
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte("queued\n"))
}
