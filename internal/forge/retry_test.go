package forge

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(url string) *client {
	return &client{base: url, token: "t", scheme: "Bearer", http: &http.Client{Timeout: 5 * time.Second}}
}

func noSleep(t *testing.T) *[]time.Duration {
	var waits []time.Duration
	old := sleep
	sleep = func(d time.Duration) { waits = append(waits, d) }
	t.Cleanup(func() { sleep = old })
	return &waits
}

// GitHub's rate limit: 403 with no requests left, then fine after the reset.
func TestWaitsOutRateLimit(t *testing.T) {
	waits := noSleep(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(10*time.Second).Unix(), 10))
			w.WriteHeader(403)
			w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	var out struct{ OK bool }
	if _, err := testClient(srv.URL).do("GET", "/x", nil, "", &out); err != nil || !out.OK {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if len(*waits) != 1 || (*waits)[0] < 5*time.Second || (*waits)[0] > 12*time.Second {
		t.Fatalf("should wait until the reset, waited %v", *waits)
	}
}

// Secondary limit: 403 with Retry-After.
func TestHonoursRetryAfter(t *testing.T) {
	waits := noSleep(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).do("GET", "/x", nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 1 || (*waits)[0] != 7*time.Second {
		t.Fatalf("waits = %v, want [7s]", *waits)
	}
}

// A long rate-limit wait isn't sat out: the call fails fast with a clear
// error and the catch-up sweep tries later.
func TestLongLimitFailsFast(t *testing.T) {
	waits := noSleep(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(40*time.Minute).Unix(), 10))
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()
	_, err := testClient(srv.URL).do("GET", "/x", nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "rate limit exceeded") || len(*waits) != 0 {
		t.Fatalf("err=%v waits=%v", err, *waits)
	}
}

// Server errors are retried with the request body resent each time, and the
// final error includes the host's message (the old loop lost it).
func TestServerErrorsRetriedThenReported(t *testing.T) {
	noSleep(t)
	var n atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		b := make([]byte, 64)
		k, _ := r.Body.Read(b)
		bodies = append(bodies, string(b[:k]))
		w.WriteHeader(502)
		w.Write([]byte("bad gateway"))
	}))
	defer srv.Close()
	_, err := testClient(srv.URL).do("POST", "/x", map[string]string{"a": "b"}, "", nil)
	if n.Load() != maxRetries+1 {
		t.Fatalf("tries = %d, want %d", n.Load(), maxRetries+1)
	}
	if err == nil || !strings.Contains(err.Error(), "502 bad gateway") {
		t.Fatalf("err = %v", err)
	}
	for _, b := range bodies {
		if b != `{"a":"b"}` {
			t.Fatalf("body not resent on retry: %q", bodies)
		}
	}
}

// Ordinary 4xx errors are not retried.
func TestClientErrorsNotRetried(t *testing.T) {
	noSleep(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(422)
	}))
	defer srv.Close()
	testClient(srv.URL).do("POST", "/x", nil, "", nil)
	if n.Load() != 1 {
		t.Fatalf("422 retried %d times", n.Load())
	}
}
