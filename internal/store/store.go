// Package store keeps Cowboy's decisions and corrections.
//
// The product plan uses SQLite. This prototype writes one JSON file so it
// runs on the Go standard library alone; the Store type is the seam where
// SQLite drops in later.
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

type data struct {
	Decisions   map[string]pipeline.Decision `json:"decisions"`
	Corrections []Correction                 `json:"corrections"`
	Repos       []string                     `json:"repos"`
}

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	d    data
}

// Open loads (or creates) the store file.
func Open(path string) (*Store, error) {
	s := &Store{path: path, d: data{Decisions: map[string]pipeline.Decision{}}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if s.d.Decisions == nil {
			s.d.Decisions = map[string]pipeline.Decision{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func key(repo string, n int) string { return fmt.Sprintf("%s#%d", repo, n) }

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.d, "", " ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(s.path), 0o755)
	// write, flush to disk, then swap in: a crash mid-write never leaves a
	// half-written store behind
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
	s.trackRepo(d.Repo)
	return s.save()
}

func (s *Store) trackRepo(repo string) {
	for _, r := range s.d.Repos {
		if r == repo {
			return
		}
	}
	s.d.Repos = append(s.d.Repos, repo)
}

// AddRepo remembers a repo to sweep.
func (s *Store) AddRepo(repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trackRepo(repo)
	return s.save()
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
	return s.save()
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
	return s.save()
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
	s.d.Corrections = append(s.d.Corrections, Correction{repo, n, from, to, by, time.Now().UTC()})
	return d, s.save()
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
