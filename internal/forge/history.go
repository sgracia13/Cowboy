package forge

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// MergeInfo is what Cowboy records about a merged pull request.
type MergeInfo struct {
	SHA       string    // the commit that landed on the default branch
	MergedAt  time.Time //
	Title     string
	Body      string
	Author    string
	Files     []string // paths changed
	Approvers []string // people who approved before the merge (not the author)
}

// Commit is one commit on the default branch.
type Commit struct {
	SHA     string
	Message string
	At      time.Time
}

// Release is a published release.
type Release struct {
	Tag string
	At  time.Time
}

// History is what outcome tracking reads from a git host after merge.
type History interface {
	MergeInfo(repo string, n int) (MergeInfo, error)
	// Commits on the default branch since a time, newest first (capped).
	Commits(repo string, since time.Time) ([]Commit, error)
	// Releases published since a time, newest first.
	Releases(repo string, since time.Time) ([]Release, error)
	// MergedSince lists pull requests merged since a time.
	MergedSince(repo string, since time.Time) ([]int, error)
	// PRForCommit finds the pull request a commit came from (0 if none).
	PRForCommit(repo, sha string) (int, error)
}

const historyPages = 10

// ---- GitHub

func (g *GitHub) MergeInfo(repo string, n int) (MergeInfo, error) {
	var pr struct {
		Title          string                 `json:"title"`
		Body           string                 `json:"body"`
		MergeCommitSHA string                 `json:"merge_commit_sha"`
		MergedAt       *time.Time             `json:"merged_at"`
		User           struct{ Login string } `json:"user"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), nil, "", &pr); err != nil {
		return MergeInfo{}, err
	}
	if pr.MergedAt == nil {
		return MergeInfo{}, fmt.Errorf("%s#%d isn't merged", repo, n)
	}
	m := MergeInfo{SHA: pr.MergeCommitSHA, MergedAt: *pr.MergedAt, Title: pr.Title, Body: pr.Body, Author: pr.User.Login}
	for page := 1; page <= 30; page++ {
		var files []struct {
			Filename string `json:"filename"`
		}
		if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, n, page), nil, "", &files); err != nil {
			return m, err
		}
		for _, f := range files {
			m.Files = append(m.Files, f.Filename)
		}
		if len(files) < 100 {
			break
		}
	}
	var reviews []struct {
		State       string                 `json:"state"`
		User        struct{ Login string } `json:"user"`
		SubmittedAt time.Time              `json:"submitted_at"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, n), nil, "", &reviews)
	m.Approvers = approvers(reviews, "APPROVED", m.Author, m.MergedAt)
	return m, nil
}

func approvers(reviews []struct {
	State       string                 `json:"state"`
	User        struct{ Login string } `json:"user"`
	SubmittedAt time.Time              `json:"submitted_at"`
}, approved, author string, before time.Time) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range reviews {
		if r.State != approved || r.User.Login == "" || strings.EqualFold(r.User.Login, author) || seen[r.User.Login] {
			continue
		}
		if !r.SubmittedAt.IsZero() && !before.IsZero() && r.SubmittedAt.After(before) {
			continue
		}
		seen[r.User.Login] = true
		out = append(out, r.User.Login)
	}
	return out
}

func (g *GitHub) defaultBranch(repo string) (string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	_, err := g.do("GET", "/repos/"+repo, nil, "", &r)
	return r.DefaultBranch, err
}

func (g *GitHub) Commits(repo string, since time.Time) ([]Commit, error) {
	branch, err := g.defaultBranch(repo)
	if err != nil {
		return nil, err
	}
	var out []Commit
	for page := 1; page <= historyPages; page++ {
		var cs []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message   string                   `json:"message"`
				Committer struct{ Date time.Time } `json:"committer"`
			} `json:"commit"`
		}
		q := fmt.Sprintf("/repos/%s/commits?sha=%s&since=%s&per_page=100&page=%d", repo, url.QueryEscape(branch), url.QueryEscape(since.UTC().Format(time.RFC3339)), page)
		if _, err := g.do("GET", q, nil, "", &cs); err != nil {
			return out, err
		}
		for _, c := range cs {
			out = append(out, Commit{SHA: c.SHA, Message: c.Commit.Message, At: c.Commit.Committer.Date})
		}
		if len(cs) < 100 {
			break
		}
	}
	return out, nil
}

func (g *GitHub) Releases(repo string, since time.Time) ([]Release, error) {
	var rs []struct {
		TagName     string     `json:"tag_name"`
		Draft       bool       `json:"draft"`
		PublishedAt *time.Time `json:"published_at"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/releases?per_page=50", repo), nil, "", &rs); err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range rs {
		if r.Draft || r.PublishedAt == nil || r.PublishedAt.Before(since) {
			continue
		}
		out = append(out, Release{Tag: r.TagName, At: *r.PublishedAt})
	}
	return out, nil
}

func (g *GitHub) MergedSince(repo string, since time.Time) ([]int, error) {
	var out []int
	for page := 1; page <= historyPages; page++ {
		var prs []struct {
			Number    int        `json:"number"`
			MergedAt  *time.Time `json:"merged_at"`
			UpdatedAt time.Time  `json:"updated_at"`
		}
		if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls?state=closed&sort=updated&direction=desc&per_page=100&page=%d", repo, page), nil, "", &prs); err != nil {
			return out, err
		}
		for _, p := range prs {
			if p.MergedAt != nil && !p.MergedAt.Before(since) {
				out = append(out, p.Number)
			}
		}
		if len(prs) < 100 || (len(prs) > 0 && prs[len(prs)-1].UpdatedAt.Before(since)) {
			break
		}
	}
	return out, nil
}

func (g *GitHub) PRForCommit(repo, sha string) (int, error) {
	var prs []struct {
		Number   int        `json:"number"`
		MergedAt *time.Time `json:"merged_at"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/commits/%s/pulls", repo, url.PathEscape(sha)), nil, "", &prs); err != nil {
		if IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	for _, p := range prs {
		if p.MergedAt != nil {
			return p.Number, nil
		}
	}
	return 0, nil
}

// ---- Gitea / Forgejo

func (g *Gitea) MergeInfo(repo string, n int) (MergeInfo, error) {
	var pr struct {
		Title          string                 `json:"title"`
		Body           string                 `json:"body"`
		MergeCommitSHA string                 `json:"merge_commit_sha"`
		MergedAt       *time.Time             `json:"merged_at"`
		User           struct{ Login string } `json:"user"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), nil, "", &pr); err != nil {
		return MergeInfo{}, err
	}
	if pr.MergedAt == nil {
		return MergeInfo{}, fmt.Errorf("%s#%d isn't merged", repo, n)
	}
	m := MergeInfo{SHA: pr.MergeCommitSHA, MergedAt: *pr.MergedAt, Title: pr.Title, Body: pr.Body, Author: pr.User.Login}
	for page := 1; page <= 30; page++ {
		var files []struct {
			Filename string `json:"filename"`
		}
		if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/files?limit=50&page=%d", repo, n, page), nil, "", &files); err != nil {
			return m, err
		}
		for _, f := range files {
			m.Files = append(m.Files, f.Filename)
		}
		if len(files) < 50 {
			break
		}
	}
	var reviews []struct {
		State       string                 `json:"state"`
		User        struct{ Login string } `json:"user"`
		SubmittedAt time.Time              `json:"submitted_at"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, n), nil, "", &reviews)
	m.Approvers = approvers(reviews, "APPROVED", m.Author, m.MergedAt)
	return m, nil
}

func (g *Gitea) Commits(repo string, since time.Time) ([]Commit, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := g.do("GET", "/repos/"+repo, nil, "", &r); err != nil {
		return nil, err
	}
	var out []Commit
	for page := 1; page <= historyPages; page++ {
		var cs []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message   string                   `json:"message"`
				Committer struct{ Date time.Time } `json:"committer"`
			} `json:"commit"`
		}
		q := fmt.Sprintf("/repos/%s/commits?sha=%s&limit=50&page=%d&stat=false&files=false", repo, url.QueryEscape(r.DefaultBranch), page)
		if _, err := g.do("GET", q, nil, "", &cs); err != nil {
			return out, err
		}
		done := len(cs) < 50
		for _, c := range cs {
			if c.Commit.Committer.Date.Before(since) {
				done = true
				continue
			}
			out = append(out, Commit{SHA: c.SHA, Message: c.Commit.Message, At: c.Commit.Committer.Date})
		}
		if done {
			break
		}
	}
	return out, nil
}

func (g *Gitea) Releases(repo string, since time.Time) ([]Release, error) {
	var rs []struct {
		TagName     string    `json:"tag_name"`
		Draft       bool      `json:"draft"`
		PublishedAt time.Time `json:"published_at"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/releases?limit=50", repo), nil, "", &rs); err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range rs {
		if r.Draft || r.PublishedAt.Before(since) {
			continue
		}
		out = append(out, Release{Tag: r.TagName, At: r.PublishedAt})
	}
	return out, nil
}

func (g *Gitea) MergedSince(repo string, since time.Time) ([]int, error) {
	var out []int
	for page := 1; page <= historyPages; page++ {
		var prs []struct {
			Number   int        `json:"number"`
			MergedAt *time.Time `json:"merged_at"`
			Updated  time.Time  `json:"updated_at"`
		}
		if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls?state=closed&sort=recentupdate&limit=50&page=%d", repo, page), nil, "", &prs); err != nil {
			return out, err
		}
		for _, p := range prs {
			if p.MergedAt != nil && !p.MergedAt.Before(since) {
				out = append(out, p.Number)
			}
		}
		if len(prs) < 50 || (len(prs) > 0 && prs[len(prs)-1].Updated.Before(since)) {
			break
		}
	}
	return out, nil
}

func (g *Gitea) PRForCommit(repo, sha string) (int, error) {
	var pr struct {
		Number int `json:"number"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/commits/%s/pull", repo, url.PathEscape(sha)), nil, "", &pr); err != nil {
		if IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	return pr.Number, nil
}
