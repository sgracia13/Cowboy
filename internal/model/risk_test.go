package model

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sgracia13/Cowboy/internal/diff"
)

// riskServer answers every request with reply (status 200) and keeps the
// request bodies it saw.
func riskServer(t *testing.T, status int, reply string) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if status != 200 {
			http.Error(w, "boom", status)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "qwen2.5-coder:7b-q4", "system_fingerprint": "fp_1",
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

var ask = Ask{Title: "add retry", Body: "Fixes #77", Files: files}

func TestRiskProvenance(t *testing.T) {
	srv, _ := riskServer(t, 200, `{"risk": 35, "why": ["retries every call", "a", "b"], "look_first": ["src/webhooks/client.ts: loop", "made/up.go: x"]}`)
	r := New(srv.URL, "qwen2.5-coder:7b", "").Risk(ask)
	if r.Error != "" || r.Score == nil || *r.Score != 35 {
		t.Fatalf("risk = %+v", r)
	}
	if r.Model != "qwen2.5-coder:7b" || r.Served != "qwen2.5-coder:7b-q4" || r.Fingerprint != "fp_1" {
		t.Errorf("model provenance: %+v", r)
	}
	if r.PromptVersion != RiskPromptVersion || !strings.HasPrefix(r.PromptVersion, "risk-v1-") || r.Temperature != 0 || r.Seed != 7 {
		t.Errorf("config provenance: %+v", r)
	}
	if len(r.InputSHA) != 64 || r.InputChars == 0 || r.At.IsZero() || r.Truncated {
		t.Errorf("input provenance: %+v", r)
	}
	if len(r.Why) != 2 || len(r.LookFirst) != 1 {
		t.Errorf("why/look_first should be trimmed and checked against the diff: %+v", r)
	}
}

func TestRiskIsReproducible(t *testing.T) {
	srv, bodies := riskServer(t, 200, `{"risk": 10}`)
	c := New(srv.URL, "m", "")
	a, b := c.Risk(ask), c.Risk(ask)
	if a.InputSHA != b.InputSHA || (*bodies)[0] != (*bodies)[1] {
		t.Error("the same PR must produce the same request and input hash")
	}
	other := ask
	other.Title = "add retries"
	if c.Risk(other).InputSHA == a.InputSHA {
		t.Error("a different PR must hash differently")
	}
}

// Only title, description and diff reach the model: repo, number and head
// SHA (which could cue a model that memorised public history) don't.
func TestRiskPromptHasNoIdentifiers(t *testing.T) {
	a := ask
	a.Repo, a.Number, a.HeadSHA = "acme/app", 4242, "deadbeef"
	p, _ := RiskPrompt(a, 12000)
	for _, s := range []string{"acme/app", "4242", "deadbeef"} {
		if strings.Contains(p, s) {
			t.Errorf("prompt contains %q", s)
		}
	}
	q, _ := RiskPrompt(ask, 12000)
	if p != q {
		t.Error("identifiers changed the prompt")
	}
}

func TestRiskFailuresHaveNoScore(t *testing.T) {
	for name, reply := range map[string]string{
		"no json":      "I think it's fine",
		"no score":     `{"why": ["x"]}`,
		"out of range": `{"risk": 140}`,
		"negative":     `{"risk": -1}`,
		"fraction":     `{"risk": 33.5}`,
		"bad json":     `{"risk": }`,
	} {
		srv, _ := riskServer(t, 200, reply)
		r := New(srv.URL, "m", "").Risk(ask)
		if r.Score != nil || r.Error == "" {
			t.Errorf("%s: want no score and an error, got %+v", name, r)
		}
	}
	srv, _ := riskServer(t, 500, "")
	r := New(srv.URL, "m", "").Risk(ask)
	if r.Score != nil || !strings.Contains(r.Error, "500") || r.PromptVersion == "" || r.InputSHA == "" {
		t.Errorf("server error should keep provenance and no score: %+v", r)
	}
}

func TestRiskTruncation(t *testing.T) {
	big := Ask{Title: "t", Files: []diff.File{{Path: "a.go", Added: []string{strings.Repeat("x", 500)}}, {Path: "b.go", Added: []string{"y"}}}}
	p, cut := RiskPrompt(big, 100)
	if !cut || !strings.Contains(p, "cut for length") {
		t.Errorf("expected a cut diff:\n%s", p)
	}
	if _, cut := RiskPrompt(big, 100000); cut {
		t.Error("nothing should be cut")
	}
}
