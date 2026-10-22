// Package reviewers tells AI code reviewers apart from other bots and turns
// what they wrote into findings Cowboy can score after merge.
package reviewers

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// known maps bot logins to the reviewer's name. Only review comments and
// submitted reviews are read, so bots that also post deploy previews or
// error reports (vercel, sentry) are counted only when they review code.
var known = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`^cursor\[bot\]$`), "Cursor Bugbot"},
	{regexp.MustCompile(`^greptile-apps`), "Greptile"},
	{regexp.MustCompile(`^coderabbitai`), "CodeRabbit"},
	{regexp.MustCompile(`^(Copilot|copilot-pull-request-reviewer)`), "Copilot"},
	{regexp.MustCompile(`^cubic-dev-ai`), "cubic"},
	{regexp.MustCompile(`^graphite-app`), "Graphite"},
	{regexp.MustCompile(`^chatgpt-codex-connector`), "Codex"},
	{regexp.MustCompile(`^devin-ai-integration`), "Devin"},
	{regexp.MustCompile(`^(sentry\[bot\]|seer-by-sentry)`), "Sentry Seer"},
	{regexp.MustCompile(`^vercel\[bot\]$`), "Vercel Agent"},
	{regexp.MustCompile(`^claude\[bot\]$`), "Claude"},
	{regexp.MustCompile(`^gemini-code-assist`), "Gemini"},
	{regexp.MustCompile(`^(qodo|codium)`), "Qodo"},
	{regexp.MustCompile(`^sourcery-ai`), "Sourcery"},
	{regexp.MustCompile(`^ellipsis-dev`), "Ellipsis"},
	{regexp.MustCompile(`^korbit-ai`), "Korbit"},
}

var (
	mu    sync.RWMutex
	extra = map[string]string{}
)

// Add registers more reviewer logins, e.g. an in-house review bot, from
// "login" or "login=Name" entries (AI_REVIEWERS).
func Add(spec string) {
	mu.Lock()
	defer mu.Unlock()
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		login, name, ok := strings.Cut(part, "=")
		if !ok {
			name = strings.TrimSuffix(login, "[bot]")
		}
		extra[strings.ToLower(strings.TrimSpace(login))] = strings.TrimSpace(name)
	}
}

// Name is the AI reviewer behind a login, or "" when it isn't one.
func Name(login string) string {
	mu.RLock()
	n, ok := extra[strings.ToLower(login)]
	mu.RUnlock()
	if ok {
		return n
	}
	for _, k := range known {
		if k.re.MatchString(login) {
			return k.name
		}
	}
	return ""
}

var (
	tags    = regexp.MustCompile(`(?s)<!--.*?-->|<[^>]+>`)
	details = regexp.MustCompile(`(?is)<details>.*?</details>`)
	fences  = regexp.MustCompile("(?s)```.*?```")
	badges  = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	links   = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	marks   = regexp.MustCompile(`[*_#>` + "`" + `|]+`)
	spaces  = regexp.MustCompile(`\s+`)
	// a leading label: after an emoji ("⚠️ Potential issue"), or before a
	// colon or dash ("Bug: ..."); a sentence that merely starts with
	// "Issue with ..." keeps its first word
	label    = `(potential issue|refactor suggestion|bug|issue|nitpick|suggestion|minor|major|critical|warning|high|medium|low|syntax|logic|style)`
	prefixes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^\s*\p{So}[\p{So}\x{FE0F}\s]*` + label + `\b\s*[:·-]?\s*`),
		regexp.MustCompile(`(?i)^\s*` + label + `\s*[:·-]\s*`),
		regexp.MustCompile(`^[\p{So}\x{FE0F}\s]+`),
	}
)

// Summary turns a review comment into one plain line of at most n
// characters: markup, folded details, code blocks and badges removed.
func Summary(text string, n int) string {
	s := details.ReplaceAllString(text, " ")
	s = fences.ReplaceAllString(s, " ")
	s = badges.ReplaceAllString(s, " ")
	s = tags.ReplaceAllString(s, " ")
	s = links.ReplaceAllString(s, "$1")
	s = marks.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	for i := 0; i < 5; i++ {
		t := s
		for _, p := range prefixes {
			t = strings.TrimSpace(p.ReplaceAllString(t, ""))
		}
		if t == s {
			break
		}
		s = t
	}
	if utf8.RuneCountInString(s) > n {
		r := []rune(s)
		cut := string(r[:n])
		if i := strings.LastIndex(cut, " "); i > n/2 {
			cut = cut[:i]
		}
		s = strings.TrimRight(cut, " .,;:") + "…"
	}
	return s
}
