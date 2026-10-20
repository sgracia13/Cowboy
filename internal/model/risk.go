package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Risk is a model's estimate that a pull request will need trouble after
// merge, recorded with enough provenance to reproduce and audit it. It is
// for the offline experiment (cmd/cowboy-ailab): nothing in the running
// product reads it, and it never changes a pen, label or comment.
type Risk struct {
	Score     *int     `json:"score"` // 0–100; nil when the model gave no usable score
	Why       []string `json:"why,omitempty"`
	LookFirst []string `json:"look_first,omitempty"`

	// provenance
	Model         string    `json:"model"`                 // the model asked for
	Served        string    `json:"served,omitempty"`      // the model the server says answered
	Fingerprint   string    `json:"fingerprint,omitempty"` // the server's system_fingerprint, if any
	PromptVersion string    `json:"prompt_version"`
	Temperature   float64   `json:"temperature"`
	Seed          int       `json:"seed"`
	InputSHA      string    `json:"input_sha256"` // of the exact system + user text sent
	InputChars    int       `json:"input_chars"`
	Truncated     bool      `json:"truncated"` // the diff was cut to fit MaxDiff
	Seconds       float64   `json:"seconds"`
	Error         string    `json:"error,omitempty"`
	At            time.Time `json:"at"` // when the score was produced
}

const riskSystem = `You estimate review risk for a pull request, before it merges.
You see only the title, the description and the diff.
Estimate how likely it is that, within 30 days of merging, this change is reverted, breaks the main branch, or needs a follow-up fix.
Most pull requests have no such trouble; use low numbers unless the diff gives a concrete reason.
Answer with JSON only, no prose, in exactly this shape:
{"risk": <integer 0-100>,
 "why": ["<short reason>", "<short reason>"],
 "look_first": ["<file path>: <why>"]}
Rules:
- "risk" is an integer from 0 (almost surely fine) to 100 (almost surely trouble).
- "why" has at most 2 short lines, each naming something in the diff.
- "look_first" has at most 2 spots, each naming a file from the diff.
- Never mention files that are not in the diff.`

// RiskPromptVersion names the risk prompt: a label plus a hash of its text
// and of how the user message is built, so any edit gives a new version.
var RiskPromptVersion = func() string {
	h := sha256.Sum256([]byte(riskSystem + "\x00" + riskTemplate))
	return "risk-v1-" + hex.EncodeToString(h[:])[:12]
}()

// riskTemplate documents the user message's layout; it is part of the
// version hash, so changing RiskPrompt means changing this too.
const riskTemplate = "Title: %s\nDescription:\n%s\n\nDiff:\n<files in order, cut at MaxDiff characters>"

// RiskPrompt builds the user message from the PR's title, description and
// diff only, and says whether the diff was cut short.
func RiskPrompt(a Ask, maxDiff int) (string, bool) {
	var b strings.Builder
	body := strings.TrimSpace(a.Body)
	if body == "" {
		body = "(none)"
	}
	fmt.Fprintf(&b, "Title: %s\nDescription:\n%s\n\nDiff:\n", strings.TrimSpace(a.Title), body)
	used, cut := 0, false
	for _, f := range a.Files {
		chunk := renderFile(f)
		if used+len(chunk) > maxDiff {
			fmt.Fprintf(&b, "(diff for %s and later files cut for length)\n", f.Path)
			cut = true
			break
		}
		b.WriteString(chunk)
		used += len(chunk)
	}
	return b.String(), cut
}

// Risk asks the model for a risk score. Failures come back inside Risk
// (Error set, Score nil); the caller decides what to do with them.
func (c *Client) Risk(a Ask) Risk {
	user, cut := RiskPrompt(a, c.MaxDiff)
	h := sha256.Sum256([]byte(riskSystem + "\x00" + user))
	r := Risk{Model: c.Model, PromptVersion: RiskPromptVersion, Temperature: Temperature, Seed: Seed,
		InputSHA: hex.EncodeToString(h[:]), InputChars: len(riskSystem) + len(user), Truncated: cut}
	start := time.Now()
	rep, err := c.chat(riskSystem, user)
	r.Seconds = time.Since(start).Seconds()
	r.At = time.Now().UTC()
	r.Served, r.Fingerprint = rep.Served, rep.Fingerprint
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if err := ParseRisk(rep.Content, &r, a); err != nil {
		r.Error = err.Error()
	}
	return r
}

// ParseRisk reads the model's JSON. A missing or out-of-range score is an
// error, never a default: a guessed number would bias the comparison.
func ParseRisk(text string, r *Risk, a Ask) error {
	m := jsonObj.FindString(text)
	if m == "" {
		return fmt.Errorf("model reply had no JSON")
	}
	var raw struct {
		Risk      *float64 `json:"risk"`
		Why       []string `json:"why"`
		LookFirst []string `json:"look_first"`
	}
	if err := json.Unmarshal([]byte(m), &raw); err != nil {
		return fmt.Errorf("model reply wasn't valid JSON: %v", err)
	}
	if raw.Risk == nil {
		return fmt.Errorf("model reply had no risk score")
	}
	v := *raw.Risk
	if v < 0 || v > 100 || v != float64(int(v)) {
		return fmt.Errorf("risk score %v isn't an integer from 0 to 100", v)
	}
	score := int(v)
	r.Score = &score
	n := Notes{Brief: raw.Why, LookFirst: raw.LookFirst}
	Check(&n, a.Files) // drops files not in the PR, trims lengths
	r.LookFirst = n.LookFirst
	if len(n.Brief) > 2 {
		n.Brief = n.Brief[:2]
	}
	r.Why = n.Brief
	return nil
}
