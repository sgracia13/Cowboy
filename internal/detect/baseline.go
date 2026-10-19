package detect

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PastPR is the slice of a finished PR's matrix that a baseline keeps.
// Storing summaries (not whole matrices) keeps the history small and lets
// baselines be rebuilt when the window or the math changes.
type PastPR struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	At     time.Time `json:"at"` // when the PR opened

	LinesPerMinute    *float64 `json:"lines_per_minute,omitempty"`
	InterCommitGapSec *float64 `json:"inter_commit_gap_sec,omitempty"` // median gap within the PR
	BranchToPRSec     *float64 `json:"branch_to_pr_sec,omitempty"`
	ReviewResponseSec *float64 `json:"review_response_sec,omitempty"`
	Commits           int      `json:"commits"`
	Hours             []int    `json:"hours,omitempty"` // UTC hour of each commit

	TypoRatePer1kWords      *float64 `json:"typo_rate_per_1k_words,omitempty"`
	MultilineCommitRatio    *float64 `json:"multiline_commit_ratio,omitempty"`
	ConventionalCommitRatio *float64 `json:"conventional_commit_ratio,omitempty"`
	CommentToCodeRatio      *float64 `json:"comment_to_code_ratio,omitempty"`
	BulletDensity           *float64 `json:"bullet_density,omitempty"`
	ChangeConcentration     *float64 `json:"change_concentration,omitempty"`
	ConditionalDensity      *float64 `json:"conditional_density,omitempty"`
	TemplateSections        bool     `json:"template_sections"`

	Signed         *bool  `json:"signed,omitempty"`
	DisclosedAgent string `json:"disclosed_agent,omitempty"`
	PreAgent       bool   `json:"pre_agent"` // opened before agents were in common use
}

// PreAgentCutoff marks the start of common agent use. PRs before it are the
// cleanest "human" examples a baseline can have.
var PreAgentCutoff = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)

func ptr[T any](o Observed[T]) *T {
	if !o.Known {
		return nil
	}
	v := o.Value
	return &v
}

// Summarize turns a finished PR's matrix (and its commit hours) into a PastPR.
func Summarize(m DetectionMatrix, opened time.Time, hours []int) PastPR {
	p := PastPR{Repo: m.Repo, Number: m.Number, At: opened.UTC(), Hours: hours,
		LinesPerMinute: ptr(m.Time.LinesPerMinute), BranchToPRSec: ptr(m.Time.BranchToPRSec),
		TypoRatePer1kWords: ptr(m.Text.TypoRatePer1kWords), MultilineCommitRatio: ptr(m.Text.MultilineCommitRatio),
		ConventionalCommitRatio: ptr(m.Text.ConventionalCommitRatio), CommentToCodeRatio: ptr(m.Text.CommentToCodeRatio),
		BulletDensity: ptr(m.Text.BulletDensity), ChangeConcentration: ptr(m.Code.ChangeConcentration),
		Signed: ptr(m.Baseline.Verification.ThisPRSigned), DisclosedAgent: m.Disclosure.Agent,
		PreAgent: opened.Before(PreAgentCutoff)}
	if m.Time.CommitCount.Known {
		p.Commits = m.Time.CommitCount.Value
	}
	if m.Time.InterCommitGapSec.Known {
		v := m.Time.InterCommitGapSec.Value.Median
		p.InterCommitGapSec = &v
	}
	if m.Time.ReviewResponseSec.Known {
		v := m.Time.ReviewResponseSec.Value.Median
		p.ReviewResponseSec = &v
	}
	if m.Code.AST.Known {
		v := m.Code.AST.Value.ConditionalDensity
		p.ConditionalDensity = &v
	}
	if m.Text.TemplateSections.Known {
		p.TemplateSections = len(m.Text.TemplateSections.Value) >= 2
	}
	return p
}

// MinSamples is how many past PRs a baseline needs before behaviour is
// compared against it. Below this, ColdStart is true.
const MinSamples = 10

// BuildBaseline computes an author's baseline from their past PRs inside
// the window. Past PRs that disclosed an agent are left out of speed and
// style stats, so a person's agent work doesn't redefine "normal for them".
func BuildBaseline(forge, author string, past []PastPR, now time.Time, windowDays int, corrections int) DeveloperBaseline {
	b := DeveloperBaseline{Author: author, Forge: forge, WindowDays: windowDays, Updated: now.UTC(), CorrectionsAgainst: corrections}
	from := now.AddDate(0, 0, -windowDays)
	var human []PastPR
	disclosed, inWindow := 0, 0
	agentCount := map[string]int{}
	signed, signedKnown := 0, 0
	for _, p := range past {
		if p.PreAgent {
			b.PreAgentSamples++
			human = append(human, p) // always keep: the cleanest examples
			continue
		}
		if p.At.Before(from) {
			continue
		}
		inWindow++
		if p.DisclosedAgent != "" {
			disclosed++
			agentCount[p.DisclosedAgent]++
			continue
		}
		human = append(human, p)
	}
	for _, p := range human {
		if p.Signed != nil {
			signedKnown++
			if *p.Signed {
				signed++
			}
		}
	}
	b.SamplePRs = len(human)
	b.ColdStart = b.SamplePRs < MinSamples
	if inWindow > 0 {
		b.DisclosedAgentShare = float64(disclosed) / float64(inWindow)
	}
	best := 0
	for a, n := range agentCount {
		if n > best || (n == best && a < b.UsualAgent) {
			b.UsualAgent, best = a, n
		}
	}
	pick := func(f func(PastPR) *float64) RobustStat {
		var xs []float64
		for _, p := range human {
			if v := f(p); v != nil && !math.IsNaN(*v) {
				xs = append(xs, *v)
			}
		}
		return robust(xs)
	}
	b.LinesPerMinute = pick(func(p PastPR) *float64 { return p.LinesPerMinute })
	b.InterCommitGapSec = pick(func(p PastPR) *float64 { return p.InterCommitGapSec })
	b.BranchToPRSec = pick(func(p PastPR) *float64 { return p.BranchToPRSec })
	b.ReviewResponseSec = pick(func(p PastPR) *float64 { return p.ReviewResponseSec })
	b.CommitsPerPR = pick(func(p PastPR) *float64 { v := float64(p.Commits); return &v })
	b.TypoRatePer1kWords = pick(func(p PastPR) *float64 { return p.TypoRatePer1kWords })
	b.MultilineCommitRatio = pick(func(p PastPR) *float64 { return p.MultilineCommitRatio })
	b.ConventionalCommitRatio = pick(func(p PastPR) *float64 { return p.ConventionalCommitRatio })
	b.CommentToCodeRatio = pick(func(p PastPR) *float64 { return p.CommentToCodeRatio })
	b.BulletDensity = pick(func(p PastPR) *float64 { return p.BulletDensity })
	b.ChangeConcentration = pick(func(p PastPR) *float64 { return p.ChangeConcentration })
	b.ConditionalDensity = pick(func(p PastPR) *float64 { return p.ConditionalDensity })
	tmpl, hours := 0, 0
	for _, p := range human {
		if p.TemplateSections {
			tmpl++
		}
		for _, h := range p.Hours {
			if h >= 0 && h < 24 {
				b.ActiveHours[h]++
				hours++
			}
		}
	}
	if len(human) > 0 {
		b.UsesTemplateSections = float64(tmpl) / float64(len(human))
	}
	if hours > 0 {
		for i := range b.ActiveHours {
			b.ActiveHours[i] /= float64(hours)
		}
	}
	if signedKnown > 0 {
		b.Verification.SignedCommitShare = known(float64(signed)/float64(signedKnown), SourceStore, "%d of %d past PRs fully signed", signed, signedKnown)
	} else {
		b.Verification.SignedCommitShare = unknown[float64]("no signing history")
	}
	return b
}

func robust(xs []float64) RobustStat {
	if len(xs) == 0 {
		return RobustStat{}
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	q := func(p float64) float64 {
		i := p * float64(len(s)-1)
		lo := int(math.Floor(i))
		hi := int(math.Ceil(i))
		return s[lo] + (s[hi]-s[lo])*(i-float64(lo))
	}
	return RobustStat{Median: median(s), MAD: mad(s), P10: q(0.10), P90: q(0.90), N: len(s)}
}

// ------------------------------------------------------------ the store

// FileBaselines keeps every author's PastPR history in one JSON file. It is
// the prototype's BaselineStore; SQLite replaces it with the rest of the store.
type FileBaselines struct {
	Path       string
	WindowDays int
	Now        func() time.Time

	mu   sync.Mutex
	data baselineFile
}

type baselineFile struct {
	Authors map[string]*authorHistory `json:"authors"` // key: forge/author (lowercase)
}

type authorHistory struct {
	Past        []PastPR `json:"past"`
	Corrections int      `json:"corrections"`
}

// OpenBaselines loads (or starts) a baseline file. An empty path keeps it in memory.
func OpenBaselines(path string, windowDays int) (*FileBaselines, error) {
	f := &FileBaselines{Path: path, WindowDays: windowDays, Now: time.Now, data: baselineFile{Authors: map[string]*authorHistory{}}}
	if path == "" {
		return f, nil
	}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &f.data); err != nil {
			return nil, err
		}
		if f.data.Authors == nil {
			f.data.Authors = map[string]*authorHistory{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return f, nil
}

func bkey(forge, author string) string { return strings.ToLower(forge + "/" + author) }

func (f *FileBaselines) save() error {
	if f.Path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(f.data, "", " ")
	os.MkdirAll(filepath.Dir(f.Path), 0o755)
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// Seed adds past PRs for an author (backfill, or the demo's sample history).
func (f *FileBaselines) Seed(forge, author string, past []PastPR) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hist(forge, author)
	h.Past = append(h.Past, past...)
	return f.save()
}

func (f *FileBaselines) hist(forge, author string) *authorHistory {
	k := bkey(forge, author)
	if f.data.Authors[k] == nil {
		f.data.Authors[k] = &authorHistory{}
	}
	return f.data.Authors[k]
}

// Get builds the author's current baseline.
func (f *FileBaselines) Get(forge, author string) (DeveloperBaseline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.data.Authors[bkey(forge, author)]
	if h == nil {
		return DeveloperBaseline{Author: author, Forge: forge, WindowDays: f.WindowDays, ColdStart: true}, nil
	}
	return BuildBaseline(forge, author, h.Past, f.Now(), f.WindowDays, h.Corrections), nil
}

// Observe adds a finished PR. Call it only when a PR is merged or closed.
func (f *FileBaselines) Observe(m DetectionMatrix) error {
	return f.ObservePast(m.Forge, m.Author, Summarize(m, m.Built, nil))
}

// ObservePast adds a ready-made summary (lets the caller supply commit hours).
func (f *FileBaselines) ObservePast(forge, author string, p PastPR) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hist(forge, author)
	for i, q := range h.Past { // replace on re-observe of the same PR
		if q.Repo == p.Repo && q.Number == p.Number {
			h.Past[i] = p
			return f.save()
		}
	}
	h.Past = append(h.Past, p)
	if len(h.Past) > 500 {
		h.Past = h.Past[len(h.Past)-500:]
	}
	return f.save()
}

// Correct records that a human removed a "likely agent" label.
func (f *FileBaselines) Correct(forge, author string, repo string, number int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hist(forge, author).Corrections++
	return f.save()
}

// SeedDir loads <author>.json files (each a []PastPR) from dir into the
// store, shifting dates so the newest PR lands a day before now. The demo
// uses it to give sample authors a history.
func SeedDir(f *FileBaselines, forge, dir string, now time.Time) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		var past []PastPR
		if err := json.Unmarshal(b, &past); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		var newest time.Time
		for _, p := range past {
			if !p.PreAgent && p.At.After(newest) {
				newest = p.At
			}
		}
		shift := now.Add(-24 * time.Hour).Sub(newest)
		for i := range past {
			if !past[i].PreAgent {
				past[i].At = past[i].At.Add(shift)
			}
		}
		if err := f.Seed(forge, strings.TrimSuffix(e.Name(), ".json"), past); err != nil {
			return err
		}
	}
	return nil
}
