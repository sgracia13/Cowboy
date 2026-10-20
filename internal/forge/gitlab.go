package forge

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/detect"
)

// GitLab implements Forge, Archiver and History for GitLab.com and
// self-managed GitLab (API v4). A repo is the project's full path,
// including any subgroups ("group/sub/project"); a pull request is a merge
// request, numbered by its iid.
type GitLab struct {
	client
	mu       sync.Mutex
	branches map[string]string // repo -> default branch, cached
}

func (g *GitLab) Name() string { return "gitlab" }

// proj is the project part of a URL: the full path, escaped as one segment.
func proj(repo string) string { return "/projects/" + url.PathEscape(repo) }

func (g *GitLab) mr(repo string, n int) string {
	return fmt.Sprintf("%s/merge_requests/%d", proj(repo), n)
}

func (g *GitLab) defaultBranch(repo string) (string, error) {
	g.mu.Lock()
	b, ok := g.branches[repo]
	g.mu.Unlock()
	if ok {
		return b, nil
	}
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := g.do("GET", proj(repo), nil, "", &p); err != nil {
		return "", err
	}
	if p.DefaultBranch == "" {
		return "", fmt.Errorf("%s has no default branch", repo)
	}
	g.mu.Lock()
	if g.branches == nil {
		g.branches = map[string]string{}
	}
	g.branches[repo] = p.DefaultBranch
	g.mu.Unlock()
	return p.DefaultBranch, nil
}

type glMR struct {
	IID             int        `json:"iid"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	State           string     `json:"state"` // opened, closed, merged, locked
	SHA             string     `json:"sha"`
	MergeCommitSHA  string     `json:"merge_commit_sha"`
	SquashCommitSHA string     `json:"squash_commit_sha"`
	MergedAt        *time.Time `json:"merged_at"`
	CreatedAt       time.Time  `json:"created_at"`
	SourceBranch    string     `json:"source_branch"`
	Author          struct {
		Username string `json:"username"`
	} `json:"author"`
	Reviewers []struct {
		ID int64 `json:"id"`
	} `json:"reviewers"`
}

type glCommit struct {
	ID            string    `json:"id"`
	Message       string    `json:"message"`
	AuthoredDate  time.Time `json:"authored_date"`
	CommittedDate time.Time `json:"committed_date"`
}

func (g *GitLab) getMR(repo string, n int) (glMR, error) {
	var m glMR
	_, err := g.do("GET", g.mr(repo, n), nil, "", &m)
	return m, err
}

func (g *GitLab) mrCommits(repo string, n int) ([]glCommit, error) {
	var cs []glCommit
	_, err := g.do("GET", g.mr(repo, n)+"/commits?per_page=100", nil, "", &cs)
	return cs, err
}

func glState(s string) string {
	switch s {
	case "opened", "locked":
		return "open"
	case "merged":
		return "merged"
	}
	return "closed"
}

func (g *GitLab) GetPR(repo string, n int) (analyze.PRMeta, string, error) {
	m, err := g.getMR(repo, n)
	if err != nil {
		return analyze.PRMeta{}, "", err
	}
	meta := analyze.PRMeta{Repo: repo, Number: n, Title: m.Title, Body: m.Description, Author: m.Author.Username, HeadSHA: m.SHA}
	if cs, err := g.mrCommits(repo, n); err == nil {
		for _, c := range cs {
			meta.Commits = append(meta.Commits, c.Message)
		}
	}
	return meta, glState(m.State), nil
}

type glDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	DeletedFile bool   `json:"deleted_file"`
	RenamedFile bool   `json:"renamed_file"`
}

// diffs lists the merge request's changed files. GitLab 15.7+ pages them
// at /diffs; older versions only have /changes.
func (g *GitLab) diffs(repo string, n int) ([]glDiff, error) {
	var all []glDiff
	for page := 1; page <= 30; page++ {
		var ds []glDiff
		_, err := g.do("GET", fmt.Sprintf("%s/diffs?per_page=100&page=%d", g.mr(repo, n), page), nil, "", &ds)
		if IsNotFound(err) && page == 1 {
			var ch struct {
				Changes []glDiff `json:"changes"`
			}
			if _, err := g.do("GET", g.mr(repo, n)+"/changes", nil, "", &ch); err != nil {
				return nil, err
			}
			return ch.Changes, nil
		}
		if err != nil {
			return all, err
		}
		all = append(all, ds...)
		if len(ds) < 100 {
			break
		}
	}
	return all, nil
}

// GetDiff rebuilds a unified diff from GitLab's per-file hunks.
func (g *GitLab) GetDiff(repo string, n int) (string, error) {
	ds, err := g.diffs(repo, n)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", d.OldPath, d.NewPath)
		switch {
		case d.NewFile:
			b.WriteString("new file mode 100644\n")
		case d.DeletedFile:
			b.WriteString("deleted file mode 100644\n")
		case d.RenamedFile:
			fmt.Fprintf(&b, "rename from %s\nrename to %s\n", d.OldPath, d.NewPath)
		}
		old, nw := "a/"+d.OldPath, "b/"+d.NewPath
		if d.NewFile {
			old = "/dev/null"
		}
		if d.DeletedFile {
			nw = "/dev/null"
		}
		fmt.Fprintf(&b, "--- %s\n+++ %s\n", old, nw)
		b.WriteString(d.Diff)
		if d.Diff != "" && !strings.HasSuffix(d.Diff, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func (g *GitLab) GetFile(repo, path, ref string) (string, error) {
	if ref == "" {
		b, err := g.defaultBranch(repo)
		if err != nil {
			return "", err
		}
		ref = b
	}
	body, err := g.do("GET", fmt.Sprintf("%s/repository/files/%s/raw?ref=%s", proj(repo), url.PathEscape(path), url.QueryEscape(ref)), nil, "*/*", nil)
	if IsNotFound(err) {
		return "", nil
	}
	return string(body), err
}

func (g *GitLab) ListOpenPRs(repo string) ([]OpenPR, error) {
	var out []OpenPR
	for page := 1; page <= 5; page++ {
		var ms []glMR
		if _, err := g.do("GET", fmt.Sprintf("%s/merge_requests?state=opened&per_page=100&page=%d", proj(repo), page), nil, "", &ms); err != nil {
			return out, err
		}
		for _, m := range ms {
			out = append(out, OpenPR{m.IID, m.SHA})
		}
		if len(ms) < 100 {
			break
		}
	}
	return out, nil
}

// ChecksOK reads the latest pipeline for a commit.
func (g *GitLab) ChecksOK(repo, sha string) (*bool, error) {
	var c struct {
		LastPipeline *struct {
			Status string `json:"status"`
		} `json:"last_pipeline"`
	}
	if _, err := g.do("GET", fmt.Sprintf("%s/repository/commits/%s", proj(repo), url.PathEscape(sha)), nil, "", &c); err != nil {
		return nil, err
	}
	if c.LastPipeline == nil {
		return nil, nil
	}
	switch c.LastPipeline.Status {
	case "success":
		ok := true
		return &ok, nil
	case "failed":
		ok := false
		return &ok, nil
	}
	return nil, nil // running, pending, canceled, skipped, manual
}

type glReviewer struct {
	State     string    `json:"state"` // unreviewed, reviewed, requested_changes, approved, unapproved
	CreatedAt time.Time `json:"created_at"`
}

// reviewers reads reviewer states (GitLab 16.x+; older versions have none,
// and strays then can't be spotted on GitLab).
func (g *GitLab) reviewers(repo string, n int) []glReviewer {
	var rs []glReviewer
	g.do("GET", g.mr(repo, n)+"/reviewers", nil, "", &rs)
	return rs
}

func (g *GitLab) Activity(repo string, n int) (Activity, error) {
	var a Activity
	for _, r := range g.reviewers(repo, n) {
		if r.State == "requested_changes" && r.CreatedAt.After(a.LastChangesRequested) {
			a.LastChangesRequested = r.CreatedAt
		}
	}
	cs, _ := g.mrCommits(repo, n)
	for _, c := range cs {
		if c.CommittedDate.After(a.LastCommit) {
			a.LastCommit = c.CommittedDate
		}
	}
	return a, nil
}

// ensureLabels creates Cowboy's labels with its colors the first time
// (GitLab would otherwise create them grey).
func (g *GitLab) ensureLabels(repo string, names []string) {
	for _, n := range names {
		color := "#ffd400"
		if strings.HasSuffix(n, ":careful") {
			color = "#111111"
		}
		// 409 means it exists already: fine
		g.do("POST", proj(repo)+"/labels", map[string]string{"name": n, "color": color}, "", nil)
	}
}

func (g *GitLab) SetLabels(repo string, n int, add, remove []string) error {
	if len(add) > 0 {
		g.ensureLabels(repo, add)
	}
	body := map[string]string{}
	if len(add) > 0 {
		body["add_labels"] = strings.Join(add, ",")
	}
	if len(remove) > 0 {
		body["remove_labels"] = strings.Join(remove, ",")
	}
	if len(body) == 0 {
		return nil
	}
	_, err := g.do("PUT", g.mr(repo, n), body, "", nil)
	return err
}

func (g *GitLab) UpsertComment(repo string, n int, marker, body string, create bool) error {
	for page := 1; page <= 10; page++ {
		var notes []struct {
			ID     int64  `json:"id"`
			Body   string `json:"body"`
			System bool   `json:"system"`
		}
		if _, err := g.do("GET", fmt.Sprintf("%s/notes?per_page=100&page=%d&sort=asc", g.mr(repo, n), page), nil, "", &notes); err != nil {
			return err
		}
		for _, c := range notes {
			if !c.System && strings.Contains(c.Body, marker) {
				_, err := g.do("PUT", fmt.Sprintf("%s/notes/%d", g.mr(repo, n), c.ID), map[string]string{"body": body}, "", nil)
				return err
			}
		}
		if len(notes) < 100 {
			break
		}
	}
	if !create {
		return nil
	}
	_, err := g.do("POST", g.mr(repo, n)+"/notes", map[string]string{"body": body}, "", nil)
	return err
}

// RequestReview adds reviewers by username, keeping the ones already
// there. GitLab Free allows one reviewer per merge request, so if a list
// is refused, the first one is tried alone.
func (g *GitLab) RequestReview(repo string, n int, users []string) error {
	users = cleanUsers(users)
	if len(users) == 0 {
		return nil
	}
	m, err := g.getMR(repo, n)
	if err != nil {
		return err
	}
	have := map[int64]bool{}
	var ids []int64
	for _, r := range m.Reviewers {
		have[r.ID] = true
		ids = append(ids, r.ID)
	}
	var added []int64
	for _, u := range users {
		var found []struct {
			ID int64 `json:"id"`
		}
		if _, err := g.do("GET", "/users?username="+url.QueryEscape(u), nil, "", &found); err != nil || len(found) == 0 {
			continue
		}
		if !have[found[0].ID] {
			have[found[0].ID] = true
			ids = append(ids, found[0].ID)
			added = append(added, found[0].ID)
		}
	}
	if len(added) == 0 {
		return nil
	}
	if _, err := g.do("PUT", g.mr(repo, n), map[string]any{"reviewer_ids": ids}, "", nil); err != nil {
		if len(m.Reviewers) > 0 {
			return err // there's already a reviewer; leave it
		}
		_, err = g.do("PUT", g.mr(repo, n), map[string]any{"reviewer_ids": added[:1]}, "", nil)
		return err
	}
	return nil
}

func (g *GitLab) Snapshot(repo string, n int) (detect.PRSnapshot, error) {
	m, err := g.getMR(repo, n)
	if err != nil {
		return detect.PRSnapshot{}, err
	}
	s := detect.PRSnapshot{Repo: repo, Number: n, Author: m.Author.Username, Forge: "gitlab", HeadSHA: m.SHA,
		Branch: m.SourceBranch, Title: m.Title, Body: m.Description, OpenedAt: m.CreatedAt}
	if cs, err := g.mrCommits(repo, n); err == nil {
		s.CommitsKnown = len(cs) > 0
		for _, c := range cs {
			s.Commits = append(s.Commits, detect.CommitInfo{SHA: c.ID, Message: c.Message, AuthorDate: c.AuthoredDate, CommitterDate: c.CommittedDate})
		}
	}
	for _, r := range g.reviewers(repo, n) {
		if r.State == "requested_changes" {
			s.Reviews = append(s.Reviews, detect.ReviewEvent{State: "changes_requested", At: r.CreatedAt})
		}
	}
	return s, nil
}

// ---- Archiver

func (g *GitLab) Head(repo string) (string, error) {
	b, err := g.defaultBranch(repo)
	if err != nil {
		return "", err
	}
	var br struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if _, err := g.do("GET", fmt.Sprintf("%s/repository/branches/%s", proj(repo), url.PathEscape(b)), nil, "", &br); err != nil {
		return "", err
	}
	return br.Commit.ID, nil
}

func (g *GitLab) Archive(ctx context.Context, repo, sha string) (io.ReadCloser, error) {
	return g.stream(ctx, fmt.Sprintf("%s/repository/archive.tar.gz?sha=%s", proj(repo), url.QueryEscape(sha)))
}

// ---- History

func (g *GitLab) MergeInfo(repo string, n int) (MergeInfo, error) {
	m, err := g.getMR(repo, n)
	if err != nil {
		return MergeInfo{}, err
	}
	if m.State != "merged" || m.MergedAt == nil {
		return MergeInfo{}, fmt.Errorf("%s!%d isn't merged", repo, n)
	}
	// the commit that landed: a merge commit, else the squash commit, else
	// the head itself (fast-forward)
	sha := m.MergeCommitSHA
	if sha == "" {
		sha = m.SquashCommitSHA
	}
	if sha == "" {
		sha = m.SHA
	}
	info := MergeInfo{SHA: sha, MergedAt: *m.MergedAt, Title: m.Title, Body: m.Description, Author: m.Author.Username}
	ds, err := g.diffs(repo, n)
	if err != nil {
		return info, err
	}
	for _, d := range ds {
		info.Files = append(info.Files, d.NewPath)
	}
	var ap struct {
		ApprovedBy []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	if _, err := g.do("GET", g.mr(repo, n)+"/approvals", nil, "", &ap); err == nil {
		info.ReviewsRead = true
		for _, a := range ap.ApprovedBy {
			if u := a.User.Username; u != "" && !strings.EqualFold(u, info.Author) {
				info.Approvers = append(info.Approvers, u)
			}
		}
	}
	return info, nil
}

func (g *GitLab) Commits(repo string, since time.Time) ([]Commit, error) {
	b, err := g.defaultBranch(repo)
	if err != nil {
		return nil, err
	}
	var out []Commit
	for page := 1; page <= historyPages; page++ {
		var cs []glCommit
		q := fmt.Sprintf("%s/repository/commits?ref_name=%s&since=%s&first_parent=true&per_page=100&page=%d",
			proj(repo), url.QueryEscape(b), url.QueryEscape(since.UTC().Format(time.RFC3339)), page)
		if _, err := g.do("GET", q, nil, "", &cs); err != nil {
			return out, err
		}
		for _, c := range cs {
			out = append(out, Commit{SHA: c.ID, Message: c.Message, At: c.CommittedDate})
		}
		if len(cs) < 100 {
			break
		}
	}
	return out, nil
}

func (g *GitLab) Releases(repo string, since time.Time) ([]Release, error) {
	var out []Release
	for page := 1; page <= historyPages; page++ {
		var rs []struct {
			TagName    string    `json:"tag_name"`
			ReleasedAt time.Time `json:"released_at"`
		}
		if _, err := g.do("GET", fmt.Sprintf("%s/releases?per_page=100&page=%d&order_by=released_at&sort=desc", proj(repo), page), nil, "", &rs); err != nil {
			return out, err
		}
		done := false
		for _, r := range rs {
			if r.ReleasedAt.Before(since) {
				done = true
				continue
			}
			out = append(out, Release{Tag: r.TagName, At: r.ReleasedAt})
		}
		if done || len(rs) < 100 {
			break
		}
	}
	return out, nil
}

func (g *GitLab) MergedSince(repo string, since time.Time) ([]int, error) {
	var out []int
	for page := 1; page <= historyPages*3; page++ {
		var ms []glMR
		q := fmt.Sprintf("%s/merge_requests?state=merged&updated_after=%s&order_by=updated_at&sort=desc&per_page=100&page=%d",
			proj(repo), url.QueryEscape(since.UTC().Format(time.RFC3339)), page)
		if _, err := g.do("GET", q, nil, "", &ms); err != nil {
			return out, err
		}
		for _, m := range ms {
			if m.MergedAt != nil && !m.MergedAt.Before(since) {
				out = append(out, m.IID)
			}
		}
		if len(ms) < 100 {
			break
		}
	}
	sort.Ints(out)
	return out, nil
}

func (g *GitLab) PRForCommit(repo, sha string) (int, error) {
	var ms []glMR
	if _, err := g.do("GET", fmt.Sprintf("%s/repository/commits/%s/merge_requests", proj(repo), url.PathEscape(sha)), nil, "", &ms); err != nil {
		if IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	for _, m := range ms {
		if m.State == "merged" {
			return m.IID, nil
		}
	}
	return 0, nil
}

// WhoAmI is the account the token signs in as (to ignore Cowboy's own
// label changes in webhooks).
func (g *GitLab) WhoAmI() (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	_, err := g.do("GET", "/user", nil, "", &u)
	return u.Username, err
}
