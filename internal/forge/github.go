package forge

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/detect"
)

// GitHub implements Forge for github.com and GitHub Enterprise.
type GitHub struct{ client }

func (g *GitHub) Name() string { return "github" }

func (g *GitHub) GetPR(repo string, n int) (analyze.PRMeta, string, error) {
	var pr struct {
		Title  string                 `json:"title"`
		Body   string                 `json:"body"`
		State  string                 `json:"state"`
		Merged bool                   `json:"merged"`
		User   struct{ Login string } `json:"user"`
		Head   struct{ Sha string }   `json:"head"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), nil, "", &pr); err != nil {
		return analyze.PRMeta{}, "", err
	}
	var commits []struct {
		Commit struct{ Message string } `json:"commit"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100", repo, n), nil, "", &commits)
	m := analyze.PRMeta{Repo: repo, Number: n, Title: pr.Title, Body: pr.Body, Author: pr.User.Login, HeadSHA: pr.Head.Sha}
	for _, c := range commits {
		m.Commits = append(m.Commits, c.Commit.Message)
	}
	state := pr.State
	if pr.Merged {
		state = "merged"
	}
	return m, state, nil
}

func (g *GitHub) GetDiff(repo string, n int) (string, error) {
	b, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), nil, "application/vnd.github.diff", nil)
	return string(b), err
}

func (g *GitHub) GetFile(repo, path, ref string) (string, error) {
	b, err := g.do("GET", fmt.Sprintf("/repos/%s/contents/%s%s", repo, esc(path), refQ(ref)), nil, "application/vnd.github.raw", nil)
	if IsNotFound(err) {
		return "", nil
	}
	return string(b), err
}

func (g *GitHub) ListOpenPRs(repo string) ([]OpenPR, error) {
	var prs []struct {
		Number int
		Head   struct{ Sha string } `json:"head"`
	}
	_, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls?state=open&per_page=100", repo), nil, "", &prs)
	var out []OpenPR
	for _, p := range prs {
		out = append(out, OpenPR{p.Number, p.Head.Sha})
	}
	return out, err
}

func (g *GitHub) ChecksOK(repo, sha string) (*bool, error) {
	var r struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/commits/%s/check-runs", repo, sha), nil, "", &r); err != nil {
		return nil, err
	}
	if len(r.CheckRuns) == 0 {
		return nil, nil
	}
	ok := true
	for _, c := range r.CheckRuns {
		if c.Status != "completed" {
			return nil, nil // still running: unknown
		}
		if c.Conclusion != "success" && c.Conclusion != "neutral" && c.Conclusion != "skipped" {
			ok = false
		}
	}
	return &ok, nil
}

func (g *GitHub) Activity(repo string, n int) (Activity, error) {
	var a Activity
	var reviews []struct {
		State       string    `json:"state"`
		SubmittedAt time.Time `json:"submitted_at"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, n), nil, "", &reviews)
	for _, r := range reviews {
		if r.State == "CHANGES_REQUESTED" && r.SubmittedAt.After(a.LastChangesRequested) {
			a.LastChangesRequested = r.SubmittedAt
		}
	}
	var commits []struct {
		Commit struct {
			Committer struct{ Date time.Time } `json:"committer"`
		} `json:"commit"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100", repo, n), nil, "", &commits)
	for _, c := range commits {
		if c.Commit.Committer.Date.After(a.LastCommit) {
			a.LastCommit = c.Commit.Committer.Date
		}
	}
	return a, nil
}

func (g *GitHub) SetLabels(repo string, n int, add, remove []string) error {
	for _, l := range remove {
		_, err := g.do("DELETE", fmt.Sprintf("/repos/%s/issues/%d/labels/%s", repo, n, esc(l)), nil, "", nil)
		if err != nil && !IsNotFound(err) {
			return err
		}
	}
	if len(add) == 0 {
		return nil
	}
	_, err := g.do("POST", fmt.Sprintf("/repos/%s/issues/%d/labels", repo, n), map[string]any{"labels": add}, "", nil)
	return err
}

func (g *GitHub) UpsertComment(repo string, n int, marker, body string, create bool) error {
	var cs []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", repo, n), nil, "", &cs)
	for _, c := range cs {
		if strings.Contains(c.Body, marker) {
			_, err := g.do("PATCH", fmt.Sprintf("/repos/%s/issues/comments/%d", repo, c.ID), map[string]string{"body": body}, "", nil)
			return err
		}
	}
	if !create {
		return nil
	}
	_, err := g.do("POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, n), map[string]string{"body": body}, "", nil)
	return err
}

func (g *GitHub) RequestReview(repo string, n int, users []string) error {
	users = cleanUsers(users)
	if len(users) == 0 {
		return nil
	}
	_, err := g.do("POST", fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", repo, n), map[string]any{"reviewers": users}, "", nil)
	return err
}

func (g *GitHub) Snapshot(repo string, n int) (detect.PRSnapshot, error) {
	var pr struct {
		Title             string                 `json:"title"`
		Body              string                 `json:"body"`
		CreatedAt         time.Time              `json:"created_at"`
		AuthorAssociation string                 `json:"author_association"`
		User              struct{ Login string } `json:"user"`
		Head              struct {
			Ref string `json:"ref"`
			Sha string `json:"sha"`
		} `json:"head"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), nil, "", &pr); err != nil {
		return detect.PRSnapshot{}, err
	}
	s := detect.PRSnapshot{Repo: repo, Number: n, Author: pr.User.Login, Forge: "github", HeadSHA: pr.Head.Sha,
		Branch: pr.Head.Ref, Title: pr.Title, Body: pr.Body, OpenedAt: pr.CreatedAt}
	member := false
	switch pr.AuthorAssociation {
	case "OWNER", "MEMBER", "COLLABORATOR":
		member = true
	}
	s.OrgMember = &member
	var commits []struct {
		Sha    string `json:"sha"`
		Commit struct {
			Message      string                   `json:"message"`
			Author       struct{ Date time.Time } `json:"author"`
			Committer    struct{ Date time.Time } `json:"committer"`
			Verification struct{ Verified bool }  `json:"verification"`
		} `json:"commit"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100", repo, n), nil, "", &commits); err == nil {
		s.CommitsKnown = len(commits) > 0
		for _, c := range commits {
			s.Commits = append(s.Commits, detect.CommitInfo{SHA: c.Sha, Message: c.Commit.Message,
				AuthorDate: c.Commit.Author.Date, CommitterDate: c.Commit.Committer.Date, Verified: c.Commit.Verification.Verified})
		}
	}
	var reviews []struct {
		State       string    `json:"state"`
		SubmittedAt time.Time `json:"submitted_at"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, n), nil, "", &reviews)
	for _, r := range reviews {
		st := strings.ToLower(r.State)
		if st == "changes_requested" {
			s.Reviews = append(s.Reviews, detect.ReviewEvent{State: st, At: r.SubmittedAt})
		}
	}
	var user struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if _, err := g.do("GET", "/users/"+url.PathEscape(pr.User.Login), nil, "", &user); err == nil {
		s.AuthorCreated = user.CreatedAt
	}
	return s, nil
}
