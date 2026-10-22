package forge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BotNote is one inline review comment a bot left on a pull request.
type BotNote struct {
	Login string    `json:"login"`
	Path  string    `json:"path"`
	Line  int       `json:"line"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
}

// BotReview is what bots did as reviewers on one pull request: which bots
// submitted a review or an inline comment, and their inline comments.
// Ordinary comments on the conversation (deploy previews, coverage) are
// not reviews and aren't read.
type BotReview struct {
	Reviewers []string  `json:"reviewers"`
	Notes     []BotNote `json:"notes"`
}

// BotReviewer is a host that can list bot reviews (GitHub, and the demo).
type BotReviewer interface {
	BotReviews(repo string, n int) (BotReview, error)
}

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (u ghUser) bot() bool {
	return u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]") || u.Login == "Copilot"
}

func (g *GitHub) BotReviews(repo string, n int) (BotReview, error) {
	var out BotReview
	seen := map[string]bool{}
	add := func(login string) {
		if !seen[login] {
			seen[login] = true
			out.Reviewers = append(out.Reviewers, login)
		}
	}
	for page := 1; page <= 3; page++ {
		var reviews []struct {
			User ghUser `json:"user"`
		}
		if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100&page=%d", repo, n, page), nil, "", &reviews); err != nil {
			return out, err
		}
		for _, r := range reviews {
			if r.User.bot() {
				add(r.User.Login)
			}
		}
		if len(reviews) < 100 {
			break
		}
	}
	for page := 1; page <= 5; page++ {
		var cs []struct {
			User         ghUser    `json:"user"`
			Path         string    `json:"path"`
			Line         *int      `json:"line"`
			OriginalLine *int      `json:"original_line"`
			Body         string    `json:"body"`
			CreatedAt    time.Time `json:"created_at"`
			InReplyTo    *int64    `json:"in_reply_to_id"`
		}
		if _, err := g.do("GET", fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100&page=%d", repo, n, page), nil, "", &cs); err != nil {
			return out, err
		}
		for _, c := range cs {
			if !c.User.bot() || c.InReplyTo != nil { // a bot's reply in a thread isn't a new finding
				continue
			}
			add(c.User.Login)
			line := 0
			if c.Line != nil {
				line = *c.Line
			} else if c.OriginalLine != nil {
				line = *c.OriginalLine
			}
			out.Notes = append(out.Notes, BotNote{Login: c.User.Login, Path: c.Path, Line: line, Text: c.Body, At: c.CreatedAt})
		}
		if len(cs) < 100 {
			break
		}
	}
	return out, nil
}

// BotReviews reads testdata/prs/<n>/bot_reviews.json (the demo's made-up
// reviewer comments); none if the file isn't there.
func (f *Fake) BotReviews(repo string, n int) (BotReview, error) {
	var out BotReview
	b, err := os.ReadFile(filepath.Join(f.dir(n), "bot_reviews.json"))
	if err != nil {
		return out, nil
	}
	return out, json.Unmarshal(b, &out)
}
