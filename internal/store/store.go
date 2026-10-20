// Package store keeps Cowboy's decisions and corrections.
//
// They live in SQLite (internal/db), in a file next to the data path
// (data/cowboy.json -> data/cowboy.db), and are held in memory for reads.
// Each change writes only the rows it touched. A cowboy.json from an
// older version is imported once and kept as cowboy.json.migrated.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/db"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
)

// Correction is a human moving a PR to another pen.
type Correction struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	From   score.Pen `json:"from"`
	To     score.Pen `json:"to"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// data is the whole store; also the layout of the legacy JSON file and of
// ExportJSON.
type data struct {
	Decisions   map[string]pipeline.Decision `json:"decisions"`
	Corrections []Correction                 `json:"corrections"`
	Repos       []string                     `json:"repos"`
}

const (
	kDecision   = "decision"
	kCorrection = "correction"
	kRepos      = "repos"
)

// Store is safe for concurrent use.
type Store struct {
	mu sync.Mutex
	db *db.DB // nil: memory only
	d  data
}

// Open loads (or creates) the store for a data path like data/cowboy.json.
// An empty path keeps everything in memory.
func Open(path string) (*Store, error) {
	s := &Store{d: data{Decisions: map[string]pipeline.Decision{}}}
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

// importJSON moves a store file from before SQLite into the database, once.
func (s *Store) importJSON(path string) error {
	if s.db.Meta("imported:store") != "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s.db.SetMeta("imported:store", "none")
	}
	if err != nil {
		return err
	}
	var old data
	if err := json.Unmarshal(b, &old); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var ops []db.Op
	for k, d := range old.Decisions {
		ops = append(ops, op(kDecision, k, d))
	}
	for i, c := range old.Corrections {
		ops = append(ops, op(kCorrection, fmt.Sprintf("%012d", i), c))
	}
	ops = append(ops, op(kRepos, "all", old.Repos))
	if err := s.db.Write(ops...); err != nil {
		return fmt.Errorf("importing %s: %w", path, err)
	}
	if err := s.db.SetMeta("imported:store", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return os.Rename(path, path+".migrated")
}

func op(kind, key string, v any) db.Op {
	b, _ := json.Marshal(v)
	return db.Op{Kind: kind, Key: key, Body: b}
}

func (s *Store) load() error {
	m, err := s.db.Load(kDecision)
	if err != nil {
		return err
	}
	for k, b := range m {
		var d pipeline.Decision
		if err := json.Unmarshal(b, &d); err != nil {
			return fmt.Errorf("decision %s: %w", k, err)
		}
		s.d.Decisions[k] = d
	}
	cs, err := s.db.Load(kCorrection)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(cs))
	for k := range cs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var c Correction
		if err := json.Unmarshal(cs[k], &c); err != nil {
			return err
		}
		s.d.Corrections = append(s.d.Corrections, c)
	}
	if r, err := s.db.Load(kRepos); err != nil {
		return err
	} else if b, ok := r["all"]; ok {
		json.Unmarshal(b, &s.d.Repos)
	}
	return nil
}

// write saves changed rows (nothing to do in memory).
func (s *Store) write(ops ...db.Op) error {
	if s.db == nil {
		return nil
	}
	return s.db.Write(ops...)
}

func (s *Store) reposOp() db.Op { return op(kRepos, "all", s.d.Repos) }

// ExportJSON writes the whole store in the legacy cowboy.json layout (for
// the evaluation scripts, which read that file).
func (s *Store) ExportJSON(path string) error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.d, "", " ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, b, 0o600)
}

func key(repo string, n int) string { return fmt.Sprintf("%s#%d", repo, n) }

// Put saves a decision.
func (s *Store) Put(d pipeline.Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.d.Decisions[key(d.Repo, d.Number)]; ok && old.Corrected != "" && old.HeadSHA == d.HeadSHA {
		d.Corrected = old.Corrected // keep a human's choice for the same code
	}
	if old, ok := s.d.Decisions[key(d.Repo, d.Number)]; ok && d.AgentCleared == "" && old.AgentCleared != "" {
		d.AgentCleared = old.AgentCleared // a person said "not an agent"; that sticks
	}
	if old, ok := s.d.Decisions[key(d.Repo, d.Number)]; ok && d.Notes == nil && old.Notes != nil && old.HeadSHA == d.HeadSHA {
		d.Notes = old.Notes // model notes are per head SHA
	}
	s.d.Decisions[key(d.Repo, d.Number)] = d
	ops := []db.Op{op(kDecision, key(d.Repo, d.Number), d)}
	if s.trackRepo(d.Repo) {
		ops = append(ops, s.reposOp())
	}
	return s.write(ops...)
}

// trackRepo adds a repo to the list and reports whether it was new.
func (s *Store) trackRepo(repo string) bool {
	for _, r := range s.d.Repos {
		if r == repo {
			return false
		}
	}
	s.d.Repos = append(s.d.Repos, repo)
	return true
}

// AddRepo remembers a repo to sweep.
func (s *Store) AddRepo(repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.trackRepo(repo) {
		return nil
	}
	return s.write(s.reposOp())
}

// RemoveRepo stops sweeping a repo (its decisions stay for the record).
func (s *Store) RemoveRepo(repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.d.Repos[:0]
	for _, r := range s.d.Repos {
		if !strings.EqualFold(r, repo) {
			kept = append(kept, r)
		}
	}
	s.d.Repos = kept
	return s.write(s.reposOp())
}

// Repos lists tracked repos.
func (s *Store) Repos() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.d.Repos...)
}

// Get returns one decision.
func (s *Store) Get(repo string, n int) (pipeline.Decision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.d.Decisions[key(repo, n)]
	return d, ok
}

// SetState marks a PR open, closed or merged.
func (s *Store) SetState(repo string, n int, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.d.Decisions[key(repo, n)]
	if !ok {
		return nil
	}
	d.State = state
	s.d.Decisions[key(repo, n)] = d
	return s.write(op(kDecision, key(repo, n), d))
}

// Correct records a human moving a PR to another pen.
func (s *Store) Correct(repo string, n int, to score.Pen, by string) (pipeline.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.d.Decisions[key(repo, n)]
	if !ok {
		return d, fmt.Errorf("no decision for %s#%d", repo, n)
	}
	from := d.Pen
	if d.Corrected != "" {
		from = d.Corrected
	}
	if from == to {
		return d, nil
	}
	d.Corrected = to
	s.d.Decisions[key(repo, n)] = d
	c := Correction{repo, n, from, to, by, time.Now().UTC()}
	s.d.Corrections = append(s.d.Corrections, c)
	return d, s.write(op(kDecision, key(repo, n), d), op(kCorrection, fmt.Sprintf("%012d", len(s.d.Corrections)-1), c))
}

// List returns decisions, newest PR number first. state "" means all.
func (s *Store) List(repo, state string) []pipeline.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pipeline.Decision
	for _, d := range s.d.Decisions {
		if (repo == "" || d.Repo == repo) && (state == "" || d.State == state) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Number > out[j].Number
	})
	return out
}

// Corrections returns all human re-pens.
func (s *Store) Corrections() []Correction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Correction(nil), s.d.Corrections...)
}
