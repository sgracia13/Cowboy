package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/diff"
)

// FakeGitLab answers like GitLab's API v4 for one project in a nested
// group, and records what Cowboy wrote.
type FakeGitLab struct {
	mu       sync.Mutex
	writes   []string // "METHOD path body"
	notes    []map[string]any
	labels   map[string]bool
	auth     string
	reviewer []int64
	oneOnly  bool // GitLab Free: one reviewer
}

const glProj = "/api/v4/projects/grp%2Fsub%2Fapp"

func (f *FakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	uri := r.RequestURI // keeps %2F as sent
	body, _ := io.ReadAll(r.Body)
	if r.Method != "GET" {
		f.writes = append(f.writes, r.Method+" "+uri+" "+string(body))
	}
	j := func(v any) { json.NewEncoder(w).Encode(v) }
	merged := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	mr := map[string]any{"iid": 7, "title": "Add retry", "description": "Closes #3", "state": "opened", "sha": "head7",
		"source_branch": "agent/retry", "created_at": "2026-08-30T10:00:00Z", "author": map[string]any{"username": "claude-bot"}, "reviewers": []any{}}
	switch {
	case uri == glProj:
		j(map[string]any{"default_branch": "trunk", "path_with_namespace": "grp/sub/app"})
	case uri == glProj+"/merge_requests/7" && r.Method == "GET":
		j(mr)
	case uri == glProj+"/merge_requests/8":
		m := map[string]any{}
		for k, v := range mr {
			m[k] = v
		}
		m["iid"], m["state"], m["merged_at"], m["merge_commit_sha"], m["squash_commit_sha"] = 8, "merged", merged, "", "5a5a5a5a8b8b8b8b"
		j(m)
	case uri == glProj+"/merge_requests/7" && r.Method == "PUT":
		var b map[string]any
		json.Unmarshal(body, &b)
		if ids, ok := b["reviewer_ids"].([]any); ok && f.oneOnly && len(ids) > 1 {
			w.WriteHeader(422)
			j(map[string]any{"message": "one reviewer only"})
			return
		}
		j(mr)
	case strings.HasPrefix(uri, glProj+"/merge_requests/7/commits") || strings.HasPrefix(uri, glProj+"/merge_requests/8/commits"):
		j([]map[string]any{{"id": "c1", "message": "Add retry\n\nCo-authored-by: Claude <noreply@anthropic.com>", "authored_date": "2026-08-30T09:00:00Z", "committed_date": "2026-08-30T09:05:00Z"}})
	case strings.HasPrefix(uri, glProj+"/merge_requests/7/diffs") || strings.HasPrefix(uri, glProj+"/merge_requests/8/diffs"):
		j([]map[string]any{
			{"old_path": "src/net/retry.go", "new_path": "src/net/retry.go", "diff": "@@ -1,2 +1,3 @@\n package net\n-var n = 1\n+var n = 3\n+var m = 2\n"},
			{"old_path": "src/net/new.go", "new_path": "src/net/new.go", "new_file": true, "diff": "@@ -0,0 +1 @@\n+package net\n"},
			{"old_path": "old.txt", "new_path": "old.txt", "deleted_file": true, "diff": "@@ -1 +0,0 @@\n-bye\n"},
		})
	case strings.HasPrefix(uri, glProj+"/repository/files/"):
		if strings.Contains(uri, "files/cowboy.json/raw?ref=trunk") {
			w.Write([]byte(`{"easy":{"max_lines":30}}`))
			return
		}
		if strings.Contains(uri, "files/docs%2Fguide.md/raw?ref=main") {
			w.Write([]byte("# guide"))
			return
		}
		w.WriteHeader(404)
	case strings.HasPrefix(uri, glProj+"/merge_requests?state=opened"):
		j([]map[string]any{{"iid": 7, "sha": "head7"}, {"iid": 9, "sha": "head9"}})
	case uri == glProj+"/repository/commits/head7":
		j(map[string]any{"id": "head7", "last_pipeline": map[string]any{"status": "failed"}})
	case uri == glProj+"/repository/commits/head9":
		j(map[string]any{"id": "head9", "last_pipeline": nil})
	case strings.HasPrefix(uri, glProj+"/merge_requests/7/reviewers"):
		j([]map[string]any{{"state": "requested_changes", "created_at": "2026-08-31T10:00:00Z"}, {"state": "approved", "created_at": "2026-08-31T11:00:00Z"}})
	case uri == glProj+"/labels":
		var b map[string]string
		json.Unmarshal(body, &b)
		if f.labels[b["name"]] {
			w.WriteHeader(409)
			return
		}
		f.labels[b["name"]] = true
		j(b)
	case strings.HasPrefix(uri, glProj+"/merge_requests/7/notes") && r.Method == "GET":
		j(f.notes)
	case uri == glProj+"/merge_requests/7/notes" && r.Method == "POST":
		var b map[string]any
		json.Unmarshal(body, &b)
		b["id"] = float64(len(f.notes) + 100)
		f.notes = append(f.notes, b)
		j(b)
	case strings.HasPrefix(uri, glProj+"/merge_requests/7/notes/") && r.Method == "PUT":
		var b map[string]any
		json.Unmarshal(body, &b)
		for _, n := range f.notes {
			if strings.HasSuffix(uri, "/"+strings.TrimSuffix(strings.TrimSuffix(jsonNum(n["id"]), ".0"), "")) {
				n["body"] = b["body"]
			}
		}
		j(b)
	case strings.HasPrefix(uri, "/api/v4/users?username="):
		ids := map[string]int64{"maria": 11, "sam": 12}
		u := strings.TrimPrefix(uri, "/api/v4/users?username=")
		if id, ok := ids[u]; ok {
			j([]map[string]any{{"id": id, "username": u}})
		} else {
			j([]any{})
		}
	case uri == glProj+"/repository/branches/trunk":
		j(map[string]any{"commit": map[string]any{"id": "tip123"}})
	case uri == glProj+"/repository/archive.tar.gz?sha=tip123":
		w.Write([]byte("TARBALL"))
	case uri == glProj+"/merge_requests/8/approvals":
		j(map[string]any{"approved_by": []map[string]any{{"user": map[string]any{"username": "maria"}}, {"user": map[string]any{"username": "claude-bot"}}}})
	case strings.HasPrefix(uri, glProj+"/repository/commits?ref_name=trunk"):
		j([]map[string]any{{"id": "s2", "message": "Revert \"Add retry\"\n\nThis reverts merge request !8", "committed_date": "2026-09-02T00:00:00Z"}, {"id": "5a5a5a5a8b8b8b8b", "message": "Add retry", "committed_date": "2026-09-01T12:00:00Z"}})
	case strings.HasPrefix(uri, glProj+"/releases"):
		j([]map[string]any{{"tag_name": "v2", "released_at": "2026-09-03T00:00:00Z"}, {"tag_name": "v1", "released_at": "2026-07-01T00:00:00Z"}})
	case strings.HasPrefix(uri, glProj+"/merge_requests?state=merged"):
		j([]map[string]any{{"iid": 8, "merged_at": merged}, {"iid": 5, "merged_at": "2026-06-01T00:00:00Z"}})
	case uri == glProj+"/repository/commits/5a5a5a5a8b8b8b8b/merge_requests":
		j([]map[string]any{{"iid": 6, "state": "closed"}, {"iid": 8, "state": "merged"}})
	case uri == glProj+"/repository/commits/nope/merge_requests":
		w.WriteHeader(404)
	case uri == "/api/v4/user":
		j(map[string]any{"username": "cowboy-bot"})
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"404 Not Found: ` + uri + `"}`))
	}
}

// Writes lists what Cowboy wrote, in order.
func (f *FakeGitLab) Writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func jsonNum(v any) string { b, _ := json.Marshal(v); return string(b) }

func NewFakeGitLab(t *testing.T) (*GitLab, *FakeGitLab) {
	f := &FakeGitLab{labels: map[string]bool{"cowboy:easy": true}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fg, err := New("gitlab", srv.URL, "glpat-secret")
	if err != nil {
		t.Fatal(err)
	}
	return fg.(*GitLab), f
}

func TestGitLabReading(t *testing.T) {
	g, f := NewFakeGitLab(t)
	meta, state, err := g.GetPR("grp/sub/app", 7)
	if err != nil || state != "open" || meta.Author != "claude-bot" || meta.HeadSHA != "head7" || len(meta.Commits) != 1 {
		t.Fatalf("GetPR: %+v %s %v", meta, state, err)
	}
	if f.auth != "Bearer glpat-secret" {
		t.Errorf("auth header %q", f.auth)
	}
	d, err := g.GetDiff("grp/sub/app", 7)
	if err != nil {
		t.Fatal(err)
	}
	files := diff.Parse(d)
	if len(files) != 3 {
		t.Fatalf("diff files: %d\n%s", len(files), d)
	}
	byPath := map[string]diff.File{}
	for _, x := range files {
		byPath[x.Path] = x
	}
	if r := byPath["src/net/retry.go"]; len(r.Added) != 2 || len(r.Removed) != 1 || r.Status != "modified" {
		t.Errorf("modified file: %+v", r)
	}
	if byPath["src/net/new.go"].Status != "added" || byPath["old.txt"].Status != "removed" {
		t.Errorf("statuses: %+v", files)
	}
	// the rule book from the default branch (trunk), a missing file, a path with a slash
	if s, err := g.GetFile("grp/sub/app", "cowboy.json", ""); err != nil || !strings.Contains(s, "max_lines") {
		t.Errorf("GetFile default branch: %q %v", s, err)
	}
	if s, err := g.GetFile("grp/sub/app", ".cowboy.json", ""); err != nil || s != "" {
		t.Errorf("missing file should be empty: %q %v", s, err)
	}
	if s, _ := g.GetFile("grp/sub/app", "docs/guide.md", "main"); s != "# guide" {
		t.Errorf("nested path: %q", s)
	}
	open, err := g.ListOpenPRs("grp/sub/app")
	if err != nil || len(open) != 2 || open[1].Number != 9 || open[1].HeadSHA != "head9" {
		t.Errorf("open MRs: %+v %v", open, err)
	}
	if ok, _ := g.ChecksOK("grp/sub/app", "head7"); ok == nil || *ok {
		t.Error("a failed pipeline should read as failing")
	}
	if ok, _ := g.ChecksOK("grp/sub/app", "head9"); ok != nil {
		t.Error("no pipeline should read as unknown")
	}
	a, _ := g.Activity("grp/sub/app", 7)
	if a.LastChangesRequested.IsZero() || a.LastCommit.IsZero() {
		t.Errorf("activity: %+v", a)
	}
	snap, err := g.Snapshot("grp/sub/app", 7)
	if err != nil || snap.Branch != "agent/retry" || len(snap.Reviews) != 1 || !snap.CommitsKnown {
		t.Errorf("snapshot: %+v %v", snap, err)
	}
}

func TestGitLabWriting(t *testing.T) {
	g, f := NewFakeGitLab(t)
	if err := g.SetLabels("grp/sub/app", 7, []string{"cowboy:careful", "cowboy:easy"}, []string{"cowboy:normal"}); err != nil {
		t.Fatal(err)
	}
	if !f.labels["cowboy:careful"] {
		t.Error("a missing label should be created")
	}
	last := f.writes[len(f.writes)-1]
	if !strings.HasPrefix(last, "PUT "+glProj+"/merge_requests/7 ") || !strings.Contains(last, `"add_labels":"cowboy:careful,cowboy:easy"`) || !strings.Contains(last, `"remove_labels":"cowboy:normal"`) {
		t.Errorf("labels write: %s", last)
	}
	// the comment is posted once, then edited in place
	if err := g.UpsertComment("grp/sub/app", 7, "<!-- cowboy -->", "<!-- cowboy -->\nfirst", true); err != nil {
		t.Fatal(err)
	}
	g.UpsertComment("grp/sub/app", 7, "<!-- cowboy -->", "<!-- cowboy -->\nsecond", true)
	if len(f.notes) != 1 || !strings.Contains(f.notes[0]["body"].(string), "second") {
		t.Errorf("notes: %+v", f.notes)
	}
	// review: maria and sam; GitLab Free takes one, so the first is kept
	f.oneOnly = true
	if err := g.RequestReview("grp/sub/app", 7, []string{"@maria", "@sam", "@team/infra"}); err != nil {
		t.Fatal(err)
	}
	last = f.writes[len(f.writes)-1]
	if !strings.Contains(last, `"reviewer_ids":[11]`) {
		t.Errorf("review fallback: %s", last)
	}
}

func TestGitLabHistoryAndArchive(t *testing.T) {
	g, _ := NewFakeGitLab(t)
	head, err := g.Head("grp/sub/app")
	if err != nil || head != "tip123" {
		t.Fatalf("head: %q %v", head, err)
	}
	rc, err := g.Archive(context.Background(), "grp/sub/app", head)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "TARBALL" {
		t.Errorf("archive: %q", b)
	}
	m, err := g.MergeInfo("grp/sub/app", 8)
	if err != nil {
		t.Fatal(err)
	}
	if m.SHA != "5a5a5a5a8b8b8b8b" || len(m.Files) != 3 || !m.ReviewsRead || len(m.Approvers) != 1 || m.Approvers[0] != "maria" {
		t.Errorf("merge info (squash commit, author's own approval dropped): %+v", m)
	}
	if _, err := g.MergeInfo("grp/sub/app", 7); err == nil {
		t.Error("an open MR has no merge info")
	}
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cs, err := g.Commits("grp/sub/app", since)
	if err != nil || len(cs) != 2 || !strings.HasPrefix(cs[0].Message, "Revert") {
		t.Errorf("commits: %+v %v", cs, err)
	}
	rs, _ := g.Releases("grp/sub/app", since)
	if len(rs) != 1 || rs[0].Tag != "v2" {
		t.Errorf("releases since Aug 1: %+v", rs)
	}
	ms, _ := g.MergedSince("grp/sub/app", since)
	if len(ms) != 1 || ms[0] != 8 {
		t.Errorf("merged since: %v", ms)
	}
	if n, _ := g.PRForCommit("grp/sub/app", "5a5a5a5a8b8b8b8b"); n != 8 {
		t.Errorf("PR for commit: %d", n)
	}
	if n, err := g.PRForCommit("grp/sub/app", "nope"); n != 0 || err != nil {
		t.Errorf("unknown commit: %d %v", n, err)
	}
	if u, _ := g.WhoAmI(); u != "cowboy-bot" {
		t.Error(u)
	}
}

func TestGitLabBaseURL(t *testing.T) {
	for in, want := range map[string]string{"": "https://gitlab.com/api/v4", "https://git.acme.dev/": "https://git.acme.dev/api/v4", "https://git.acme.dev/api/v4": "https://git.acme.dev/api/v4"} {
		f, _ := New("gitlab", in, "t")
		if got := f.(*GitLab).base; got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
