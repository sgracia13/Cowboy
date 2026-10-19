// Package outcome tracks what happened to pull requests after they merged:
// reverts, main breaking, follow-up fixes, incidents and how much code
// survived. It ties each outcome back to the pen Cowboy gave the PR, so a
// team can see whether the rules sent attention to the right places.
//
// Outcomes Cowboy is sure of (a revert, failing checks on the merge commit)
// count straight away unless someone rejects them. Outcomes that are a
// guess (a follow-up fix, an incident linked by timing and files) are
// suggestions until a person confirms them.
package outcome

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kinds of outcome.
const (
	Revert   = "revert"   // the change was reverted
	Break    = "break"    // checks failed on the merge commit after a green PR
	Fix      = "fix"      // a follow-up PR fixed it (suggested)
	Incident = "incident" // linked to an incident (suggested)
)

// Statuses.
const (
	Auto      = "auto"      // Cowboy is sure; counts unless rejected
	Suggested = "suggested" // a guess; counts once confirmed
	Confirmed = "confirmed"
	Rejected  = "rejected"
)

// Severity: 1 minor (main broke, a follow-up fix), 2 reverted,
// 3 incident, 4 major incident.
var KindLabel = map[string]string{Revert: "reverted", Break: "broke main", Fix: "needed a fix", Incident: "incident"}

// Merge is what Cowboy records about a merged pull request.
type Merge struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	SHA       string    `json:"sha"`
	MergedAt  time.Time `json:"merged_at"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	Files     []string  `json:"files"`
	Approvers []string  `json:"approvers,omitempty"`
	// ReviewsKnown: the host's reviews were read (Approvers is complete).
	ReviewsKnown bool      `json:"reviews_known,omitempty"`
	Release      string    `json:"release,omitempty"`
	ReleasedAt   time.Time `json:"released_at,omitempty"`
	// BreakChecked: the merge commit's checks have been looked at.
	BreakChecked bool `json:"break_checked,omitempty"`
	// Lines holds short hashes of a sample of added lines per file, to
	// measure how much of the change is still there later. No code is kept,
	// and the hashes are dropped after the 90-day measurement.
	Lines    map[string][]uint32 `json:"lines,omitempty"`
	Survival map[string]float64  `json:"survival,omitempty"` // "30d", "90d" -> share still there
	// Removed holds short hashes of a sample of the lines this PR removed or
	// rewrote, to tell when it changed lines an earlier PR added.
	Removed []uint32 `json:"removed,omitempty"`
}

// Outcome is one thing that happened to a merged PR.
type Outcome struct {
	ID        string    `json:"id"`
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Kind      string    `json:"kind"`
	Severity  int       `json:"severity"`
	At        time.Time `json:"at"`    // when it happened
	Found     time.Time `json:"found"` // when Cowboy noticed
	Evidence  string    `json:"evidence"`
	Link      string    `json:"link,omitempty"`
	Source    string    `json:"source"` // git, checks, sentry, pagerduty, webhook, person
	Status    string    `json:"status"`
	By        string    `json:"by,omitempty"`
	DecidedAt time.Time `json:"decided_at,omitempty"`
	Incident  string    `json:"incident,omitempty"`
	Score     float64   `json:"score,omitempty"`
}

// Counts reports whether an outcome counts against its PR.
func (o Outcome) Counts() bool { return o.Status == Auto || o.Status == Confirmed }

// IncidentRecord is an incident received from a monitoring tool.
type IncidentRecord struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	URL      string    `json:"url,omitempty"`
	Source   string    `json:"source"`
	Repo     string    `json:"repo"`
	Release  string    `json:"release,omitempty"`
	Severity int       `json:"severity"`
	Started  time.Time `json:"started"`
	Files    []string  `json:"files,omitempty"`
	Received time.Time `json:"received"`
}

// Scan remembers how far each repo has been read.
type Scan struct {
	Commits  time.Time `json:"commits"`
	Releases time.Time `json:"releases"`
}

type data struct {
	Merges    map[string]*Merge `json:"merges"`
	Outcomes  []Outcome         `json:"outcomes"`
	Incidents []IncidentRecord  `json:"incidents"`
	Scans     map[string]*Scan  `json:"scans"`
}

// State is the outcomes file (outcomes.json next to the data file).
type State struct {
	mu    sync.Mutex
	path  string
	d     data
	dirty bool
}

func key(repo string, n int) string { return fmt.Sprintf("%s#%d", strings.ToLower(repo), n) }

// Open loads or creates the outcomes file. An empty path keeps it in memory.
func Open(path string) (*State, error) {
	s := &State{path: path, d: data{Merges: map[string]*Merge{}, Scans: map[string]*Scan{}}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.d.Merges == nil {
		s.d.Merges = map[string]*Merge{}
	}
	if s.d.Scans == nil {
		s.d.Scans = map[string]*Scan{}
	}
	return s, nil
}

// Flush writes changes made by the watcher (which batches its writes).
func (s *State) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	return s.save()
}

func (s *State) save() error {
	s.dirty = false
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.d)
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Merge returns the record for a merged PR.
func (s *State) Merge(repo string, n int) (Merge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.d.Merges[key(repo, n)]
	if !ok {
		return Merge{}, false
	}
	return *m, true
}

// PutMerge stores a merge record.
func (s *State) PutMerge(m Merge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := m
	s.d.Merges[key(m.Repo, m.Number)] = &cp
	s.dirty = true
	return nil
}

// Merges lists a repo's merge records ("" = all repos), oldest first.
func (s *State) Merges(repo string) []Merge {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Merge
	for _, m := range s.d.Merges {
		if repo == "" || strings.EqualFold(m.Repo, repo) {
			out = append(out, *m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].MergedAt.Equal(out[j].MergedAt) {
			return out[i].MergedAt.Before(out[j].MergedAt)
		}
		return out[i].Number < out[j].Number
	})
	return out
}

// update changes a merge record in place.
func (s *State) update(repo string, n int, f func(m *Merge)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.d.Merges[key(repo, n)]
	if !ok {
		return fmt.Errorf("no merge record for %s#%d", repo, n)
	}
	f(m)
	s.dirty = true
	return nil
}

func outcomeID(o Outcome) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s", strings.ToLower(o.Repo), o.Number, o.Kind, o.Incident, o.Evidence)))
	return hex.EncodeToString(h[:6])
}

// Add records an outcome once; the same evidence again is ignored. It
// reports whether the outcome was new.
func (s *State) Add(o Outcome) (Outcome, bool, error) {
	if o.ID == "" {
		o.ID = outcomeID(o)
	}
	if o.Found.IsZero() {
		o.Found = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.d.Outcomes {
		if x.ID == o.ID {
			return x, false, nil
		}
	}
	s.d.Outcomes = append(s.d.Outcomes, o)
	return o, true, s.save()
}

// Decide confirms or rejects an outcome.
func (s *State) Decide(id string, confirm bool, by string) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.d.Outcomes {
		if s.d.Outcomes[i].ID != id {
			continue
		}
		o := &s.d.Outcomes[i]
		if confirm {
			o.Status = Confirmed
		} else {
			o.Status = Rejected
		}
		o.By, o.DecidedAt = by, time.Now().UTC()
		return *o, s.save()
	}
	return Outcome{}, fmt.Errorf("no outcome %q", id)
}

// Outcomes lists outcomes, newest first. repo "" = all; n 0 = all PRs.
func (s *State) Outcomes(repo string, n int) []Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Outcome
	for _, o := range s.d.Outcomes {
		if (repo == "" || strings.EqualFold(o.Repo, repo)) && (n == 0 || o.Number == n) {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// AddIncident stores an incident; the same ID again is ignored.
func (s *State) AddIncident(inc IncidentRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.d.Incidents {
		if x.ID == inc.ID {
			return false, nil
		}
	}
	s.d.Incidents = append(s.d.Incidents, inc)
	return true, s.save()
}

// Incidents lists incidents, newest first.
func (s *State) Incidents() []IncidentRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]IncidentRecord(nil), s.d.Incidents...)
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// Incident finds one incident by ID.
func (s *State) Incident(id string) (IncidentRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.d.Incidents {
		if x.ID == id {
			return x, true
		}
	}
	return IncidentRecord{}, false
}

func (s *State) scan(repo string) Scan {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc := s.d.Scans[strings.ToLower(repo)]; sc != nil {
		return *sc
	}
	return Scan{}
}

func (s *State) setScan(repo string, sc Scan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := sc
	s.d.Scans[strings.ToLower(repo)] = &cp
	s.dirty = true
	return nil
}

// Rewind makes the next check read the default branch and releases from
// `since` again (after a backfill adds older PRs).
func (s *State) Rewind(repo string, since time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.d.Scans[strings.ToLower(repo)]
	if sc == nil {
		return
	}
	if sc.Commits.After(since) {
		sc.Commits = since
	}
	if sc.Releases.After(since) {
		sc.Releases = since
	}
	s.dirty = true
}
