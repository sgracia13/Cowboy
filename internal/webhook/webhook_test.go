package webhook

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const payload = `{"action":"opened","number":42,"pull_request":{"number":42},"repository":{"full_name":"acme/app"}}`

func TestGitHubSigned(t *testing.T) {
	q := make(chan Job, 4)
	h := &Handler{Secret: "s3cret", Queue: q}
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", Sign("s3cret", []byte(payload)))
	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d", rec.Code)
	}
	if j := <-q; j.Repo != "acme/app" || j.Number != 42 {
		t.Fatalf("bad job %+v", j)
	}
}

func TestBadSignature(t *testing.T) {
	h := &Handler{Secret: "s3cret", Queue: make(chan Job, 1)}
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", Sign("wrong", []byte(payload)))
	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestForgejo(t *testing.T) {
	q := make(chan Job, 4)
	h := &Handler{Secret: "s3cret", Queue: q}
	req := httptest.NewRequest("POST", "/webhook/gitea", strings.NewReader(payload))
	req.Header.Set("X-Forgejo-Event", "pull_request")
	req.Header.Set("X-Forgejo-Signature", strings.TrimPrefix(Sign("s3cret", []byte(payload)), "sha256="))
	rec := httptest.NewRecorder()
	h.Gitea(rec, req)
	if rec.Code != http.StatusAccepted || len(q) != 1 {
		t.Fatalf("got %d, queued %d", rec.Code, len(q))
	}
}

func TestHumanRelabel(t *testing.T) {
	q := make(chan Job, 4)
	h := &Handler{Secret: "s", Queue: q}
	body := `{"action":"labeled","number":7,"label":{"name":"cowboy:careful"},"sender":{"login":"sam"},"repository":{"full_name":"acme/app"}}`
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", Sign("s", []byte(body)))
	h.GitHub(httptest.NewRecorder(), req)
	if j := <-q; j.Repen != "careful" || j.By != "sam" {
		t.Fatalf("bad job %+v", j)
	}
}

func TestUnlabelAgentLikely(t *testing.T) {
	q := make(chan Job, 4)
	h := &Handler{Secret: "s", Queue: q}
	body := `{"action":"unlabeled","number":9,"label":{"name":"agent:likely"},"sender":{"login":"priya"},"repository":{"full_name":"acme/app"}}`
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", Sign("s", []byte(body)))
	h.GitHub(httptest.NewRecorder(), req)
	if j := <-q; !j.Unflag || j.By != "priya" {
		t.Fatalf("bad job %+v", j)
	}
}

// Redelivered webhooks (same delivery ID) are handled once.
func TestDuplicateDeliveryIgnored(t *testing.T) {
	q := make(chan Job, 4)
	h := &Handler{Secret: "s", Queue: q}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(payload))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "abc-123")
		req.Header.Set("X-Hub-Signature-256", Sign("s", []byte(payload)))
		h.GitHub(httptest.NewRecorder(), req)
	}
	if len(q) != 1 {
		t.Fatalf("queued %d jobs, want 1", len(q))
	}
}
