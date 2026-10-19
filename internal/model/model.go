// Package model is Cowboy's optional local model. It never picks the pen.
//
// It reads an agent PR and answers two questions for the reviewer:
//
//  1. Did the agent do what it was asked? (scope check)
//  2. What should a reviewer look at first? (a three-line brief)
//
// It talks to any server that speaks the OpenAI chat API: Ollama, vLLM,
// llama.cpp's server, LM Studio, LocalAI. The team points Cowboy at the model
// server they already run; nothing leaves their network.
package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/diff"
)

// Notes are what the model says about one PR. They are advice, shown next to
// the rule reasons; they never change the pen.
type Notes struct {
	Model     string   `json:"model"`
	OnTask    *bool    `json:"on_task,omitempty"` // nil = the model couldn't tell
	Outside   []string `json:"outside,omitempty"` // changes the issue didn't ask for
	Brief     []string `json:"brief,omitempty"`   // up to 3 plain lines
	LookFirst []string `json:"look_first,omitempty"`
	Dropped   int      `json:"dropped,omitempty"` // notes thrown out for naming files not in the PR
	HeadSHA   string   `json:"head_sha"`
	Seconds   float64  `json:"seconds"`
	Error     string   `json:"error,omitempty"`
}

// Drift reports whether the model thinks the agent went off task.
func (n *Notes) Drift() bool { return n != nil && n.OnTask != nil && !*n.OnTask }

// Ask is the PR as the model sees it.
type Ask struct {
	Repo, Title, Body string
	Number            int
	Files             []diff.File
	HeadSHA           string
}

// Model is anything that can write Notes.
type Model interface {
	Name() string
	Notes(a Ask) Notes
}

// ---------- the real thing: any OpenAI-compatible server ----------

// Client talks to a local model server.
type Client struct {
	BaseURL string // e.g. http://localhost:11434 (Ollama) or http://gpu-box:8000 (vLLM)
	Model   string // e.g. qwen2.5-coder:7b
	Key     string // optional; most local servers ignore it
	MaxDiff int    // characters of diff sent to the model (default 12000)
	HTTP    *http.Client
}

// New builds a client. baseURL may end in /v1 or not.
func New(baseURL, name, key string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return &Client{BaseURL: baseURL, Model: name, Key: key, MaxDiff: 12000,
		HTTP: &http.Client{Timeout: 3 * time.Minute}}
}

func (c *Client) Name() string { return c.Model }

const system = `You help a code reviewer triage a pull request written by an AI coding agent.
You do not decide whether to merge. You do not review style.
Answer with JSON only, no prose, in exactly this shape:
{"on_task": true|false|null,
 "outside": ["<file path>: <change the task did not ask for>"],
 "brief": ["<line 1>", "<line 2>", "<line 3>"],
 "look_first": ["<file path>: <why>"]}
Rules:
- "on_task" is false only if the diff makes a change the title/description/issue did not ask for. Use null if the task is too vague to judge.
- "outside" lists only those extra changes, each naming a file from the diff. Empty list if none.
- "brief" is at most 3 short plain-English lines: what changed and anything odd (dead code, padding, a change that does nothing).
- "look_first" is at most 2 spots, each naming a file from the diff.
- Never mention files that are not in the diff.`

// Prompt builds the user message. Exported for tests and for `cowboy score --show-prompt`.
func Prompt(a Ask, maxDiff int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task title: %s\n", a.Title)
	body := strings.TrimSpace(a.Body)
	if body == "" {
		body = "(none)"
	}
	fmt.Fprintf(&b, "Task description:\n%s\n\nDiff:\n", body)
	used := 0
	for _, f := range a.Files {
		chunk := renderFile(f)
		if used+len(chunk) > maxDiff {
			fmt.Fprintf(&b, "(diff for %s and later files cut for length)\n", f.Path)
			break
		}
		b.WriteString(chunk)
		used += len(chunk)
	}
	return b.String()
}

func renderFile(f diff.File) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s (%s)\n", f.Path, f.Status)
	for _, l := range f.Removed {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range f.Added {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

// Notes asks the model. Errors come back inside Notes so sorting never stops.
func (c *Client) Notes(a Ask) Notes {
	start := time.Now()
	n := Notes{Model: c.Model, HeadSHA: a.HeadSHA}
	text, err := c.complete(system, Prompt(a, c.MaxDiff))
	n.Seconds = time.Since(start).Seconds()
	if err != nil {
		n.Error = err.Error()
		return n
	}
	if err := Parse(text, &n); err != nil {
		n.Error = err.Error()
		return n
	}
	Check(&n, a.Files)
	return n
}

// Ping sends a tiny request to check the server and model are there.
func (c *Client) Ping() (string, error) {
	return c.complete("Reply with the single word: ok", "ping")
}

func (c *Client) complete(sys, user string) (string, error) {
	req := map[string]any{
		"model":       c.Model,
		"temperature": 0,
		"seed":        7,
		"messages":    []map[string]string{{"role": "system", "content": sys}, {"role": "user", "content": user}},
	}
	b, _ := json.Marshal(req)
	r, err := http.NewRequest("POST", c.BaseURL+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	r.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		r.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(r)
	if err != nil {
		return "", fmt.Errorf("can't reach the model server at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("model server said %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("unexpected reply from model server")
	}
	return out.Choices[0].Message.Content, nil
}

var jsonObj = regexp.MustCompile(`(?s)\{.*\}`)

// Parse reads the model's JSON (tolerating code fences or chatter around it).
func Parse(text string, n *Notes) error {
	m := jsonObj.FindString(text)
	if m == "" {
		return fmt.Errorf("model reply had no JSON")
	}
	var raw struct {
		OnTask    *bool    `json:"on_task"`
		Outside   []string `json:"outside"`
		Brief     []string `json:"brief"`
		LookFirst []string `json:"look_first"`
	}
	if err := json.Unmarshal([]byte(m), &raw); err != nil {
		return fmt.Errorf("model reply wasn't valid JSON: %v", err)
	}
	n.OnTask, n.Outside, n.Brief, n.LookFirst = raw.OnTask, raw.Outside, raw.Brief, raw.LookFirst
	return nil
}

// Check throws out notes that name files not in the PR (a model making
// things up), trims lengths, and keeps on_task consistent with "outside".
func Check(n *Notes, files []diff.File) {
	in := map[string]bool{}
	for _, f := range files {
		in[f.Path] = true
	}
	keep := func(xs []string, max int) []string {
		var out []string
		for _, x := range xs {
			x = strings.TrimSpace(x)
			path, _, _ := strings.Cut(x, ":")
			if !in[strings.TrimSpace(path)] {
				n.Dropped++
				continue
			}
			if len(out) < max {
				out = append(out, clip(x, 200))
			}
		}
		return out
	}
	n.Outside = keep(n.Outside, 5)
	n.LookFirst = keep(n.LookFirst, 2)
	var brief []string
	for _, l := range n.Brief {
		if l = strings.TrimSpace(l); l != "" && len(brief) < 3 {
			brief = append(brief, clip(l, 200))
		}
	}
	n.Brief = brief
	if n.OnTask != nil && !*n.OnTask && len(n.Outside) == 0 {
		n.OnTask = nil // claimed drift but named nothing real: don't alarm anyone
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// Render turns notes into the markdown block added to the PR comment.
func Render(n *Notes) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**local model notes** · %s on your server · advice only, the pen is set by the rules above\n\n", n.Model)
	if n.Error != "" {
		fmt.Fprintf(&b, "- couldn't get notes this time (%s)\n", n.Error)
		return b.String()
	}
	switch {
	case n.OnTask == nil:
		b.WriteString("- on task: couldn't tell from the description\n")
	case *n.OnTask:
		b.WriteString("- on task: yes, the changes match what was asked\n")
	default:
		b.WriteString("- **on task: no.** changes the task didn't ask for:\n")
		for _, o := range n.Outside {
			fmt.Fprintf(&b, "  - %s\n", o)
		}
	}
	for _, l := range n.Brief {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	if len(n.LookFirst) > 0 {
		fmt.Fprintf(&b, "- look first: %s\n", strings.Join(n.LookFirst, "; "))
	}
	return b.String()
}

// ---------- demo model: canned answers, no server needed ----------

// Demo returns prewritten notes from testdata/prs/<n>/model.json so the demo
// shows the feature without a model server. It is labelled as such everywhere.
type Demo struct{ Dir string }

func (d Demo) Name() string { return "demo model (prewritten answers)" }

func (d Demo) Notes(a Ask) Notes {
	n := Notes{Model: d.Name(), HeadSHA: a.HeadSHA}
	b, err := os.ReadFile(filepath.Join(d.Dir, strconv.Itoa(a.Number), "model.json"))
	if err != nil {
		t := true
		n.OnTask = &t
		n.Brief = []string{fmt.Sprintf("%d file(s) changed: %s", len(a.Files), paths(a.Files))}
		return n
	}
	if err := Parse(string(b), &n); err != nil {
		n.Error = err.Error()
	}
	Check(&n, a.Files)
	return n
}

func paths(fs []diff.File) string {
	var ps []string
	for _, f := range fs {
		ps = append(ps, f.Path)
	}
	sort.Strings(ps)
	return strings.Join(ps, ", ")
}
