package app

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/detect"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/pipeline"
)

// checked is one run of the undisclosed-agent check, kept in memory so the
// PR's summary can join the author's baseline when it closes.
type checked struct {
	m      detect.DetectionMatrix
	a      detect.Assessment
	opened time.Time
	hours  []int
}

// detectAgent runs the undisclosed-agent check on a PR that didn't declare
// an agent. ok=false means the check couldn't run (no baselines, host error).
func (a *App) detectAgent(meta analyze.PRMeta, files []diff.File, cfg config.Config) (checked, bool) {
	if a.Baselines == nil || !cfg.DetectOn() {
		return checked{}, false
	}
	snap, err := a.Forge.Snapshot(meta.Repo, meta.Number)
	if err != nil {
		log.Printf("%s#%d agent check: %v", meta.Repo, meta.Number, err)
		return checked{}, false
	}
	if snap.Title == "" {
		snap.Title, snap.Body = meta.Title, meta.Body
	}
	for _, f := range files {
		snap.Files = append(snap.Files, detect.FileChange{Path: f.Path, OldPath: f.OldPath, Status: f.Status, Added: f.Added, Removed: f.Removed})
	}
	if !snap.RepoHasPRTemplate {
		for _, p := range []string{".github/pull_request_template.md", ".github/PULL_REQUEST_TEMPLATE.md", "PULL_REQUEST_TEMPLATE.md", ".gitea/pull_request_template.md", "docs/pull_request_template.md"} {
			if body, _ := a.Forge.GetFile(meta.Repo, p, ""); body != "" {
				snap.RepoHasPRTemplate = true
				break
			}
		}
	}
	base, _ := a.Baselines.Get(a.Forge.Name(), snap.Author)
	m := detect.Build(snap, base, a.Now())
	sc := detect.ScorerV0{Prior: cfg.Detect.Prior, LabelAt: float64(cfg.Detect.LabelAt) / 100}
	as := sc.Score(m)
	var hours []int
	for _, c := range snap.Commits {
		if !c.AuthorDate.IsZero() {
			hours = append(hours, c.AuthorDate.UTC().Hour())
		}
	}
	ch := checked{m: m, a: as, opened: snap.OpenedAt, hours: hours}
	if ch.opened.IsZero() {
		ch.opened = a.Now()
	}
	a.mu.Lock()
	if a.checks == nil {
		a.checks = map[string]checked{}
	}
	a.checks[fmt.Sprintf("%s#%d", meta.Repo, meta.Number)] = ch
	a.mu.Unlock()
	return ch, true
}

// learn folds a closed PR's check into its author's baseline. Agent PRs
// that were labelled are kept out, so agent work doesn't redefine "normal".
func (a *App) learn(repo string, n int) {
	if a.Baselines == nil {
		return
	}
	k := fmt.Sprintf("%s#%d", repo, n)
	a.mu.Lock()
	ch, ok := a.checks[k]
	delete(a.checks, k)
	a.mu.Unlock()
	if !ok {
		return
	}
	p := detect.Summarize(ch.m, ch.opened, ch.hours)
	if ch.a.Label == "likely agent" {
		if d, ok := a.Store.Get(repo, n); !ok || d.AgentCleared == "" {
			p.DisclosedAgent = "likely:" + detect.GuessAgent(ch.m)
		}
	}
	if err := a.Baselines.ObservePast(ch.m.Forge, ch.m.Author, p); err != nil {
		log.Printf("baseline %s: %v", ch.m.Author, err)
	}
}

// ClearAgent records a person saying "this PR isn't from an agent": the
// label comes off, the PR is kept as human work, and the author's baseline
// asks for more evidence next time.
func (a *App) ClearAgent(repo string, n int, by string) error {
	d, ok := a.Store.Get(repo, n)
	if !ok {
		return fmt.Errorf("no decision for %s#%d", repo, n)
	}
	if d.AgentCleared != "" || d.Detection == nil || d.Detection.Label != "likely agent" {
		return nil
	}
	if by == "" {
		by = "someone"
	}
	if a.Baselines != nil {
		a.Baselines.Correct(a.Forge.Name(), d.Author, repo, n)
	}
	d.AgentCleared = by
	d.Agent = ""
	d.Labels = minus(d.Labels, []string{"agent:likely"})
	a.Store.Put(d)
	a.publish("cleared", repo, n, "", "#%d marked as not an agent by %s", n, by)
	if a.DryRun {
		return nil
	}
	if err := a.Forge.SetLabels(repo, n, d.Labels, []string{"agent:likely"}); err != nil {
		return err
	}
	return a.Forge.UpsertComment(repo, n, marker, pipeline.FullComment(d), a.startComment(d, d.CurrentPen()))
}

func lowerKeys(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}
