// Package pipeline runs one PR through Cowboy: measure, decide, add
// double/stray labels, build the comment. It does no I/O itself.
package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/detect"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/graph"
	"github.com/sgracia13/Cowboy/internal/model"
	"github.com/sgracia13/Cowboy/internal/score"
)

// Decision is everything Cowboy decided about one PR at one head SHA.
type Decision struct {
	Repo      string         `json:"repo"`
	Number    int            `json:"number"`
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	Author    string         `json:"author"`
	HeadSHA   string         `json:"head_sha"`
	Issue     int            `json:"issue"`
	Agent     string         `json:"agent"`
	Pen       score.Pen      `json:"pen"`
	Labels    []string       `json:"labels"`
	Reasons   []score.Reason `json:"reasons"`
	Signals   score.Signals  `json:"signals"`
	DoubleOf  int            `json:"double_of,omitempty"`
	Stray     bool           `json:"stray"`
	Comment   string         `json:"comment"`
	DecidedAt time.Time      `json:"decided_at"`
	Corrected score.Pen      `json:"corrected,omitempty"` // set when a human moves it
	State     string         `json:"state"`               // open, merged, closed
	Notes     *model.Notes   `json:"notes,omitempty"`     // local model advice; never changes the pen
	// Detection is the undisclosed-agent check, when it ran.
	Detection *detect.Assessment `json:"detection,omitempty"`
	// AgentCleared names the person who removed an agent:likely label.
	AgentCleared string `json:"agent_cleared,omitempty"`
	// Backfilled: sorted after it merged, to rate past PRs. Nothing was
	// written to the git host for it, and it stays out of the tally.
	Backfilled bool `json:"backfilled,omitempty"`
}

// Input is one PR plus the context needed to sort it.
type Input struct {
	Meta       analyze.PRMeta
	Files      []diff.File
	Config     config.Config
	Graph      *graph.Graph // may be nil
	Codeowners string
	OpenPRs    []Decision // other open agent PRs in the same repo, for doubles
	Now        time.Time

	// AgentOverride names the agent when the undisclosed-agent check found
	// one; AgentLabel replaces the agent:<name> label (e.g. "agent:likely").
	AgentOverride, AgentHow, AgentLabel string
	// SortAll sorts the PR even when no agent was found.
	SortAll bool
}

// Run sorts one PR. It returns ok=false when the PR is not agent-authored.
func Run(in Input) (Decision, bool) {
	sig := score.Measure(in.Meta, in.Files, in.Config, in.Graph)
	if sig.Agent == "" && in.AgentOverride != "" {
		sig.Agent, sig.AgentHow = in.AgentOverride, in.AgentHow
	}
	if sig.Agent == "" && !in.SortAll {
		return Decision{}, false
	}
	// Hosts refuse review requests to a PR's own author, so leave them out.
	for _, o := range score.OwnersFor(in.Codeowners, sig.Paths) {
		if !strings.EqualFold(strings.TrimPrefix(o, "@"), in.Meta.Author) {
			sig.Owners = append(sig.Owners, o)
		}
	}
	pen, reasons := score.Decide(sig, in.Config)

	issue := in.Meta.Issue
	if issue == 0 {
		issue = analyze.LinkedIssue(in.Meta.Body)
	}
	d := Decision{
		Repo: in.Meta.Repo, Number: in.Meta.Number, Title: in.Meta.Title, Body: in.Meta.Body, Author: in.Meta.Author,
		HeadSHA: in.Meta.HeadSHA, Issue: issue, Agent: sig.Agent, Pen: pen, Reasons: reasons,
		Signals: sig, DecidedAt: in.Now, State: "open",
	}
	d.Labels = []string{"cowboy:" + string(pen)}
	switch {
	case in.AgentLabel != "":
		d.Labels = append(d.Labels, in.AgentLabel)
	case sig.Agent != "":
		d.Labels = append(d.Labels, "agent:"+sig.Agent)
	}

	var extra []string
	for _, o := range in.OpenPRs {
		// doubles are agent work repeating other work; a person's PR is
		// never labelled a double
		if sig.Agent == "" {
			break
		}
		if o.Number == d.Number || o.Repo != d.Repo || o.State != "open" {
			continue
		}
		sameIssue := issue != 0 && o.Issue == issue
		overlap := score.Overlap(sig.Paths, o.Signals.Paths)
		if sameIssue || overlap >= 0.8 {
			// the newer PR is the double
			if o.Number < d.Number {
				d.DoubleOf = o.Number
				why := fmt.Sprintf("same issue #%d", issue)
				if !sameIssue {
					why = fmt.Sprintf("%.0f%% of the same files", overlap*100)
				}
				extra = append(extra, fmt.Sprintf("double: looks like the same work as #%d (%s)", o.Number, why))
				d.Labels = append(d.Labels, "cowboy:double")
				break
			}
		}
	}
	d.Comment = score.Comment(pen, sig, reasons, extra)
	return d, true
}

// FullComment is the PR comment: the rule reasons, plus the local model's
// notes (if any) just above the footer.
func FullComment(d Decision) string {
	var blocks []string
	switch {
	case d.AgentCleared != "":
		blocks = append(blocks, fmt.Sprintf("**agent check** · %s removed the agent:likely label. cowboy will ask for more evidence on this author's PRs from now on.\n", d.AgentCleared))
	case d.Detection != nil:
		if r := detect.Render(*d.Detection); r != "" {
			blocks = append(blocks, r)
		}
	}
	if d.Notes != nil && (d.Notes.HeadSHA == "" || d.Notes.HeadSHA == d.HeadSHA) {
		blocks = append(blocks, model.Render(d.Notes))
	}
	if len(blocks) == 0 {
		return d.Comment
	}
	extra := strings.Join(blocks, "\n")
	if i := strings.LastIndex(d.Comment, "\n<sub>"); i >= 0 {
		return d.Comment[:i] + "\n" + extra + d.Comment[i:]
	}
	return d.Comment + "\n" + extra
}

// StrayCheck marks a PR as a stray if the agent went quiet after a reviewer
// asked for changes. lastChangesRequested and lastCommit are zero if unknown.
func StrayCheck(d *Decision, cfg config.Config, lastChangesRequested, lastCommit, now time.Time) (nudge, archive bool) {
	if lastChangesRequested.IsZero() || lastCommit.After(lastChangesRequested) {
		d.Stray = false
		return false, false
	}
	days := int(now.Sub(lastChangesRequested).Hours() / 24)
	if days >= cfg.Strays.NudgeAfterDays {
		d.Stray = true
		if !contains(d.Labels, "cowboy:stray") {
			d.Labels = append(d.Labels, "cowboy:stray")
		}
		return true, days >= cfg.Strays.ArchiveAfterDays
	}
	return false, false
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// CurrentPen is the pen a person moved the PR to, or else the rules' pen.
func (d Decision) CurrentPen() score.Pen {
	if d.Corrected != "" {
		return d.Corrected
	}
	return d.Pen
}
