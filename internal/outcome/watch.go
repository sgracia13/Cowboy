package outcome

import (
	"fmt"
	"hash/fnv"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/pipeline"
)

// Decisions is the part of Cowboy's store the watcher reads.
type Decisions interface {
	List(repo, state string) []pipeline.Decision
	Get(repo string, n int) (pipeline.Decision, bool)
}

// Event is reported for each new outcome and recorded merge (for the audit
// record and live updates).
type Event struct {
	Kind    string // "merge-recorded", "outcome"
	Repo    string
	Number  int
	Outcome *Outcome
	Merge   *Merge
}

// Watcher looks at merged PRs and the default branch. Run Check hourly.
type Watcher struct {
	Forge     forge.Forge
	Decisions Decisions
	State     *State
	Now       func() time.Time
	OnEvent   func(Event)
	// Lookback is how far back the first scan reads (default 90 days).
	Lookback time.Duration
	// MaxPerRun caps merge records fetched per repo per run (default 200).
	MaxPerRun int
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Watcher) emit(e Event) {
	if w.OnEvent != nil {
		w.OnEvent(e)
	}
}

func (w *Watcher) add(o Outcome) {
	o, isNew, err := w.State.Add(o)
	if err != nil {
		log.Printf("outcomes: %v", err)
		return
	}
	if isNew {
		log.Printf("outcome %s#%d: %s (%s, %s)", o.Repo, o.Number, KindLabel[o.Kind], o.Status, o.Evidence)
		w.emit(Event{Kind: "outcome", Repo: o.Repo, Number: o.Number, Outcome: &o})
	}
}

// Check runs every step for one repo and returns a short summary.
func (w *Watcher) Check(repo string) string {
	h, ok := w.Forge.(forge.History)
	if !ok {
		return repo + ": this git host can't report history; outcomes off"
	}
	defer w.State.Flush()
	newMerges := w.recordMerges(repo, h)
	w.State.Flush()
	reverts := w.scanReverts(repo, h)
	breaks := w.checkBreaks(repo)
	fixes := w.followUps(repo, newMerges)
	w.markReleases(repo, h)
	w.measureSurvival(repo)
	return fmt.Sprintf("%s: %d merges recorded, %d reverts, %d broke main, %d suggested fixes", repo, len(newMerges), reverts, breaks, fixes)
}

// RecordMerge fetches and stores one merged PR right away (called when the
// merge webhook arrives).
func (w *Watcher) RecordMerge(repo string, n int) {
	if h, ok := w.Forge.(forge.History); ok {
		if _, done := w.State.Merge(repo, n); !done {
			w.recordOne(repo, n, h)
			w.State.Flush()
		}
	}
}

func (w *Watcher) recordMerges(repo string, h forge.History) []Merge {
	limit := w.MaxPerRun
	if limit <= 0 {
		limit = 200
	}
	var out []Merge
	for _, d := range w.Decisions.List(repo, "merged") {
		if len(out) >= limit {
			break
		}
		if _, done := w.State.Merge(repo, d.Number); done {
			continue
		}
		if m, ok := w.recordOne(repo, d.Number, h); ok {
			out = append(out, m)
		}
	}
	return out
}

func (w *Watcher) recordOne(repo string, n int, h forge.History) (Merge, bool) {
	mi, err := h.MergeInfo(repo, n)
	if err != nil {
		log.Printf("outcomes %s#%d: %v", repo, n, err)
		return Merge{}, false
	}
	m := Merge{Repo: repo, Number: n, SHA: mi.SHA, MergedAt: mi.MergedAt.UTC(), Title: mi.Title, Author: mi.Author,
		Files: mi.Files, Approvers: mi.Approvers, ReviewsKnown: mi.ReviewsRead}
	if text, err := w.Forge.GetDiff(repo, n); err == nil {
		files := diff.Parse(text)
		m.Lines = lineHashes(files)
		m.Removed = removedHashes(files)
	}
	if err := w.State.PutMerge(m); err != nil {
		log.Printf("outcomes: %v", err)
		return Merge{}, false
	}
	w.emit(Event{Kind: "merge-recorded", Repo: repo, Number: n, Merge: &m})
	return m, true
}

// removedHashes keeps a short hash of an even sample of the source and test
// lines a PR removed (a rewritten line shows up as removed and added).
func removedHashes(files []diff.File) []uint32 {
	var all []uint32
	for _, f := range files {
		c := analyze.Classify(f.Path)
		if c != analyze.Source && c != analyze.Test {
			continue
		}
		for _, l := range f.Removed {
			if t := strings.TrimSpace(l); len(t) >= 8 {
				all = append(all, hash32(t))
			}
		}
	}
	if len(all) <= SurvivalSample {
		return all
	}
	out := make([]uint32, 0, SurvivalSample)
	step := float64(len(all)) / SurvivalSample
	for i := 0.0; int(i) < len(all) && len(out) < SurvivalSample; i += step {
		out = append(out, all[int(i)])
	}
	return out
}

// SurvivalSample is the most line hashes kept per PR: an even sample is
// enough to measure the share of code still there to within a few percent.
const SurvivalSample = 200

// lineHashes keeps a short hash of an even sample of meaningful added lines
// in source and test files, so survival can be measured later without
// keeping code.
func lineHashes(files []diff.File) map[string][]uint32 {
	type line struct {
		file string
		h    uint32
	}
	var all []line
	for _, f := range files {
		if f.Status == "removed" {
			continue
		}
		c := analyze.Classify(f.Path)
		if c != analyze.Source && c != analyze.Test {
			continue
		}
		for _, l := range f.Added {
			if t := strings.TrimSpace(l); len(t) >= 8 {
				all = append(all, line{f.Path, hash32(t)})
			}
		}
	}
	if len(all) == 0 {
		return nil
	}
	step := 1.0
	if len(all) > SurvivalSample {
		step = float64(len(all)) / SurvivalSample
	}
	out := map[string][]uint32{}
	for i := 0.0; int(i) < len(all); i += step {
		l := all[int(i)]
		out[l.file] = append(out[l.file], l.h)
	}
	return out
}

func hash32(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

func (w *Watcher) scanReverts(repo string, h forge.History) int {
	sc := w.State.scan(repo)
	lookback := w.Lookback
	if lookback == 0 {
		lookback = 90 * 24 * time.Hour
	}
	since := sc.Commits
	if since.IsZero() {
		since = w.now().Add(-lookback)
	}
	commits, err := h.Commits(repo, since.Add(-time.Hour))
	if err != nil {
		log.Printf("outcomes %s: reading the default branch: %v", repo, err)
		return 0
	}
	bySHA := map[string]Merge{}
	for _, m := range w.State.Merges(repo) {
		if m.SHA != "" {
			bySHA[strings.ToLower(m.SHA)] = m
		}
	}
	findSHA := func(sha string) (int, bool) {
		for full, m := range bySHA {
			if strings.HasPrefix(full, sha) {
				return m.Number, true
			}
		}
		n, err := h.PRForCommit(repo, sha)
		return n, err == nil && n > 0
	}
	found := 0
	newest := sc.Commits
	for _, c := range commits {
		if c.At.After(newest) {
			newest = c.At
		}
		refs, ok := ParseRevert(c.Message)
		if !ok {
			continue
		}
		own := OwnNumber(c.Message)
		if len(refs) == 0 {
			// only the quoted title to go on: the latest earlier PR with that title
			if t := RevertedTitle(c.Message); t != "" {
				best := Merge{}
				for _, m := range w.State.Merges(repo) {
					if SameTitle(m.Title, t) && m.MergedAt.Before(c.At) && m.MergedAt.After(best.MergedAt) {
						best = m
					}
				}
				if best.Number > 0 {
					refs = append(refs, RevertRef{PR: best.Number})
				}
			}
		}
		done := map[int]bool{}
		for _, r := range refs {
			n := r.PR
			if n == 0 && r.SHA != "" {
				var ok bool
				if n, ok = findSHA(r.SHA); !ok {
					continue
				}
			}
			if done[n] || n == own {
				continue // a commit never reverts its own PR
			}
			done[n] = true
			if _, sorted := w.Decisions.Get(repo, n); !sorted {
				continue // a PR Cowboy never sorted: nothing to learn about its pen
			}
			if _, rec := w.State.Merge(repo, n); !rec {
				w.recordOne(repo, n, h)
			}
			title, _, _ := strings.Cut(c.Message, "\n")
			ev := fmt.Sprintf("reverted by %.7s: %s", c.SHA, strings.TrimSpace(title))
			if own := OwnNumber(c.Message); own > 0 && own != n {
				ev = fmt.Sprintf("reverted by #%d: %s", own, strings.TrimSpace(title))
			}
			w.add(Outcome{Repo: repo, Number: n, Kind: Revert, Severity: 2, At: c.At.UTC(), Evidence: ev, Source: "git", Status: Auto})
			found++
		}
	}
	sc.Commits = newest
	w.State.setScan(repo, sc)
	return found
}

// checkBreaks: checks failing on the merge commit after the PR itself was
// green. A PR merged with failing checks is already careful for that reason,
// so it isn't counted again here.
func (w *Watcher) checkBreaks(repo string) int {
	found := 0
	now := w.now()
	for _, m := range w.State.Merges(repo) {
		if m.BreakChecked || m.SHA == "" || now.Sub(m.MergedAt) < 2*time.Hour {
			continue
		}
		d, ok := w.Decisions.Get(repo, m.Number)
		green := ok && d.Signals.ChecksOK != nil && *d.Signals.ChecksOK
		res, err := w.Forge.ChecksOK(repo, m.SHA)
		if err != nil {
			continue
		}
		if res == nil && now.Sub(m.MergedAt) < 48*time.Hour {
			continue // still running, or no checks yet
		}
		if res != nil && !*res && green {
			w.add(Outcome{Repo: repo, Number: m.Number, Kind: Break, Severity: 1, At: m.MergedAt, Evidence: fmt.Sprintf("checks failed on the merge commit %.7s", m.SHA), Source: "checks", Status: Auto})
			found++
		}
		w.State.update(repo, m.Number, func(x *Merge) { x.BreakChecked = true })
	}
	return found
}

var fixWords = regexp.MustCompile(`(?i)\b(fix|fixes|fixed|fixing|hotfix|regression|broke|broken|bug)\b`)
var hashRef = regexp.MustCompile(`#(\d+)\b`)

// followUps suggests "needed a fix" for an earlier PR when a newer merged
// PR says it fixes something and either names the earlier PR, or rewrites
// lines the earlier PR added (within two weeks). Sharing files isn't enough:
// lockfiles and busy files make unrelated PRs look connected. Lines that
// show up in many PRs (boilerplate like "if err != nil {") don't count.
func (w *Watcher) followUps(repo string, newer []Merge) int {
	if len(newer) == 0 {
		return 0
	}
	all := w.State.Merges(repo)
	// how many PRs added each line: common lines say nothing
	df := map[uint32]int{}
	for _, m := range all {
		seen := map[uint32]bool{}
		for _, hs := range m.Lines {
			for _, h := range hs {
				if !seen[h] {
					seen[h] = true
					df[h]++
				}
			}
		}
	}
	found := 0
	for _, m := range newer {
		if _, isRevert := ParseRevert(m.Title); isRevert || !fixWords.MatchString(m.Title) {
			continue
		}
		info, _ := w.Decisions.Get(repo, m.Number)
		named := map[int]bool{}
		for _, r := range hashRef.FindAllStringSubmatch(m.Title+"\n"+info.Body, -1) {
			if n, _ := strconv.Atoi(r[1]); n > 0 && n != m.Number {
				named[n] = true
			}
		}
		removed := map[uint32]bool{}
		for _, h := range m.Removed {
			if df[h] <= 3 {
				removed[h] = true
			}
		}
		best, bestScore, why := 0, 0.0, ""
		for _, prev := range all {
			if prev.Number == m.Number || !prev.MergedAt.Before(m.MergedAt) || m.MergedAt.Sub(prev.MergedAt) > 14*24*time.Hour {
				continue
			}
			days := plural(int(m.MergedAt.Sub(prev.MergedAt).Hours()/24), "day")
			if named[prev.Number] {
				best, bestScore, why = prev.Number, 100, fmt.Sprintf("#%d %q names #%d, %s later", m.Number, m.Title, prev.Number, days)
				break
			}
			if len(removed) == 0 {
				continue
			}
			hits := 0
			for _, hs := range prev.Lines {
				for _, h := range hs {
					if removed[h] {
						hits++
					}
				}
			}
			if hits >= 3 && float64(hits) > bestScore {
				best, bestScore, why = prev.Number, float64(hits), fmt.Sprintf("#%d %q rewrote %d lines it added, %s later", m.Number, m.Title, hits, days)
			}
		}
		if best == 0 {
			continue
		}
		if _, sorted := w.Decisions.Get(repo, best); !sorted {
			continue
		}
		w.add(Outcome{Repo: repo, Number: best, Kind: Fix, Severity: 1, At: m.MergedAt, Evidence: why, Source: "git", Status: Suggested, Score: bestScore})
		found++
	}
	return found
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	if n == 0 {
		return "under a " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// overlap is the share of a's files that b also changed.
func overlap(a, b []string) float64 {
	if len(a) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, f := range b {
		set[f] = true
	}
	n := 0
	for _, f := range a {
		if set[f] {
			n++
		}
	}
	return float64(n) / float64(len(a))
}

// markReleases notes the first release after each merge. Releases are
// matched by time: a PR merged before a release was published is in it.
func (w *Watcher) markReleases(repo string, h forge.History) {
	sc := w.State.scan(repo)
	since := sc.Releases
	if since.IsZero() {
		since = w.now().Add(-120 * 24 * time.Hour)
	}
	rs, err := h.Releases(repo, since)
	if err != nil || len(rs) == 0 {
		return
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].At.Before(rs[j].At) })
	for _, r := range rs {
		for _, m := range w.State.Merges(repo) {
			if m.Release == "" && m.MergedAt.Before(r.At) {
				tag, at := r.Tag, r.At.UTC()
				w.State.update(repo, m.Number, func(x *Merge) { x.Release, x.ReleasedAt = tag, at })
			}
		}
		if r.At.After(sc.Releases) {
			sc.Releases = r.At
		}
	}
	w.State.setScan(repo, sc)
}

// measureSurvival: 30 and 90 days after merge, the share of the PR's added
// lines still in the default branch (by line hash, per file).
func (w *Watcher) measureSurvival(repo string) {
	now := w.now()
	for _, m := range w.State.Merges(repo) {
		if len(m.Lines) == 0 {
			continue
		}
		for _, p := range []struct {
			key string
			age time.Duration
		}{{"30d", 30 * 24 * time.Hour}, {"90d", 90 * 24 * time.Hour}} {
			if _, done := m.Survival[p.key]; done || now.Sub(m.MergedAt) < p.age {
				continue
			}
			share, ok := w.survival(repo, m)
			if !ok {
				continue
			}
			k := p.key
			w.State.update(repo, m.Number, func(x *Merge) {
				if x.Survival == nil {
					x.Survival = map[string]float64{}
				}
				x.Survival[k] = share
				if k == "90d" {
					x.Lines = nil // measured for good: the hashes aren't needed any more
				}
			})
		}
	}
}

func (w *Watcher) survival(repo string, m Merge) (float64, bool) {
	files := make([]string, 0, len(m.Lines))
	for f := range m.Lines {
		files = append(files, f)
	}
	sort.Strings(files)
	if len(files) > 30 {
		files = files[:30]
	}
	kept, total := 0, 0
	for _, f := range files {
		want := m.Lines[f]
		src, err := w.Forge.GetFile(repo, f, "")
		if err != nil {
			return 0, false
		}
		have := map[uint32]int{}
		for _, l := range strings.Split(src, "\n") {
			if t := strings.TrimSpace(l); len(t) >= 8 {
				have[hash32(t)]++
			}
		}
		for _, h := range want {
			total++
			if have[h] > 0 {
				have[h]--
				kept++
			}
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(kept) / float64(total), true
}

// Area is the top folder most of a PR's files are in ("(root)" for files at
// the top level).
func Area(files []string) string {
	count := map[string]int{}
	for _, f := range files {
		top := "(root)"
		if i := strings.Index(f, "/"); i > 0 {
			top = f[:i]
		}
		count[top]++
	}
	best, bestN := "(root)", -1
	for a, n := range count {
		if n > bestN || (n == bestN && a < best) {
			best, bestN = a, n
		}
	}
	return best
}
