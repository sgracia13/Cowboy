package report

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFromEnv(t *testing.T) {
	if _, ok, err := FromEnv(envOf(nil)); ok || err != nil {
		t.Fatalf("nothing set: ok=%v err=%v, want off and no error", ok, err)
	}
	c, ok, err := FromEnv(envOf(map[string]string{"TALLY_SLACK_WEBHOOK": "https://hooks.slack.com/services/x", "TALLY_DAY": "fri", "TALLY_HOUR": "16"}))
	if !ok || err != nil || c.Day != time.Friday || c.Hour != 16 {
		t.Fatalf("slack: %+v ok=%v err=%v", c, ok, err)
	}
	c, ok, err = FromEnv(envOf(map[string]string{"TALLY_EMAIL_TO": "a@x.com, b@x.com", "SMTP_HOST": "smtp.x.com", "SMTP_USER": "bot@x.com"}))
	if !ok || err != nil || len(c.EmailTo) != 2 || c.From != "bot@x.com" || c.SMTPPort != 587 || c.Day != time.Monday || c.Hour != 9 {
		t.Fatalf("email: %+v ok=%v err=%v", c, ok, err)
	}
	for name, env := range map[string]map[string]string{
		"email without host": {"TALLY_EMAIL_TO": "a@x.com"},
		"email without from": {"TALLY_EMAIL_TO": "a@x.com", "SMTP_HOST": "smtp.x.com"},
		"bad day":            {"TALLY_SLACK_WEBHOOK": "https://x", "TALLY_DAY": "someday"},
		"bad hour":           {"TALLY_SLACK_WEBHOOK": "https://x", "TALLY_HOUR": "25"},
		"webhook not a url":  {"TALLY_SLACK_WEBHOOK": "T000/B000/xyz"},
		"header injection":   {"TALLY_EMAIL_TO": "a@x.com\r\nBcc: z@y.com", "SMTP_HOST": "h", "SMTP_FROM": "f@x.com"},
		"not an address":     {"TALLY_EMAIL_TO": "alice", "SMTP_HOST": "h", "SMTP_FROM": "f@x.com"},
		"port not a number":  {"TALLY_SLACK_WEBHOOK": "https://x", "SMTP_PORT": "smtp"},
	} {
		if _, ok, err := FromEnv(envOf(env)); ok || err == nil {
			t.Errorf("%s: ok=%v err=%v, want an error", name, ok, err)
		}
	}
}

func TestDue(t *testing.T) {
	c := Config{Day: time.Monday, Hour: 9}
	at := func(s string) time.Time { tm, _ := time.ParseInLocation("2006-01-02 15:04", s, time.UTC); return tm }
	for now, want := range map[string]string{
		"2026-10-19 09:00": "2026-10-19 09:00", // Monday, right on time
		"2026-10-19 08:59": "2026-10-12 09:00", // Monday, a minute early: last week's
		"2026-10-21 14:00": "2026-10-19 09:00", // Wednesday
		"2026-10-18 23:00": "2026-10-12 09:00", // Sunday night
	} {
		if got := c.Due(at(now)); !got.Equal(at(want)) {
			t.Errorf("Due(%s) = %s, want %s", now, got.Format("2006-01-02 15:04"), want)
		}
	}
	if got := c.Next(at("2026-10-18 23:00")); !got.Equal(at("2026-10-19 09:00")) {
		t.Errorf("Next = %s", got)
	}
}

func sample() Report {
	from := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	return Report{From: from, To: from.AddDate(0, 0, 7), Link: "https://cowboy.example.com", Rows: []app.AgentTally{
		{Agent: "", Total: 20, Easy: 10, Normal: 8, Careful: 2, Corrected: 1},
		{Agent: "claude-code", Total: 12, Easy: 3, Normal: 5, Careful: 4, Doubles: 1},
		{Agent: "<b>bot</b>", Total: 1, Normal: 1, Strays: 2},
	}}
}

func TestReportText(t *testing.T) {
	r := sample()
	if got, want := r.Summary(), "33 pull requests sorted: 13 easy, 14 normal, 6 careful. 13 from agents. 1 double, 2 strays, your team moved 1 to another pen."; got != want {
		t.Errorf("summary:\n got %q\nwant %q", got, want)
	}
	if got := r.Subject(); got != "Cowboy tally, Oct 12–18: 33 pull requests, 6 careful" {
		t.Errorf("subject %q", got)
	}
	table := r.Table()
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "people") || len(lines[0]) != len(lines[2]) {
		t.Errorf("table not aligned:\n%s", table)
	}
	if s := r.Slack(); !strings.Contains(s, "```\nwho") || !strings.Contains(s, "<https://cowboy.example.com/tally|Open the full tally>") {
		t.Errorf("slack text:\n%s", s)
	}
	h := r.HTML()
	if strings.Contains(h, "<b>bot</b>") || !strings.Contains(h, "&lt;b&gt;bot&lt;/b&gt;") {
		t.Errorf("html doesn't escape agent names")
	}
	if got := (Report{From: r.From, To: r.To}).Summary(); got != "No pull requests were sorted this week." {
		t.Errorf("empty summary %q", got)
	}
	cross := Report{From: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
	if got := cross.span(); got != "Oct 26 – Nov 1" {
		t.Errorf("span across months %q", got)
	}
}

func TestPostChat(t *testing.T) {
	var got map[string]string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, "ok")
	}))
	defer ok.Close()
	if err := PostChat(context.Background(), ok.URL, sample()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got["text"], "*Cowboy tally, Oct 12–18*") {
		t.Errorf("posted %q", got["text"])
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid_token", http.StatusForbidden)
	}))
	defer bad.Close()
	if err := PostChat(context.Background(), bad.URL, sample()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("want a 403 error, got %v", err)
	}
}

// fakeSMTP is just enough of an SMTP server to receive one message.
type fakeSMTP struct {
	addr string
	mu   sync.Mutex
	from string
	to   []string
	data string
}

func startSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(s string) { io.WriteString(conn, s+"\r\n") }
	say("220 fake ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			f.mu.Lock()
			f.from = strings.Trim(strings.TrimSpace(line)[10:], "<>")
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			f.mu.Lock()
			f.to = append(f.to, strings.Trim(strings.TrimSpace(line)[8:], "<>"))
			f.mu.Unlock()
			say("250 ok")
		case cmd == "DATA":
			say("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func TestEmailOverSMTP(t *testing.T) {
	srv := startSMTP(t)
	host, port, _ := net.SplitHostPort(srv.addr)
	p, _ := strconv.Atoi(port)
	c := Config{EmailTo: []string{"team@x.com", "lead@x.com"}, SMTPHost: host, SMTPPort: p, From: "cowboy@x.com"}
	if err := Email(c, sample()); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.from != "cowboy@x.com" || strings.Join(srv.to, ",") != "team@x.com,lead@x.com" {
		t.Errorf("envelope from=%q to=%v", srv.from, srv.to)
	}
	for _, want := range []string{
		"Subject: =?utf-8?q?Cowboy_tally",
		"Content-Type: multipart/alternative",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Type: text/html; charset=utf-8",
		"claude-code",
		"Full tally: https://cowboy.example.com/tally",
	} {
		if !strings.Contains(srv.data, want) {
			t.Errorf("message is missing %q", want)
		}
	}
}

func testApp(t *testing.T, decided ...time.Time) (*app.App, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cowboy.json")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, at := range decided {
		agent := ""
		if i%2 == 1 {
			agent = "claude-code"
		}
		st.Put(pipeline.Decision{Repo: "acme/web", Number: i + 1, Agent: agent, Pen: score.Easy, DecidedAt: at, State: "open"})
	}
	return app.New(nil, st, nil), path
}

func TestTallyBetween(t *testing.T) {
	end := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	a, _ := testApp(t, end.AddDate(0, 0, -8), end.AddDate(0, 0, -7), end.Add(-time.Minute), end)
	total := 0
	for _, r := range a.TallyBetween(end.AddDate(0, 0, -7), end) {
		total += r.Total
	}
	if total != 2 {
		t.Errorf("week window counted %d prs, want 2 (start included, end excluded)", total)
	}
}

func TestScheduler(t *testing.T) {
	monday := time.Date(2026, 10, 19, 9, 0, 0, 0, time.Local)
	a, path := testApp(t, monday.AddDate(0, 0, -2), monday.AddDate(0, 0, -1))

	var mu sync.Mutex
	posts := 0
	fail := false
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		posts++
	}))
	defer chat.Close()
	mails := 0
	sendMail = func(Config, []byte) error { mails++; return nil }
	defer func() { sendMail = smtpSend }()

	c := Config{SlackURL: chat.URL, EmailTo: []string{"t@x.com"}, From: "c@x.com", SMTPHost: "h", Day: time.Monday, Hour: 9}
	s := NewScheduler(c, a, path)
	// The first start happened last week, before this Monday's slot.
	for k := range s.state {
		s.state[k] = monday.AddDate(0, 0, -7)
	}
	ctx := context.Background()

	s.Tick(ctx, monday.Add(-time.Minute))
	if posts != 0 || mails != 0 {
		t.Fatalf("sent before it was due: posts=%d mails=%d", posts, mails)
	}
	fail = true
	s.Tick(ctx, monday)
	if posts != 0 || mails != 1 {
		t.Fatalf("on time with chat down: posts=%d mails=%d, want 0 and 1", posts, mails)
	}
	s.Tick(ctx, monday.Add(10*time.Minute))
	if posts != 0 || mails != 1 {
		t.Fatalf("retried too soon: posts=%d mails=%d", posts, mails)
	}
	fail = false
	s.Tick(ctx, monday.Add(31*time.Minute))
	if posts != 1 || mails != 1 {
		t.Fatalf("retry: posts=%d mails=%d, want 1 and 1 (email not sent twice)", posts, mails)
	}

	// A restart the same week doesn't send again.
	s2 := NewScheduler(c, a, path)
	s2.Tick(ctx, monday.Add(2*time.Hour))
	if posts != 1 || mails != 1 {
		t.Fatalf("restart resent: posts=%d mails=%d", posts, mails)
	}

	// A server that was down for days skips the stale post.
	s2.Tick(ctx, monday.AddDate(0, 0, 9))
	if posts != 1 || mails != 1 {
		t.Fatalf("sent a stale tally: posts=%d mails=%d", posts, mails)
	}
	// ...and is back on schedule the Monday after.
	s2.Tick(ctx, monday.AddDate(0, 0, 14))
	if posts != 2 || mails != 2 {
		t.Fatalf("next week: posts=%d mails=%d", posts, mails)
	}
}

func TestFirstStartWaits(t *testing.T) {
	a, path := testApp(t)
	sent := 0
	sendMail = func(Config, []byte) error { sent++; return nil }
	defer func() { sendMail = smtpSend }()
	c := Config{EmailTo: []string{"t@x.com"}, From: "c@x.com", SMTPHost: "h", Day: time.Now().Weekday(), Hour: 0}
	s := NewScheduler(c, a, path)
	s.Tick(context.Background(), time.Now())
	if sent != 0 {
		t.Fatal("the first start sent a tally straight away; it should wait for the next slot")
	}
}

func TestSendReportsEachFailure(t *testing.T) {
	sendMail = func(Config, []byte) error { return errors.New("535 bad password") }
	defer func() { sendMail = smtpSend }()
	errs := Send(context.Background(), Config{EmailTo: []string{"t@x.com"}}, sample(), nil)
	if errs["email"] == nil || errs["chat"] != nil {
		t.Errorf("errs = %v", errs)
	}
}
