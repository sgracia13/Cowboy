package outcome

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IncidentHooks receives incidents from monitoring tools.
//
//	POST /webhook/incident   any tool: Cowboy's own JSON, signed or with a bearer token
//	POST /webhook/sentry     Sentry's integration webhooks (issue alerts, issues)
//	POST /webhook/pagerduty  PagerDuty v3 webhooks (incident.triggered)
type IncidentHooks struct {
	Secret          string // INCIDENT_SECRET for /webhook/incident
	SentrySecret    string // the Sentry integration's client secret
	PagerDutySecret string // the PagerDuty webhook's signing secret
	// Repo picks the repo for an incident that doesn't name one (from a
	// mapping like SENTRY_PROJECTS, or the only repo Cowboy sorts).
	Repo   func(hint string) string
	Ingest func(IncidentRecord)
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return nil, false
	}
	return b, true
}

func hmacHex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// severity maps the many ways tools say "how bad" to 2..4.
func severity(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "1", "sev1", "sev-1", "p1", "critical", "fatal", "major", "4":
		return 4
	case "2", "sev2", "sev-2", "p2", "high", "error", "3":
		return 3
	}
	return 2
}

// Generic handles Cowboy's own incident JSON:
//
//	{"id": "INC-12", "title": "Checkout errors", "severity": "sev2",
//	 "started_at": "2026-10-19T14:02:00Z", "url": "https://…",
//	 "repo": "acme/web", "release": "v2.3.0", "files": ["src/pay/api.ts"]}
//
// Sign the body with X-Cowboy-Signature: sha256=<hmac>, or send
// Authorization: Bearer <INCIDENT_SECRET> from tools that can't sign.
func (h *IncidentHooks) Generic(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if h.Secret == "" {
		http.Error(w, "set INCIDENT_SECRET to receive incidents", http.StatusNotFound)
		return
	}
	sig := strings.TrimPrefix(r.Header.Get("X-Cowboy-Signature"), "sha256=")
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !(sig != "" && equal(sig, hmacHex(h.Secret, body))) && !(bearer != "" && equal(bearer, h.Secret)) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var p struct {
		ID, Title, URL, Repo, Release string
		Severity                      json.RawMessage
		StartedAt                     time.Time `json:"started_at"`
		Files                         []string
	}
	if err := json.Unmarshal(body, &p); err != nil || p.Title == "" {
		http.Error(w, "need JSON with at least a title", http.StatusBadRequest)
		return
	}
	sev := strings.Trim(string(p.Severity), `"`)
	if p.StartedAt.IsZero() {
		p.StartedAt = time.Now().UTC()
	}
	if p.ID == "" {
		p.ID = "webhook-" + hmacHex("id", body)[:12]
	}
	h.ingest(w, IncidentRecord{ID: p.ID, Title: p.Title, URL: p.URL, Source: "webhook", Repo: p.Repo, Release: p.Release,
		Severity: severity(sev), Started: p.StartedAt.UTC(), Files: p.Files})
}

// Sentry handles Sentry integration webhooks. Issue alerts ("event_alert")
// carry the release and stack frames, which is what makes a link likely;
// plain issue events carry only the title and time.
func (h *IncidentHooks) Sentry(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if h.SentrySecret == "" || !equal(r.Header.Get("Sentry-Hook-Signature"), hmacHex(h.SentrySecret, body)) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	resource := r.Header.Get("Sentry-Hook-Resource")
	var p struct {
		Action string `json:"action"`
		Data   struct {
			Event struct {
				EventID   string  `json:"event_id"`
				Title     string  `json:"title"`
				Release   string  `json:"release"`
				Level     string  `json:"level"`
				WebURL    string  `json:"web_url"`
				Timestamp float64 `json:"timestamp"`
				Datetime  string  `json:"datetime"`
				Project   any     `json:"project"`
				Exception struct {
					Values []struct {
						Stacktrace struct {
							Frames []struct {
								Filename string `json:"filename"`
								AbsPath  string `json:"abs_path"`
								InApp    *bool  `json:"in_app"`
							} `json:"frames"`
						} `json:"stacktrace"`
					} `json:"values"`
				} `json:"exception"`
			} `json:"event"`
			Issue struct {
				ID        string `json:"id"`
				Title     string `json:"title"`
				Level     string `json:"level"`
				Permalink string `json:"permalink"`
				FirstSeen string `json:"firstSeen"`
				Project   struct {
					Slug string `json:"slug"`
				} `json:"project"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	switch {
	case resource == "event_alert" || p.Data.Event.Title != "":
		e := p.Data.Event
		inc := IncidentRecord{ID: "sentry-" + e.EventID, Title: e.Title, URL: e.WebURL, Source: "sentry", Release: e.Release, Severity: severity(e.Level)}
		if t, err := time.Parse(time.RFC3339, e.Datetime); err == nil {
			inc.Started = t.UTC()
		} else if e.Timestamp > 0 {
			inc.Started = time.Unix(int64(e.Timestamp), 0).UTC()
		}
		seen := map[string]bool{}
		for _, v := range e.Exception.Values {
			for _, f := range v.Stacktrace.Frames {
				name := f.Filename
				if name == "" {
					name = f.AbsPath
				}
				if name == "" || (f.InApp != nil && !*f.InApp) || seen[name] {
					continue
				}
				seen[name] = true
				inc.Files = append(inc.Files, name)
			}
		}
		inc.Repo = h.repoFor(fmt.Sprint(projectSlug(e.Project)))
		h.ingest(w, inc)
	case resource == "issue" && p.Action == "created":
		is := p.Data.Issue
		inc := IncidentRecord{ID: "sentry-issue-" + is.ID, Title: is.Title, URL: is.Permalink, Source: "sentry", Severity: severity(is.Level)}
		if t, err := time.Parse(time.RFC3339, is.FirstSeen); err == nil {
			inc.Started = t.UTC()
		}
		inc.Repo = h.repoFor(is.Project.Slug)
		h.ingest(w, inc)
	default:
		w.WriteHeader(http.StatusNoContent) // installation, comments…: nothing to do
	}
}

func projectSlug(p any) string {
	switch v := p.(type) {
	case string:
		return v
	case map[string]any:
		if s, ok := v["slug"].(string); ok {
			return s
		}
	case float64:
		return strconv.FormatInt(int64(v), 10)
	}
	return ""
}

// PagerDuty handles v3 webhook subscriptions (incident.triggered).
func (h *IncidentHooks) PagerDuty(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	valid := false
	want := "v1=" + hmacHex(h.PagerDutySecret, body)
	for _, s := range strings.Split(r.Header.Get("X-PagerDuty-Signature"), ",") {
		if h.PagerDutySecret != "" && equal(strings.TrimSpace(s), want) {
			valid = true
		}
	}
	if !valid {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var p struct {
		Event struct {
			EventType string `json:"event_type"`
			Data      struct {
				ID        string    `json:"id"`
				Title     string    `json:"title"`
				HTMLURL   string    `json:"html_url"`
				Urgency   string    `json:"urgency"`
				CreatedAt time.Time `json:"created_at"`
				Service   struct {
					Summary string `json:"summary"`
				} `json:"service"`
				Priority *struct {
					Summary string `json:"summary"`
				} `json:"priority"`
			} `json:"data"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if p.Event.EventType != "incident.triggered" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	d := p.Event.Data
	sev := d.Urgency
	if d.Priority != nil && d.Priority.Summary != "" {
		sev = d.Priority.Summary
	}
	h.ingest(w, IncidentRecord{ID: "pagerduty-" + d.ID, Title: d.Title, URL: d.HTMLURL, Source: "pagerduty",
		Severity: severity(sev), Started: d.CreatedAt.UTC(), Repo: h.repoFor(d.Service.Summary)})
}

func (h *IncidentHooks) repoFor(hint string) string {
	if h.Repo == nil {
		return ""
	}
	return h.Repo(hint)
}

func (h *IncidentHooks) ingest(w http.ResponseWriter, inc IncidentRecord) {
	if inc.Started.IsZero() {
		inc.Started = time.Now().UTC()
	}
	inc.Received = time.Now().UTC()
	if inc.Repo == "" && h.Repo != nil {
		inc.Repo = h.Repo("")
	}
	if h.Ingest != nil {
		h.Ingest(inc)
	}
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte("received\n"))
}

// Candidate is a merged PR that may have caused an incident.
type Candidate struct {
	Merge  Merge
	Score  float64
	Reason string
}

// Window is how far before an incident Cowboy looks for a cause.
const Window = 72 * time.Hour

// Attribute ranks merged PRs that could have caused an incident: shipped in
// the incident's release (or shortly before it started), and changing files
// that show up in its errors. The pen is not used, so the evaluation of the
// pens isn't biased by it.
func Attribute(inc IncidentRecord, merges []Merge) []Candidate {
	var out []Candidate
	for _, m := range merges {
		var timeScore float64
		var when string
		switch {
		case inc.Release != "" && m.Release == inc.Release:
			timeScore, when = 1, "shipped in "+inc.Release
		case inc.Release == "":
			ref := m.MergedAt
			what := "merged"
			if !m.ReleasedAt.IsZero() {
				ref, what = m.ReleasedAt, "released"
			}
			age := inc.Started.Sub(ref)
			if age < 0 || age > Window {
				continue
			}
			timeScore, when = 1-age.Hours()/Window.Hours(), fmt.Sprintf("%s %s before it started", what, ago(age))
		default:
			continue
		}
		matched := matchFiles(inc.Files, m.Files)
		if len(inc.Files) > 0 && len(matched) == 0 {
			continue // the errors are elsewhere
		}
		score := timeScore + 2*float64(min(len(matched), 3))
		reason := when
		if len(matched) > 0 {
			shown := matched
			if len(shown) > 2 {
				shown = shown[:2]
			}
			reason += "; the errors are in files it changed (" + strings.Join(shown, ", ") + ")"
		}
		if len(inc.Files) == 0 && timeScore < 0.6 {
			continue // no files to go on and not close in time: too weak
		}
		out = append(out, Candidate{Merge: m, Score: score, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Merge.Number > out[j].Merge.Number
	})
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func ago(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// matchFiles pairs incident file names (from stack traces: often absolute
// paths or module paths) with PR files by their ending.
func matchFiles(incFiles, prFiles []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range prFiles {
		for _, g := range incFiles {
			g = strings.TrimPrefix(strings.ReplaceAll(g, "\\", "/"), "./")
			g = strings.TrimPrefix(g, "webpack:///")
			g = strings.TrimPrefix(g, "app:///")
			if g == "" || seen[f] {
				continue
			}
			if f == g || strings.HasSuffix(g, "/"+f) || strings.HasSuffix(f, "/"+g) {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// Link records an incident and suggests its likely causes.
func (s *State) Link(inc IncidentRecord, onNew func(Outcome)) []Outcome {
	isNew, err := s.AddIncident(inc)
	if err != nil {
		log.Printf("outcomes: %v", err)
	}
	if !isNew {
		return nil
	}
	var out []Outcome
	for _, c := range Attribute(inc, s.Merges(inc.Repo)) {
		o := Outcome{Repo: c.Merge.Repo, Number: c.Merge.Number, Kind: Incident, Severity: inc.Severity, At: inc.Started,
			Evidence: fmt.Sprintf("%s: %s", inc.Title, c.Reason), Link: inc.URL, Source: inc.Source, Status: Suggested,
			Incident: inc.ID, Score: c.Score}
		if o, added, err := s.Add(o); err == nil && added {
			out = append(out, o)
			if onNew != nil {
				onNew(o)
			}
		}
	}
	return out
}

// LinkByHand ties an incident to a PR a person names (confirmed at once).
func (s *State) LinkByHand(incidentID, repo string, n int, by string) (Outcome, error) {
	inc, ok := s.Incident(incidentID)
	if !ok {
		return Outcome{}, fmt.Errorf("no incident %q", incidentID)
	}
	if _, ok := s.Merge(repo, n); !ok {
		return Outcome{}, fmt.Errorf("%s#%d isn't a merged pull request Cowboy has recorded", repo, n)
	}
	o := Outcome{Repo: repo, Number: n, Kind: Incident, Severity: inc.Severity, At: inc.Started, Evidence: inc.Title + ": linked by " + by,
		Link: inc.URL, Source: "person", Status: Confirmed, By: by, DecidedAt: time.Now().UTC(), Incident: inc.ID}
	o, _, err := s.Add(o)
	return o, err
}
