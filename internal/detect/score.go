package detect

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Scorer v0: a capped log-odds sum.
//
// Evidence is added in log-odds (the logit scale), which is how independent
// clues combine in Bayes' rule: each clue nudges the odds up or down, and
// strong clues can outweigh many weak ones.
//
//	L = prior
//	  + D                        deterministic signals (branch prefix, agent files)
//	  + clamp(B, −1.5, +3.5)     behavioural anomalies, vs the author's own baseline
//	  + clamp(T, −2.0, +2.2)     textual typology
//	  + clamp(C, −0.8, +1.5)     code entropy
//	  + clamp(V, −2.4, +1.5)     account and history (signing, past corrections)
//	p = 1 / (1 + e^−L)            Score = round(100 × p)
//
// The caps are the main protection for busy humans: speed alone (B) can add
// at most +3.5, which from the default prior reaches p ≈ 0.74, below the
// label line. Being fast is never enough; it has to come with text, code or
// a deterministic signal pointing the same way.
//
// Behaviour is measured as "doublings beyond this author's own range":
//
//	faster(x) = clamp(log2(x / P90_author), 0, 3)   e.g. lines per minute
//	sooner(x) = clamp(log2(P10_author / x), 0, 3)   e.g. gaps between commits
//
// so 4× the author's usual 90th percentile counts as 2, not as "very fast
// in general". New authors (ColdStart) are compared to population defaults
// at half weight.
//
// Range: the estimate is reported as [Low, High] = σ(L ∓ u), where the
// half-width u (in log-odds) starts at 1.2, shrinks as independent evidence
// accumulates, and grows for cold-start authors and unmeasured fields.
// The "likely agent" label requires Low ≥ LabelAt, so thin evidence can't
// produce a confident label even when the point estimate is high.
//
// v0 weights are hand-set from published fingerprinting work and common
// sense, not yet fitted to data. Fit them on a design partner's history
// (disclosed agent PRs with markers removed vs pre-2023 PRs) before trusting
// the numbers.
type ScorerV0 struct {
	Prior   float64 // base rate of masked agent PRs; default 0.08
	LabelAt float64 // label when Low ≥ this; default 0.80
}

// NewScorer returns v0 with default settings.
func NewScorer() ScorerV0 { return ScorerV0{Prior: 0.08, LabelAt: 0.80} }

func (s ScorerV0) Version() string { return "v0-handset" }

type contrib struct {
	list []Contribution
}

func (c *contrib) add(module, field string, effect float64, detail string) {
	if effect == 0 || math.IsNaN(effect) {
		return
	}
	c.list = append(c.list, Contribution{Module: module, Field: field, Detail: detail, Effect: round2(effect)})
}

func (c *contrib) sum(module string) float64 {
	t := 0.0
	for _, x := range c.list {
		if x.Module == module {
			t += x.Effect
		}
	}
	return t
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
func clamp(x, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, x))
}
func logit(p float64) float64   { return math.Log(p / (1 - p)) }
func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

func faster(x, p90 float64) float64 {
	if p90 <= 0 || x <= p90 {
		return 0
	}
	return clamp(math.Log2(x/p90), 0, 3)
}

func sooner(x, p10 float64) float64 {
	if p10 <= 0 || x >= p10 {
		return 0
	}
	if x <= 0 {
		return 3
	}
	return clamp(math.Log2(p10/x), 0, 3)
}

// population defaults for authors with no baseline yet
var popDefault = struct{ LPM90, Gap10, BranchToPR10, Review10 float64 }{15, 120, 600, 300}

// Score turns a matrix into an Assessment. It is pure and deterministic.
func (s ScorerV0) Score(m DetectionMatrix) Assessment {
	if s.Prior == 0 {
		s = NewScorer()
	}
	a := Assessment{Scorer: s.Version(), Schema: SchemaVersion}
	if m.Disclosure.Agent != "" {
		a.Score, a.Low, a.High, a.Label = 100, 100, 100, "disclosed agent"
		a.Reasons = []Contribution{{Module: "disclosure", Field: "Agent", Detail: m.Disclosure.Agent + " (" + strings.Join(m.Disclosure.How, ", ") + ")", Effect: 0}}
		return a
	}
	var c contrib
	b := m.Baseline
	cold := b.ColdStart || b.SamplePRs < MinSamples
	bw := 1.0 // behaviour weight
	if cold {
		bw = 0.5
	}

	// ---- deterministic
	d := m.Disclosure
	if d.AgentBranchPrefix.Known && d.AgentBranchPrefix.Value != "" {
		name := AgentBranchPrefixes[d.AgentBranchPrefix.Value]
		c.add("disclosure", "AgentBranchPrefix", 4.5, fmt.Sprintf("%s: the branch name %s uses by default", d.AgentBranchPrefix.Detail, name))
	}
	if d.AgentConfigTouched.Known && len(d.AgentConfigTouched.Value) > 0 {
		c.add("disclosure", "AgentConfigTouched", 0.8, "changes an agent instruction file: "+strings.Join(d.AgentConfigTouched.Value, ", "))
	}
	if d.StrippedTrailer.Known && d.StrippedTrailer.Value {
		c.add("disclosure", "StrippedTrailer", 5, "an agent co-author line was removed from a commit")
	}

	// ---- behaviour
	t := m.Time
	lpm90, gap10, b2p10, rev10 := b.LinesPerMinute.P90, b.InterCommitGapSec.P10, b.BranchToPRSec.P10, b.ReviewResponseSec.P10
	if cold || b.LinesPerMinute.N < 5 {
		lpm90 = popDefault.LPM90
	}
	if cold || b.InterCommitGapSec.N < 5 {
		gap10 = popDefault.Gap10
	}
	if cold || b.BranchToPRSec.N < 5 {
		b2p10 = popDefault.BranchToPR10
	}
	if cold || b.ReviewResponseSec.N < 5 {
		rev10 = popDefault.Review10
	}
	who := "this author's usual"
	if cold {
		who = "typical"
	}
	lines := 0
	if m.Code.LinesChanged.Known {
		lines = m.Code.LinesChanged.Value
	}
	if t.LinesPerMinute.Known {
		if f := faster(t.LinesPerMinute.Value, lpm90); f > 0 {
			c.add("time", "LinesPerMinute", 0.7*bw*f, fmt.Sprintf("%s; %s top pace is %.0f/min", t.LinesPerMinute.Detail, who, lpm90))
		} else if !cold && b.LinesPerMinute.N >= 5 && t.LinesPerMinute.Value >= b.LinesPerMinute.P10 {
			c.add("time", "LinesPerMinute", -0.3, fmt.Sprintf("%.0f lines/min is within this author's normal range", t.LinesPerMinute.Value))
		}
	}
	if t.InterCommitGapSec.Known && t.InterCommitGapSec.Value.Count >= 2 {
		md := t.InterCommitGapSec.Value.Median
		if f := sooner(md, gap10); f > 0 {
			c.add("time", "InterCommitGapSec", 0.5*bw*f, fmt.Sprintf("%s; %s shortest gaps are %s", t.InterCommitGapSec.Detail, who, dur(gap10)))
		} else if md > 1800 {
			c.add("time", "InterCommitGapSec", -0.6, fmt.Sprintf("commits %s apart: a person's pace", dur(md)))
		}
	}
	if t.GapRegularity.Known && t.InterCommitGapSec.Known && t.InterCommitGapSec.Value.Count >= 3 &&
		t.GapRegularity.Value < 0.35 && t.InterCommitGapSec.Value.Median < 120 {
		c.add("time", "GapRegularity", 0.6*bw, "commit gaps are machine-regular")
	}
	if t.BranchToPRSec.Known && lines >= 50 {
		if f := sooner(t.BranchToPRSec.Value, b2p10); f > 0 {
			c.add("time", "BranchToPRSec", 0.4*bw*f, fmt.Sprintf("%s for %d lines", t.BranchToPRSec.Detail, lines))
		}
	}
	if t.FirstPushToPRSec.Known && t.FirstPushToPRSec.Value <= 15 && lines >= 50 {
		c.add("time", "FirstPushToPRSec", 0.6*bw, t.FirstPushToPRSec.Detail+": pushed and opened in one step")
	}
	if t.BurstMaxCommits.Known && t.BurstMaxCommits.Value >= 3 {
		e := 0.5
		if t.BurstMaxCommits.Value >= 5 {
			e = 0.8
		}
		c.add("time", "BurstMaxCommits", e*bw, t.BurstMaxCommits.Detail)
	}
	if t.OffHoursShare.Known && t.OffHoursShare.Value >= 0.75 {
		c.add("time", "OffHoursShare", 0.6*bw, t.OffHoursShare.Detail)
	}
	if t.ReviewResponseSec.Known {
		if f := sooner(t.ReviewResponseSec.Value.Median, rev10); f > 0 && t.ReviewResponseSec.Value.Median < 120 {
			c.add("time", "ReviewResponseSec", 0.7*bw*math.Min(f, 2)/2+0.2, t.ReviewResponseSec.Detail)
		}
	}
	if t.ForcePushCount.Known && t.ForcePushCount.Value > 0 {
		c.add("time", "ForcePushCount", -0.3, t.ForcePushCount.Detail+": tidying history by hand")
	}

	// ---- text
	x := m.Text
	words := 0
	if x.WordCount.Known {
		words = x.WordCount.Value
	}
	if x.TypoRatePer1kWords.Known && words >= 60 {
		r := x.TypoRatePer1kWords.Value
		switch {
		case r >= 2:
			c.add("text", "TypoRatePer1kWords", -0.8, x.TypoRatePer1kWords.Detail+": human slips")
		case r == 0 && !cold && b.TypoRatePer1kWords.N >= 5 && b.TypoRatePer1kWords.Median >= 2:
			c.add("text", "TypoRatePer1kWords", 0.7, fmt.Sprintf("no slips in %d words; this author usually makes ~%.0f per 1,000", words, b.TypoRatePer1kWords.Median))
		case r == 0 && words >= 150:
			c.add("text", "TypoRatePer1kWords", 0.3, fmt.Sprintf("no slips in %d words", words))
		}
	}
	if x.SyntaxPerfection.Known && x.SyntaxPerfection.Value >= 0.95 && words >= 80 {
		c.add("text", "SyntaxPerfection", 0.3, x.SyntaxPerfection.Detail)
	}
	if x.PhraseHits.Known && len(x.PhraseHits.Value) > 0 {
		c.add("text", "PhraseHits", math.Min(1.2, 0.35*float64(len(x.PhraseHits.Value))), x.PhraseHits.Detail)
	}
	if x.TemplateSections.Known && len(x.TemplateSections.Value) >= 2 && !(x.RepoHasPRTemplate.Known && x.RepoHasPRTemplate.Value) {
		switch {
		case !cold && b.UsesTemplateSections < 0.2:
			c.add("text", "TemplateSections", 0.9, fmt.Sprintf("%s; this author's PRs rarely use sections (%.0f%%)", x.TemplateSections.Detail, b.UsesTemplateSections*100))
		case cold:
			c.add("text", "TemplateSections", 0.4, x.TemplateSections.Detail)
		}
	}
	if x.MultilineCommitRatio.Known && t.CommitCount.Known && t.CommitCount.Value >= 2 && x.MultilineCommitRatio.Value >= 0.75 {
		switch {
		case !cold && b.MultilineCommitRatio.N >= 5 && b.MultilineCommitRatio.Median <= 0.25:
			c.add("text", "MultilineCommitRatio", 0.7, fmt.Sprintf("%s; this author usually writes one-line messages", x.MultilineCommitRatio.Detail))
		case cold:
			c.add("text", "MultilineCommitRatio", 0.3, x.MultilineCommitRatio.Detail)
		}
	}
	if x.BulletDensity.Known && x.BulletDensity.Value >= 0.5 && (cold || b.BulletDensity.P90 < 0.3) {
		c.add("text", "BulletDensity", 0.3, x.BulletDensity.Detail)
	}
	if x.EmojiMarkers.Known && x.EmojiMarkers.Value >= 2 {
		c.add("text", "EmojiMarkers", 0.4, x.EmojiMarkers.Detail)
	}
	if x.NarratingCommentRatio.Known && x.NarratingCommentRatio.Value >= 0.3 {
		c.add("text", "NarratingCommentRatio", 0.4, x.NarratingCommentRatio.Detail)
	}
	if x.CommentToCodeRatio.Known {
		limit := math.Max(0.5, 2*b.CommentToCodeRatio.P90)
		if x.CommentToCodeRatio.Value > limit {
			c.add("text", "CommentToCodeRatio", 0.3, x.CommentToCodeRatio.Detail)
		}
	}
	if x.ModelStyleProbability.Known {
		c.add("text", "ModelStyleProbability", clamp((x.ModelStyleProbability.Value-0.5)*1.6, -0.8, 0.8), x.ModelStyleProbability.Detail)
	}

	// ---- code
	k := m.Code
	if k.AdjacentRefactorLines.Known && lines >= 30 && float64(k.AdjacentRefactorLines.Value)/float64(lines) >= 0.2 {
		c.add("code", "AdjacentRefactorLines", 0.4, k.AdjacentRefactorLines.Detail+" beside the actual change")
	}
	if k.DuplicateBlockRatio.Known && k.DuplicateBlockRatio.Value >= 0.3 {
		c.add("code", "DuplicateBlockRatio", 0.3, k.DuplicateBlockRatio.Detail)
	}
	if k.AST.Known {
		ast := k.AST.Value
		if ast.NodesAdded >= 20 && float64(ast.DefensiveChecks)*100/float64(ast.NodesAdded) >= 4 {
			c.add("code", "DefensiveChecks", 0.4, fmt.Sprintf("%d defensive checks in %d new lines", ast.DefensiveChecks, ast.NodesAdded))
		}
		if ast.NodesAdded >= 20 && ast.ConditionalDensity > math.Max(8, 1.5*b.ConditionalDensity.P90) {
			c.add("code", "ConditionalDensity", 0.3, fmt.Sprintf("%.0f conditionals per 100 lines", ast.ConditionalDensity))
		}
		if ast.FunctionsRewritten >= 2 {
			c.add("code", "FunctionsRewritten", 0.3, fmt.Sprintf("%d functions rewritten wholesale", ast.FunctionsRewritten))
		}
	}

	// ---- account and history
	v := b.Verification
	if v.SignedCommitShare.Known && v.SignedCommitShare.Value >= 0.8 && v.ThisPRSigned.Known && !v.ThisPRSigned.Value {
		c.add("baseline", "ThisPRSigned", 0.6, fmt.Sprintf("this author signs %.0f%% of their PRs; these commits aren't signed", v.SignedCommitShare.Value*100))
	}
	if b.DisclosedAgentShare >= 0.3 {
		c.add("baseline", "DisclosedAgentShare", 0.4, fmt.Sprintf("discloses %s in %.0f%% of their PRs, but not this one", orStr(b.UsualAgent, "an agent"), b.DisclosedAgentShare*100))
	}
	if b.CorrectionsAgainst > 0 {
		c.add("baseline", "CorrectionsAgainst", -math.Min(2.4, 0.8*float64(b.CorrectionsAgainst)), fmt.Sprintf("a person has removed this label from their PRs %d time(s) before", b.CorrectionsAgainst))
	}

	// ---- combine
	D := c.sum("disclosure")
	B := clamp(c.sum("time"), -1.5, 3.5)
	T := clamp(c.sum("text"), -2.0, 2.2)
	C := clamp(c.sum("code"), -0.8, 1.5)
	V := clamp(c.sum("baseline"), -2.4, 1.5)
	L := logit(s.Prior) + D + B + T + C + V
	a.Modules = []ModuleTotal{
		{"disclosure", round2(D), round2(D)},
		{"time", round2(c.sum("time")), round2(B)},
		{"text", round2(c.sum("text")), round2(T)},
		{"code", round2(c.sum("code")), round2(C)},
		{"baseline", round2(c.sum("baseline")), round2(V)},
	}

	// evidence count and coverage
	evidence := len(c.list)
	measured, total := 0, 0
	for _, o := range []bool{t.LinesPerMinute.Known, t.InterCommitGapSec.Known, t.BranchToPRSec.Known, t.FirstPushToPRSec.Known,
		t.BurstMaxCommits.Known, t.OffHoursShare.Known, x.TypoRatePer1kWords.Known, x.MultilineCommitRatio.Known,
		x.TemplateSections.Known, k.AST.Known, v.ThisPRSigned.Known} {
		total++
		if o {
			measured++
		}
	}
	u := math.Max(0.5, 1.2-0.12*float64(evidence))
	if cold {
		u += 0.6
	}
	u += 0.4 * float64(total-measured) / float64(total)

	a.Score = int(math.Round(100 * sigmoid(L)))
	a.Low = int(math.Floor(100 * sigmoid(L-u)))
	a.High = int(math.Ceil(100 * sigmoid(L+u)))
	if a.High > 100 {
		a.High = 100
	}
	a.Reasons = c.list
	sort.SliceStable(a.Reasons, func(i, j int) bool { return math.Abs(a.Reasons[i].Effect) > math.Abs(a.Reasons[j].Effect) })
	if len(a.Reasons) > 10 {
		a.Reasons = a.Reasons[:10]
	}
	if D == 0 && measured < 3 {
		a.Abstained = true
		return a
	}
	if float64(a.Low) >= 100*s.LabelAt {
		a.Label = "likely agent"
	}
	return a
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// GuessAgent names the most likely agent when the evidence points at one.
func GuessAgent(m DetectionMatrix) string {
	if m.Disclosure.Agent != "" {
		return m.Disclosure.Agent
	}
	if p := m.Disclosure.AgentBranchPrefix; p.Known && p.Value != "" {
		return AgentBranchPrefixes[p.Value]
	}
	if m.Baseline.UsualAgent != "" {
		return m.Baseline.UsualAgent
	}
	return ""
}

// Render is the markdown block added to the PR comment.
func Render(a Assessment) string {
	var b strings.Builder
	switch {
	case a.Label == "disclosed agent":
		return ""
	case a.Abstained:
		b.WriteString("**agent check** · not enough to go on for this PR\n")
		return b.String()
	case a.Label == "likely agent":
		fmt.Fprintf(&b, "**agent check** · not disclosed, but looks agent-written: **%d** (range %d–%d)\n\n", a.Score, a.Low, a.High)
	default:
		fmt.Fprintf(&b, "**agent check** · looks human-written: %d (range %d–%d)\n\n", a.Score, a.Low, a.High)
	}
	for _, r := range a.Reasons {
		sign := "+"
		if r.Effect < 0 {
			sign = "−"
		}
		fmt.Fprintf(&b, "- %s %s\n", sign, r.Detail)
	}
	b.WriteString("\nadvice only. wrong? remove the `agent:likely` label and Cowboy will weigh this author's PRs more carefully.\n")
	return b.String()
}
