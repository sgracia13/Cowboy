package webhook

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(h *Handler, event, secret, body string) int {
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", Sign(secret, []byte(body)))
	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	return rec.Code
}

func TestAppInstallEvents(t *testing.T) {
	q := make(chan Job, 4)
	h := &Handler{Secret: "s", Queue: q}
	post(h, "installation", "s", `{"action":"created","repositories":[{"full_name":"acme/web"},{"full_name":"acme/api"}]}`)
	if j := <-q; strings.Join(j.Track, ",") != "acme/web,acme/api" {
		t.Fatalf("install: %+v", j)
	}
	post(h, "installation_repositories", "s", `{"action":"added","repositories_added":[{"full_name":"acme/docs"}]}`)
	if j := <-q; strings.Join(j.Track, ",") != "acme/docs" {
		t.Fatalf("added: %+v", j)
	}
	post(h, "installation_repositories", "s", `{"action":"removed","repositories_removed":[{"full_name":"acme/api"}]}`)
	if j := <-q; strings.Join(j.Untrack, ",") != "acme/api" {
		t.Fatalf("removed: %+v", j)
	}
	post(h, "installation", "s", `{"action":"deleted","repositories":[{"full_name":"acme/web"}]}`)
	if j := <-q; strings.Join(j.Untrack, ",") != "acme/web" {
		t.Fatalf("uninstalled: %+v", j)
	}
}

func TestBotLabelsAreNotCorrections(t *testing.T) {
	q := make(chan Job, 4)
	h := &Handler{Secret: "s", Queue: q}
	post(h, "pull_request", "s", `{"action":"labeled","number":7,"label":{"name":"cowboy:careful"},"sender":{"login":"cowboy-ab12[bot]","type":"Bot"},"repository":{"full_name":"acme/app"}}`)
	select {
	case j := <-q:
		t.Fatalf("cowboy's own label was taken as a correction: %+v", j)
	default:
	}
}

func TestSetSecret(t *testing.T) {
	q := make(chan Job, 4)
	h := &Handler{Queue: q} // setup not finished: no secret
	if code := post(h, "pull_request", "", payload); code != http.StatusUnauthorized {
		t.Fatalf("with no secret yet, every delivery must be refused, got %d", code)
	}
	h.SetSecret("whsec")
	if code := post(h, "pull_request", "whsec", payload); code != http.StatusAccepted {
		t.Fatalf("after SetSecret: %d", code)
	}
}
