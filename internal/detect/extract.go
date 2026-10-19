package detect

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Build runs every extractor and returns a filled matrix. It is pure: the
// same snapshot and baseline always give the same matrix.
func Build(s PRSnapshot, base DeveloperBaseline, built time.Time) DetectionMatrix {
	m := DetectionMatrix{Schema: SchemaVersion, Repo: s.Repo, Number: s.Number, HeadSHA: s.HeadSHA,
		Author: s.Author, Forge: s.Forge, Built: built.UTC(), Language: languages(s.Files)}
	for _, e := range Extractors() {
		e.Extract(s, base, &m)
	}
	return m
}

// Extractors is the default set, one per module.
func Extractors() []Extractor {
	return []Extractor{disclosureX{}, timeX{}, textX{}, codeX{}, baselineX{}}
}

func known[T any](v T, src Source, detail string, args ...any) Observed[T] {
	return Observed[T]{Value: v, Known: true, Source: src, Detail: fmt.Sprintf(detail, args...)}
}

func unknown[T any](why string) Observed[T] {
	return Observed[T]{Source: SourceUnavailable, Detail: why}
}

// ---------------------------------------------------------------- disclosure

// AgentBranchPrefixes maps branch prefixes that agents create by default to
// the agent's name.
var AgentBranchPrefixes = map[string]string{
	"codex/": "codex", "copilot/": "copilot", "cursor/": "cursor", "claude/": "claude-code",
	"devin/": "devin", "jules/": "jules", "aider/": "aider", "openhands/": "openhands",
	"sweep/": "sweep", "amazon-q/": "amazon-q",
}

// AgentConfigFiles are instruction files agents read.
var AgentConfigFiles = []string{"CLAUDE.md", "AGENTS.md", ".cursorrules", ".cursor/rules", ".github/copilot-instructions.md", ".windsurfrules", "GEMINI.md"}

type disclosureX struct{}

func (disclosureX) Module() string { return "disclosure" }
func (disclosureX) Extract(s PRSnapshot, _ DeveloperBaseline, m *DetectionMatrix) error {
	d := Disclosure{Agent: s.DisclosedAgent}
	if s.DisclosedHow != "" {
		d.How = append(d.How, s.DisclosedHow)
	}
	if s.Branch == "" {
		d.AgentBranchPrefix = unknown[string]("branch name not available")
	} else {
		d.AgentBranchPrefix = Observed[string]{Known: true, Source: SourceForgeAPI}
		lb := strings.ToLower(s.Branch)
		for p := range AgentBranchPrefixes {
			if strings.HasPrefix(lb, p) {
				d.AgentBranchPrefix = known(p, SourceForgeAPI, "branch %s", s.Branch)
			}
		}
	}
	var touched []string
	for _, f := range s.Files {
		for _, c := range AgentConfigFiles {
			if f.Path == c || strings.HasPrefix(f.Path, c+"/") || path.Base(f.Path) == c {
				touched = append(touched, f.Path)
			}
		}
	}
	d.AgentConfigTouched = known(touched, SourceDiff, "%d agent instruction file(s) in the diff", len(touched))
	// Seeing a trailer get removed needs the PR's edit history, which most
	// hosts don't expose through one call. Left unknown in v0.
	d.StrippedTrailer = unknown[bool]("needs PR edit history")
	m.Disclosure = d
	return nil
}

// --------------------------------------------------------------------- time

type timeX struct{}

func (timeX) Module() string { return "time" }
func (timeX) Extract(s PRSnapshot, base DeveloperBaseline, m *DetectionMatrix) error {
	t := &m.Time
	n := len(s.Commits)
	if !s.CommitsKnown || n == 0 {
		why := "the host gave no commit times"
		t.CommitCount, t.AuthorCommitterDeltaSec, t.InterCommitGapSec = unknown[int](why), unknown[Span](why), unknown[Span](why)
		t.GapRegularity, t.BranchToPRSec, t.BurstMaxCommits = unknown[float64](why), unknown[float64](why), unknown[int](why)
		t.LinesPerMinute, t.OffHoursShare = unknown[float64](why), unknown[float64](why)
	} else {
		cs := append([]CommitInfo(nil), s.Commits...)
		sort.Slice(cs, func(i, j int) bool { return cs[i].CommitterDate.Before(cs[j].CommitterDate) })
		t.CommitCount = known(n, SourceGit, "%d commit(s)", n)

		var deltas []float64
		for _, c := range cs {
			deltas = append(deltas, math.Abs(c.CommitterDate.Sub(c.AuthorDate).Seconds()))
		}
		t.AuthorCommitterDeltaSec = known(span(deltas), SourceGit, "author vs committer date gaps")

		var gaps []float64
		for i := 1; i < n; i++ {
			gaps = append(gaps, cs[i].CommitterDate.Sub(cs[i-1].CommitterDate).Seconds())
		}
		if len(gaps) == 0 {
			t.InterCommitGapSec = unknown[Span]("only one commit")
			t.GapRegularity = unknown[float64]("only one commit")
		} else {
			sp := span(gaps)
			t.InterCommitGapSec = known(sp, SourceGit, "gaps of %s to %s between commits", dur(sp.Min), dur(sp.Max))
			if len(gaps) >= 2 && sp.Median > 0 {
				t.GapRegularity = known(mad(gaps)*1.4826/sp.Median, SourceGit, "gap spread vs median")
			} else {
				t.GapRegularity = unknown[float64]("needs 3+ commits")
			}
		}

		first, last := cs[0], cs[n-1]
		if !s.OpenedAt.IsZero() {
			t.BranchToPRSec = known(math.Max(0, s.OpenedAt.Sub(first.AuthorDate).Seconds()), SourceGit,
				"PR opened %s after the first commit", dur(s.OpenedAt.Sub(first.AuthorDate).Seconds()))
		} else {
			t.BranchToPRSec = unknown[float64]("PR open time not available")
		}

		burst := 0
		for i := range cs {
			k := 0
			for j := i; j < n && cs[j].CommitterDate.Sub(cs[i].CommitterDate) <= time.Minute; j++ {
				k++
			}
			burst = max(burst, k)
		}
		t.BurstMaxCommits = known(burst, SourceGit, "%d commit(s) within one minute", burst)

		lines := 0
		for _, c := range cs {
			lines += c.LinesAdded + c.LinesRemoved
		}
		if lines == 0 {
			for _, f := range s.Files {
				lines += len(f.Added) + len(f.Removed)
			}
		}
		active := last.CommitterDate.Sub(first.AuthorDate).Minutes()
		if active < 1 {
			active = 1
		}
		lpm := float64(lines) / active
		t.LinesPerMinute = known(lpm, SourceGit, "%.0f lines in %s (%.0f lines/min)", float64(lines), dur(active*60), lpm)

		if hoursKnown(base.ActiveHours) && base.SamplePRs >= 5 {
			off := 0
			for _, c := range cs {
				if base.ActiveHours[c.AuthorDate.UTC().Hour()] < 0.02 {
					off++
				}
			}
			t.OffHoursShare = known(float64(off)/float64(n), SourceStore, "%d of %d commits outside this author's usual hours (%s UTC)", off, n, first.AuthorDate.UTC().Format("15:04"))
		} else {
			t.OffHoursShare = unknown[float64]("no usual hours for this author yet")
		}
	}

	if !s.FirstPushAt.IsZero() && !s.OpenedAt.IsZero() {
		v := math.Max(0, s.OpenedAt.Sub(s.FirstPushAt).Seconds())
		t.FirstPushToPRSec = known(v, SourceForgeAPI, "PR opened %s after the first push", dur(v))
	} else {
		t.FirstPushToPRSec = unknown[float64]("host didn't report the first push time")
	}

	var resp []float64
	for _, r := range s.Reviews {
		if r.State != "changes_requested" {
			continue
		}
		for _, c := range s.Commits {
			if c.CommitterDate.After(r.At) {
				resp = append(resp, c.CommitterDate.Sub(r.At).Seconds())
				break
			}
		}
	}
	if len(resp) > 0 {
		sp := span(resp)
		t.ReviewResponseSec = known(sp, SourceForgeAPI, "new commits %s after changes were requested", dur(sp.Median))
	} else {
		t.ReviewResponseSec = unknown[Span]("no changes-requested reviews answered yet")
	}

	if s.ForcePushesKnown {
		t.ForcePushCount = known(len(s.ForcePushes), SourceForgeAPI, "%d force push(es)", len(s.ForcePushes))
	} else {
		t.ForcePushCount = unknown[int]("host didn't report force pushes")
	}
	return nil
}

// --------------------------------------------------------------------- text

// PhraseListVersion pins LLMPhrases.
const phraseListVersion = "2026-10"

// LLMPhrases are stock phrases common in model-written PR text. Each hit is
// weak evidence on its own.
var LLMPhrases = []string{
	"certainly!", "this pr introduces", "this pull request introduces", "i've updated", "i have updated",
	"comprehensive", "robust", "seamlessly", "ensures that", "enhance", "streamline", "leverage",
	"in summary", "key changes", "the following changes", "let me know if", "as requested",
	"improves maintainability", "for better readability", "best practices", "edge cases",
}

// commonTypos is a small list of frequent misspellings. A real spell checker
// can replace it later; this keeps the binary dependency-free.
var commonTypos = []string{
	"teh", "recieve", "seperate", "definately", "occured", "untill", "wich", "becuase", "accomodate",
	"adress", "arguement", "begining", "beleive", "calender", "comming", "dependancy", "enviroment",
	"existance", "foward", "fucntion", "funtion", "lenght", "neccessary", "occurence", "paramater",
	"recieved", "refered", "succesful", "tommorow", "truely", "writting", "retun", "retrun", "lgtm?",
	"thier", "doesnt", "dont", "cant", "wont", "isnt", "im", "ive", "u", "pls", "thx", "wip",
}

var (
	reWord        = regexp.MustCompile(`[A-Za-z']+`)
	reHeading     = regexp.MustCompile(`(?m)^\s{0,3}(#{1,4})\s+(.+?)\s*$|^\s*\*\*([^*]{2,40})\*\*:?\s*$`)
	reBullet      = regexp.MustCompile(`^\s*([-*+]|\d+\.)\s+`)
	reConvCommit  = regexp.MustCompile(`^(feat|fix|chore|docs|refactor|test|perf|build|ci|style|revert)(\([^)]*\))?!?:\s`)
	emojiMarkers  = []string{"✅", "🚀", "✨", "🤖", "📝", "🔧", "🐛", "♻️", "⚡", "🎉", "✔️", "📦"}
	knownSections = []string{"summary", "changes", "what changed", "test plan", "testing", "how to test", "motivation", "why", "notes", "screenshots", "checklist", "description", "overview", "key changes"}
)

type textX struct{}

func (textX) Module() string { return "text" }
func (textX) Extract(s PRSnapshot, _ DeveloperBaseline, m *DetectionMatrix) error {
	t := &m.Text
	var msgs []string
	for _, c := range s.Commits {
		msgs = append(msgs, c.Message)
	}
	prose := strings.TrimSpace(s.Title + "\n" + s.Body + "\n" + strings.Join(msgs, "\n"))
	words := reWord.FindAllString(prose, -1)
	t.WordCount = known(len(words), SourceText, "%d words of PR text and commit messages", len(words))

	typos := 0
	typoSet := map[string]bool{}
	for _, w := range commonTypos {
		typoSet[w] = true
	}
	var found []string
	for _, w := range words {
		if typoSet[strings.ToLower(w)] {
			typos++
			found = append(found, w)
		}
	}
	for i := 1; i < len(words); i++ { // a word typed twice in a row ("the the")
		if len(words[i]) > 1 && strings.EqualFold(words[i], words[i-1]) {
			typos++
			found = append(found, words[i]+" "+words[i])
		}
	}
	typos += strings.Count(prose, " i ") // lowercase "i"
	if len(words) >= 20 {
		rate := float64(typos) * 1000 / float64(len(words))
		d := fmt.Sprintf("%d typo-like slip(s) in %d words", typos, len(words))
		if len(found) > 0 {
			d += fmt.Sprintf(" (e.g. %q)", found[0])
		}
		t.TypoRatePer1kWords = Observed[float64]{Value: rate, Known: true, Source: SourceText, Detail: d}
	} else {
		t.TypoRatePer1kWords = unknown[float64]("too little text to judge")
	}

	sentences, perfect := 0, 0
	for _, line := range strings.Split(s.Body+"\n"+strings.Join(msgs, "\n"), "\n") {
		line = strings.TrimSpace(reBullet.ReplaceAllString(line, ""))
		if len(reWord.FindAllString(line, -1)) < 4 || strings.HasPrefix(line, "#") || strings.HasPrefix(strings.ToLower(line), "co-authored-by") {
			continue
		}
		sentences++
		r := []rune(line)
		if unicode.IsUpper(r[0]) && strings.ContainsRune(".!?:", r[len(r)-1]) {
			perfect++
		}
	}
	if sentences >= 3 {
		t.SyntaxPerfection = known(float64(perfect)/float64(sentences), SourceText, "%d of %d sentences capitalised and punctuated", perfect, sentences)
	} else {
		t.SyntaxPerfection = unknown[float64]("fewer than 3 sentences")
	}

	low := strings.ToLower(prose)
	var hits []PhraseHit
	for _, p := range LLMPhrases {
		if strings.Contains(low, p) {
			where := "commit message"
			if strings.Contains(strings.ToLower(s.Title+"\n"+s.Body), p) {
				where = "pr_body"
			}
			hits = append(hits, PhraseHit{Phrase: p, Where: where})
		}
	}
	d := "no stock phrases"
	if len(hits) > 0 {
		var ps []string
		for _, h := range hits {
			ps = append(ps, fmt.Sprintf("%q", h.Phrase))
		}
		d = "stock phrases: " + strings.Join(ps, ", ")
	}
	t.PhraseHits = Observed[[]PhraseHit]{Value: hits, Known: true, Source: SourceText, Detail: d}
	t.PhraseListVersion = phraseListVersion

	var secs []string
	for _, g := range reHeading.FindAllStringSubmatch(s.Body, -1) {
		name := strings.ToLower(strings.Trim(g[2]+g[3], " :"))
		for _, k := range knownSections {
			if name == k {
				secs = append(secs, strings.Trim(g[2]+g[3], " :"))
			}
		}
	}
	t.TemplateSections = known(secs, SourceText, "headings: %s", strings.Join(secs, ", "))
	t.RepoHasPRTemplate = known(s.RepoHasPRTemplate, SourceForgeAPI, "repo PR template: %v", s.RepoHasPRTemplate)

	bodyLines, bullets := 0, 0
	for _, l := range strings.Split(s.Body, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		bodyLines++
		if reBullet.MatchString(l) {
			bullets++
		}
	}
	if bodyLines >= 3 {
		t.BulletDensity = known(float64(bullets)/float64(bodyLines), SourceText, "%d of %d body lines are bullets", bullets, bodyLines)
	} else {
		t.BulletDensity = unknown[float64]("PR body too short")
	}
	em := 0
	for _, e := range emojiMarkers {
		em += strings.Count(s.Body, e)
	}
	t.EmojiMarkers = known(em, SourceText, "%d status emoji", em)

	if len(msgs) > 0 {
		multi, conv := 0, 0
		for _, msg := range msgs {
			body := strings.TrimSpace(stripTrailers(msg))
			if i := strings.Index(body, "\n"); i >= 0 && strings.TrimSpace(body[i:]) != "" {
				multi++
			}
			if reConvCommit.MatchString(body) {
				conv++
			}
		}
		t.MultilineCommitRatio = known(float64(multi)/float64(len(msgs)), SourceGit, "%d of %d commit messages have a body", multi, len(msgs))
		t.ConventionalCommitRatio = known(float64(conv)/float64(len(msgs)), SourceGit, "%d of %d conventional commits", conv, len(msgs))
	} else {
		t.MultilineCommitRatio = unknown[float64]("no commit messages")
		t.ConventionalCommitRatio = unknown[float64]("no commit messages")
	}

	comments, code, narrating := 0, 0, 0
	for _, f := range s.Files {
		if classOf(f.Path) != "source" && classOf(f.Path) != "test" {
			continue
		}
		for i, l := range f.Added {
			x := strings.TrimSpace(l)
			if x == "" || isBraceOnly(x) {
				continue
			}
			if isComment(x) {
				comments++
				if i+1 < len(f.Added) && narrates(x, f.Added[i+1]) {
					narrating++
				}
			} else {
				code++
			}
		}
	}
	if code >= 5 {
		t.CommentToCodeRatio = known(float64(comments)/float64(code), SourceDiff, "%d comment lines for %d code lines", comments, code)
	} else {
		t.CommentToCodeRatio = unknown[float64]("too little code added")
	}
	if comments >= 3 {
		t.NarratingCommentRatio = known(float64(narrating)/float64(comments), SourceDiff, "%d of %d comments restate the next line", narrating, comments)
	} else {
		t.NarratingCommentRatio = unknown[float64]("fewer than 3 comments added")
	}
	t.ModelStyleProbability = unknown[float64]("no local text model configured")
	return nil
}

// --------------------------------------------------------------------- code

var (
	reIdent       = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)
	reFuncDecl    = regexp.MustCompile(`^\s*(export\s+)?(async\s+)?(func|def|function|fn)\s+\w+|^\s*(public|private|protected|static|\s)*[\w<>\[\]]+\s+\w+\s*\([^;]*\)\s*\{\s*$|^\s*(export\s+)?const\s+\w+\s*=\s*(async\s*)?\([^)]*\)\s*=>`)
	reConditional = regexp.MustCompile(`\b(if|else if|elif|switch|case|when)\b|\?[^:?]+:`)
	reDefensive   = regexp.MustCompile(`(== ?null|=== ?undefined|!= ?nil|== ?nil|is None|is not None|\?\?|\?\.|\btry\b|\bcatch\b|\bexcept\b|len\(\w+\) ?== ?0|\.length ?=== ?0|throw new Error)`)
	reImport      = regexp.MustCompile(`^\s*(import\s|from\s+\S+\s+import|#include|use\s+\w|require\()`)
	reKeywords    = map[string]bool{"return": true, "const": true, "function": true, "import": true, "export": true, "await": true, "async": true, "true": true, "false": true, "null": true, "nil": true, "None": true, "def": true, "func": true, "var": true, "let": true, "new": true, "this": true, "self": true, "else": true, "for": true, "while": true, "string": true, "int": true, "err": true}
)

type codeX struct{}

func (codeX) Module() string { return "code" }
func (codeX) Extract(s PRSnapshot, base DeveloperBaseline, m *DetectionMatrix) error {
	c := &m.Code
	total, formatting, adjacent := 0, 0, 0
	perFile := []float64{}
	var added []string
	var ast ASTDelta
	srcAdded, srcCount := 0, 0
	testsTouched, srcTouched := false, false
	for _, f := range s.Files {
		n := len(f.Added) + len(f.Removed)
		total += n
		perFile = append(perFile, float64(n))
		cls := classOf(f.Path)
		if cls == "test" {
			testsTouched = true
		}
		if cls != "source" && cls != "test" {
			continue
		}
		if cls == "source" {
			srcTouched = true
		}
		// pair removed/added lines in order: a cheap stand-in for hunk alignment.
		// Paired lines are reformat/rename edits and are left out of the
		// structural counts below.
		k := min(len(f.Added), len(f.Removed))
		pairedA, pairedR := map[int]bool{}, map[int]bool{}
		for i := 0; i < k; i++ {
			a, r := f.Added[i], f.Removed[i]
			if normWS(a) == normWS(r) {
				formatting++
				adjacent++
				pairedA[i], pairedR[i] = true, true
			} else if normIdents(a) == normIdents(r) {
				adjacent++ // a rename-only edit
				pairedA[i], pairedR[i] = true, true
			}
		}
		remSet, addSet := map[string]bool{}, map[string]bool{}
		for _, l := range f.Removed {
			if reImport.MatchString(l) {
				remSet[strings.TrimSpace(l)] = true
			}
		}
		for _, l := range f.Added {
			if reImport.MatchString(l) {
				addSet[strings.TrimSpace(l)] = true
			}
		}
		if len(addSet) > 1 && len(addSet) == len(remSet) {
			same := true
			for l := range addSet {
				if !remSet[l] {
					same = false
				}
			}
			ast.ImportsReordered = ast.ImportsReordered || same
		}
		removedFuncs, addedFuncs := 0, 0
		for i, l := range f.Removed {
			if !pairedR[i] && reFuncDecl.MatchString(l) {
				removedFuncs++
			}
		}
		for i, l := range f.Added {
			x := strings.TrimSpace(l)
			if x == "" || isComment(x) || pairedA[i] {
				continue
			}
			srcCount++
			if cls == "source" {
				srcAdded++
			}
			added = append(added, x)
			if reFuncDecl.MatchString(l) {
				ast.FunctionsAdded++
				addedFuncs++
			}
			ast.ConditionalDensity += float64(len(reConditional.FindAllString(x, -1)))
			ast.DefensiveChecks += len(reDefensive.FindAllString(x, -1))
			ast.NodesAdded++
		}
		for i, l := range f.Removed {
			if strings.TrimSpace(l) != "" && !pairedR[i] {
				ast.NodesRemoved++
			}
		}
		ast.FunctionsRewritten += min(removedFuncs, addedFuncs)
	}
	ast.NodesModified = min(ast.NodesAdded, ast.NodesRemoved)
	if srcCount > 0 {
		ast.ConditionalDensity = ast.ConditionalDensity * 100 / float64(srcCount)
	}
	ast.TestsAddedWithCode = testsTouched && srcTouched
	c.LinesChanged = known(total, SourceDiff, "%d lines changed", total)
	c.AdjacentRefactorLines = known(adjacent, SourceDiff, "%d line(s) only reformatted or renamed", adjacent)
	c.FormattingOnlyLines = known(formatting, SourceDiff, "%d whitespace/quote-only line(s)", formatting)
	if len(perFile) >= 2 {
		c.ChangeConcentration = known(gini(perFile), SourceDiff, "change concentration across %d files", len(perFile))
	} else {
		c.ChangeConcentration = unknown[float64]("one file")
	}
	if len(added) >= 10 {
		dup := duplicateShare(added, 4)
		c.DuplicateBlockRatio = known(dup, SourceDiff, "%.0f%% of added lines sit in repeated blocks", dup*100)
		c.BoilerplatePurity = known(boilerShare(added), SourceDiff, "share of added lines that are scaffold patterns")
		c.IdentifierEntropy = known(identEntropy(added), SourceDiff, "identifier entropy")
	} else {
		why := "fewer than 10 code lines added"
		c.DuplicateBlockRatio, c.BoilerplatePurity, c.IdentifierEntropy = unknown[float64](why), unknown[float64](why), unknown[float64](why)
	}
	if srcCount > 0 {
		c.AST = known(ast, SourceASTRegex, "%d function(s) added, %d conditional(s) per 100 lines, %d defensive check(s)",
			ast.FunctionsAdded, int(math.Round(ast.ConditionalDensity)), ast.DefensiveChecks)
	} else {
		c.AST = unknown[ASTDelta]("no source code added")
	}
	_ = srcAdded
	return nil
}

// ----------------------------------------------------------------- baseline

type baselineX struct{}

func (baselineX) Module() string { return "baseline" }
func (baselineX) Extract(s PRSnapshot, base DeveloperBaseline, m *DetectionMatrix) error {
	b := base
	if b.Author == "" {
		b.Author, b.Forge, b.ColdStart = s.Author, s.Forge, true
	}
	v := &b.Verification
	if !s.AuthorCreated.IsZero() && !s.OpenedAt.IsZero() {
		days := int(s.OpenedAt.Sub(s.AuthorCreated).Hours() / 24)
		v.AccountAgeDays = known(days, SourceForgeAPI, "account %d days old", days)
	} else {
		v.AccountAgeDays = unknown[int]("host didn't give the account's age")
	}
	if s.CommitsKnown && len(s.Commits) > 0 {
		all := true
		for _, c := range s.Commits {
			all = all && c.Verified
		}
		v.ThisPRSigned = known(all, SourceForgeAPI, "every commit signed: %v", all)
	} else {
		v.ThisPRSigned = unknown[bool]("no commit data")
	}
	if s.OrgMember != nil {
		v.OrgMember = known(*s.OrgMember, SourceForgeAPI, "org member: %v", *s.OrgMember)
	} else {
		v.OrgMember = unknown[bool]("host didn't say")
	}
	bot := strings.HasSuffix(strings.ToLower(s.Author), "[bot]")
	v.IsBotAccount = known(bot, SourceForgeAPI, "bot account: %v", bot)
	m.Baseline = b
	return nil
}

// ------------------------------------------------------------------ helpers

func span(xs []float64) Span {
	if len(xs) == 0 {
		return Span{}
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return Span{Min: s[0], Median: median(s), Max: s[len(s)-1], Count: len(s)}
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func mad(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	md := median(s)
	dev := make([]float64, len(s))
	for i, x := range s {
		dev[i] = math.Abs(x - md)
	}
	sort.Float64s(dev)
	return median(dev)
}

func dur(sec float64) string {
	switch {
	case sec < 90:
		return fmt.Sprintf("%.0f s", sec)
	case sec < 5400:
		return fmt.Sprintf("%.0f min", sec/60)
	case sec < 2*86400:
		return fmt.Sprintf("%.1f h", sec/3600)
	}
	return fmt.Sprintf("%.1f days", sec/86400)
}

func hoursKnown(h [24]float64) bool {
	sum := 0.0
	for _, x := range h {
		sum += x
	}
	return sum > 0.5
}

func stripTrailers(msg string) string {
	var keep []string
	for _, l := range strings.Split(msg, "\n") {
		ll := strings.ToLower(strings.TrimSpace(l))
		if strings.HasPrefix(ll, "co-authored-by:") || strings.HasPrefix(ll, "signed-off-by:") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

func classOf(p string) string {
	lp := strings.ToLower(p)
	switch {
	case strings.Contains(lp, "test") || strings.Contains(lp, "spec"):
		return "test"
	case strings.HasSuffix(lp, ".md") || strings.HasSuffix(lp, ".txt") || strings.HasPrefix(lp, "docs/"):
		return "docs"
	case strings.HasSuffix(lp, ".json") || strings.HasSuffix(lp, ".yml") || strings.HasSuffix(lp, ".yaml") || strings.HasSuffix(lp, ".toml") || strings.HasSuffix(lp, ".lock"):
		return "config"
	}
	return "source"
}

func languages(fs []FileChange) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		l := f.Language
		if l == "" {
			l = strings.TrimPrefix(path.Ext(f.Path), ".")
		}
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

func isComment(x string) bool {
	return strings.HasPrefix(x, "//") || strings.HasPrefix(x, "#") || strings.HasPrefix(x, "/*") ||
		strings.HasPrefix(x, "*") || strings.HasPrefix(x, "--") || strings.HasPrefix(x, `"""`)
}

func isBraceOnly(x string) bool {
	return strings.Trim(x, "{}()[];, ") == ""
}

// narrates reports whether a comment mostly repeats the words of the next line.
func narrates(comment, next string) bool {
	cw := map[string]bool{}
	for _, w := range reWord.FindAllString(strings.ToLower(comment), -1) {
		if len(w) >= 4 {
			cw[w] = true
		}
	}
	if len(cw) < 2 {
		return false
	}
	nl := strings.ToLower(next)
	hit := 0
	for w := range cw {
		if strings.Contains(nl, strings.TrimSuffix(w, "s")) {
			hit++
		}
	}
	return float64(hit)/float64(len(cw)) >= 0.5
}

func normWS(s string) string {
	r := strings.NewReplacer(" ", "", "\t", "", "'", `"`, ";", "", ",", "")
	return r.Replace(s)
}

func normIdents(s string) string { return reIdent.ReplaceAllString(normWS(s), "x") }

func gini(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := float64(len(s))
	sum, cum := 0.0, 0.0
	for i, x := range s {
		sum += x
		cum += float64(i+1) * x
	}
	if sum == 0 {
		return 0
	}
	return (2*cum)/(n*sum) - (n+1)/n
}

func duplicateShare(lines []string, w int) float64 {
	if len(lines) < w*2 {
		return 0
	}
	count := map[string]int{}
	for i := 0; i+w <= len(lines); i++ {
		count[strings.Join(normAll(lines[i:i+w]), "\n")]++
	}
	dup := make([]bool, len(lines))
	for i := 0; i+w <= len(lines); i++ {
		if count[strings.Join(normAll(lines[i:i+w]), "\n")] > 1 {
			for j := i; j < i+w; j++ {
				dup[j] = true
			}
		}
	}
	n := 0
	for _, d := range dup {
		if d {
			n++
		}
	}
	return float64(n) / float64(len(lines))
}

func normAll(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = reIdent.ReplaceAllString(strings.TrimSpace(l), "x")
	}
	return out
}

var reBoiler = regexp.MustCompile(`^(if err != nil \{|return (nil, )?err|return nil|\} catch \(\w+\) \{|try \{|except \w*.*:|raise|throw |import |from \S+ import|export (default )?|module\.exports|package |const \w+ = require|@\w+|pass$|self\.\w+ = \w+$|this\.\w+ = \w+;?$)`)

func boilerShare(lines []string) float64 {
	n := 0
	for _, l := range lines {
		if reBoiler.MatchString(l) {
			n++
		}
	}
	return float64(n) / float64(len(lines))
}

func identEntropy(lines []string) float64 {
	freq := map[string]int{}
	total := 0
	for _, l := range lines {
		for _, id := range reIdent.FindAllString(l, -1) {
			if reKeywords[id] {
				continue
			}
			freq[id]++
			total++
		}
	}
	if total == 0 {
		return 0
	}
	h := 0.0
	for _, c := range freq {
		p := float64(c) / float64(total)
		h -= p * math.Log2(p)
	}
	return h
}
