package forge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubArchive(t *testing.T) {
	var leaked string
	codeload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
		w.Write([]byte("TARBALL"))
	}))
	defer codeload.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no auth", 401)
			return
		}
		switch {
		case r.URL.Path == "/repos/acme/web":
			w.Write([]byte(`{"default_branch":"main"}`))
		case r.URL.Path == "/repos/acme/web/commits/main" && r.Header.Get("Accept") == "application/vnd.github.sha":
			w.Write([]byte("abc123def456\n"))
		case r.URL.Path == "/repos/acme/web/tarball/abc123def456":
			http.Redirect(w, r, codeload.URL+"/acme/web/legacy.tar.gz/abc123def456?token=signed", http.StatusFound)
		default:
			http.Error(w, r.URL.Path, 404)
		}
	}))
	defer api.Close()
	f, _ := New("github", api.URL, "tok")
	a := f.(Archiver)
	sha, err := a.Head("acme/web")
	if err != nil || sha != "abc123def456" {
		t.Fatalf("head %q %v", sha, err)
	}
	rc, err := a.Archive(context.Background(), "acme/web", sha)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "TARBALL" {
		t.Errorf("body %q", b)
	}
	if leaked != "" {
		t.Errorf("the token followed the redirect to another host: %q", leaked)
	}
}

func TestGiteaArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/acme/web":
			w.Write([]byte(`{"default_branch":"trunk"}`))
		case "/api/v1/repos/acme/web/branches/trunk":
			w.Write([]byte(`{"commit":{"id":"fff000"}}`))
		case "/api/v1/repos/acme/web/archive/fff000.tar.gz":
			if r.Header.Get("Authorization") != "token tok" {
				http.Error(w, "no auth", 401)
				return
			}
			w.Write([]byte("TGZ"))
		default:
			http.Error(w, "nope", 404)
		}
	}))
	defer srv.Close()
	f, _ := New("forgejo", srv.URL, "tok")
	a := f.(Archiver)
	sha, err := a.Head("acme/web")
	if err != nil || sha != "fff000" {
		t.Fatalf("head %q %v", sha, err)
	}
	rc, err := a.Archive(context.Background(), "acme/web", sha)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	if string(b) != "TGZ" {
		t.Errorf("body %q", b)
	}
	if _, err := a.Archive(context.Background(), "acme/web", "nope"); !IsNotFound(err) {
		t.Errorf("missing archive: %v", err)
	}
	_ = strings.TrimSpace
}
