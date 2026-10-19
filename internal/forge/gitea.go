package forge

import (
	"fmt"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/detect"
)

// Gitea implements Forge for Gitea and Forgejo (same API).
type Gitea struct {
	client
	kind string
}

func (g *Gitea) Name() string { return g.kind }

func (g *Gitea) GetPR(repo string, n int) (analyze.PRMeta, string, error) {
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
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/commits?limit=50", repo, n), nil, "", &commits)
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

func (g *Gitea) GetDiff(repo string, n int) (string, error) {
	b, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d.diff", repo, n), nil, "text/plain", nil)
	return string(b), err
}

func (g *Gitea) GetFile(repo, path, ref string) (string, error) {
	b, err := g.do("GET", fmt.Sprintf("/repos/%s/raw/%s%s", repo, esc(path), refQ(ref)), nil, "*/*", nil)
	if IsNotFound(err) {
		return "", nil
	}
	return string(b), err
}

func (g *Gitea) ListOpenPRs(repo string) ([]OpenPR, error) {
	var prs []struct {
		Number int
		Head   struct{ Sha string } `json:"head"`
	}
	_, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls?state=open&limit=50", repo), nil, "", &prs)
	var out []OpenPR
	for _, p := range prs {
		out = append(out, OpenPR{p.Number, p.Head.Sha})
	}
	return out, err
}

func (g *Gitea) ChecksOK(repo, sha string) (*bool, error) {
	var r struct {
		State string `json:"state"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/commits/%s/status", repo, sha), nil, "", &r); err != nil {
		return nil, err
	}
	switch r.State {
	case "success":
		ok := true
		return &ok, nil
	case "failure", "error":
		ok := false
		return &ok, nil
	}
	return nil, nil
}

func (g *Gitea) Activity(repo string, n int) (Activity, error) {
	var a Activity
	var reviews []struct {
		State       string    `json:"state"`
		SubmittedAt time.Time `json:"submitted_at"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, n), nil, "", &reviews)
	for _, r := range reviews {
		if r.State == "REQUEST_CHANGES" && r.SubmittedAt.After(a.LastChangesRequested) {
			a.LastChangesRequested = r.SubmittedAt
		}
	}
	var commits []struct {
		Commit struct {
			Committer struct{ Date time.Time } `json:"committer"`
		} `json:"commit"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/commits?limit=50", repo, n), nil, "", &commits)
	for _, c := range commits {
		if c.Commit.Committer.Date.After(a.LastCommit) {
			a.LastCommit = c.Commit.Committer.Date
		}
	}
	return a, nil
}

// Gitea labels are addressed by ID, so look them up (and create missing ones).
func (g *Gitea) labelIDs(repo string, names []string, create bool) (map[string]int64, error) {
	var ls []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/labels?limit=100", repo), nil, "", &ls); err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, l := range ls {
		ids[l.Name] = l.ID
	}
	if create {
		for _, n := range names {
			if _, ok := ids[n]; ok {
				continue
			}
			color := "#ffd400"
			if strings.HasSuffix(n, ":careful") {
				color = "#111111"
			}
			var made struct {
				ID int64 `json:"id"`
			}
			if _, err := g.do("POST", fmt.Sprintf("/repos/%s/labels", repo), map[string]string{"name": n, "color": color}, "", &made); err != nil {
				return nil, err
			}
			ids[n] = made.ID
		}
	}
	return ids, nil
}

func (g *Gitea) SetLabels(repo string, n int, add, remove []string) error {
	ids, err := g.labelIDs(repo, add, true)
	if err != nil {
		return err
	}
	for _, l := range remove {
		if id, ok := ids[l]; ok {
			_, err := g.do("DELETE", fmt.Sprintf("/repos/%s/issues/%d/labels/%d", repo, n, id), nil, "", nil)
			if err != nil && !IsNotFound(err) {
				return err
			}
		}
	}
	var addIDs []int64
	for _, l := range add {
		addIDs = append(addIDs, ids[l])
	}
	if len(addIDs) == 0 {
		return nil
	}
	_, err = g.do("POST", fmt.Sprintf("/repos/%s/issues/%d/labels", repo, n), map[string]any{"labels": addIDs}, "", nil)
	return err
}

func (g *Gitea) UpsertComment(repo string, n int, marker, body string, create bool) error {
	var cs []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	g.do("GET", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, n), nil, "", &cs)
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

func (g *Gitea) RequestReview(repo string, n int, users []string) error {
	users = cleanUsers(users)
	if len(users) == 0 {
		return nil
	}
	_, err := g.do("POST", fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", repo, n), map[string]any{"reviewers": users}, "", nil)
	return err
}

func (g *Gitea) Snapshot(repo string, n int) (detect.PRSnapshot, error) {
	var pr struct {
		Title     string    `json:"title"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Login   string    `json:"login"`
			Created time.Time `json:"created"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
			Sha string `json:"sha"`
		} `json:"head"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), nil, "", &pr); err != nil {
		return detect.PRSnapshot{}, err
	}
	s := detect.PRSnapshot{Repo: repo, Number: n, Author: pr.User.Login, Forge: g.kind, HeadSHA: pr.Head.Sha,
		Branch: pr.Head.Ref, Title: pr.Title, Body: pr.Body, OpenedAt: pr.CreatedAt, AuthorCreated: pr.User.Created}
	var commits []struct {
		Sha    string `json:"sha"`
		Commit struct {
			Message      string                   `json:"message"`
			Author       struct{ Date time.Time } `json:"author"`
			Committer    struct{ Date time.Time } `json:"committer"`
			Verification struct{ Verified bool }  `json:"verification"`
		} `json:"commit"`
	}
	if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/commits?limit=50", repo, n), nil, "", &commits); err == nil {
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
	g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, n), nil, "", &reviews)
	for _, r := range reviews {
		if r.State == "REQUEST_CHANGES" {
			s.Reviews = append(s.Reviews, detect.ReviewEvent{State: "changes_requested", At: r.SubmittedAt})
		}
	}
	return s, nil
}
