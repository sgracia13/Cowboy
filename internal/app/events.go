package app

import (
	"github.com/sgracia13/Cowboy/internal/hooks"
)

// eventTypes maps audit record kinds to the events other tools receive.
// merge-recorded is bookkeeping (pr.merged covers it) and isn't sent.
var eventTypes = map[string]string{
	"sorted":            hooks.PRSorted,
	"moved":             hooks.PRMoved,
	"merged":            hooks.PRMerged,
	"closed":            hooks.PRClosed,
	"not-agent":         hooks.PRNotAgent,
	"outcome":           hooks.OutcomeFound,
	"outcome-confirmed": hooks.OutcomeConfirmed, // data.confirmed = "true"
	"outcome-rejected":  hooks.OutcomeConfirmed, // data.confirmed = "false"
	"incident":          hooks.IncidentReceived,
}

// emit sends an audit-worthy event to the event hooks. Sorting PRs that
// already merged (rating past history) isn't news, so it isn't sent.
func (a *App) emit(kind, repo string, n int, actor string, data map[string]string) {
	if a.Hooks == nil {
		return
	}
	t, ok := eventTypes[kind]
	if !ok || data["backfill"] == "true" {
		return
	}
	if t == hooks.OutcomeConfirmed {
		d := map[string]string{"confirmed": "true"}
		if kind == "outcome-rejected" {
			d["confirmed"] = "false"
		}
		for k, v := range data {
			d[k] = v
		}
		data = d
	}
	e := hooks.Event{Type: t, Repo: repo, Number: n, Actor: actor, Data: data}
	if n > 0 {
		e.PR = a.PRInfo(repo, n)
	}
	a.Hooks.Send(e)
}

// PRInfo is a pull request as events and the API describe it (nil if
// Cowboy hasn't seen it).
func (a *App) PRInfo(repo string, n int) *hooks.PR {
	d, ok := a.Store.Get(repo, n)
	if !ok {
		return nil
	}
	p := &hooks.PR{Title: d.Title, State: d.State, Pen: string(d.CurrentPen()), RulesPen: string(d.Pen),
		MovedBy: d.Corrected != "", Agent: d.Agent, HeadSHA: d.HeadSHA, Lines: d.Signals.LinesChanged, Files: d.Signals.Files}
	if a.PRLink != nil {
		p.URL = a.PRLink(d.Repo, d.Number)
	}
	for _, r := range d.Reasons {
		if r.Effect == "careful" {
			p.Careful = append(p.Careful, r.Detail)
		}
	}
	for _, s := range d.Signals.Shortcuts {
		p.Shortcuts = append(p.Shortcuts, hooks.Shortcut{Kind: s.Kind, File: s.File, Detail: s.Detail})
	}
	return p
}
