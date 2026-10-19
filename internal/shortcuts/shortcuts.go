// Package shortcuts finds the six "shortcut flags": patterns that make checks
// pass without fixing the problem. Every check looks only at lines this PR
// added (or, for removed asserts and deleted tests, removed), never at code
// that was already there.
package shortcuts

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/diff"
)

// Flag is one shortcut found in the diff.
type Flag struct {
	Kind   string `json:"kind"`
	File   string `json:"file"`
	Detail string `json:"detail"`
}

var (
	skipRe = regexp.MustCompile(`@pytest\.mark\.(skip|xfail)|pytest\.skip\(|\b(it|test|describe)\.skip\(|\bx(it|describe|test)\(|@Disabled\b|@Ignore\b|\bt\.Skip(Now|f)?\(|unittest\.skip|\.todo\(`)
	// broad catches that swallow errors
	pyBroad   = regexp.MustCompile(`^\s*except\s*(Exception|BaseException)?\s*(as\s+\w+)?\s*:\s*(pass)?\s*$`)
	emptyCtch = regexp.MustCompile(`catch\s*(\([^)]*\))?\s*\{\s*\}`)
	javaBroad = regexp.MustCompile(`catch\s*\(\s*(final\s+)?(Exception|Throwable|RuntimeException)\s+\w+\s*\)`)
	goIgnore  = regexp.MustCompile(`_\s*=\s*err\b|if err != nil \{\s*\}`)
	suppress  = regexp.MustCompile(`#\s*type:\s*ignore|#\s*noqa|#\s*pylint:\s*disable|eslint-disable|@ts-ignore|@ts-nocheck|@ts-expect-error|//\s*nolint|@SuppressWarnings|#\s*nosec|//\s*NOSONAR`)
	special   = regexp.MustCompile(`(?i)(#|//|/\*|\*).*(special[\s_-]?case|hard[\s_-]?code[d]?|for (the )?tests?\b|make (the )?tests? pass|to pass (the )?tests?)`)
	assertRe  = regexp.MustCompile(`\bassert|\bexpect\(|\.should\b|\bt\.(Error|Fatal)|\bassertThat\(|\brequire\.\w+\(|\bassert\.\w+\(`)
	ciRelax   = regexp.MustCompile(`(?i)continue-on-error:\s*true|allow_failure:\s*true|--no-verify|\|\|\s*true|fail_under\s*[=:]\s*\d|coverage.*threshold|--passWithNoTests|-k\s+['"]not\s`)
)

func isCI(p string) bool {
	l := strings.ToLower(p)
	b := path.Base(l)
	return strings.HasPrefix(l, ".github/workflows/") || strings.HasPrefix(l, ".forgejo/workflows/") || strings.HasPrefix(l, ".gitea/workflows/") ||
		b == ".gitlab-ci.yml" || b == "jenkinsfile" || b == ".woodpecker.yml" || strings.HasPrefix(l, ".woodpecker/") ||
		b == "pytest.ini" || b == "setup.cfg" || b == "tox.ini" || b == "jest.config.js" || b == "jest.config.ts" || b == "vitest.config.ts" ||
		b == ".coveragerc" || b == "codecov.yml" || b == "conftest.py"
}

func countMatches(re *regexp.Regexp, lines []string) int {
	n := 0
	for _, l := range lines {
		if re.MatchString(l) {
			n++
		}
	}
	return n
}

func firstMatch(re *regexp.Regexp, lines []string) string {
	for _, l := range lines {
		if re.MatchString(l) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// Find runs every enabled check over the diff.
func Find(files []diff.File, enabled map[string]bool) []Flag {
	var out []Flag
	on := func(k string) bool { v, ok := enabled[k]; return !ok || v }
	touchesSource := false
	for _, f := range files {
		if analyze.Classify(f.Path) == analyze.Source && f.Status != "removed" {
			touchesSource = true
		}
	}
	// 1. tests deleted or asserts removed, judged across the whole PR: asserts
	// that moved to another test file don't count, and neither do PRs that
	// remove a feature along with its tests. (Tested on 7,500 real PRs: the
	// per-file version was a false alarm about 80% of the time.)
	if on("tests_removed") && touchesSource {
		if f, ok := testsRemoved(files); ok {
			out = append(out, f)
		}
	}
	for _, f := range files {
		isTest := analyze.IsTest(f.Path)
		// 2. new skip markers
		if on("skips") {
			if l := firstMatch(skipRe, f.Added); l != "" {
				out = append(out, Flag{"skips", f.Path, "adds a skip marker: " + short(l)})
			}
		}
		// 3. new broad catch / swallowed error
		if on("catch_all") && !isTest {
			for _, re := range []*regexp.Regexp{pyBroad, emptyCtch, javaBroad, goIgnore} {
				if l := firstMatch(re, f.Added); l != "" {
					out = append(out, Flag{"catch_all", f.Path, "adds a catch-all that may hide errors: " + short(l)})
					break
				}
			}
		}
		// 4. new suppressions
		if on("suppressions") {
			if l := firstMatch(suppress, f.Added); l != "" {
				out = append(out, Flag{"suppressions", f.Path, "adds a warning silencer: " + short(l)})
			}
		}
		// 5. comments that admit test-specific handling
		if on("special_case_comments") && !isTest {
			if l := firstMatch(special, f.Added); l != "" {
				out = append(out, Flag{"special_case_comments", f.Path, "comment suggests test-specific handling: " + short(l)})
			}
		}
		// 6. CI or test config loosened
		if on("ci_loosened") && isCI(f.Path) {
			removedSteps := 0
			for _, l := range f.Removed {
				t := strings.TrimSpace(l)
				if strings.HasPrefix(t, "- run:") || strings.HasPrefix(t, "run:") || strings.HasPrefix(t, "script:") {
					removedSteps++
				}
			}
			if l := firstMatch(ciRelax, f.Added); l != "" {
				out = append(out, Flag{"ci_loosened", f.Path, "loosens CI or test settings: " + short(l)})
			} else if removedSteps > 0 {
				out = append(out, Flag{"ci_loosened", f.Path, fmt.Sprintf("removes %d CI step(s) from %s", removedSteps, f.Path)})
			} else if path.Base(f.Path) == "conftest.py" && len(f.Added) > 0 {
				out = append(out, Flag{"ci_loosened", f.Path, "changes test hooks in conftest.py"})
			}
		}
	}
	return out
}

// testsRemoved reports lost asserts across the PR's test files.
func testsRemoved(files []diff.File) (Flag, bool) {
	net, srcAdded, srcRemoved, srcDeleted := 0, 0, 0, 0
	worst, worstLost := "", 0
	var deleted []string
	for _, f := range files {
		if analyze.IsTest(f.Path) {
			lost := countMatches(assertRe, f.Removed) - countMatches(assertRe, f.Added)
			net -= lost
			if lost > worstLost {
				worst, worstLost = f.Path, lost
			}
			if f.Status == "removed" {
				deleted = append(deleted, f.Path)
			}
			continue
		}
		if analyze.Classify(f.Path) == analyze.Source {
			srcAdded += len(f.Added)
			srcRemoved += len(f.Removed)
			if f.Status == "removed" {
				srcDeleted++
			}
		}
	}
	// removing a feature removes its tests too
	if srcDeleted > 0 || (srcRemoved > 0 && float64(srcRemoved) > 1.5*float64(srcAdded)) {
		return Flag{}, false
	}
	if net >= 0 {
		return Flag{}, false
	}
	if len(deleted) > 0 {
		return Flag{"tests_removed", deleted[0], fmt.Sprintf("deletes test file %s (%d assert(s) fewer overall)", deleted[0], -net)}, true
	}
	return Flag{"tests_removed", worst, fmt.Sprintf("removes %d assert(s) overall, most from %s", -net, worst)}, true
}

func short(s string) string {
	if len(s) > 70 {
		return s[:67] + "..."
	}
	return s
}
