package model

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sgracia13/Cowboy/internal/diff"
)

var files = []diff.File{
	{Path: "src/webhooks/client.ts", Status: "modified", Added: []string{"retry()"}},
	{Path: "src/webhooks/sender.ts", Status: "modified", Added: []string{"keepalive: true"}},
}

// A fake OpenAI-compatible server, like Ollama or vLLM.
func fakeServer(t *testing.T, reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		var req struct {
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
			Messages    []struct{ Role, Content string }
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		if req.Model != "qwen2.5-coder:7b" || req.Temperature != 0 || len(req.Messages) != 2 {
			t.Errorf("bad request %+v", req)
		}
		if !strings.Contains(req.Messages[1].Content, "keepalive: true") {
			t.Errorf("diff not in prompt")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": reply}}}})
	}))
}

func TestClient(t *testing.T) {
	reply := "Sure!\n```json\n" + `{"on_task": false, "outside": ["src/webhooks/sender.ts: adds keepalive"], "brief": ["wraps post in a retry"], "look_first": ["src/webhooks/sender.ts: affects every caller"]}` + "\n```"
	srv := fakeServer(t, reply)
	defer srv.Close()
	n := New(srv.URL+"/v1/", "qwen2.5-coder:7b", "").Notes(Ask{Title: "add retry", Body: "Fixes #77", Files: files, HeadSHA: "abc"})
	if n.Error != "" || !n.Drift() || len(n.Outside) != 1 || n.HeadSHA != "abc" {
		t.Fatalf("notes = %+v", n)
	}
}

func TestMadeUpFilesAreDropped(t *testing.T) {
	n := Notes{}
	Parse(`{"on_task": false, "outside": ["src/payments/stripe.ts: touched billing"], "look_first": ["nope.go: x", "src/webhooks/client.ts: retry loop"]}`, &n)
	Check(&n, files)
	if len(n.Outside) != 0 || n.Dropped != 2 || len(n.LookFirst) != 1 {
		t.Fatalf("got %+v", n)
	}
	if n.OnTask != nil {
		t.Fatalf("drift with no real file named should become 'unclear'")
	}
}

func TestServerDown(t *testing.T) {
	n := New("http://127.0.0.1:1", "m", "").Notes(Ask{Files: files})
	if n.Error == "" {
		t.Fatal("want an error inside the notes, not a crash")
	}
	if !strings.Contains(Render(&n), "couldn't get notes") {
		t.Fatal("render should say notes failed")
	}
}

func TestPromptCut(t *testing.T) {
	big := diff.File{Path: "big.txt", Added: []string{strings.Repeat("x", 20000)}}
	p := Prompt(Ask{Title: "t", Files: []diff.File{files[0], big}}, 1000)
	if !strings.Contains(p, "cut for length") || strings.Contains(p, strings.Repeat("x", 2000)) {
		t.Fatal("long diffs must be cut")
	}
}
