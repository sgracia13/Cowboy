// Package detect holds the data model for spotting agent-written pull
// requests that don't say so ("masked" agent PRs).
//
// This file is data structures and interfaces only. Nothing here computes a
// score. Extractors fill a DetectionMatrix from a PR; a Scorer (written later)
// turns the matrix into an Agent Probability Score.
//
// # How to read the fields
//
// Every measured value is an Observed[T]. Known=false means "we could not
// measure this" (no history, no parser for the language, host API didn't
// expose it). Unknown is not the same as zero: a scorer must skip unknown
// values, never treat them as evidence either way.
//
// Each field's doc comment says which way it points:
//
//	AI marker:    higher (or true) is more typical of an agent
//	Human marker: higher (or true) is more typical of a person
//	Context:      not evidence by itself; used to compare or to weigh
//
// Behavioural values are only meaningful next to the same developer's
// DeveloperBaseline. "800 lines in 10 minutes" is a marker only if it is
// unusual for that person. Fast humans are protected by comparing them to
// themselves, not to everyone.
//
// # Trust in timestamps
//
// Git author and committer dates are set by the client and can be faked or
// reset (rebase, amend, --date). Times recorded by the git host when it
// received a push or opened the PR are harder to fake. Fields say which
// clock they use; prefer host clocks when both exist.
//
// # Use
//
// The score this feeds is advice. It labels a PR "likely agent" for review
// routing and the per-agent tally. It never changes the pen and is never an
// accusation; a human can remove the label in one click.
package detect

import "time"

// SchemaVersion changes whenever a field is added, removed or redefined, so
// stored matrices and trained weights can be matched to the shape they used.
const SchemaVersion = 1

// ---------------------------------------------------------------------------
// Shared building blocks
// ---------------------------------------------------------------------------

// Source says where a value came from, so a scorer can weigh it and a person
// can audit it.
type Source string

const (
	SourceGit         Source = "git"             // commit objects (client-set, forgeable)
	SourceForgeAPI    Source = "forge-api"       // the git host's own records (harder to fake)
	SourceDiff        Source = "diff"            // the unified diff text
	SourceASTRegex    Source = "ast:regex"       // pattern-based parse (approximate)
	SourceASTTree     Source = "ast:tree-sitter" // real syntax tree (precise)
	SourceText        Source = "text"            // PR title, body, commit messages, comments
	SourceLocalModel  Source = "local-model"     // an open-weight model on the user's server
	SourceStore       Source = "store"           // Cowboy's own history of this author
	SourceUnavailable Source = "unavailable"
)

// Observed is one measured value plus whether it could be measured at all.
type Observed[T any] struct {
	Value  T      `json:"value"`
	Known  bool   `json:"known"`
	Source Source `json:"source,omitempty"`
	// Detail is a short, human-readable reason shown next to the score,
	// e.g. "3 commits in 41 s" or "matched 'Certainly! Here is'".
	Detail string `json:"detail,omitempty"`
}

// RobustStat summarises a distribution with numbers that one odd PR can't
// drag around: median and MAD (median absolute deviation) instead of mean
// and standard deviation.
type RobustStat struct {
	Median float64 `json:"median"`
	MAD    float64 `json:"mad"` // spread; z ≈ (x − Median) / (1.4826 × MAD)
	P10    float64 `json:"p10"`
	P90    float64 `json:"p90"`
	N      int     `json:"n"` // samples behind this stat; small N = weak baseline
}

// Span covers a set of numeric observations from one PR (e.g. every gap
// between consecutive commits).
type Span struct {
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
	Count  int     `json:"count"`
}

// ---------------------------------------------------------------------------
// The matrix
// ---------------------------------------------------------------------------

// DetectionMatrix is everything the engine knows about one PR at one head
// commit, ready to hand to a Scorer.
type DetectionMatrix struct {
	Schema   int       `json:"schema"`
	Repo     string    `json:"repo"`
	Number   int       `json:"number"`
	HeadSHA  string    `json:"head_sha"`
	Author   string    `json:"author"` // login on the git host
	Forge    string    `json:"forge"`  // github, gitea, forgejo, gitlab
	Built    time.Time `json:"built"`
	Language []string  `json:"languages"` // languages touched, for picking parsers and weights

	// Disclosure carries the existing rule-based checks. It is context, not
	// one of the four modules: when a PR already declares its agent there is
	// nothing masked to detect, and the scorer can stop early.
	Disclosure Disclosure `json:"disclosure"`

	Time     TimeVelocityMetrics `json:"time"`
	Text     TextualTypology     `json:"text"`
	Code     CodeEntropy         `json:"code"`
	Baseline DeveloperBaseline   `json:"baseline"`
}

// Disclosure is what the PR openly says about agent involvement. These are
// binary facts, already used today by analyze.DetectAgent.
type Disclosure struct {
	// Agent is the declared agent name, "" if none was declared.
	Agent string `json:"agent,omitempty"`
	// How names the evidence: "bot account", "co-author trailer",
	// "pr signature", "agent branch prefix", "agent config file".
	How []string `json:"how,omitempty"`
	// StrippedTrailer is true when a later commit removed an agent
	// co-author line that an earlier commit in the same PR had.
	// AI marker (deliberate masking).
	StrippedTrailer Observed[bool] `json:"stripped_trailer"`
	// AgentBranchPrefix is a branch name like codex/…, cursor/…, copilot/…,
	// claude/…, devin/…. AI marker (strong).
	AgentBranchPrefix Observed[string] `json:"agent_branch_prefix"`
	// AgentConfigTouched lists agent instruction files changed or present in
	// the PR: CLAUDE.md, AGENTS.md, .cursor/rules, .github/copilot-instructions.md.
	// AI marker (moderate: humans edit these too).
	AgentConfigTouched Observed[[]string] `json:"agent_config_touched"`
}

// ---------------------------------------------------------------------------
// Module 1: time and velocity
// ---------------------------------------------------------------------------

// TimeVelocityMetrics is the rhythm of the work: how fast code appeared and
// how evenly. Agents write in bursts with machine-regular gaps; people
// pause, context-switch and work in their own hours.
type TimeVelocityMetrics struct {
	// AuthorCommitterDeltaSec is the gap between author date and committer
	// date per commit (git clock). Context: a rebase or amend makes it large
	// for humans; agents that commit once tend to show ~0 on every commit.
	// AI marker when every commit is ~0 AND commits are many.
	AuthorCommitterDeltaSec Observed[Span] `json:"author_committer_delta_sec"`

	// InterCommitGapSec are the gaps between consecutive commits (git clock).
	// AI marker: small median and small spread (metronome-like).
	// Human marker: long, irregular gaps.
	InterCommitGapSec Observed[Span] `json:"inter_commit_gap_sec"`

	// GapRegularity is the coefficient of variation of InterCommitGapSec
	// (spread ÷ median). AI marker when low: gaps are suspiciously even.
	GapRegularity Observed[float64] `json:"gap_regularity"`

	// BranchToPRSec is the time from the branch's first commit to the PR
	// opening. AI marker when very short for a large diff.
	BranchToPRSec Observed[float64] `json:"branch_to_pr_sec"`

	// FirstPushToPRSec is the time from the host receiving the first push to
	// the PR opening (host clock, hard to fake). AI marker when near 0:
	// agents push and open the PR in one step.
	FirstPushToPRSec Observed[float64] `json:"first_push_to_pr_sec"`

	// BurstMaxCommits is the most commits inside any 60-second window.
	// AI marker when high.
	BurstMaxCommits Observed[int] `json:"burst_max_commits"`

	// LinesPerMinute is changed lines divided by active minutes (time from
	// first to last commit, floored at 1). AI marker when far above this
	// author's baseline; humans with codemods or generators also spike here,
	// so it must be read next to CodeEntropy.BoilerplatePurity.
	LinesPerMinute Observed[float64] `json:"lines_per_minute"`

	// ReviewResponseSec is the time from a "changes requested" review to the
	// next commit (host clock). AI marker when consistently seconds.
	ReviewResponseSec Observed[Span] `json:"review_response_sec"`

	// OffHoursShare is the share of commits outside this author's usual
	// hours (from DeveloperBaseline.ActiveHours). AI marker when high,
	// e.g. a 3 a.m. burst from someone who never works at night.
	OffHoursShare Observed[float64] `json:"off_hours_share"`

	// ForcePushCount is how many times history was rewritten on the PR
	// branch (host events). Human marker when present with long gaps
	// (tidying up); context otherwise.
	ForcePushCount Observed[int] `json:"force_push_count"`

	// CommitCount is the number of commits in the PR. Context for every rate above.
	CommitCount Observed[int] `json:"commit_count"`
}

// ---------------------------------------------------------------------------
// Module 2: textual typology
// ---------------------------------------------------------------------------

// TextualTypology is the style of the words: PR title and body, commit
// messages, and comments added in the diff. Weakest of the four modules on
// its own: careful writers and non-native speakers can look "machine-clean",
// so a scorer must cap this module's weight.
type TextualTypology struct {
	// TypoRatePer1kWords counts misspellings and doubled words per 1,000
	// words of PR body and commit messages. Human marker when above ~0;
	// AI marker only when 0 across a LONG text (WordCount large).
	TypoRatePer1kWords Observed[float64] `json:"typo_rate_per_1k_words"`

	// WordCount is total words analysed. Context: the typo rate means
	// nothing on a 12-word PR.
	WordCount Observed[int] `json:"word_count"`

	// SyntaxPerfection is the share of sentences that are complete, title-cased
	// and end in punctuation, with consistent list formatting. AI marker when
	// near 1.0 across many sentences.
	SyntaxPerfection Observed[float64] `json:"syntax_perfection"`

	// PhraseHits are matches against a versioned list of phrases typical of
	// model output ("Certainly!", "This PR introduces", "I've updated",
	// "comprehensive", "robust", "seamlessly"). AI marker; each hit is weak.
	PhraseHits Observed[[]PhraseHit] `json:"phrase_hits"`
	// PhraseListVersion pins which phrase list produced PhraseHits.
	PhraseListVersion string `json:"phrase_list_version,omitempty"`

	// TemplateSections lists structured headings found in the PR body
	// ("Summary", "Changes", "Test plan", "Testing"). AI marker when the
	// author's own past PRs don't use them; context if the repo has a PR template.
	TemplateSections Observed[[]string] `json:"template_sections"`
	// RepoHasPRTemplate is true when the repo ships a PR template. Context:
	// structured sections are expected and stop counting as a marker.
	RepoHasPRTemplate Observed[bool] `json:"repo_has_pr_template"`

	// BulletDensity is bullet lines divided by total lines in the PR body.
	// AI marker when high (research: strong signal for some agents).
	BulletDensity Observed[float64] `json:"bullet_density"`

	// EmojiMarkers counts checkmark and status emoji (✅ 🚀 ✨ 🤖) in PR text.
	// AI marker.
	EmojiMarkers Observed[int] `json:"emoji_markers"`

	// MultilineCommitRatio is the share of commits whose message has a body
	// below the subject line. AI marker when high (the single strongest
	// text feature in published agent-fingerprinting work).
	MultilineCommitRatio Observed[float64] `json:"multiline_commit_ratio"`

	// ConventionalCommitRatio is the share of commit subjects shaped like
	// "feat: …", "fix(scope): …". Context: strong team conventions make
	// this high for everyone, so compare to the baseline.
	ConventionalCommitRatio Observed[float64] `json:"conventional_commit_ratio"`

	// CommentToCodeRatio is added comment lines divided by added code lines.
	// AI marker when well above the author's baseline.
	CommentToCodeRatio Observed[float64] `json:"comment_to_code_ratio"`

	// NarratingCommentRatio is the share of added comments that restate the
	// next line ("// increment the counter"). AI marker.
	NarratingCommentRatio Observed[float64] `json:"narrating_comment_ratio"`

	// ModelStyleProbability is a local model's estimate (0–1) that the PR
	// text is model-written. AI marker, weak and capped; Source must be
	// SourceLocalModel. Unknown when no model is configured.
	ModelStyleProbability Observed[float64] `json:"model_style_probability"`
}

// PhraseHit is one matched phrase and where it was found.
type PhraseHit struct {
	Phrase string `json:"phrase"`
	Where  string `json:"where"` // "pr_body", "commit:<sha7>", "comment:<path>:<line>"
}

// ---------------------------------------------------------------------------
// Module 3: code entropy
// ---------------------------------------------------------------------------

// CodeEntropy is the shape of the change itself. Agents tend to touch more
// than the task needed, write uniform boilerplate, and restructure code
// wholesale; people make narrower, messier, more idiosyncratic edits.
type CodeEntropy struct {
	// AdjacentRefactorLines counts changed lines outside the hunks the task
	// plausibly needed: reformatting, renames and "while I was here" edits in
	// neighbouring code. AI marker when large relative to LinesChanged.
	AdjacentRefactorLines Observed[int] `json:"adjacent_refactor_lines"`
	// LinesChanged is added + removed lines. Context for every ratio here.
	LinesChanged Observed[int] `json:"lines_changed"`

	// ChangeConcentration is a Gini coefficient of lines changed across
	// files (0 = spread evenly, 1 = all in one file). Context: research shows
	// it separates agents from each other more than from humans; compare to
	// the baseline.
	ChangeConcentration Observed[float64] `json:"change_concentration"`

	// BoilerplatePurity is the share of added lines that match common
	// scaffold patterns or near-duplicate blocks within the PR. AI marker
	// when high, but code generators and codemods also score high, so read
	// it with Disclosure and TimeVelocityMetrics.
	BoilerplatePurity Observed[float64] `json:"boilerplate_purity"`

	// DuplicateBlockRatio is the share of added lines inside blocks of 5+
	// lines that appear more than once in the PR. AI marker.
	DuplicateBlockRatio Observed[float64] `json:"duplicate_block_ratio"`

	// IdentifierEntropy is the Shannon entropy (bits) of identifier tokens
	// added. AI marker when unusually low and uniform (descriptive,
	// predictable names); context per language.
	IdentifierEntropy Observed[float64] `json:"identifier_entropy"`

	// FormattingOnlyLines counts lines whose only change is whitespace,
	// quotes or trailing commas. AI marker when many, outside a declared
	// formatting PR.
	FormattingOnlyLines Observed[int] `json:"formatting_only_lines"`

	// AST is the structural change, from a real or approximate parser.
	AST Observed[ASTDelta] `json:"ast"`
}

// ASTDelta counts structural edits per PR. The Observed wrapper's Source
// says whether a real syntax tree (tree-sitter) or patterns (regex) produced
// it; scorers should trust tree-sitter more.
type ASTDelta struct {
	NodesAdded    int `json:"nodes_added"`
	NodesRemoved  int `json:"nodes_removed"`
	NodesModified int `json:"nodes_modified"`

	// FunctionsAdded and FunctionsRewritten (body replaced > 80%).
	// AI marker when rewrites dominate over small edits.
	FunctionsAdded     int `json:"functions_added"`
	FunctionsRewritten int `json:"functions_rewritten"`

	// ConditionalDensity is if/else/switch/ternary nodes per 100 added
	// lines. AI marker for some agents (defensive branching); compare to baseline.
	ConditionalDensity float64 `json:"conditional_density"`

	// DefensiveChecks counts added null/undefined/empty guards and
	// try/catch blocks. AI marker when many relative to code added.
	DefensiveChecks int `json:"defensive_checks"`

	// ImportsReordered is true when imports were re-sorted without a
	// functional change. AI marker (weak).
	ImportsReordered bool `json:"imports_reordered"`

	// TestsAddedWithCode is true when tests change alongside source.
	// Context only: agents and careful humans both do this.
	TestsAddedWithCode bool `json:"tests_added_with_code"`
}

// ---------------------------------------------------------------------------
// Module 4: developer baseline
// ---------------------------------------------------------------------------

// DeveloperBaseline is this author's normal, built from their own past PRs
// in this Cowboy install. It's what makes behavioural markers fair: every
// rate above is judged against the same person's history.
type DeveloperBaseline struct {
	Author     string    `json:"author"`
	Forge      string    `json:"forge"`
	WindowDays int       `json:"window_days"` // rolling window, e.g. 90
	Updated    time.Time `json:"updated"`

	// SamplePRs is how many of this author's PRs feed the baseline.
	// Context: under ~10, ColdStart is true and the scorer should lean on
	// Disclosure and Text, not on deviations from a thin baseline.
	SamplePRs int  `json:"sample_prs"`
	ColdStart bool `json:"cold_start"`

	// PreAgentSamples counts PRs from before agents were in use (e.g. before
	// 2023) in this author's history. Context: the cleanest "human" baseline
	// there is; prefer it when available.
	PreAgentSamples int `json:"pre_agent_samples"`

	// Speed: the same quantities as TimeVelocityMetrics, as rolling robust
	// stats, so the scorer can compute "how unusual is this PR for them".
	LinesPerMinute    RobustStat `json:"lines_per_minute"`
	InterCommitGapSec RobustStat `json:"inter_commit_gap_sec"`
	BranchToPRSec     RobustStat `json:"branch_to_pr_sec"`
	ReviewResponseSec RobustStat `json:"review_response_sec"`
	CommitsPerPR      RobustStat `json:"commits_per_pr"`

	// ActiveHours is the share of this author's commits in each UTC hour
	// (24 buckets summing to 1). Used to compute OffHoursShare.
	ActiveHours [24]float64 `json:"active_hours"`

	// Style: the same quantities as TextualTypology and CodeEntropy.
	TypoRatePer1kWords      RobustStat `json:"typo_rate_per_1k_words"`
	MultilineCommitRatio    RobustStat `json:"multiline_commit_ratio"`
	ConventionalCommitRatio RobustStat `json:"conventional_commit_ratio"`
	CommentToCodeRatio      RobustStat `json:"comment_to_code_ratio"`
	BulletDensity           RobustStat `json:"bullet_density"`
	ChangeConcentration     RobustStat `json:"change_concentration"`
	ConditionalDensity      RobustStat `json:"conditional_density"`
	// UsesTemplateSections is the share of past PRs with structured headings.
	UsesTemplateSections float64 `json:"uses_template_sections"`

	// Verification: who this account is, from the host and the repo.
	Verification Verification `json:"verification"`

	// DisclosedAgentShare is the share of this author's past PRs that openly
	// declared an agent. Context: someone who usually discloses and suddenly
	// doesn't is a different case from someone who never uses agents.
	DisclosedAgentShare float64 `json:"disclosed_agent_share"`
	// UsualAgent is the agent this author most often discloses, "" if none.
	UsualAgent string `json:"usual_agent,omitempty"`

	// CorrectionsAgainst counts times a human removed a "likely agent" label
	// from this author's PRs. Human marker for future scores: the engine has
	// been wrong about this person before and should ask for more evidence.
	CorrectionsAgainst int `json:"corrections_against"`
}

// Verification describes the account, not the code.
type Verification struct {
	// AccountAgeDays on the git host. Context: brand-new accounts have no baseline.
	AccountAgeDays Observed[int] `json:"account_age_days"`
	// SignedCommitShare is the share of past commits with a verified GPG/SSH
	// signature. A sudden drop on this PR is worth surfacing.
	SignedCommitShare Observed[float64] `json:"signed_commit_share"`
	// ThisPRSigned is true when every commit in this PR is verified-signed.
	ThisPRSigned Observed[bool] `json:"this_pr_signed"`
	// OrgMember is true for members of the repo's organisation. Context.
	OrgMember Observed[bool] `json:"org_member"`
	// IsBotAccount is true when the host marks the account as a bot or app.
	// Already covered by Disclosure; repeated here so the baseline is complete.
	IsBotAccount Observed[bool] `json:"is_bot_account"`
}

// ---------------------------------------------------------------------------
// Inputs and interfaces
// ---------------------------------------------------------------------------

// PRSnapshot is the raw material extractors read. Forge adapters fill it;
// extractors never call the network themselves, so they stay pure and testable.
type PRSnapshot struct {
	Repo, Author, Forge, HeadSHA, Branch string
	Number                               int
	Title, Body                          string
	OpenedAt                             time.Time // host clock
	FirstPushAt                          time.Time // host clock; zero if unknown
	Commits                              []CommitInfo
	Reviews                              []ReviewEvent
	ForcePushes                          []time.Time // host clock
	Files                                []FileChange
	RepoHasPRTemplate                    bool
	// ForcePushesKnown is true when the host reported force pushes (so an
	// empty ForcePushes means "none", not "unknown").
	ForcePushesKnown bool
	// AuthorCreated is when the author's account was made (host); zero if unknown.
	AuthorCreated time.Time
	// OrgMember is the host's view of the author's relationship to the repo;
	// nil if unknown.
	OrgMember *bool
	// DisclosedAgent and DisclosedHow come from the existing rule-based
	// checks (bot account, co-author trailer, PR signature).
	DisclosedAgent, DisclosedHow string
	// CommitsKnown is false when the host didn't give commit timing at all.
	CommitsKnown bool
}

// CommitInfo is one commit as git and the host report it.
type CommitInfo struct {
	SHA           string
	Message       string
	AuthorDate    time.Time // git clock, client-set
	CommitterDate time.Time // git clock, client-set
	PushedAt      time.Time // host clock; zero if the host doesn't expose it
	Verified      bool      // host-verified signature
	CoAuthors     []string  // parsed Co-authored-by trailers
	LinesAdded    int
	LinesRemoved  int
	ChangedPaths  []string
}

// ReviewEvent is a review on the PR (host clock).
type ReviewEvent struct {
	State string // "changes_requested", "approved", "commented"
	At    time.Time
}

// FileChange is one file in the diff, with enough to run text and AST
// extractors. Old/New are full file contents when the host provides them.
type FileChange struct {
	Path, OldPath, Status, Language string
	Added, Removed                  []string
	Old, New                        []byte
}

// Extractor fills one module of the matrix. Each module has its own
// extractor so they can be built, tested and switched off independently.
type Extractor interface {
	// Module is "disclosure", "time", "text", "code" or "baseline".
	Module() string
	// Extract reads the snapshot (and the author's baseline when it needs
	// one) and writes only its own module of m. It returns an error only for
	// bugs; missing data is recorded as Known=false, not as an error.
	Extract(s PRSnapshot, base DeveloperBaseline, m *DetectionMatrix) error
}

// BaselineStore keeps each author's rolling baseline.
type BaselineStore interface {
	Get(forge, author string) (DeveloperBaseline, error) // zero value + ColdStart for new authors
	// Observe folds a finished PR's matrix into the author's baseline. Call
	// it only after the PR is merged or closed, so open PRs can't shift the
	// baseline they are being judged against.
	Observe(m DetectionMatrix) error
	// Correct records that a human removed a "likely agent" label.
	Correct(forge, author string, repo string, number int) error
}

// Scorer turns a matrix into an Agent Probability Score. The math is not
// written yet; this fixes the contract.
type Scorer interface {
	Score(m DetectionMatrix) Assessment
	Version() string // pins weights and rules, stored with every Assessment
}

// Assessment is the scorer's output.
type Assessment struct {
	// Score is 0–100, the scorer's best estimate that an agent wrote the PR.
	Score int `json:"score"`
	// Low and High bound the estimate. Labels act on Low, never on Score,
	// so thin evidence can't produce a confident label.
	Low  int `json:"low"`
	High int `json:"high"`
	// Label is "", "likely agent" or "disclosed agent".
	Label string `json:"label,omitempty"`
	// Reasons are the top contributions, in plain words, shown on the PR.
	Reasons []Contribution `json:"reasons"`
	// Modules are each module's total before and after its cap, so a
	// person can see when a module hit its ceiling.
	Modules []ModuleTotal `json:"modules,omitempty"`
	// Abstained is true when evidence was too thin to say anything
	// (e.g. cold-start author, tiny PR, no text).
	Abstained bool   `json:"abstained"`
	Scorer    string `json:"scorer"` // Scorer.Version()
	Schema    int    `json:"schema"`
}

// ModuleTotal is one module's evidence: Raw is the sum of its
// contributions, Counted is what entered the score after the module's cap.
type ModuleTotal struct {
	Module  string  `json:"module"`
	Raw     float64 `json:"raw"`
	Counted float64 `json:"counted"`
}

// Contribution is one field's effect on the score, for explaining it.
type Contribution struct {
	Module string  `json:"module"` // disclosure, time, text, code, baseline
	Field  string  `json:"field"`  // e.g. "LinesPerMinute"
	Detail string  `json:"detail"` // e.g. "62 lines/min, usual for this author is 4"
	Effect float64 `json:"effect"` // signed, in the scorer's own units
}
