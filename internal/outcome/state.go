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
	"github.com/sgracia13/Cowboy/internal/db"
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
	// AI reviewers that reviewed this PR before merge, and what they
	// flagged (inline comments). AIKnown: the host's reviews were read, so
	// an empty list means no AI reviewer took part.
	AIReviewers []string    `json:"ai_reviewers,omitempty"`
	AIFindings  []AIFinding `json:"ai_findings,omitempty"`
	AIKnown     bool        `json:"ai_known,omitempty"`
}

// AIFinding is one thing an AI reviewer flagged before merge. A person can
// mark the one that named what later went wrong ("called it").
type AIFinding struct {
	Reviewer string    `json:"reviewer"`
	Path     string    `json:"path,omitempty"`
	Line     int       `json:"line,omitempty"`
	Text     string    `json:"text"` // a one-line summary, not the whole comment
	At       time.Time `json:"at"`
	CalledBy string    `json:"called_by,omitempty"`
	CalledAt time.Time `json:"called_at,omitempty"`
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

// State is what Cowboy knows after merge: merge records, outcomes,
// incidents and how far each repo has been read. It lives in SQLite next
// to the data path (data/outcomes.json -> data/outcomes.db) and in memory
// for reads; changes are written row by row. An outcomes.json from an
// older version is imported once and kept as outcomes.json.migrated.
type State struct {
	mu      sync.Mutex
	db      *db.DB // nil: memory only
	d       data
	pending map[string]map[string]bool // kind -> keys changed since the last save
	dirty   bool
}

const (
	kMerge    = "merge"
	kOutcome  = "outcome"
	kIncident = "incident"
	kScan     = "scan"
)

func key(repo string, n int) string { return fmt.Sprintf("%s#%d", strings.ToLower(repo), n) }

func newState() *State {
	return &State{d: data{Merges: map[string]*Merge{}, Scans: map[string]*Scan{}}, pending: map[string]map[string]bool{}}
}

// Open loads or creates the outcomes store for a path like
// data/outcomes.json. An empty path keeps it in memory.
func Open(path string) (*State, error) {
	s := newState()
	if path == "" {
		return s, nil
	}
	d, err := db.Open(db.PathFor(path))
	if err != nil {
		return nil, err
	}
	s.db = d
	if err := s.importJSON(path); err != nil {
		return nil, err
	}
	return s, s.load()
}

// Load builds an in-memory state from the JSON layout (the demo's replayed
// history ships this way). Nothing is written to disk.
func Load(b []byte) (*State, error) {
	s := newState()
	if err := json.Unmarshal(b, &s.d); err != nil {
		return nil, err
	}
	s.fill()
	return s, nil
}

func (s *State) fill() {
	if s.d.Merges == nil {
		s.d.Merges = map[string]*Merge{}
	}
	if s.d.Scans == nil {
		s.d.Scans = map[string]*Scan{}
	}
}

// importJSON moves an outcomes file from before SQLite into the database,
// once.
func (s *State) importJSON(path string) error {
	if s.db.Meta("imported:outcomes") != "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s.db.SetMeta("imported:outcomes", "none")
	}
	if err != nil {
		return err
	}
	old := newState()
	if err := json.Unmarshal(b, &old.d); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	old.fill()
	if err := s.db.Write(old.allOps()...); err != nil {
		return fmt.Errorf("importing %s: %w", path, err)
	}
	if err := s.db.SetMeta("imported:outcomes", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return os.Rename(path, path+".migrated")
}

func rowOp(kind, k string, v any) db.Op {
	b, _ := json.Marshal(v)
	return db.Op{Kind: kind, Key: k, Body: b}
}

func (s *State) allOps() []db.Op {
	var ops []db.Op
	for k, m := range s.d.Merges {
		ops = append(ops, rowOp(kMerge, k, m))
	}
	for _, o := range s.d.Outcomes {
		ops = append(ops, rowOp(kOutcome, o.ID, o))
	}
	for _, x := range s.d.Incidents {
		ops = append(ops, rowOp(kIncident, x.ID, x))
	}
	for k, sc := range s.d.Scans {
		ops = append(ops, rowOp(kScan, k, sc))
	}
	return ops
}

func (s *State) load() error {
	ms, err := s.db.Load(kMerge)
	if err != nil {
		return err
	}
	for k, b := range ms {
		var m Merge
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("merge %s: %w", k, err)
		}
		s.d.Merges[k] = &m
	}
	os_, err := s.db.Load(kOutcome)
	if err != nil {
		return err
	}
	for _, b := range os_ {
		var o Outcome
		if err := json.Unmarshal(b, &o); err != nil {
			return err
		}
		s.d.Outcomes = append(s.d.Outcomes, o)
	}
	// a stable order (the file kept insertion order; rows have none)
	sort.Slice(s.d.Outcomes, func(i, j int) bool {
		if !s.d.Outcomes[i].Found.Equal(s.d.Outcomes[j].Found) {
			return s.d.Outcomes[i].Found.Before(s.d.Outcomes[j].Found)
		}
		return s.d.Outcomes[i].ID < s.d.Outcomes[j].ID
	})
	is, err := s.db.Load(kIncident)
	if err != nil {
		return err
	}
	for _, b := range is {
		var x IncidentRecord
		if err := json.Unmarshal(b, &x); err != nil {
			return err
		}
		s.d.Incidents = append(s.d.Incidents, x)
	}
	ss, err := s.db.Load(kScan)
	if err != nil {
		return err
	}
	for k, b := range ss {
		var sc Scan
		if err := json.Unmarshal(b, &sc); err != nil {
			return err
		}
		s.d.Scans[k] = &sc
	}
	return nil
}

// mark notes a changed row (caller holds mu).
func (s *State) mark(kind, k string) {
	if s.pending[kind] == nil {
		s.pending[kind] = map[string]bool{}
	}
	s.pending[kind][k] = true
	s.dirty = true
}

// ExportJSON writes everything in the legacy outcomes.json layout (the
// evaluation scripts read that file).
func (s *State) ExportJSON(path string) error {
	s.mu.Lock()
	b, err := json.Marshal(s.d)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, b, 0o600)
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

// save writes the changed rows (caller holds mu).
func (s *State) save() error {
	s.dirty = false
	pending := s.pending
	s.pending = map[string]map[string]bool{}
	if s.db == nil {
		return nil
	}
	var ops []db.Op
	for k := range pending[kMerge] {
		if m, ok := s.d.Merges[k]; ok {
			ops = append(ops, rowOp(kMerge, k, m))
		} else {
			ops = append(ops, db.Op{Kind: kMerge, Key: k, Delete: true})
		}
	}
	for i := range s.d.Outcomes {
		if o := s.d.Outcomes[i]; pending[kOutcome][o.ID] {
			ops = append(ops, rowOp(kOutcome, o.ID, o))
		}
	}
	for _, x := range s.d.Incidents {
		if pending[kIncident][x.ID] {
			ops = append(ops, rowOp(kIncident, x.ID, x))
		}
	}
	for k := range pending[kScan] {
		if sc, ok := s.d.Scans[k]; ok {
			ops = append(ops, rowOp(kScan, k, sc))
		}
	}
	if err := s.db.Write(ops...); err != nil {
		// keep them pending so the next save retries
		for kind, ks := range pending {
			for k := range ks {
				s.mark(kind, k)
			}
		}
		return err
	}
	return nil
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
	s.mark(kMerge, key(m.Repo, m.Number))
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
	s.mark(kMerge, key(repo, n))
	return nil
}

// SetAIReview stores the AI reviewers' findings on a merge record.
func (s *State) SetAIReview(repo string, n int, reviewers []string, findings []AIFinding) error {
	return s.update(repo, n, func(m *Merge) { m.AIReviewers, m.AIFindings, m.AIKnown = reviewers, findings, true })
}

// CallIt marks (or unmarks) finding i of a merged PR as the one that named
// what went wrong, and saves.
func (s *State) CallIt(repo string, n, i int, by string, on bool, at time.Time) (AIFinding, error) {
	var f AIFinding
	err := s.update(repo, n, func(m *Merge) {
		if i < 0 || i >= len(m.AIFindings) {
			return
		}
		if on {
			m.AIFindings[i].CalledBy, m.AIFindings[i].CalledAt = by, at
		} else {
			m.AIFindings[i].CalledBy, m.AIFindings[i].CalledAt = "", time.Time{}
		}
		f = m.AIFindings[i]
	})
	if err != nil {
		return f, err
	}
	if f.Reviewer == "" {
		return f, fmt.Errorf("no finding %d on %s#%d", i, repo, n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return f, s.save()
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
	s.mark(kOutcome, o.ID)
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
		s.mark(kOutcome, o.ID)
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
	s.mark(kIncident, inc.ID)
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
	s.mark(kScan, strings.ToLower(repo))
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
	s.mark(kScan, strings.ToLower(repo))
}
