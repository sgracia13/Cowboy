package forge

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubBotReviews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/pulls/7/reviews":
			w.Write([]byte(`[{"user":{"login":"maria","type":"User"}},{"user":{"login":"greptile-apps[bot]","type":"Bot"}},{"user":{"login":"Copilot","type":"Bot"}}]`))
		case "/repos/o/r/pulls/7/comments":
			w.Write([]byte(`[
			 {"id":1,"user":{"login":"greptile-apps[bot]","type":"Bot"},"path":"a.go","line":12,"body":"nil map","created_at":"2026-05-01T10:00:00Z"},
			 {"id":2,"user":{"login":"greptile-apps[bot]","type":"Bot"},"path":"a.go","line":null,"original_line":9,"body":"outdated","created_at":"2026-05-01T10:00:00Z"},
			 {"id":3,"user":{"login":"maria","type":"User"},"path":"a.go","line":12,"body":"agreed","created_at":"2026-05-01T11:00:00Z"},
			 {"id":4,"user":{"login":"greptile-apps[bot]","type":"Bot"},"path":"a.go","line":12,"body":"thanks!","in_reply_to_id":1,"created_at":"2026-05-01T12:00:00Z"}]`))
		default:
			http.Error(w, "nope", 404)
		}
	}))
	defer srv.Close()
	f, _ := New("github", srv.URL, "tok")
	br, err := f.(BotReviewer).BotReviews("o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(br.Reviewers) != 2 || br.Reviewers[0] != "greptile-apps[bot]" || br.Reviewers[1] != "Copilot" {
		t.Errorf("reviewers (bots only): %v", br.Reviewers)
	}
	if len(br.Notes) != 2 || br.Notes[0].Line != 12 || br.Notes[1].Line != 9 || br.Notes[0].At.IsZero() {
		t.Errorf("notes (bots, no replies, outdated lines kept): %+v", br.Notes)
	}
}
