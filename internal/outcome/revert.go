package outcome

import (
	"regexp"
	"strconv"
	"strings"
)

// RevertRef points at what a revert commit undid: a pull request number, a
// commit SHA, or both.
type RevertRef struct {
	PR  int
	SHA string
}

var (
	// git revert: "This reverts commit 1a2b3c4d…"
	revertsCommit = regexp.MustCompile(`(?i)\bThis reverts commit ([0-9a-f]{7,40})\b`)
	// GitHub's Revert button writes this into the revert PR's body
	revertsPR = regexp.MustCompile(`(?im)^\s*Reverts\s+[\w.-]+/[\w.-]+#(\d+)\b`)
	// a squash-merged PR's title ends in (#123): Revert "Add x (#123)"
	revertTitle = regexp.MustCompile(`^Revert\s+"(.*)"`)
	prSuffix    = regexp.MustCompile(`\(#(\d+)\)\s*$`)
	// "Merge pull request #130 from acme/revert-123-some-branch"
	revertBranch = regexp.MustCompile(`(?i)^Merge pull request #\d+ from [^\s/]+/revert-(\d+)-`)
	// a PR named in a revert title: "(#123)" or "from #123" (a bare #123 is
	// often an issue, so it isn't used)
	titleRef = regexp.MustCompile(`\(#(\d+)\)|\bfrom #(\d+)\b`)
	// the revert PR's own number, at the end of a squash title
	ownNumber = regexp.MustCompile(`\(#(\d+)\)\s*$`)
	// GitLab's Revert button: "This reverts merge request !123" (also with
	// the project path before the !)
	revertsMR = regexp.MustCompile(`(?i)\bThis reverts merge request\s+(?:[\w./-]+)?!(\d+)\b`)
	// GitLab's merge commit for a revert merge request
	gitlabRevertMerge = regexp.MustCompile(`(?i)^Merge branch '(?:[^']*/)?revert-[0-9a-f]{6,40}' into `)
)

// ParseRevert reads a commit message and returns what it reverted. ok is
// false when the commit isn't a revert.
func ParseRevert(msg string) (refs []RevertRef, ok bool) {
	title, body, _ := strings.Cut(msg, "\n")
	title = strings.TrimSpace(title)
	seenPR := map[int]bool{}
	addPR := func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && !seenPR[n] {
			seenPR[n] = true
			refs = append(refs, RevertRef{PR: n})
		}
	}
	// a revert of a revert puts the change back: not a problem with the PR
	if strings.HasPrefix(title, `Revert "Revert `) || strings.HasPrefix(title, `Reapply "`) {
		return nil, false
	}
	// Only the title decides. A squash commit's body often lists commits
	// from the PR's own branch, including "This reverts commit …" for a
	// commit reverted before merge: that isn't a revert of a merged PR.
	isRevert := strings.HasPrefix(title, "Revert ") || strings.HasPrefix(title, "revert ") || strings.HasPrefix(title, "Revert:") || strings.HasPrefix(title, "revert:")
	if m := revertTitle.FindStringSubmatch(title); m != nil {
		if p := prSuffix.FindStringSubmatch(m[1]); p != nil {
			addPR(p[1])
		}
	}
	if m := revertBranch.FindStringSubmatch(title); m != nil {
		isRevert = true
		addPR(m[1])
	}
	if gitlabRevertMerge.MatchString(title) {
		isRevert = true
	}
	if !isRevert {
		return nil, false
	}
	own := OwnNumber(msg)
	// "Revert: x (#122634) (#122641)", "Revert changes from #132256 (#133394)"
	for _, m := range titleRef.FindAllStringSubmatch(title, -1) {
		n := m[1]
		if n == "" {
			n = m[2]
		}
		if v, _ := strconv.Atoi(n); v != own {
			addPR(n)
		}
	}
	for _, m := range revertsPR.FindAllStringSubmatch(body, -1) {
		if n, _ := strconv.Atoi(m[1]); n != own {
			addPR(m[1])
		}
	}
	for _, m := range revertsMR.FindAllStringSubmatch(body, -1) {
		addPR(m[1])
	}
	for _, m := range revertsCommit.FindAllStringSubmatch(msg, -1) {
		refs = append(refs, RevertRef{SHA: strings.ToLower(m[1])})
	}
	return refs, true
}

// OwnNumber is the PR number at the end of a squash commit's title, if any.
func OwnNumber(msg string) int {
	title, _, _ := strings.Cut(msg, "\n")
	if m := ownNumber.FindStringSubmatch(strings.TrimSpace(title)); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// RevertedTitle is the quoted title in `Revert "…"`, without a trailing
// (#123), for matching against PR titles when nothing else points at the PR.
func RevertedTitle(msg string) string {
	title, _, _ := strings.Cut(msg, "\n")
	m := revertTitle.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return ""
	}
	t := strings.TrimSpace(prSuffix.ReplaceAllString(m[1], ""))
	if strings.HasSuffix(t, "…") || strings.HasSuffix(t, "...") {
		return "" // GitHub cut the title short: too risky to match
	}
	return t
}

// SameTitle compares PR titles loosely (case, spaces, a trailing (#123)).
func SameTitle(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(prSuffix.ReplaceAllString(strings.TrimSpace(s), ""))
		return strings.ToLower(strings.Join(strings.Fields(s), " "))
	}
	return a != "" && norm(a) == norm(b)
}
