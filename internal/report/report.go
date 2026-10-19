// Package report sends the weekly tally to a chat webhook and/or by email.
//
// Everything here is optional: with no TALLY_SLACK_WEBHOOK and no
// TALLY_EMAIL_TO, nothing is sent and the tally stays a dashboard page.
package report

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
)

// Config says where and when the weekly tally goes.
type Config struct {
	SlackURL string   // Slack incoming webhook (or anything that takes {"text": ...})
	EmailTo  []string // recipients

	SMTPHost, SMTPUser, SMTPPass, From string
	SMTPPort                           int

	Day  time.Weekday // default Monday
	Hour int          // 0-23, server time zone (TZ), default 9

	Link string // optional dashboard address, linked from the post
}

// FromEnv reads the TALLY_* and SMTP_* settings. ok is false when no
// destination is set. err explains a setting that can't work.
func FromEnv(getenv func(string) string) (c Config, ok bool, err error) {
	c.SlackURL = strings.TrimSpace(getenv("TALLY_SLACK_WEBHOOK"))
	for _, to := range strings.FieldsFunc(getenv("TALLY_EMAIL_TO"), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		c.EmailTo = append(c.EmailTo, to)
	}
	c.SMTPHost = getenv("SMTP_HOST")
	c.SMTPUser = getenv("SMTP_USER")
	c.SMTPPass = getenv("SMTP_PASSWORD")
	c.From = getenv("SMTP_FROM")
	if c.From == "" {
		c.From = c.SMTPUser
	}
	c.SMTPPort = 587
	if p := getenv("SMTP_PORT"); p != "" {
		if c.SMTPPort, err = strconv.Atoi(p); err != nil {
			return c, false, fmt.Errorf("SMTP_PORT %q isn't a number", p)
		}
	}
	c.Day, c.Hour = time.Monday, 9
	if d := getenv("TALLY_DAY"); d != "" {
		if c.Day, err = parseDay(d); err != nil {
			return c, false, err
		}
	}
	if h := getenv("TALLY_HOUR"); h != "" {
		if c.Hour, err = strconv.Atoi(h); err != nil || c.Hour < 0 || c.Hour > 23 {
			return c, false, fmt.Errorf("TALLY_HOUR %q should be 0 to 23", h)
		}
	}
	c.Link = strings.TrimRight(getenv("PUBLIC_URL"), "/")

	if c.SlackURL == "" && len(c.EmailTo) == 0 {
		return c, false, nil
	}
	if c.SlackURL != "" && !strings.HasPrefix(c.SlackURL, "https://") && !strings.HasPrefix(c.SlackURL, "http://") {
		return c, false, errors.New("TALLY_SLACK_WEBHOOK should be the full https:// webhook address")
	}
	if len(c.EmailTo) > 0 {
		if c.SMTPHost == "" {
			return c, false, errors.New("TALLY_EMAIL_TO needs SMTP_HOST (and usually SMTP_USER and SMTP_PASSWORD)")
		}
		if c.From == "" {
			return c, false, errors.New("TALLY_EMAIL_TO needs SMTP_FROM (or SMTP_USER) as the sender address")
		}
	}
	for _, s := range append([]string{c.From}, c.EmailTo...) {
		if strings.ContainsAny(s, "\r\n") || (s != "" && !strings.Contains(s, "@")) {
			return c, false, fmt.Errorf("%q isn't an email address", s)
		}
	}
	return c, true, nil
}

func parseDay(s string) (time.Weekday, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for d := time.Sunday; d <= time.Saturday; d++ {
		name := strings.ToLower(d.String())
		if s == name || (len(s) >= 3 && strings.HasPrefix(name, s)) {
			return d, nil
		}
	}
	return 0, fmt.Errorf("TALLY_DAY %q should be a day like monday or mon", s)
}

// Where describes the destinations, for logs and the dashboard.
func (c Config) Where() string {
	var w []string
	if c.SlackURL != "" {
		w = append(w, "chat webhook")
	}
	if len(c.EmailTo) > 0 {
		w = append(w, "email to "+strings.Join(c.EmailTo, ", "))
	}
	return strings.Join(w, " and ")
}

// When describes the schedule, e.g. "Mondays at 09:00 (America/New_York)".
func (c Config) When() string {
	zone := time.Local.String()
	if zone == "Local" || zone == "" {
		zone = "server time; set TZ to change"
	}
	return fmt.Sprintf("%ss at %02d:00 (%s)", c.Day, c.Hour, zone)
}

// Due is the most recent scheduled send at or before now.
func (c Config) Due(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), c.Hour, 0, 0, 0, now.Location())
	back := (int(now.Weekday()) - int(c.Day) + 7) % 7
	t = t.AddDate(0, 0, -back)
	if t.After(now) {
		t = t.AddDate(0, 0, -7)
	}
	return t
}

// Next is the next scheduled send after now.
func (c Config) Next(now time.Time) time.Time { return c.Due(now).AddDate(0, 0, 7) }

// Report is one week's tally, ready to send.
type Report struct {
	From, To time.Time
	Rows     []app.AgentTally
	Link     string
	// Audit is the audit record's fingerprint ("" if none is kept).
	Audit string
}

func (r Report) totals() (all app.AgentTally, agents int) {
	for _, x := range r.Rows {
		all.Total += x.Total
		all.Easy += x.Easy
		all.Normal += x.Normal
		all.Careful += x.Careful
		all.Doubles += x.Doubles
		all.Strays += x.Strays
		all.Corrected += x.Corrected
		all.Trouble += x.Trouble
		if x.Agent != "" {
			agents += x.Total
		}
	}
	return
}

func (r Report) span() string {
	// A Monday 09:00 cut-off reads as "through Sunday"; a send at 23:00 reads as "through today".
	end := r.To.Add(-12 * time.Hour)
	if r.From.Month() == end.Month() {
		return fmt.Sprintf("%s %d–%d", r.From.Format("Jan"), r.From.Day(), end.Day())
	}
	return fmt.Sprintf("%s – %s", r.From.Format("Jan 2"), end.Format("Jan 2"))
}

// Subject is the email subject.
func (r Report) Subject() string {
	all, _ := r.totals()
	return fmt.Sprintf("Cowboy tally, %s: %d pull requests, %d careful", r.span(), all.Total, all.Careful)
}

// Summary is the one-line headline.
func (r Report) Summary() string {
	all, agents := r.totals()
	if all.Total == 0 {
		return "No pull requests were sorted this week."
	}
	s := fmt.Sprintf("%d pull requests sorted: %d easy, %d normal, %d careful. %d from agents.",
		all.Total, all.Easy, all.Normal, all.Careful, agents)
	var extra []string
	if all.Doubles > 0 {
		extra = append(extra, plural(all.Doubles, "double", "doubles"))
	}
	if all.Strays > 0 {
		extra = append(extra, plural(all.Strays, "stray", "strays"))
	}
	if all.Corrected > 0 {
		extra = append(extra, fmt.Sprintf("your team moved %d to another pen", all.Corrected))
	}
	if all.Trouble > 0 {
		extra = append(extra, plural(all.Trouble, "problem", "problems")+" found after merge")
	}
	if len(extra) > 0 {
		s += " " + upper(strings.Join(extra, ", ")) + "."
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var cols = []string{"who", "prs", "easy", "normal", "careful", "doubles", "strays", "moved", "trouble"}

func (r Report) cells() [][]string {
	out := [][]string{cols}
	for _, x := range r.Rows {
		who := x.Agent
		if who == "" {
			who = "people"
		}
		out = append(out, []string{who, n(x.Total), n(x.Easy), n(x.Normal), n(x.Careful), n(x.Doubles), n(x.Strays), n(x.Corrected), n(x.Trouble)})
	}
	return out
}

func n(i int) string { return strconv.Itoa(i) }

// Table is the tally as aligned plain text.
func (r Report) Table() string {
	cells := r.cells()
	w := make([]int, len(cols))
	for _, row := range cells {
		for i, c := range row {
			w[i] = max(w[i], len(c))
		}
	}
	var b strings.Builder
	for _, row := range cells {
		for i, c := range row {
			if i == 0 {
				fmt.Fprintf(&b, "%-*s", w[i], c)
			} else {
				fmt.Fprintf(&b, "  %*s", w[i], c)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Text is the plain-text body (chat and the email's text part).
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cowboy tally, %s\n%s\n", r.span(), r.Summary())
	if len(r.Rows) > 0 {
		b.WriteString("\n" + r.Table())
	}
	if r.Link != "" {
		fmt.Fprintf(&b, "\nFull tally: %s/tally\n", r.Link)
	}
	if r.Audit != "" {
		fmt.Fprintf(&b, "\n%s\n", r.Audit)
	}
	return b.String()
}

// Slack is the chat message: a bold title, the summary, and the table in a code block.
func (r Report) Slack() string {
	var b strings.Builder
	fmt.Fprintf(&b, "*Cowboy tally, %s*\n%s", r.span(), r.Summary())
	if len(r.Rows) > 0 {
		b.WriteString("\n```\n" + r.Table() + "```")
	}
	if r.Link != "" {
		fmt.Fprintf(&b, "\n<%s/tally|Open the full tally>", r.Link)
	}
	if r.Audit != "" {
		fmt.Fprintf(&b, "\n_%s_", r.Audit)
	}
	return b.String()
}

// HTML is the email's html part.
func (r Report) HTML() string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:Helvetica,Arial,sans-serif;color:#111;max-width:640px">`)
	fmt.Fprintf(&b, `<h2 style="margin:0 0 8px">Cowboy tally, %s</h2><p style="margin:0 0 16px">%s</p>`, html.EscapeString(r.span()), html.EscapeString(r.Summary()))
	if len(r.Rows) > 0 {
		b.WriteString(`<table style="border-collapse:collapse;font-size:14px">`)
		for i, row := range r.cells() {
			b.WriteString("<tr>")
			for j, c := range row {
				tag, style := "td", "padding:6px 10px;border-bottom:1px solid #ddd"
				if i == 0 {
					tag, style = "th", "padding:6px 10px;background:#ffd400;text-align:left"
				}
				if j > 0 {
					style += ";text-align:right"
				}
				fmt.Fprintf(&b, `<%s style="%s">%s</%s>`, tag, style, html.EscapeString(c), tag)
			}
			b.WriteString("</tr>")
		}
		b.WriteString("</table>")
	}
	if r.Link != "" {
		fmt.Fprintf(&b, `<p style="margin:16px 0 0"><a href="%s/tally">Open the full tally</a></p>`, html.EscapeString(r.Link))
	}
	if r.Audit != "" {
		fmt.Fprintf(&b, `<p style="margin:16px 0 0;color:#666;font-size:12px">%s</p>`, html.EscapeString(r.Audit))
	}
	b.WriteString(`<p style="margin:16px 0 0;color:#666;font-size:12px">Sent by your Cowboy server.</p></div>`)
	return b.String()
}

// Build makes the report for the 7 days ending at end.
func Build(a *app.App, end time.Time, link string) Report {
	from := end.AddDate(0, 0, -7)
	r := Report{From: from, To: end, Rows: a.TallyBetween(from, end), Link: link}
	if n, fp := a.Audit.Fingerprint(); n > 0 {
		r.Audit = fmt.Sprintf("Audit record: %d entries, fingerprint %s. Keep this post: it lets you check the record wasn't changed later.", n, fp)
	}
	return r
}

// client is swapped in tests.
var client = &http.Client{Timeout: 20 * time.Second}

// PostChat sends the report to a Slack-style incoming webhook.
func PostChat(ctx context.Context, url string, r Report) error {
	body, _ := json.Marshal(map[string]string{"text": r.Slack()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("chat webhook answered %s", resp.Status)
	}
	return nil
}

// sendMail is swapped in tests.
var sendMail = smtpSend

// Email sends the report as a text + html email.
func Email(c Config, r Report) error {
	return sendMail(c, message(c, r))
}

func message(c Config, r Report) []byte {
	var rb [12]byte
	rand.Read(rb[:])
	boundary := "cowboy-" + hex.EncodeToString(rb[:])
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", c.From)
	h("To", strings.Join(c.EmailTo, ", "))
	h("Subject", mimeWord(r.Subject()))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("MIME-Version", "1.0")
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	part := func(ctype, body string) {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n", boundary, ctype)
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
		b.WriteString("\r\n")
	}
	part("text/plain", r.Text())
	part("text/html", r.HTML())
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}

// mimeWord encodes a header value with non-ascii characters (the en dash).
func mimeWord(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?utf-8?q?" + qEncode(s) + "?="
		}
	}
	return s
}

func qEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c == ' ':
			b.WriteByte('_')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "=%02X", c)
		}
	}
	return b.String()
}

func smtpSend(c Config, msg []byte) error {
	addr := net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort))
	var auth smtp.Auth
	if c.SMTPUser != "" {
		auth = smtp.PlainAuth("", c.SMTPUser, c.SMTPPass, c.SMTPHost)
	}
	if c.SMTPPort != 465 {
		// smtp.SendMail upgrades to TLS with STARTTLS when the server offers it,
		// and refuses to send a password over a plain connection.
		return smtp.SendMail(addr, auth, c.From, c.EmailTo, msg)
	}
	// Port 465 speaks TLS from the first byte.
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 20 * time.Second}, "tcp", addr, &tls.Config{ServerName: c.SMTPHost})
	if err != nil {
		return err
	}
	cl, err := smtp.NewClient(conn, c.SMTPHost)
	if err != nil {
		return err
	}
	defer cl.Close()
	if auth != nil {
		if err := cl.Auth(auth); err != nil {
			return err
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return err
	}
	for _, to := range c.EmailTo {
		if err := cl.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// Send delivers the report to every destination in c, returning one error
// per destination that failed.
func Send(ctx context.Context, c Config, r Report, only map[string]bool) map[string]error {
	errs := map[string]error{}
	if c.SlackURL != "" && (only == nil || only["chat"]) {
		if err := PostChat(ctx, c.SlackURL, r); err != nil {
			errs["chat"] = err
		}
	}
	if len(c.EmailTo) > 0 && (only == nil || only["email"]) {
		if err := Email(c, r); err != nil {
			errs["email"] = err
		}
	}
	return errs
}

// Scheduler sends the tally once a week and remembers what it sent in a
// small file next to the data file, so a restart never sends it twice.
type Scheduler struct {
	Config Config
	App    *app.App
	Path   string // sent-state file

	mu    sync.Mutex
	state map[string]time.Time // destination → the scheduled time last delivered
	retry time.Time
}

func NewScheduler(c Config, a *app.App, dataPath string) *Scheduler {
	s := &Scheduler{Config: c, App: a, Path: filepath.Join(filepath.Dir(dataPath), "tally-sent.json"), state: map[string]time.Time{}}
	if b, err := os.ReadFile(s.Path); err == nil {
		json.Unmarshal(b, &s.state)
	}
	// First start: don't send last week's tally straight away; the first
	// post goes out at the next scheduled time.
	now := time.Now()
	for _, d := range s.dests() {
		if _, ok := s.state[d]; !ok {
			s.state[d] = c.Due(now)
		}
	}
	s.save()
	return s
}

func (s *Scheduler) dests() []string {
	var d []string
	if s.Config.SlackURL != "" {
		d = append(d, "chat")
	}
	if len(s.Config.EmailTo) > 0 {
		d = append(d, "email")
	}
	return d
}

func (s *Scheduler) save() {
	b, _ := json.MarshalIndent(s.state, "", "  ")
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		os.Rename(tmp, s.Path)
	}
}

// Tick sends anything that's due. It's called every minute.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Before(s.retry) {
		return
	}
	due := s.Config.Due(now)
	pending := map[string]bool{}
	for _, d := range s.dests() {
		if s.state[d].Before(due) {
			pending[d] = true
		}
	}
	if len(pending) == 0 {
		return
	}
	// A server that was off all week doesn't post a stale tally.
	if now.Sub(due) > 24*time.Hour {
		for d := range pending {
			s.state[d] = due
		}
		s.save()
		log.Printf("weekly tally: skipped the %s post, the server was down when it was due", due.Format("Jan 2"))
		return
	}
	r := Build(s.App, due, s.Config.Link)
	errs := Send(ctx, s.Config, r, pending)
	for d := range pending {
		if err := errs[d]; err != nil {
			log.Printf("weekly tally: %s failed: %v (trying again in 30 minutes)", d, err)
			s.retry = now.Add(30 * time.Minute)
			continue
		}
		s.state[d] = due
		log.Printf("weekly tally: sent to %s", d)
	}
	s.save()
}

// Run ticks every minute until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	s.Tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.Tick(ctx, now)
		}
	}
}
