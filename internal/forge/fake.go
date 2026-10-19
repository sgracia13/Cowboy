package forge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/detect"
)

// Fake is an in-memory forge backed by testdata. `cowboy demo` uses it so
// the whole loop (webhook -> fetch -> score -> label -> comment) runs with no
// network. It records every write so the UI can show what Cowboy did.
//
// PRs start closed-to-the-world until Open is called, so the demo can have
// them "arrive" one at a time. OpenAll opens everything at once.
type Fake struct {
	PRDir    string // testdata/prs
	RepoRoot string // testdata/acme (for CODEOWNERS, cowboy.json)
	Now      func() time.Time

	mu       sync.Mutex
	opened   map[int]bool
	state    map[int]string // "", "closed" or "merged"
	Labels   map[int][]string
	Comments map[int]string
	Reviews  map[int][]string
	Log      []string
}

// NewFake builds a demo forge with every sample PR open.
func NewFake(prDir, repoRoot string) *Fake {
	f := &Fake{PRDir: prDir, RepoRoot: repoRoot, Now: time.Now,
		opened: map[int]bool{}, state: map[int]string{},
		Labels: map[int][]string{}, Comments: map[int]string{}, Reviews: map[int][]string{}}
	f.OpenAll()
	return f
}

// All lists every sample PR number, open or not.
func (f *Fake) All() []int {
	ents, _ := os.ReadDir(f.PRDir)
	var out []int
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// HideAll makes every PR not-yet-opened (for the live demo).
func (f *Fake) HideAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = map[int]bool{}
}

// OpenAll opens every sample PR.
func (f *Fake) OpenAll() {
	for _, n := range f.All() {
		f.Open(n)
	}
}

// Open makes one PR appear, as if an agent just opened it.
func (f *Fake) Open(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened[n] = true
}

// Close closes (or merges) a PR.
func (f *Fake) Close(n int, merged bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if merged {
		f.state[n] = "merged"
	} else {
		f.state[n] = "closed"
	}
}

func (f *Fake) Name() string { return "demo" }

func (f *Fake) dir(n int) string { return filepath.Join(f.PRDir, strconv.Itoa(n)) }

func (f *Fake) GetPR(repo string, n int) (analyze.PRMeta, string, error) {
	var m analyze.PRMeta
	f.mu.Lock()
	open, st := f.opened[n], f.state[n]
	f.mu.Unlock()
	if !open {
		return m, "", errNotFound
	}
	b, err := os.ReadFile(filepath.Join(f.dir(n), "meta.json"))
	if err != nil {
		return m, "", errNotFound
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, "", err
	}
	if m.HeadSHA == "" {
		m.HeadSHA = fmt.Sprintf("demo%04d", n)
	}
	if st == "" {
		st = "open"
	}
	return m, st, nil
}

func (f *Fake) GetDiff(repo string, n int) (string, error) {
	b, err := os.ReadFile(filepath.Join(f.dir(n), "pr.diff"))
	return string(b), err
}

func (f *Fake) GetFile(repo, path, ref string) (string, error) {
	b, err := os.ReadFile(filepath.Join(f.RepoRoot, path))
	if err != nil {
		return "", nil
	}
	return string(b), nil
}

func (f *Fake) ListOpenPRs(repo string) ([]OpenPR, error) {
	var out []OpenPR
	for _, n := range f.All() {
		f.mu.Lock()
		ok := f.opened[n] && f.state[n] == ""
		f.mu.Unlock()
		if !ok {
			continue
		}
		m, _, err := f.GetPR(repo, n)
		if err == nil {
			out = append(out, OpenPR{n, m.HeadSHA})
		}
	}
	return out, nil
}

func (f *Fake) ChecksOK(repo, sha string) (*bool, error) { return nil, nil } // meta.json carries checks_ok

func (f *Fake) Activity(repo string, n int) (Activity, error) {
	var a struct {
		CR   int `json:"changes_requested_days_ago"`
		Last int `json:"last_commit_days_ago"`
	}
	b, err := os.ReadFile(filepath.Join(f.dir(n), "activity.json"))
	if err != nil {
		return Activity{}, nil
	}
	json.Unmarshal(b, &a)
	now := f.Now()
	return Activity{
		LastChangesRequested: now.Add(-time.Duration(a.CR) * 24 * time.Hour),
		LastCommit:           now.Add(-time.Duration(a.Last) * 24 * time.Hour),
	}, nil
}

func (f *Fake) SetLabels(repo string, n int, add, remove []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := map[string]bool{}
	for _, l := range f.Labels[n] {
		cur[l] = true
	}
	for _, l := range remove {
		delete(cur, l)
	}
	for _, l := range add {
		cur[l] = true
	}
	var out []string
	for l := range cur {
		out = append(out, l)
	}
	sort.Strings(out)
	f.Labels[n] = out
	f.Log = append(f.Log, fmt.Sprintf("#%d labels +%v -%v", n, add, remove))
	return nil
}

func (f *Fake) UpsertComment(repo string, n int, marker, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	verb := "posted"
	if _, ok := f.Comments[n]; ok {
		verb = "updated"
	}
	f.Comments[n] = body
	f.Log = append(f.Log, fmt.Sprintf("#%d comment %s", n, verb))
	return nil
}

func (f *Fake) RequestReview(repo string, n int, users []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reviews[n] = cleanUsers(users)
	f.Log = append(f.Log, fmt.Sprintf("#%d review requested from %v", n, cleanUsers(users)))
	return nil
}

// fakeSnapshot is testdata/prs/<n>/snapshot.json: the timing and account
// facts a real host would report for the undisclosed-agent check.
type fakeSnapshot struct {
	Branch            string      `json:"branch"`
	OpenedAt          time.Time   `json:"opened_at"`
	FirstPushAt       time.Time   `json:"first_push_at"`
	AuthorCreated     time.Time   `json:"author_created"`
	OrgMember         *bool       `json:"org_member"`
	ForcePushes       []time.Time `json:"force_pushes"`
	ForcePushesKnown  bool        `json:"force_pushes_known"`
	RepoHasPRTemplate bool        `json:"repo_has_pr_template"`
	Commits           []struct {
		SHA           string    `json:"sha"`
		Message       string    `json:"message"`
		AuthorDate    time.Time `json:"author_date"`
		CommitterDate time.Time `json:"committer_date"`
		Verified      bool      `json:"verified"`
		LinesAdded    int       `json:"lines_added"`
		LinesRemoved  int       `json:"lines_removed"`
	} `json:"commits"`
	Reviews []struct {
		State string    `json:"state"`
		At    time.Time `json:"at"`
	} `json:"reviews"`
}

func (f *Fake) Snapshot(repo string, n int) (detect.PRSnapshot, error) {
	m, _, err := f.GetPR(repo, n)
	if err != nil {
		return detect.PRSnapshot{}, err
	}
	s := detect.PRSnapshot{Repo: repo, Number: n, Author: m.Author, Forge: f.Name(), HeadSHA: m.HeadSHA,
		Title: m.Title, Body: m.Body}
	b, err := os.ReadFile(filepath.Join(f.dir(n), "snapshot.json"))
	if err != nil {
		// no timing data: commit messages only, everything else unknown
		for _, msg := range m.Commits {
			s.Commits = append(s.Commits, detect.CommitInfo{Message: msg})
		}
		return s, nil
	}
	var fs fakeSnapshot
	if err := json.Unmarshal(b, &fs); err != nil {
		return s, err
	}
	s.Branch, s.OpenedAt, s.FirstPushAt, s.AuthorCreated, s.OrgMember = fs.Branch, fs.OpenedAt, fs.FirstPushAt, fs.AuthorCreated, fs.OrgMember
	s.ForcePushes, s.ForcePushesKnown, s.RepoHasPRTemplate = fs.ForcePushes, fs.ForcePushesKnown, fs.RepoHasPRTemplate
	s.CommitsKnown = len(fs.Commits) > 0
	for _, c := range fs.Commits {
		s.Commits = append(s.Commits, detect.CommitInfo{SHA: c.SHA, Message: c.Message, AuthorDate: c.AuthorDate,
			CommitterDate: c.CommitterDate, Verified: c.Verified, LinesAdded: c.LinesAdded, LinesRemoved: c.LinesRemoved})
	}
	for _, r := range fs.Reviews {
		s.Reviews = append(s.Reviews, detect.ReviewEvent{State: r.State, At: r.At})
	}
	return s, nil
}
