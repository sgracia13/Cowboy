package app

import (
	"fmt"
	"log"
	"sort"
	"strings"

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
