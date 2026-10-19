package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Auth guards the dashboard. Webhooks and /healthz are never behind it:
// webhooks carry their own signatures.
//
// Two ways in:
//   - Password: set UI_PASSWORD. People sign in once; a signed cookie lasts
//     SessionDays.
//   - Proxy: set AUTH_PROXY_HEADER (e.g. X-Forwarded-User) when Cowboy sits
//     behind an SSO proxy (oauth2-proxy, Authelia, Cloudflare Access...).
//     Requests without the header are refused, and the header's value is
//     the name recorded when someone moves a PR.
type Auth struct {
	Password    string
	ProxyHeader string
	SessionDays int
	// Open allows an unguarded dashboard (for local use or the demo).
	Open bool
}

// Enabled reports whether any login is configured.
func (a Auth) Enabled() bool { return a.Password != "" || a.ProxyHeader != "" }

const cookieName = "cowboy_session"

func (a Auth) key() []byte {
	k := sha256.Sum256([]byte("cowboy-session-v1:" + a.Password))
	return k[:]
}

func (a Auth) sign(exp int64) string {
	m := hmac.New(sha256.New, a.key())
	fmt.Fprintf(m, "%d", exp)
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(m.Sum(nil))
}

func (a Auth) validCookie(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	expS, mac, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expS, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	want := a.sign(exp)
	_, wantMAC, _ := strings.Cut(want, ".")
	return hmac.Equal([]byte(mac), []byte(wantMAC))
}

// Wrap returns h guarded by the login, the same-origin check on form
// posts, and security headers. exempt paths skip the login.
func (a Auth) Wrap(h http.Handler) http.Handler {
	days := a.SessionDays
	if days <= 0 {
		days = 14
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/webhook/") || p == "/healthz" {
			h.ServeHTTP(w, r)
			return
		}
		securityHeaders(w)
		// GitHub sends the browser back here from github.com, and a strict
		// sign-in cookie isn't sent on that redirect. These two pages check
		// a single-use state value (callback) or only start a repo sync.
		if r.Method == http.MethodGet && (p == "/setup/github/callback" || p == "/setup/github/installed") {
			h.ServeHTTP(w, r)
			return
		}
		// buttons on the dashboard post forms; refuse posts from other sites
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		switch {
		case a.ProxyHeader != "":
			user := strings.TrimSpace(r.Header.Get(a.ProxyHeader))
			if user == "" {
				http.Error(w, "sign in through your proxy first", http.StatusUnauthorized)
				return
			}
			r.Header.Set("X-Forwarded-User", user)
		case a.Password != "":
			if p == "/login" {
				a.login(w, r, days)
				return
			}
			if p == "/logout" {
				http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			if !a.validCookie(r) {
				if strings.HasPrefix(p, "/api/") || p == "/events" || r.Method != http.MethodGet {
					http.Error(w, "sign in first", http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			// one shared password: record who moved a PR as "team", never a
			// name the browser sent
			r.Header.Set("X-Forwarded-User", "team")
		case !a.Open:
			http.Error(w, "dashboard login not configured", http.StatusForbidden)
			return
		default:
			r.Header.Del("X-Forwarded-User") // nobody vouches for it
		}
		h.ServeHTTP(w, r)
	})
}

func (a Auth) login(w http.ResponseWriter, r *http.Request, days int) {
	next := r.FormValue("next")
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	msg := ""
	if r.Method == http.MethodPost {
		if subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(a.Password)) == 1 {
			exp := time.Now().Add(time.Duration(days) * 24 * time.Hour).Unix()
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: a.sign(exp), Path: "/", Expires: time.Unix(exp, 0),
				HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
		time.Sleep(500 * time.Millisecond) // slow down guessing
		msg = `<p class="note">wrong password</p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>cowboy · sign in</title><link rel="stylesheet" href="/static/cowboy.css"><link rel="icon" href="/static/icon.svg"></head>
<body><main class="login"><h1>cowboy</h1>
<form method="post" action="/login"><input type="hidden" name="next" value="%s">
<label for="pw">password</label><input id="pw" name="password" type="password" autocomplete="current-password" autofocus required>
<button class="btn" type="submit">sign in</button></form>%s</main></body></html>`, html.EscapeString(next), msg)
}

func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin") // a same-site form post must carry its Origin (no-referrer sends "null")
}

// sameOrigin accepts a post whose Origin (or, failing that, Referer) is this
// host. Browsers send Origin on form posts; tools like curl send neither and
// still need the session cookie or proxy header to get in.
func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return true
	}
	u, err := url.Parse(src)
	if err != nil {
		return false
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return strings.EqualFold(u.Host, host)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// IsLocal reports whether addr only listens on this machine.
func IsLocal(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
