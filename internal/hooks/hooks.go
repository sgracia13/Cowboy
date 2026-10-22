// Package hooks sends Cowboy's events to other tools (an orchestrator, a
// chat bot, a CI system) as signed HTTP POSTs, so they can react instead of
// polling: route a careful PR to a human, stop handing an agent work in an
// area where its PRs keep coming back, and so on.
//
// Sending never slows sorting down: events go into a queue and one worker
// delivers them in order, retrying with backoff. If the queue is full the
// event is dropped and logged. Every event is also in the audit record and
// the API, so a receiver that missed one can catch up.
package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Version is the event schema version. Fields are only ever added within a
// version; a breaking change gets a new number.
const Version = 1

// The event types.
const (
	PRSorted         = "pr.sorted"         // sorted or re-sorted into a pen (new PR, new commits, rule change)
	PRMoved          = "pr.moved"          // a person moved it to another pen
	PRMerged         = "pr.merged"         // merged
	PRClosed         = "pr.closed"         // closed without merging
	PRNotAgent       = "pr.not_agent"      // a person said an agent:likely PR is human work
	OutcomeFound     = "outcome.found"     // trouble found after merge (a revert, broken main; or a suggestion to confirm)
	OutcomeConfirmed = "outcome.confirmed" // a person confirmed or rejected a suggested outcome
	IncidentReceived = "incident.received" // an incident arrived from a monitoring tool
	Test             = "test"              // sent by `cowboy hooks test`
)

// Types are all event types, for docs and validation.
var Types = []string{PRSorted, PRMoved, PRMerged, PRClosed, PRNotAgent, OutcomeFound, OutcomeConfirmed, IncidentReceived, Test}

// PR is the pull request an event is about, as Cowboy sees it now.
type PR struct {
	Title     string     `json:"title,omitempty"`
	URL       string     `json:"url,omitempty"`
	State     string     `json:"state,omitempty"` // open, merged, closed
	Pen       string     `json:"pen,omitempty"`   // the pen in force: a person's move, else Cowboy's
	RulesPen  string     `json:"rules_pen,omitempty"`
	MovedBy   bool       `json:"moved_by_person,omitempty"`
	Agent     string     `json:"agent,omitempty"` // "" = a person
	HeadSHA   string     `json:"head_sha,omitempty"`
	Careful   []string   `json:"careful_reasons,omitempty"`
	Shortcuts []Shortcut `json:"shortcuts,omitempty"`
	Lines     int        `json:"lines,omitempty"`
	Files     int        `json:"files,omitempty"`
}

// Shortcut is one shortcut found in the PR.
type Shortcut struct {
	Kind   string `json:"kind"`
	File   string `json:"file"`
	Detail string `json:"detail"`
}

// Event is one thing that happened.
type Event struct {
	V      int               `json:"v"`
	ID     string            `json:"id"` // unique; receivers can use it to drop repeats
	Type   string            `json:"type"`
	At     time.Time         `json:"at"`
	Repo   string            `json:"repo,omitempty"`
	Number int               `json:"number,omitempty"`
	Actor  string            `json:"actor,omitempty"` // who did it, when a person or tool did
	Data   map[string]string `json:"data,omitempty"`  // type-specific details (see docs/api.md)
	PR     *PR               `json:"pr,omitempty"`
}

// Sign returns the X-Cowboy-Signature value for a body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a signature a receiver got (for receivers written in Go).
func Verify(secret string, body []byte, sig string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(sig))
}

// Config says where events go.
type Config struct {
	URLs   []string
	Secret string          // signs every body; empty = unsigned (not recommended)
	Only   map[string]bool // nil = every type
}

// Parse reads EVENT_HOOKS-style settings: comma-separated http(s) URLs and
// an optional comma-separated list of types.
func Parse(urls, secret, only string) (Config, error) {
	c := Config{Secret: secret}
	for _, u := range strings.Split(urls, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
			return c, fmt.Errorf("event hook %q isn't an http(s) URL", u)
		}
		c.URLs = append(c.URLs, u)
	}
	if only = strings.TrimSpace(only); only != "" {
		c.Only = map[string]bool{}
		known := map[string]bool{}
		for _, t := range Types {
			known[t] = true
		}
		for _, t := range strings.Split(only, ",") {
			t = strings.TrimSpace(t)
			if !known[t] {
				return c, fmt.Errorf("unknown event type %q (known: %s)", t, strings.Join(Types, ", "))
			}
			c.Only[t] = true
		}
	}
	return c, nil
}

// Sender delivers events.
type Sender struct {
	cfg     Config
	client  *http.Client
	q       chan Event
	backoff []time.Duration
	wg      sync.WaitGroup
	once    sync.Once
	mu      sync.Mutex
	stats   Stats
}

// Stats count deliveries since start.
type Stats struct {
	Sent, Failed, Dropped int
	LastError             string
}

// New starts a sender (nil when no URLs are set: Send on nil does nothing).
func New(cfg Config) *Sender {
	if len(cfg.URLs) == 0 {
		return nil
	}
	s := &Sender{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}, q: make(chan Event, 1000),
		backoff: []time.Duration{time.Second, 4 * time.Second, 15 * time.Second, time.Minute}}
	s.wg.Add(1)
	go s.run()
	return s
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Send queues an event; it never blocks.
func (s *Sender) Send(e Event) {
	if s == nil || (s.cfg.Only != nil && !s.cfg.Only[e.Type]) {
		return
	}
	e.V = Version
	if e.ID == "" {
		e.ID = newID()
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	select {
	case s.q <- e:
	default:
		s.mu.Lock()
		s.stats.Dropped++
		s.mu.Unlock()
		log.Printf("event hooks: queue full, dropped %s for %s#%d", e.Type, e.Repo, e.Number)
	}
}

// Stats returns delivery counts.
func (s *Sender) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close stops taking events and delivers what is queued, until ctx ends.
func (s *Sender) Close(ctx context.Context) {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.q) })
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *Sender) run() {
	defer s.wg.Done()
	for e := range s.q {
		body, _ := json.Marshal(e)
		for _, u := range s.cfg.URLs {
			err := s.deliver(u, e, body)
			s.mu.Lock()
			if err != nil {
				s.stats.Failed++
				s.stats.LastError = err.Error()
			} else {
				s.stats.Sent++
			}
			s.mu.Unlock()
			if err != nil {
				log.Printf("event hooks: %s %s#%d to %s: %v", e.Type, e.Repo, e.Number, host(u), err)
			}
		}
	}
}

func host(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return "?"
}

// deliver POSTs one event, retrying on network errors, 429 and 5xx.
func (s *Sender) deliver(u string, e Event, body []byte) error {
	var last error
	for attempt := 0; attempt <= len(s.backoff); attempt++ {
		if attempt > 0 {
			time.Sleep(s.backoff[attempt-1])
		}
		req, err := http.NewRequest("POST", u, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "cowboy-hooks")
		req.Header.Set("X-Cowboy-Event", e.Type)
		req.Header.Set("X-Cowboy-Delivery", e.ID)
		if s.cfg.Secret != "" {
			req.Header.Set("X-Cowboy-Signature", Sign(s.cfg.Secret, body))
		}
		resp, err := s.client.Do(req)
		if err != nil {
			last = err
			continue
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode < 300:
			return nil
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
			last = fmt.Errorf("receiver said %d", resp.StatusCode)
			continue
		default:
			return fmt.Errorf("receiver said %d (not retried)", resp.StatusCode)
		}
	}
	return last
}
