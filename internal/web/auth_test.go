package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// echo stands in for the dashboard: it reports who the request is from.
var echo = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok user=" + r.Header.Get("X-Forwarded-User")))
})

func do(h http.Handler, method, target string, form url.Values, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPasswordLogin(t *testing.T) {
	h := Auth{Password: "hunter2"}.Wrap(echo)

	if w := do(h, "GET", "/tally", nil, nil); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/login") {
		t.Fatalf("signed-out page should redirect to login, got %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do(h, "GET", "/api/decisions", nil, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out API should be 401, got %d", w.Code)
	}
	if w := do(h, "GET", "/events", nil, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out live stream should be 401, got %d", w.Code)
	}
	if w := do(h, "POST", "/login", url.Values{"password": {"nope"}}, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "wrong password") {
		t.Fatalf("wrong password: %d %q", w.Code, w.Body.String())
	}
	w := do(h, "POST", "/login", url.Values{"password": {"hunter2"}, "next": {"/tally"}}, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/tally" {
		t.Fatalf("right password should redirect to next, got %d %s", w.Code, w.Header().Get("Location"))
	}
	c := w.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags: %+v", c)
	}
	// a browser-sent name is never trusted
	if w := do(h, "GET", "/", nil, map[string]string{"X-Forwarded-User": "mallory"}, c); w.Code != 200 || w.Body.String() != "ok user=team" {
		t.Fatalf("signed in: %d %q", w.Code, w.Body.String())
	}
	// a tampered cookie fails
	bad := *c
	bad.Value = c.Value[:len(c.Value)-2] + "00"
	if w := do(h, "GET", "/api/decisions", nil, nil, &bad); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered cookie accepted: %d", w.Code)
	}
	// open redirects are refused
	w = do(h, "POST", "/login", url.Values{"password": {"hunter2"}, "next": {"//evil.example"}}, nil)
	if w.Header().Get("Location") != "/" {
		t.Fatalf("next=//evil should go to /, got %s", w.Header().Get("Location"))
	}
}

func TestCrossSitePostRefused(t *testing.T) {
	h := Auth{Password: "hunter2"}.Wrap(echo)
	c := do(h, "POST", "/login", url.Values{"password": {"hunter2"}}, nil).Result().Cookies()[0]
	w := do(h, "POST", "/pr/move", url.Values{"to": {"easy"}}, map[string]string{"Origin": "https://evil.example"}, c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site post should be 403, got %d", w.Code)
	}
	w = do(h, "POST", "/pr/move", url.Values{"to": {"easy"}}, map[string]string{"Origin": "http://example.com"}, c)
	if w.Code != 200 {
		t.Fatalf("same-site post should pass, got %d", w.Code)
	}
}

func TestWebhooksAndHealthSkipLogin(t *testing.T) {
	h := Auth{Password: "hunter2"}.Wrap(echo)
	for _, p := range []string{"/webhook/github", "/webhook/forgejo", "/healthz"} {
		if w := do(h, "POST", p, url.Values{}, map[string]string{"Origin": "https://github.com"}); w.Code != 200 {
			t.Errorf("%s should skip the login, got %d", p, w.Code)
		}
	}
}

func TestProxyLogin(t *testing.T) {
	h := Auth{ProxyHeader: "X-Auth-Request-User"}.Wrap(echo)
	if w := do(h, "GET", "/", nil, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no proxy header should be 401, got %d", w.Code)
	}
	if w := do(h, "GET", "/", nil, map[string]string{"X-Auth-Request-User": "maria"}); w.Body.String() != "ok user=maria" {
		t.Fatalf("proxy user: %q", w.Body.String())
	}
}

func TestOpenAndUnconfigured(t *testing.T) {
	if w := do(Auth{}.Wrap(echo), "GET", "/", nil, nil); w.Code != http.StatusForbidden {
		t.Fatalf("no login and not open should be 403, got %d", w.Code)
	}
	w := do(Auth{Open: true}.Wrap(echo), "GET", "/", nil, map[string]string{"X-Forwarded-User": "mallory"})
	if w.Body.String() != "ok user=" {
		t.Fatalf("open mode must drop a browser-sent name: %q", w.Body.String())
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers missing: %v", w.Header())
	}
}

func TestIsLocal(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:8080": true, "localhost:8080": true, "[::1]:8080": true, ":8080": false, "0.0.0.0:8080": false, "10.0.0.5:8080": false} {
		if got := IsLocal(addr); got != want {
			t.Errorf("IsLocal(%q) = %v, want %v", addr, got, want)
		}
	}
}
