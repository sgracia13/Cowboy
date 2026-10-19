package outcome

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

// Count is n PRs, bad of them had trouble.
type Count struct {
	N   int `json:"n"`
	Bad int `json:"bad"`
}

// Bench is the anonymized summary a team may choose to share. It holds
// counts only: no repo names, file paths, titles, people or code. Agent
// names are tool names; custom agent names become "other-agent".
type Bench struct {
	Format     int              `json:"format"`
	Cowboy     string           `json:"cowboy"`
	From       string           `json:"from"` // month only
	To         string           `json:"to"`
	WindowDays int              `json:"window_days"`
	PRs        int              `json:"prs"`
	Pens       map[string]Count `json:"pens"`
	Agents     map[string]Count `json:"agents"`        // agent × all pens
	AgentPens  map[string]Count `json:"agent_pens"`    // "agent/pen"
	Rules      map[string]Count `json:"rules"`         // careful rules
	Kinds      map[string]int   `json:"outcome_kinds"` // revert, break, fix, incident
	Reviewed   Count            `json:"careful_reviewed"`
	Unreviewed Count            `json:"careful_unreviewed"`
}

var knownAgents = map[string]bool{"people": true, "claude-code": true, "copilot": true, "codex": true, "devin": true, "cursor": true, "jules": true, "likely": true, "aider": true, "windsurf": true, "amp": true, "gemini": true}

func benchAgent(a string) string {
	if knownAgents[a] {
		return a
	}
	return "other-agent"
}

// Benchmark builds the shareable summary over merged PRs whose window has
// passed.
func Benchmark(decisions []pipeline.Decision, st *State, now time.Time, window time.Duration, version string) Bench {
	b := Bench{Format: 1, Cowboy: version, WindowDays: int(window.Hours() / 24), Pens: map[string]Count{}, Agents: map[string]Count{},
		AgentPens: map[string]Count{}, Rules: map[string]Count{}, Kinds: map[string]int{}}
	merges := map[string]Merge{}
	for _, m := range st.Merges("") {
		merges[key(m.Repo, m.Number)] = m
	}
	counted := map[string][]Outcome{}
	for _, o := range st.Outcomes("", 0) {
		if o.Counts() {
			counted[key(o.Repo, o.Number)] = append(counted[key(o.Repo, o.Number)], o)
		}
	}
	var first, last time.Time
	bump := func(m map[string]Count, k string, bad bool) {
		c := m[k]
		c.N++
		if bad {
			c.Bad++
		}
		m[k] = c
	}
	for _, d := range decisions {
		m, ok := merges[key(d.Repo, d.Number)]
		if d.State != "merged" || !ok || now.Sub(m.MergedAt) < window {
			continue
		}
		bad := false
		for _, o := range counted[key(d.Repo, d.Number)] {
			if o.At.Sub(m.MergedAt) <= window {
				bad = true
				b.Kinds[o.Kind]++
			}
		}
		b.PRs++
		if first.IsZero() || m.MergedAt.Before(first) {
			first = m.MergedAt
		}
		if m.MergedAt.After(last) {
			last = m.MergedAt
		}
		pen := string(PenOf(d))
		agent := benchAgent(AgentName(d))
		bump(b.Pens, pen, bad)
		bump(b.Agents, agent, bad)
		bump(b.AgentPens, agent+"/"+pen, bad)
		for _, r := range rules(d) {
			bump(b.Rules, r, bad)
		}
		if PenOf(d) == score.Careful && m.ReviewsKnown {
			c := &b.Unreviewed
			if len(m.Approvers) > 0 {
				c = &b.Reviewed
			}
			c.N++
			if bad {
				c.Bad++
			}
		}
	}
	if !first.IsZero() {
		b.From, b.To = first.Format("2006-01"), last.Format("2006-01")
	}
	return b
}

// CombinedReport pools several teams' benchmark files into a markdown report.
func CombinedReport(benches []Bench) string {
	var sb strings.Builder
	total := Bench{Pens: map[string]Count{}, Agents: map[string]Count{}, AgentPens: map[string]Count{}, Rules: map[string]Count{}, Kinds: map[string]int{}}
	add := func(dst map[string]Count, src map[string]Count) {
		for k, c := range src {
			d := dst[k]
			d.N += c.N
			d.Bad += c.Bad
			dst[k] = d
		}
	}
	from, to := "", ""
	for _, b := range benches {
		total.PRs += b.PRs
		add(total.Pens, b.Pens)
		add(total.Agents, b.Agents)
		add(total.AgentPens, b.AgentPens)
		add(total.Rules, b.Rules)
		for k, v := range b.Kinds {
			total.Kinds[k] += v
		}
		total.Reviewed.N += b.Reviewed.N
		total.Reviewed.Bad += b.Reviewed.Bad
		total.Unreviewed.N += b.Unreviewed.N
		total.Unreviewed.Bad += b.Unreviewed.Bad
		if from == "" || (b.From != "" && b.From < from) {
			from = b.From
		}
		if b.To > to {
			to = b.To
		}
	}
	rate := func(c Count) string {
		lo, hi := Wilson(c.Bad, c.N)
		if c.N == 0 {
			return "–"
		}
		return fmt.Sprintf("%d of %d (%.1f%%, likely %.1f–%.1f%%)", c.Bad, c.N, 100*float64(c.Bad)/float64(c.N), lo*100, hi*100)
	}
	fmt.Fprintf(&sb, "# How pull requests fared after merge\n\n%d teams, %d merged pull requests, %s to %s. \"Trouble\" = reverted, broke main, needed a confirmed fix, or a confirmed incident, within each team's window.\n\n", len(benches), total.PRs, from, to)
	sb.WriteString("## By pen\n\n| pen | had trouble |\n|---|---|\n")
	for _, p := range []string{"easy", "normal", "careful"} {
		fmt.Fprintf(&sb, "| %s | %s |\n", p, rate(total.Pens[p]))
	}
	sb.WriteString("\n## By author\n\n| author | had trouble |\n|---|---|\n")
	keys := sortedKeys(total.Agents)
	for _, k := range keys {
		fmt.Fprintf(&sb, "| %s | %s |\n", k, rate(total.Agents[k]))
	}
	sb.WriteString("\n## Careful rules\n\n| rule | had trouble |\n|---|---|\n")
	for _, k := range sortedKeys(total.Rules) {
		fmt.Fprintf(&sb, "| %s | %s |\n", RuleLabel(k), rate(total.Rules[k]))
	}
	fmt.Fprintf(&sb, "\n## Did review help (careful PRs)\n\n| | had trouble |\n|---|---|\n| approved before merge | %s |\n| merged without an approval | %s |\n", rate(total.Reviewed), rate(total.Unreviewed))
	sb.WriteString("\nThese are counts from teams that chose to share them, not a random sample. A pen's rate shows what happened, not that the pen caused it: careful PRs also get more attention.\n")
	return sb.String()
}

func sortedKeys(m map[string]Count) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]].N != m[out[j]].N {
			return m[out[i]].N > m[out[j]].N
		}
		return out[i] < out[j]
	})
	return out
}
