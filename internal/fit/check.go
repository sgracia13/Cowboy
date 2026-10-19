package fit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Check is how a fit did on a month it didn't see, next to the default
// rules and "biggest PRs first" given the same share of PRs.
type Check struct {
	From, To     time.Time
	PRs          int
	Trouble      int
	Fitted       int     // troubled PRs the fitted rules flagged
	FittedShare  float64 // share of PRs the fitted rules flagged
	Defaults     int     // troubled PRs the default rules flagged
	DefaultShare float64
	Biggest      int // troubled PRs among the same share of largest PRs
	Wins         bool
}

// Sized is an example with what the default rules said and its size.
type Sized struct {
	Example
	DefaultCareful bool
	Lines          int
}

// CheckFit fits as of `from` (on the Days before it) and tests the fit on
// the PRs merged from `from` to `to`. Pick `to` at least Window days ago so
// every test PR's trouble window has passed. With no usable fit the
// default rules stand in, as they would on the live repo.
func CheckFit(repo string, ex []Sized, from, to time.Time, o Options) (Result, Check) {
	base := make([]Example, len(ex))
	for i, e := range ex {
		base[i] = e.Example
	}
	r := Fit(repo, base, from, o)
	c := Check{From: from, To: to}
	var test []Sized
	for _, e := range ex {
		if !e.Merged.Before(from) && e.Merged.Before(to) {
			test = append(test, e)
		}
	}
	c.PRs = len(test)
	if len(test) == 0 {
		return r, c
	}
	win := time.Duration(o.Window) * 24 * time.Hour
	bad := func(e Sized) bool { return !e.TroubleAt.IsZero() && e.TroubleAt.Sub(e.Merged) <= win }
	flagged := 0
	for _, e := range test {
		b := bad(e)
		if b {
			c.Trouble++
		}
		careful := e.DefaultCareful
		if r.Enough && len(r.Rules) > 0 {
			careful = len(r.Careful(e.Facts)) > 0
		}
		if careful {
			flagged++
			if b {
				c.Fitted++
			}
		}
		if e.DefaultCareful {
			c.DefaultShare++
			if b {
				c.Defaults++
			}
		}
	}
	c.FittedShare = float64(flagged) / float64(len(test))
	c.DefaultShare /= float64(len(test))
	big := append([]Sized(nil), test...)
	sort.SliceStable(big, func(i, j int) bool {
		if big[i].Lines != big[j].Lines {
			return big[i].Lines > big[j].Lines
		}
		return big[i].Number < big[j].Number
	})
	for _, e := range big[:flagged] {
		if bad(e) {
			c.Biggest++
		}
	}
	// Wins: caught more than biggest-first with the same share, and was at
	// least as precise as the defaults.
	precise := func(caught int, share float64) float64 {
		if share == 0 {
			return 0
		}
		return float64(caught) / share
	}
	c.Wins = r.Enough && len(r.Rules) > 0 && c.Trouble > 0 && c.Fitted > c.Biggest &&
		precise(c.Fitted, c.FittedShare) >= precise(c.Defaults, c.DefaultShare)
	return r, c
}

// Store keeps the fits in use, one per repo (fits.json next to the data
// file).
type Store struct {
	mu   sync.Mutex
	path string
	m    map[string]Result
}

// OpenStore loads fits.json; a missing file is empty.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, m: map[string]Result{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, &s.m)
}

// Get returns the fit in use for a repo.
func (s *Store) Get(repo string) (Result, bool) {
	if s == nil {
		return Result{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[strings.ToLower(repo)]
	return r, ok
}

// Put stores (or, with no rules, clears) a repo's fit.
func (s *Store) Put(r Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := strings.ToLower(r.Repo)
	if len(r.Rules) == 0 {
		delete(s.m, k)
	} else {
		s.m[k] = r
	}
	b, err := json.MarshalIndent(s.m, "", " ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
