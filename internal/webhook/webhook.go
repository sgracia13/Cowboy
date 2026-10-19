// Package webhook receives pull request events from GitHub and from
// Gitea/Forgejo, checks the signature, and queues the PR for sorting.
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
}

// Handler turns webhook deliveries into Jobs.
type Handler struct {
	Secret string
	Queue  chan<- Job

	mu    sync.Mutex
	seen  map[string]bool
	order []string
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
	} `json:"sender"`
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
	if !Verify(h.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
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
	if !Verify(h.Secret, body, sig) {
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
			if pen, ok := strings.CutPrefix(e.Label.Name, "cowboy:"); ok && (pen == "easy" || pen == "normal" || pen == "careful") {
				jobs = append(jobs, Job{Repo: repo, Number: n, Repen: pen, By: e.Sender.Login})
			}
		case "unlabeled":
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
