package fit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/config"
)

// A learned rule moves through these states, and only a person moves it:
//
//	suggested -> shadow -> active
//	     \          \         \
//	      rejected   rejected  off (back to shadow) or rejected
//
// Suggested: the fit found it in the repo's history; it changes nothing.
// Shadow: it is scored on every new pull request but sorts nothing, so it
// builds a record on PRs it never saw. Active: a person promoted it, and it
// sends matching PRs to careful alongside the default rules. Rejected: a
// person said no; a later fit won't suggest it again.
const (
	Suggested = "suggested"
	Shadow    = "shadow"
	Active    = "active"
	Rejected  = "rejected"
)

// Learned is one rule in a repo's rule book, with the history that
// suggested it and who moved it.
type Learned struct {
	Repo   string `json:"repo"`
	Fact   string `json:"fact"`
	Status string `json:"status"`
	// from the fit that suggested it
	N       float64   `json:"n"`
	K       int       `json:"k"`
	Rate    float64   `json:"rate"`
	Base    float64   `json:"base"`
	LiftLow float64   `json:"lift_low"`
	FitAt   time.Time `json:"fit_at"`
	From    time.Time `json:"from"`
	// AlreadyCareful is the share of the PRs with the fact that the
	// default rules already sent to careful.
	AlreadyCareful float64 `json:"already_careful"`
	// ShadowSince is when the shadow trial started: only PRs merged after
	// it count toward the rule's record.
	ShadowSince time.Time `json:"shadow_since,omitempty"`
	ActiveSince time.Time `json:"active_since,omitempty"`
	By          string    `json:"by,omitempty"`
	Changed     time.Time `json:"changed"`
}

// History is the rule's record in the history it was learned from.
func (l Learned) History() string {
	days := int(l.FitAt.Sub(l.From).Hours()/24 + 0.5)
	return fmt.Sprintf("%d of %s such PRs had trouble in the %d days before %s: %.1f× the usual rate, at least %.1f× allowing for chance",
		l.K, trimNum(l.N), days, l.FitAt.Format("Jan 2"), safeDiv(l.Rate, l.Base), l.LiftLow)
}

// Label is the reason shown on a PR the rule sends to careful.
func (l Learned) Label() string {
	return Describe(l.Fact) + ": a rule learned from this repo's history (" + l.History() + ")"
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// Book is every repo's learned rules (learned.json next to the data file).
type Book struct {
	mu   sync.Mutex
	path string
	m    map[string][]Learned // lower-case repo -> rules
}

// OpenBook loads learned.json; a missing file is an empty book. An empty
// path keeps the book in memory only.
func OpenBook(path string) (*Book, error) {
	b := &Book{path: path, m: map[string][]Learned{}}
	if path == "" {
		return b, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	return b, json.Unmarshal(data, &b.m)
}

func (b *Book) save() error {
	if b.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(b.m, "", " ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(b.path), 0o755)
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}

// Suggest records a fresh fit's rules as suggestions. Rules a person
// already moved (shadow, active, rejected) are left alone; old suggestions
// the new fit no longer makes are dropped. already gives, per fact, the
// share of its PRs the default rules already sent to careful. It returns
// how many suggestions are new.
func (b *Book) Suggest(r Result, already map[string]float64, now time.Time) (int, error) {
	if b == nil {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	k := strings.ToLower(r.Repo)
	kept := map[string]bool{}
	var out []Learned
	for _, l := range b.m[k] {
		if l.Status != Suggested {
			out = append(out, l)
			kept[l.Fact] = true
		}
	}
	added := 0
	old := map[string]bool{}
	for _, l := range b.m[k] {
		if l.Status == Suggested {
			old[l.Fact] = true
		}
	}
	for _, x := range r.Rules {
		if kept[x.Fact] {
			continue
		}
		if !old[x.Fact] {
			added++
		}
		out = append(out, Learned{Repo: r.Repo, Fact: x.Fact, Status: Suggested, N: x.N, K: x.K, Rate: x.Rate,
			Base: r.Base, LiftLow: x.LiftLow, FitAt: r.At, From: r.From, AlreadyCareful: already[x.Fact], Changed: now})
	}
	b.m[k] = out
	return added, b.save()
}

// Move changes a rule's status. Only these moves are allowed: suggested to
// shadow or rejected; shadow to active or rejected; active to shadow
// ("off": it stops sorting and keeps its record) or rejected.
func (b *Book) Move(repo, fact, to, by string, now time.Time) (Learned, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rules := b.m[strings.ToLower(repo)]
	for i := range rules {
		l := &rules[i]
		if l.Fact != fact {
			continue
		}
		ok := map[string][]string{Suggested: {Shadow, Rejected}, Shadow: {Active, Rejected}, Active: {Shadow, Rejected}}[l.Status]
		allowed := false
		for _, s := range ok {
			allowed = allowed || s == to
		}
		if !allowed {
			return *l, fmt.Errorf("a %s rule can't be moved to %s", l.Status, to)
		}
		switch {
		case to == Shadow && l.Status == Suggested:
			l.ShadowSince = now
		case to == Active:
			l.ActiveSince = now
		case to == Shadow && l.Status == Active:
			l.ActiveSince = time.Time{}
		}
		l.Status, l.By, l.Changed = to, by, now
		return *l, b.save()
	}
	return Learned{}, fmt.Errorf("no learned rule %q for %s", fact, repo)
}

// List returns a repo's learned rules ("" = every repo): active first, then
// shadow, suggested and rejected; by strength within each.
func (b *Book) List(repo string) []Learned {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Learned
	for k, rules := range b.m {
		if repo == "" || k == strings.ToLower(repo) {
			out = append(out, rules...)
		}
	}
	order := map[string]int{Active: 0, Shadow: 1, Suggested: 2, Rejected: 3}
	sort.SliceStable(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if a.Repo != c.Repo {
			return a.Repo < c.Repo
		}
		if order[a.Status] != order[c.Status] {
			return order[a.Status] < order[c.Status]
		}
		if a.LiftLow != c.LiftLow {
			return a.LiftLow > c.LiftLow
		}
		return a.Fact < c.Fact
	})
	return out
}

// Active is the rules a person promoted for a repo, ready for
// config.Config.Learned.
func (b *Book) Active(repo string) []config.FittedRule {
	var out []config.FittedRule
	for _, l := range b.List(repo) {
		if l.Status == Active {
			out = append(out, config.FittedRule{Fact: l.Fact, Label: l.Label()})
		}
	}
	return out
}
