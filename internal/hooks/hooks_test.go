package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type got struct {
	mu     sync.Mutex
	events []Event
	sigs   []string
	heads  []http.Header
}

func receiver(t *testing.T, g *got, status func(n int32) int) *httptest.Server {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := atomic.AddInt32(&n, 1)
		b, _ := io.ReadAll(r.Body)
		code := 200
		if status != nil {
			code = status(i)
		}
		if code < 300 {
			var e Event
			if err := json.Unmarshal(b, &e); err != nil {
				t.Errorf("bad body: %s", b)
			}
			g.mu.Lock()
			g.events = append(g.events, e)
			g.sigs = append(g.sigs, r.Header.Get("X-Cowboy-Signature"))
			g.heads = append(g.heads, r.Header.Clone())
			if !Verify("s3cret", b, r.Header.Get("X-Cowboy-Signature")) {
				t.Errorf("signature doesn't verify")
			}
			g.mu.Unlock()
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fast(s *Sender) *Sender {
	s.backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	return s
}

func TestDeliversSignedInOrder(t *testing.T) {
	var g got
	srv := receiver(t, &g, nil)
	s := fast(New(Config{URLs: []string{srv.URL}, Secret: "s3cret"}))
	for i := 1; i <= 5; i++ {
		s.Send(Event{Type: PRSorted, Repo: "acme/app", Number: i, PR: &PR{Pen: "careful", Shortcuts: []Shortcut{{Kind: "skips", File: "a_test.go"}}}})
	}
	s.Close(context.Background())
	if len(g.events) != 5 {
		t.Fatalf("got %d events", len(g.events))
	}
	for i, e := range g.events {
		if e.Number != i+1 || e.V != Version || e.ID == "" || e.At.IsZero() || e.PR.Pen != "careful" {
			t.Errorf("event %d: %+v", i, e)
		}
		h := g.heads[i]
		if h.Get("X-Cowboy-Event") != PRSorted || h.Get("X-Cowboy-Delivery") != e.ID || h.Get("Content-Type") != "application/json" {
			t.Errorf("headers: %v", h)
		}
	}
	if st := s.Stats(); st.Sent != 5 || st.Failed != 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestRetriesOn5xxNotOn4xx(t *testing.T) {
	var g got
	srv := receiver(t, &g, func(n int32) int {
		if n <= 2 {
			return 503
		}
		return 200
	})
	s := fast(New(Config{URLs: []string{srv.URL}, Secret: "s3cret"}))
	s.Send(Event{Type: PRMoved, Repo: "a/b", Number: 1})
	s.Close(context.Background())
	if len(g.events) != 1 || s.Stats().Sent != 1 {
		t.Fatalf("a 503 should be retried until it lands: %+v", s.Stats())
	}
	var calls int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(400)
	}))
	defer bad.Close()
	s = fast(New(Config{URLs: []string{bad.URL}}))
	s.Send(Event{Type: PRMoved})
	s.Close(context.Background())
	if calls != 1 || s.Stats().Failed != 1 {
		t.Errorf("a 400 is the receiver's answer, not retried: %d calls, %+v", calls, s.Stats())
	}
}

func TestFilterAndNil(t *testing.T) {
	var g got
	srv := receiver(t, &g, nil)
	c, err := Parse(srv.URL, "s3cret", "pr.moved, outcome.found")
	if err != nil {
		t.Fatal(err)
	}
	s := fast(New(c))
	s.Send(Event{Type: PRSorted})
	s.Send(Event{Type: PRMoved})
	s.Send(Event{Type: OutcomeFound})
	s.Close(context.Background())
	if len(g.events) != 2 || g.events[0].Type != PRMoved {
		t.Errorf("filter: %+v", g.events)
	}
	var none *Sender // no URLs configured
	none.Send(Event{Type: PRSorted})
	none.Close(context.Background())
	if New(Config{}) != nil {
		t.Error("no URLs: no sender")
	}
}

func TestParse(t *testing.T) {
	for _, bad := range []string{"ftp://x", "not a url", "https://"} {
		if _, err := Parse(bad, "", ""); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	if _, err := Parse("https://a.example/h", "", "pr.sideways"); err == nil {
		t.Error("an unknown type should be refused")
	}
	c, err := Parse(" https://a.example/h , http://b.example ", "k", "")
	if err != nil || len(c.URLs) != 2 || c.Only != nil {
		t.Errorf("%+v %v", c, err)
	}
}

func TestNeverBlocks(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	s := New(Config{URLs: []string{srv.URL}})
	start := time.Now()
	for i := 0; i < 1100; i++ { // more than the queue holds
		s.Send(Event{Type: PRSorted, Number: i})
	}
	if time.Since(start) > time.Second {
		t.Fatal("Send blocked")
	}
	if s.Stats().Dropped == 0 {
		t.Error("a full queue should drop, and count it")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Close(ctx) // returns at the deadline even though the receiver hangs
}
