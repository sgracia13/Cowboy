package detect

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// example is a hand-filled matrix for a masked agent PR: opened from a
// personal account with no disclosure, but on a codex/ branch, pushed and
// opened in one step, far faster than this author usually works, with
// textbook model-style text. It shows the shape a Scorer will receive.
func example() DetectionMatrix {
	t := time.Date(2026, 10, 18, 3, 12, 0, 0, time.UTC)
	return DetectionMatrix{
		Schema: SchemaVersion, Repo: "acme/app", Number: 540, HeadSHA: "9f3c2e1",
		Author: "jdoe", Forge: "github", Built: t, Language: []string{"typescript"},
		Disclosure: Disclosure{
			AgentBranchPrefix:  Observed[string]{Value: "codex/", Known: true, Source: SourceForgeAPI, Detail: "branch codex/add-retry"},
			StrippedTrailer:    Observed[bool]{Value: false, Known: true, Source: SourceGit},
			AgentConfigTouched: Observed[[]string]{Value: nil, Known: true, Source: SourceDiff},
		},
		Time: TimeVelocityMetrics{
			CommitCount:       Observed[int]{Value: 4, Known: true, Source: SourceGit},
			InterCommitGapSec: Observed[Span]{Value: Span{Min: 9, Median: 12, Max: 15, Count: 3}, Known: true, Source: SourceGit},
			GapRegularity:     Observed[float64]{Value: 0.21, Known: true, Source: SourceGit, Detail: "gaps of 9–15 s"},
			FirstPushToPRSec:  Observed[float64]{Value: 4, Known: true, Source: SourceForgeAPI, Detail: "PR opened 4 s after first push"},
			BurstMaxCommits:   Observed[int]{Value: 4, Known: true, Source: SourceGit, Detail: "4 commits in 41 s"},
			LinesPerMinute:    Observed[float64]{Value: 310, Known: true, Source: SourceGit},
			OffHoursShare:     Observed[float64]{Value: 1, Known: true, Source: SourceStore, Detail: "all commits at 3 a.m.; author usually works 9–6"},
			ReviewResponseSec: Observed[Span]{Known: false, Source: SourceUnavailable},
		},
		Text: TextualTypology{
			WordCount:            Observed[int]{Value: 214, Known: true, Source: SourceText},
			TypoRatePer1kWords:   Observed[float64]{Value: 0, Known: true, Source: SourceText},
			TemplateSections:     Observed[[]string]{Value: []string{"Summary", "Changes", "Testing"}, Known: true, Source: SourceText},
			RepoHasPRTemplate:    Observed[bool]{Value: false, Known: true, Source: SourceForgeAPI},
			MultilineCommitRatio: Observed[float64]{Value: 1, Known: true, Source: SourceGit},
			PhraseHits: Observed[[]PhraseHit]{Value: []PhraseHit{
				{Phrase: "This PR introduces", Where: "pr_body"},
				{Phrase: "comprehensive", Where: "pr_body"},
			}, Known: true, Source: SourceText},
			PhraseListVersion:     "2026-10",
			ModelStyleProbability: Observed[float64]{Known: false, Source: SourceUnavailable, Detail: "no local model configured"},
		},
		Code: CodeEntropy{
			LinesChanged:          Observed[int]{Value: 186, Known: true, Source: SourceDiff},
			AdjacentRefactorLines: Observed[int]{Value: 41, Known: true, Source: SourceDiff},
			AST: Observed[ASTDelta]{Value: ASTDelta{
				NodesAdded: 92, FunctionsAdded: 3, FunctionsRewritten: 2,
				ConditionalDensity: 9.1, DefensiveChecks: 7, TestsAddedWithCode: true,
			}, Known: true, Source: SourceASTRegex},
		},
		Baseline: DeveloperBaseline{
			Author: "jdoe", Forge: "github", WindowDays: 90, Updated: t, SamplePRs: 37, PreAgentSamples: 112,
			LinesPerMinute:       RobustStat{Median: 4.2, MAD: 2.1, P10: 0.8, P90: 11, N: 37},
			MultilineCommitRatio: RobustStat{Median: 0.05, MAD: 0.05, N: 37},
			TypoRatePer1kWords:   RobustStat{Median: 6.5, MAD: 2.4, N: 37},
			UsesTemplateSections: 0,
			DisclosedAgentShare:  0,
			Verification: Verification{
				AccountAgeDays:    Observed[int]{Value: 2210, Known: true, Source: SourceForgeAPI},
				SignedCommitShare: Observed[float64]{Value: 0.9, Known: true, Source: SourceForgeAPI},
				ThisPRSigned:      Observed[bool]{Value: false, Known: true, Source: SourceForgeAPI, Detail: "usually signs commits; these aren't signed"},
				OrgMember:         Observed[bool]{Value: true, Known: true, Source: SourceForgeAPI},
			},
		},
	}
}

// The matrix survives a JSON round trip unchanged, so it can be stored with
// each decision and replayed through a newer scorer later.
func TestMatrixRoundTrip(t *testing.T) {
	m := example()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var back DetectionMatrix
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, back) {
		t.Fatalf("round trip changed the matrix")
	}
	if m.Text.ModelStyleProbability.Known {
		t.Fatal("unknown must stay unknown, not zero")
	}
}
