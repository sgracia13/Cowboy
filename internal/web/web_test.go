package web

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/detect"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/store"
)

// Every page renders for every sample PR.
func TestPages(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := app.New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	bs, _ := detect.OpenBaselines("", 90)
	detect.SeedDir(bs, f.Name(), filepath.Join(td, "history"), time.Now())
	a.Baselines = bs
	nums := f.All()
	for _, n := range nums {
		a.HandlePR("acme/app", n)
	}
	a.Sweep()
	mux := http.NewServeMux()
	New(a).Routes(mux)
	urls := []string{"/", "/tally", "/rules", "/trail", "/api/decisions", "/static/cowboy.css", "/demo/host?n=507", "/demo/host?n=506"}
	for _, d := range st.List("", "") {
		urls = append(urls, "/pr?repo=acme/app&n="+strconv.Itoa(d.Number))
	}
	for _, u := range urls {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", u, nil))
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "template:") {
			t.Errorf("%s: %d %s", u, rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
		}
	}
}

// The /events stream sends each sort to the browser, and the partial
// corral render works for live redraws.
func TestLiveStream(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := app.New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	mux := http.NewServeMux()
	New(a).Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				got <- sc.Text()
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond) // let the stream subscribe
	a.HandlePR("acme/app", 507)
	select {
	case line := <-got:
		if !strings.Contains(line, `"number":507`) || !strings.Contains(line, `"pen":"careful"`) {
			t.Fatalf("event = %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event received")
	}

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("X-Cowboy-Partial", "1")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if strings.Contains(string(b), "<html") || !strings.Contains(string(b), `data-pr="acme/app#507"`) {
		t.Fatalf("partial render wrong: %.200s", b)
	}
}
