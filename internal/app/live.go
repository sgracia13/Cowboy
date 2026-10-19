package app

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// Event is one thing Cowboy did. The corral listens to these to update live,
// and the trail page lists the recent ones.
type Event struct {
	Kind   string    `json:"kind"` // sorted, notes, moved, closed, stray, caught-up
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	Pen    string    `json:"pen,omitempty"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

type hub struct {
	mu     sync.Mutex
	subs   map[chan Event]bool
	recent []Event
}

// Subscribe returns a channel of new events and a function to stop.
func (a *App) Subscribe() (<-chan Event, func()) {
	h := a.hub()
	ch := make(chan Event, 32)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if h.subs[ch] {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Recent returns the latest events, newest first.
func (a *App) Recent() []Event {
	h := a.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Event, len(h.recent))
	for i, e := range h.recent {
		out[len(h.recent)-1-i] = e
	}
	return out
}

func (a *App) hub() *hub {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.events == nil {
		a.events = &hub{subs: map[chan Event]bool{}}
	}
	return a.events
}

func (a *App) publish(kind, repo string, n int, pen, format string, args ...any) {
	e := Event{Kind: kind, Repo: repo, Number: n, Pen: pen, Text: fmt.Sprintf(format, args...), At: a.Now().UTC()}
	h := a.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recent = append(h.recent, e)
	if len(h.recent) > 200 {
		h.recent = h.recent[len(h.recent)-200:]
	}
	for ch := range h.subs {
		select {
		case ch <- e:
		default: // a slow browser tab misses one update; its next one catches up
		}
	}
}

// Announce posts a free-text line to the activity feed (the demo uses it).
func (a *App) Announce(repo string, n int, text string) {
	a.publish("info", repo, n, "", "%s", text)
}

// skip remembers PRs that aren't from an agent, per head SHA, so the
// catch-up sweep doesn't fetch them again until they change.
func (a *App) skip(repo string, n int, sha string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.skipped == nil {
		a.skipped = map[string]bool{}
	}
	a.skipped[fmt.Sprintf("%s#%d@%s", repo, n, sha)] = true
}

func (a *App) isSkipped(repo string, n int, sha string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.skipped[fmt.Sprintf("%s#%d@%s", repo, n, sha)]
}

// Reconcile is the catch-up sweep. Webhooks get lost (server restarts,
// network blips, a full queue), so every few minutes Cowboy lists each
// repo's open PRs and:
//   - sorts any agent PR it has never seen, or whose code changed since
//   - marks PRs it thought were open as merged or closed
//
// It costs one list call per repo when nothing changed.
func (a *App) Reconcile() (caught []string) {
	for _, repo := range a.Store.Repos() {
		open, err := a.Forge.ListOpenPRs(repo)
		if err != nil {
			log.Printf("catch-up %s: %v", repo, err)
			continue
		}
		// Oldest first, so a newer PR that repeats an older one is the one
		// marked as the double (found on a real repo: hosts list newest first).
		sort.Slice(open, func(i, j int) bool { return open[i].Number < open[j].Number })
		isOpen := map[int]bool{}
		for _, o := range open {
			isOpen[o.Number] = true
			d, known := a.Store.Get(repo, o.Number)
			if known && d.HeadSHA == o.HeadSHA && d.State == "open" {
				continue
			}
			if !known && a.isSkipped(repo, o.Number, o.HeadSHA) {
				continue
			}
			got, ok, err := a.HandlePR(repo, o.Number)
			if err != nil || !ok {
				continue
			}
			why := "missed webhook"
			if known {
				why = "new commits missed"
			}
			msg := fmt.Sprintf("%s#%d caught up (%s) -> %s", repo, o.Number, why, got.Pen)
			caught = append(caught, msg)
			a.publish("caught-up", repo, o.Number, string(got.Pen), "#%d caught up by the sweep (%s), sorted %s", o.Number, why, got.Pen)
		}
		for _, d := range a.Store.List(repo, "open") {
			if isOpen[d.Number] {
				continue
			}
			_, state, err := a.Forge.GetPR(repo, d.Number)
			if err != nil || state == "open" {
				continue
			}
			a.Closed(repo, d.Number, state == "merged")
			caught = append(caught, fmt.Sprintf("%s#%d marked %s by the sweep", repo, d.Number, state))
		}
	}
	return caught
}
