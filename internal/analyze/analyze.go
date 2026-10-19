// Package analyze detects which agent wrote a PR and classifies changed files.
package analyze

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// PRMeta is what Cowboy knows about a pull request before reading the diff.
type PRMeta struct {
	Repo     string   `json:"repo"`
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	Author   string   `json:"author"`
	Body     string   `json:"body"`
	Commits  []string `json:"commits"` // commit messages
	HeadSHA  string   `json:"head_sha"`
	Labels   []string `json:"labels"`
	Issue    int      `json:"issue"` // linked issue number, 0 if none
	ChecksOK *bool    `json:"checks_ok,omitempty"`
}

var coAuthor = regexp.MustCompile(`(?im)^co-authored-by:\s*([^<\n]+)`)
var signature = regexp.MustCompile(`(?i)generated with \[?([a-z][a-z0-9 ._-]+?)\]?(?:\(|\s|$)`)
var issueRef = regexp.MustCompile(`(?i)\b(?:fixes|closes|resolves)\s+#(\d+)`)

// DetectAgent returns the agent name and how it was found, or "" if this PR
// does not look agent-authored. agents maps lowercase names to agent labels.
func DetectAgent(m PRMeta, agents map[string]string) (agent, how string) {
	login := strings.ToLower(m.Author)
	if a, ok := agents[login]; ok {
		return a, "bot account " + m.Author
	}
	for _, msg := range m.Commits {
		for _, g := range coAuthor.FindAllStringSubmatch(msg, -1) {
			name := strings.ToLower(strings.TrimSpace(g[1]))
			if a, ok := agents[name]; ok {
				return a, "co-author trailer"
			}
			for k, a := range agents {
				if strings.Contains(name, k) {
					return a, "co-author trailer"
				}
			}
		}
	}
	if g := signature.FindStringSubmatch(m.Body); g != nil {
		name := strings.ToLower(strings.TrimSpace(g[1]))
		if a, ok := agents[name]; ok {
			return a, "PR signature"
		}
	}
	return "", ""
}

// LinkedIssue finds "Fixes #12" in the PR body.
func LinkedIssue(body string) int {
	if g := issueRef.FindStringSubmatch(body); g != nil {
		n := 0
		for _, c := range g[1] {
			n = n*10 + int(c-'0')
		}
		return n
	}
	return 0
}

// Class is the kind of file.
type Class string

const (
	Source    Class = "source"
	Test      Class = "test"
	Docs      Class = "docs"
	Config    Class = "config"
	Lockfile  Class = "lockfile"
	Generated Class = "generated"
)

var lockfiles = map[string]bool{"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "go.sum": true, "Cargo.lock": true, "poetry.lock": true, "Gemfile.lock": true, "composer.lock": true, "uv.lock": true}

// Classify returns the file's class from its path.
func Classify(p string) Class {
	base := path.Base(p)
	ext := strings.ToLower(path.Ext(p))
	lower := strings.ToLower(p)
	switch {
	case lockfiles[base]:
		return Lockfile
	case strings.Contains(lower, "/generated/") || strings.HasSuffix(lower, ".pb.go") || strings.HasSuffix(lower, ".gen.go") || strings.HasSuffix(lower, ".min.js"):
		return Generated
	case IsTest(p):
		return Test
	case ext == ".md" || ext == ".rst" || ext == ".txt" || ext == ".adoc" || strings.HasPrefix(lower, "docs/"):
		return Docs
	case ext == ".json" || ext == ".yml" || ext == ".yaml" || ext == ".toml" || ext == ".ini" || ext == ".cfg" || base == "Dockerfile" || base == "Makefile" || strings.HasPrefix(base, ".") || ext == ".lock":
		return Config
	}
	return Source
}

// IsTest reports whether the path is a test file.
func IsTest(p string) bool {
	lower := strings.ToLower(p)
	base := path.Base(lower)
	return strings.HasSuffix(base, "_test.go") ||
		strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.HasSuffix(base, "test.java") || strings.HasSuffix(base, "tests.java") ||
		base == "conftest.py" ||
		strings.Contains(lower, "/tests/") || strings.HasPrefix(lower, "tests/") ||
		strings.Contains(lower, "/__tests__/") || strings.Contains(lower, "/test/") || strings.HasPrefix(lower, "test/")
}

// Dirs returns the distinct top-level-ish directories touched (parent dir of each file).
func Dirs(paths []string) []string {
	seen := map[string]bool{}
	for _, p := range paths {
		d := path.Dir(p)
		seen[d] = true
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
