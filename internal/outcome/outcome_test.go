package outcome

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/shortcuts"
)

func TestParseRevert(t *testing.T) {
	cases := []struct {
		msg  string
		want string // refs as "pr:N" / "sha:X", "" = not a revert
	}{
		{"Revert \"Add billing retries (#123)\" (#130)\n\nReverts acme/web#123", "pr:123"},
		{"Revert \"Add billing retries\"\n\nThis reverts commit 1a2b3c4d5e6f7a8b9c0d.", "sha:1a2b3c4d5e6f7a8b9c0d"},
		{"Merge pull request #130 from acme/revert-123-billing-retries\n\nRevert \"Add billing retries\"", "pr:123"},
		{"Revert \"Revert \"Add billing retries (#123)\"\"", ""},
		{"Reapply \"Add billing retries (#123)\"", ""},
		{"Fix billing retries (#140)", ""},
		{"Revert: drop the flaky cache", "-"}, // a revert, but nothing to point at
	}
	for _, c := range cases {
		refs, ok := ParseRevert(c.msg)
		var got []string
		for _, r := range refs {
			if r.PR > 0 {
				got = append(got, fmt.Sprintf("pr:%d", r.PR))
			} else {
				got = append(got, "sha:"+r.SHA)
			}
		}
		g := strings.Join(got, ",")
		switch {
		case c.want == "" && ok:
			t.Errorf("%q: read as a revert (%s)", c.msg, g)
		case c.want == "-" && (!ok || g != ""):
			t.Errorf("%q: ok=%v refs=%s, want a revert with no refs", c.msg, ok, g)
		case c.want != "" && c.want != "-" && g != c.want:
			t.Errorf("%q: got %q, want %q", c.msg, g, c.want)
		}
	}
	if OwnNumber("Revert \"x (#123)\" (#130)\n\nbody") != 130 {
		t.Error("own number")
	}
}

// ---- a fake git host with history

type host struct {
	forge.Forge
	merges   map[int]forge.MergeInfo
	commits  []forge.Commit
	releases []forge.Release
	checks   map[string]*bool
	diffs    map[int]string
	files    map[string]string
	shaPR    map[string]int
}

func (h *host) MergeInfo(repo string, n int) (forge.MergeInfo, error) {
	m, ok := h.merges[n]
	if !ok {
		return m, fmt.Errorf("not merged")
	}
	return m, nil
}
func (h *host) Commits(repo string, since time.Time) ([]forge.Commit, error) {
	var out []forge.Commit
	for _, c := range h.commits {
		if !c.At.Before(since) {
			out = append(out, c)
		}
	}
	return out, nil
}
func (h *host) Releases(repo string, since time.Time) ([]forge.Release, error) {
	return h.releases, nil
}
func (h *host) MergedSince(repo string, since time.Time) ([]int, error) { return nil, nil }
func (h *host) PRForCommit(repo, sha string) (int, error)               { return h.shaPR[sha], nil }
func (h *host) ChecksOK(repo, sha string) (*bool, error)                { return h.checks[sha], nil }
func (h *host) GetDiff(repo string, n int) (string, error)              { return h.diffs[n], nil }
func (h *host) GetFile(repo, path, ref string) (string, error)          { return h.files[path], nil }

type decisions map[int]pipeline.Decision

func (d decisions) List(repo, state string) []pipeline.Decision {
	var out []pipeline.Decision
	for i := 1; i <= 100; i++ {
		if x, ok := d[i]; ok && (state == "" || x.State == state) {
			out = append(out, x)
		}
	}
	return out
}
func (d decisions) Get(repo string, n int) (pipeline.Decision, bool) { x, ok := d[n]; return x, ok }

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func TestWatcher(t *testing.T) {
	ds := decisions{
		1: {Repo: "acme/web", Number: 1, Title: "Add billing retries", Pen: score.Careful, State: "merged", Signals: score.Signals{ChecksOK: yes()}},
		2: {Repo: "acme/web", Number: 2, Title: "Tidy logs", Pen: score.Easy, State: "merged", Signals: score.Signals{ChecksOK: yes()}},
		3: {Repo: "acme/web", Number: 3, Title: "Fix retry loop", Body: "follow-up", Pen: score.Normal, State: "merged", Signals: score.Signals{ChecksOK: yes()}},
		4: {Repo: "acme/web", Number: 4, Title: "Speed up search", Pen: score.Normal, State: "merged", Signals: score.Signals{ChecksOK: yes()}},
		5: {Repo: "acme/web", Number: 5, Title: "Still open", Pen: score.Easy, State: "open"},
	}
	h := &host{
		Forge: forge.NewFake("", ""),
		merges: map[int]forge.MergeInfo{
			1: {SHA: "aaa111", MergedAt: t0, Title: "Add billing retries (#1)", Files: []string{"billing/retry.go", "billing/client.go"}, Approvers: []string{"maria"}},
			2: {SHA: "bbb2222", MergedAt: t0.Add(time.Hour), Title: "Tidy logs", Files: []string{"log/log.go"}},
			3: {SHA: "ccc333", MergedAt: t0.Add(48 * time.Hour), Title: "Fix retry loop", Files: []string{"billing/retry.go"}},
			4: {SHA: "ddd444", MergedAt: t0.Add(72 * time.Hour), Title: "Speed up search", Files: []string{"search/index.go"}},
		},
		commits: []forge.Commit{
			{SHA: "eee555", At: t0.Add(5 * 24 * time.Hour), Message: "Revert \"Speed up search (#4)\" (#9)\n\nReverts acme/web#4"},
			{SHA: "fff666", At: t0.Add(6 * 24 * time.Hour), Message: "Revert \"Tidy logs\"\n\nThis reverts commit bbb2222."},
			{SHA: "ggg777", At: t0.Add(6 * 24 * time.Hour), Message: "Normal commit"},
		},
		releases: []forge.Release{{Tag: "v1.0", At: t0.Add(30 * time.Minute)}, {Tag: "v1.1", At: t0.Add(50 * time.Hour)}},
		checks:   map[string]*bool{"aaa111": no(), "bbb2222": yes(), "ccc333": yes(), "ddd444": nil},
		diffs: map[int]string{
			1: "diff --git a/billing/retry.go b/billing/retry.go\n--- a/billing/retry.go\n+++ b/billing/retry.go\n@@ -1,1 +1,3 @@\n package billing\n+func Retry(n int) int { return n * 2 }\n+var maxRetries = 5 // tuned\n",
		},
		files: map[string]string{"billing/retry.go": "package billing\nfunc Retry(n int) int { return n * 2 }\n"},
	}
	st, _ := Open(filepath.Join(t.TempDir(), "outcomes.json"))
	now := t0.Add(7 * 24 * time.Hour)
	var events []Event
	w := &Watcher{Forge: h, Decisions: ds, State: st, Now: func() time.Time { return now }, OnEvent: func(e Event) { events = append(events, e) }}
	sum := w.Check("acme/web")
	if !strings.Contains(sum, "4 merges recorded, 2 reverts, 1 broke main, 1 suggested fixes") {
		t.Errorf("summary: %s", sum)
	}
	got := map[string]string{}
	for _, o := range st.Outcomes("acme/web", 0) {
		got[fmt.Sprintf("#%d %s", o.Number, o.Kind)] = o.Status
	}
	want := map[string]string{"#4 revert": Auto, "#2 revert": Auto, "#1 break": Auto, "#1 fix": Suggested}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("outcomes %v", got)
	}
	m1, _ := st.Merge("acme/web", 1)
	if m1.Release != "v1.0" || len(m1.Lines["billing/retry.go"]) != 2 {
		t.Errorf("merge 1: release %q lines %v", m1.Release, m1.Lines)
	}
	if m3, _ := st.Merge("acme/web", 3); m3.Release != "v1.1" {
		t.Errorf("merge 3 release %q", m3.Release)
	}
	// running again finds nothing new
	n := len(st.Outcomes("", 0))
	w.Check("acme/web")
	if len(st.Outcomes("", 0)) != n {
		t.Error("a second run added outcomes again")
	}
	// 30 days on: survival — one of the two added lines is still there
	now = t0.Add(31 * 24 * time.Hour)
	w.Check("acme/web")
	m1, _ = st.Merge("acme/web", 1)
	if s := m1.Survival["30d"]; s != 0.5 {
		t.Errorf("survival %v, want 0.5", m1.Survival)
	}
	if len(events) == 0 {
		t.Error("no events")
	}
}

func TestAttribute(t *testing.T) {
	merges := []Merge{
		{Repo: "acme/web", Number: 1, MergedAt: t0, Release: "v2", Files: []string{"src/pay/api.ts", "src/pay/types.ts"}},
		{Repo: "acme/web", Number: 2, MergedAt: t0, Release: "v2", Files: []string{"docs/readme.md"}},
		{Repo: "acme/web", Number: 3, MergedAt: t0.Add(-10 * 24 * time.Hour), Release: "v1", Files: []string{"src/pay/api.ts"}},
	}
	inc := IncidentRecord{Release: "v2", Started: t0.Add(time.Hour), Files: []string{"webpack:///./src/pay/api.ts", "/app/node_modules/react/index.js"}}
	cs := Attribute(inc, merges)
	if len(cs) != 1 || cs[0].Merge.Number != 1 || !strings.Contains(cs[0].Reason, "src/pay/api.ts") {
		t.Fatalf("candidates %+v", cs)
	}
	// no release, no files: only PRs that shipped shortly before, closest first
	inc2 := IncidentRecord{Started: t0.Add(2 * time.Hour)}
	cs = Attribute(inc2, merges)
	if len(cs) != 2 || cs[0].Merge.Number != 2 {
		t.Fatalf("time-only candidates %+v", cs)
	}
}

func signedPost(t *testing.T, h http.HandlerFunc, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestIncidentHooks(t *testing.T) {
	var got []IncidentRecord
	hooks := &IncidentHooks{Secret: "inc", SentrySecret: "sen", PagerDutySecret: "pd",
		Repo:   func(hint string) string { return map[string]string{"web-frontend": "acme/web", "": "acme/web"}[hint] },
		Ingest: func(i IncidentRecord) { got = append(got, i) }}
	body := `{"id":"INC-7","title":"Checkout errors","severity":"sev1","started_at":"2026-09-01T13:00:00Z","files":["src/pay/api.ts"]}`
	if w := signedPost(t, hooks.Generic, body, map[string]string{"X-Cowboy-Signature": "sha256=" + hmacHex("inc", []byte(body))}); w.Code != 202 {
		t.Fatalf("generic signed: %d", w.Code)
	}
	if w := signedPost(t, hooks.Generic, body, map[string]string{"Authorization": "Bearer inc"}); w.Code != 202 {
		t.Fatalf("generic bearer: %d", w.Code)
	}
	if w := signedPost(t, hooks.Generic, body, map[string]string{"Authorization": "Bearer nope"}); w.Code != 401 {
		t.Fatalf("generic bad token: %d", w.Code)
	}
	sentry := `{"action":"triggered","data":{"event":{"event_id":"e1","title":"TypeError: x is undefined","release":"v2","level":"error","web_url":"https://sentry.io/e1","datetime":"2026-09-01T13:05:00Z","project":{"slug":"web-frontend"},"exception":{"values":[{"stacktrace":{"frames":[{"filename":"node_modules/react/index.js","in_app":false},{"filename":"./src/pay/api.ts","in_app":true}]}}]}}}}`
	if w := signedPost(t, hooks.Sentry, sentry, map[string]string{"Sentry-Hook-Resource": "event_alert", "Sentry-Hook-Signature": hmacHex("sen", []byte(sentry))}); w.Code != 202 {
		t.Fatalf("sentry: %d", w.Code)
	}
	if w := signedPost(t, hooks.Sentry, sentry, map[string]string{"Sentry-Hook-Resource": "event_alert", "Sentry-Hook-Signature": "bad"}); w.Code != 401 {
		t.Fatalf("sentry bad sig: %d", w.Code)
	}
	pd := `{"event":{"event_type":"incident.triggered","data":{"id":"P1","title":"API down","html_url":"https://pd/P1","urgency":"high","created_at":"2026-09-01T13:10:00Z","service":{"summary":"web-frontend"}}}}`
	if w := signedPost(t, hooks.PagerDuty, pd, map[string]string{"X-PagerDuty-Signature": "v1=zzz, v1=" + hmacHex("pd", []byte(pd))}); w.Code != 202 {
		t.Fatalf("pagerduty: %d", w.Code)
	}
	if len(got) != 4 {
		t.Fatalf("got %d incidents", len(got))
	}
	s := got[2]
	if s.Source != "sentry" || s.Release != "v2" || s.Repo != "acme/web" || len(s.Files) != 1 || s.Files[0] != "./src/pay/api.ts" || s.Severity != 3 {
		t.Errorf("sentry incident %+v", s)
	}
	if got[0].Severity != 4 || got[3].Severity != 3 || got[3].Repo != "acme/web" {
		t.Errorf("severities/repos: %+v %+v", got[0], got[3])
	}
}

func TestLinkAndDecide(t *testing.T) {
	st, _ := Open("")
	st.PutMerge(Merge{Repo: "acme/web", Number: 1, MergedAt: t0, Release: "v2", Files: []string{"src/pay/api.ts"}})
	inc := IncidentRecord{ID: "INC-7", Title: "Checkout errors", Repo: "acme/web", Release: "v2", Severity: 4, Started: t0.Add(time.Hour), Files: []string{"src/pay/api.ts"}}
	out := st.Link(inc, nil)
	if len(out) != 1 || out[0].Status != Suggested || out[0].Counts() {
		t.Fatalf("link %+v", out)
	}
	if again := st.Link(inc, nil); len(again) != 0 {
		t.Error("the same incident twice made new suggestions")
	}
	o, err := st.Decide(out[0].ID, true, "maria")
	if err != nil || !o.Counts() || o.By != "maria" {
		t.Fatalf("confirm %+v %v", o, err)
	}
	if _, err := st.LinkByHand("INC-7", "acme/web", 99, "sam"); err == nil {
		t.Error("linking to an unknown PR should fail")
	}
}

func TestWilson(t *testing.T) {
	lo, hi := Wilson(0, 10)
	if lo != 0 || hi < 0.27 || hi > 0.32 {
		t.Errorf("0/10: %.3f–%.3f", lo, hi)
	}
	lo, hi = Wilson(50, 100)
	if lo < 0.39 || lo > 0.41 || hi < 0.59 || hi > 0.61 {
		t.Errorf("50/100: %.3f–%.3f", lo, hi)
	}
}

func TestReport(t *testing.T) {
	st, _ := Open("")
	var ds []pipeline.Decision
	now := t0.Add(100 * 24 * time.Hour)
	n := 0
	mk := func(pen score.Pen, agent, area string, fan int, bad bool, approvers []string, reasons []score.Reason) {
		n++
		d := pipeline.Decision{Repo: "acme/web", Number: n, Title: fmt.Sprintf("pr %d", n), Pen: pen, Agent: agent, State: "merged", Reasons: reasons,
			Signals: score.Signals{FanIn: fan, FanInKnown: true}}
		ds = append(ds, d)
		st.PutMerge(Merge{Repo: "acme/web", Number: n, MergedAt: t0, Files: []string{area + "/x.go"}, Approvers: approvers})
		if bad {
			st.Add(Outcome{Repo: "acme/web", Number: n, Kind: Revert, Severity: 2, At: t0.Add(24 * time.Hour), Evidence: "r", Status: Auto})
		}
	}
	sens := []score.Reason{{Check: "sensitive", Detail: "touches billing", Effect: "careful"}}
	for i := 0; i < 40; i++ {
		mk(score.Easy, "", "web", 1, false, nil, nil)
	}
	for i := 0; i < 6; i++ {
		mk(score.Easy, "claude-code", "payments", 2, true, nil, nil) // easy PRs in payments keep breaking
	}
	for i := 0; i < 30; i++ {
		mk(score.Careful, "claude-code", "billing", 30, i < 12, []string{"maria"}, sens)
	}
	// a late outcome (after the window) doesn't count
	mk(score.Normal, "", "web", 1, false, nil, nil)
	st.Add(Outcome{Repo: "acme/web", Number: n, Kind: Revert, Severity: 2, At: t0.Add(60 * 24 * time.Hour), Evidence: "late", Status: Auto})
	// a recent merge isn't rated yet
	ds = append(ds, pipeline.Decision{Repo: "acme/web", Number: 999, Pen: score.Easy, State: "merged"})
	st.PutMerge(Merge{Repo: "acme/web", Number: 999, MergedAt: now.Add(-time.Hour)})

	rep := Build(ds, st, now, 30*24*time.Hour, map[string]int{"acme/web": 20})
	if rep.Matured != 77 || rep.Recent != 1 {
		t.Errorf("matured %d recent %d", rep.Matured, rep.Recent)
	}
	pens := map[score.Pen]Rate{}
	for _, p := range rep.ByPen {
		pens[p.Pen] = p.Rate
	}
	if pens[score.Easy].N != 46 || pens[score.Easy].Bad != 6 || pens[score.Careful].Bad != 12 || pens[score.Normal].Bad != 0 {
		t.Errorf("pens %+v", pens)
	}
	if len(rep.Missed) != 6 || rep.Missed[0].Area != "payments" {
		t.Errorf("missed %+v", rep.Missed)
	}
	if rep.Rules[0].Rule != "sensitive" || rep.Rules[0].Rate.Bad != 12 {
		t.Errorf("rules %+v", rep.Rules)
	}
	if rep.Reviewed.N != 30 || rep.NotReviewed.N != 0 {
		t.Errorf("review %+v %+v", rep.Reviewed, rep.NotReviewed)
	}
	var area bool
	for _, s := range rep.Suggestions {
		if strings.Contains(s.Change, `"payments/**"`) {
			area = true
		}
	}
	if !area {
		t.Errorf("expected a payments/** suggestion: %+v", rep.Suggestions)
	}

	b := Benchmark(ds, st, now, 30*24*time.Hour, "v0.4.0")
	raw, _ := json.Marshal(b)
	for _, leak := range []string{"acme", "web", "billing", "maria", "pr 1"} {
		if bytes.Contains(raw, []byte(leak)) && leak != "web" {
			t.Errorf("benchmark leaks %q: %s", leak, raw)
		}
	}
	if b.PRs != 77 || b.Agents["claude-code"].N != 36 || b.Pens["careful"].Bad != 12 {
		t.Errorf("bench %+v", b)
	}
	md := CombinedReport([]Bench{b, b})
	if !strings.Contains(md, "2 teams, 154 merged pull requests") {
		t.Errorf("combined:\n%s", md)
	}
}

func TestBenchCustomAgentHidden(t *testing.T) {
	if benchAgent("acme-internal-bot") != "other-agent" || benchAgent("claude-code") != "claude-code" {
		t.Error("custom agent names must not leave the server")
	}
}

func TestRulesFromShortcuts(t *testing.T) {
	d := pipeline.Decision{Reasons: []score.Reason{{Check: "shortcut", Detail: "tests removed", Effect: "careful"}, {Check: "size", Detail: "x", Effect: "info"}},
		Signals: score.Signals{Shortcuts: []shortcuts.Flag{{Kind: "tests_removed", Detail: "tests removed"}}}}
	if r := rules(d); len(r) != 1 || r[0] != "shortcut:tests_removed" {
		t.Errorf("rules %v", r)
	}
}

func TestLineSampleIsCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1,5000 @@\n")
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, "+var line%05d = %d\n", i, i)
	}
	h := lineHashes(diffParse(b.String()))
	n := 0
	for _, v := range h {
		n += len(v)
	}
	if n != SurvivalSample {
		t.Errorf("kept %d hashes, want %d", n, SurvivalSample)
	}
}

var diffParse = diff.Parse
