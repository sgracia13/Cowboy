package app

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/outcome"
)

// AIReview is what AI reviewers said on one pull request.
type AIReview struct {
	Reviewers []string
	Findings  []outcome.AIFinding
	Known     bool // the host's reviews were read
	Live      bool // read just now from the git host (the PR isn't merged yet)
}

type aiEntry struct {
	r  AIReview
	at time.Time
}

var aiCache = struct {
	sync.Mutex
	m map[string]aiEntry
}{m: map[string]aiEntry{}}

// AICacheFor is how long an open PR's AI reviews are reused before they
// are read from the git host again.
const AICacheFor = 5 * time.Minute

// AIReviewOf returns the AI reviewers' findings on a pull request: from the
// merge record once it merged, otherwise read from the git host (cached).
func (a *App) AIReviewOf(repo string, n int) AIReview {
	if a.Outcomes != nil {
		if m, ok := a.Outcomes.Merge(repo, n); ok && m.AIKnown {
			return AIReview{Reviewers: m.AIReviewers, Findings: m.AIFindings, Known: true}
		}
	}
	k := strings.ToLower(repo) + "#" + strconv.Itoa(n)
	now := a.Now()
	aiCache.Lock()
	e, ok := aiCache.m[k]
	aiCache.Unlock()
	if ok && now.Sub(e.at) < AICacheFor {
		return e.r
	}
	names, fs, known := outcome.ReadAIReview(a.Forge, repo, n, time.Time{})
	r := AIReview{Reviewers: names, Findings: fs, Known: known, Live: known}
	aiCache.Lock()
	if len(aiCache.m) > 2000 {
		aiCache.m = map[string]aiEntry{}
	}
	aiCache.m[k] = aiEntry{r, now}
	aiCache.Unlock()
	return r
}

// CallIt marks finding i on a merged PR as the one that named what went
// wrong (on=false takes the mark back), and records who did it.
func (a *App) CallIt(repo string, n, i int, by string, on bool) error {
	if a.Outcomes == nil {
		return fmt.Errorf("after-merge tracking is off")
	}
	f, err := a.Outcomes.CallIt(repo, n, i, by, on, a.Now().UTC())
	if err != nil {
		return err
	}
	kind := "called-it"
	if !on {
		kind = "called-it-undone"
	}
	a.record(kind, repo, n, by, map[string]string{"reviewer": f.Reviewer, "path": f.Path, "finding": f.Text})
	return nil
}
