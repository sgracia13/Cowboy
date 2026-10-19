package app

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
)

// record adds an entry to the audit record, if one is kept.
func (a *App) record(kind, repo string, n int, actor string, data map[string]string) {
	if a.Audit == nil {
		return
	}
	if err := a.Audit.Append(kind, repo, n, actor, data); err != nil {
		log.Printf("audit record: %v", err)
	}
}

func carefulRules(d pipeline.Decision) string {
	var out []string
	for _, r := range d.Reasons {
		if r.Effect == "careful" {
			out = append(out, r.Check)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func agentOf(d pipeline.Decision) string {
	if d.Agent == "" {
		return "person"
	}
	return d.Agent
}

// OnOutcome is called by the outcome watcher for each new outcome or merge.
func (a *App) OnOutcome(e outcome.Event) {
	switch e.Kind {
	case "merge-recorded":
		m := e.Merge
		a.record("merge-recorded", e.Repo, e.Number, "", map[string]string{"sha": m.SHA, "approvers": strings.Join(m.Approvers, ","), "files": fmt.Sprint(len(m.Files))})
	case "outcome":
		o := e.Outcome
		a.record("outcome", e.Repo, e.Number, "", map[string]string{"kind": o.Kind, "status": o.Status, "severity": fmt.Sprint(o.Severity), "evidence": o.Evidence, "id": o.ID})
		a.publish("outcome", e.Repo, e.Number, "", "#%d %s (%s)", e.Number, outcome.KindLabel[o.Kind], o.Status)
	}
}

// DecideOutcome confirms or rejects a suggested (or automatic) outcome.
func (a *App) DecideOutcome(id string, confirm bool, by string) error {
	if a.Outcomes == nil {
		return fmt.Errorf("outcomes are off")
	}
	o, err := a.Outcomes.Decide(id, confirm, by)
	if err != nil {
		return err
	}
	kind := "outcome-rejected"
	if confirm {
		kind = "outcome-confirmed"
	}
	a.record(kind, o.Repo, o.Number, by, map[string]string{"id": o.ID, "kind": o.Kind, "evidence": o.Evidence})
	a.publish("outcome", o.Repo, o.Number, "", "#%d %s %s by %s", o.Number, outcome.KindLabel[o.Kind], strings.TrimPrefix(kind, "outcome-"), by)
	return nil
}

// IngestIncident stores an incident and suggests its likely causes.
func (a *App) IngestIncident(inc outcome.IncidentRecord) {
	if a.Outcomes == nil {
		return
	}
	a.record("incident", inc.Repo, 0, inc.Source, map[string]string{"id": inc.ID, "title": inc.Title, "severity": fmt.Sprint(inc.Severity), "release": inc.Release})
	sugg := a.Outcomes.Link(inc, func(o outcome.Outcome) {
		a.OnOutcome(outcome.Event{Kind: "outcome", Repo: o.Repo, Number: o.Number, Outcome: &o})
	})
	log.Printf("incident %q from %s: %d likely causes suggested", inc.Title, inc.Source, len(sugg))
}

// LinkIncident ties an incident to a PR by hand.
func (a *App) LinkIncident(id, repo string, n int, by string) error {
	if a.Outcomes == nil {
		return fmt.Errorf("outcomes are off")
	}
	o, err := a.Outcomes.LinkByHand(id, repo, n, by)
	if err != nil {
		return err
	}
	a.record("outcome-confirmed", repo, n, by, map[string]string{"id": o.ID, "kind": o.Kind, "evidence": o.Evidence})
	a.publish("outcome", repo, n, "", "#%d linked to incident %s by %s", n, id, by)
	return nil
}

// Backfill sorts PRs that merged in the last few days, the way Cowboy
// would have sorted them, so after-merge results don't need a month of
// waiting. It uses today's rules (cowboy.json on the default branch) and
// today's import graph, writes nothing to the git host, and keeps the PRs
// out of the tally. progress is called after each PR. It returns how many
// PRs it sorted.
func (a *App) Backfill(repo string, days, max int, progress func(done, total int)) (int, error) {
	h, ok := a.Forge.(forge.History)
	if !ok {
		return 0, fmt.Errorf("%s can't list merged pull requests", a.Forge.Name())
	}
	since := a.Now().UTC().AddDate(0, 0, -days)
	nums, err := h.MergedSince(repo, since)
	if err != nil {
		return 0, err
	}
	if max > 0 && len(nums) > max {
		nums = nums[:max]
	}
	sorted := 0
	for i, n := range nums {
		if progress != nil {
			progress(i, len(nums))
		}
		if _, known := a.Store.Get(repo, n); known {
			continue
		}
		mi, err := h.MergeInfo(repo, n)
		if err != nil {
			log.Printf("backfill %s#%d: %v", repo, n, err)
			continue
		}
		if ok, err := a.sortPast(repo, n, mi.MergedAt); err != nil {
			log.Printf("backfill %s#%d: %v", repo, n, err)
		} else if ok {
			sorted++
		}
	}
	if progress != nil {
		progress(len(nums), len(nums))
	}
	return sorted, nil
}

func (a *App) sortPast(repo string, n int, mergedAt time.Time) (bool, error) {
	meta, _, err := a.Forge.GetPR(repo, n)
	if err != nil {
		return false, err
	}
	patch, err := a.Forge.GetDiff(repo, n)
	if err != nil {
		return false, err
	}
	if meta.ChecksOK == nil && meta.HeadSHA != "" {
		meta.ChecksOK, _ = a.Forge.ChecksOK(repo, meta.HeadSHA)
	}
	owners := ""
	for _, p := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS", ".gitea/CODEOWNERS"} {
		if owners, _ = a.Forge.GetFile(repo, p, ""); owners != "" {
			break
		}
	}
	d, _, ok := a.classify(meta, patch, owners, a.Config(repo))
	if !ok {
		return false, nil
	}
	d.State, d.Backfilled, d.DecidedAt = "merged", true, mergedAt.UTC()
	if err := a.Store.Put(d); err != nil {
		return false, err
	}
	a.record("sorted", repo, n, "", map[string]string{"pen": string(d.Pen), "agent": agentOf(d), "rules": carefulRules(d), "head": d.HeadSHA, "backfill": "true"})
	return true, nil
}
