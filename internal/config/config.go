// Package config holds the per-repo sorting rules (cowboy.json).
//
// The product plan calls for cowboy.yml; this prototype uses JSON so it can
// run on the Go standard library alone. The fields are the same.
package config

import (
	"encoding/json"
	"os"
	"path"
	"strings"
)

// Config is the rule book for one repo.
type Config struct {
	// Agents maps a bot login (or a name found in a co-author trailer or PR
	// body signature) to the agent name shown on labels.
	Agents map[string]string `json:"agents"`

	Easy struct {
		MaxLines int `json:"max_lines"`
		MaxDirs  int `json:"max_dirs"`
		MaxFanIn int `json:"max_fan_in"`
	} `json:"easy"`

	Careful struct {
		// MinSourceLines sends PRs this big to careful. Off (0) by default:
		// on real repos, big PRs were reverted less often, not more. Big PRs
		// get a "too big to review well" note instead (Notes.BigSourceLines).
		MinSourceLines int `json:"min_source_lines"`
		MinFanIn       int `json:"min_fan_in"`
		// TopFanInPercent raises the blast-radius bar to the repo's own most
		// imported files: a changed file must be in this top share of the
		// files anything imports, and still have MinFanIn importers. In a big
		// monorepo 20 importers is ordinary; 100 turns the extra bar off.
		TopFanInPercent float64  `json:"top_fan_in_percent"`
		Paths           []string `json:"paths"`
		// Shortcuts lists the shortcut flags that send a PR to careful. The
		// others are still found and shown as notes.
		Shortcuts []string `json:"shortcuts"`
	} `json:"careful"`

	// Notes are shown on the PR but never change its pen.
	Notes struct {
		BigSourceLines int `json:"big_source_lines"`
	} `json:"notes"`

	// Shortcuts switches each of the six shortcut flags on or off.
	Shortcuts map[string]bool `json:"shortcuts"`

	Strays struct {
		NudgeAfterDays   int `json:"nudge_after_days"`
		ArchiveAfterDays int `json:"suggest_archive_after_days"`
	} `json:"strays"`

	// FanInIgnore lists module paths never counted for blast radius
	// (e.g. a shared "utils" everyone imports).
	FanInIgnore []string `json:"fan_in_ignore"`

	// People controls how loud Cowboy is on PRs from people (no agent).
	// They get a pen label either way; Comment is "careful" (default: a
	// comment only when the PR lands in careful), "always" or "never".
	People struct {
		Comment string `json:"comment"`
	} `json:"people"`

	// Detect controls the check for agent PRs that don't say so.
	Detect struct {
		// Enabled runs the check on PRs with no disclosed agent (default on).
		Enabled *bool `json:"enabled"`
		// SortAll puts every PR in a pen, not just agent PRs (default on).
		// The risk rules don't care who wrote the code, and unmarked agent
		// work can't be spotted reliably, so sorting everything is how
		// nothing risky goes unsorted.
		SortAll *bool `json:"sort_all"`
		// LabelAt is the score the LOW end of the range must reach before a
		// PR gets the agent:likely label (default 80).
		LabelAt int `json:"label_at"`
		// Prior is the expected share of undisclosed agent PRs (default 0.08).
		Prior float64 `json:"prior"`
	} `json:"detect"`
}

// SortAllOn reports whether every PR is sorted, not just agent PRs.
func (c Config) SortAllOn() bool { return c.Detect.SortAll == nil || *c.Detect.SortAll }

// CommentOnPerson reports whether Cowboy should start a comment on a PR
// from a person (not an agent) that landed in pen.
func (c Config) CommentOnPerson(careful bool) bool {
	switch c.People.Comment {
	case "always":
		return true
	case "never":
		return false
	}
	return careful
}

// DetectOn reports whether the undisclosed-agent check is enabled.
func (c Config) DetectOn() bool { return c.Detect.Enabled == nil || *c.Detect.Enabled }

// Default returns the rules Cowboy uses when a repo has no cowboy.json.
func Default() Config {
	var c Config
	c.Agents = map[string]string{
		"copilot":                 "copilot",
		"copilot-swe-agent[bot]":  "copilot",
		"github-copilot[bot]":     "copilot",
		"claude":                  "claude-code",
		"claude[bot]":             "claude-code",
		"claude code":             "claude-code",
		"codex":                   "codex",
		"chatgpt-codex-connector": "codex",
		"codex-bot":               "codex",
		"devin-ai-integration":    "devin",
		"devin":                   "devin",
		"cursor":                  "cursor",
		"cursor[bot]":             "cursor",
		"cursor agent":            "cursor",
		"google-labs-jules":       "jules",
		"jules":                   "jules",
	}
	c.Easy.MaxLines = 50
	c.Easy.MaxDirs = 2
	c.Easy.MaxFanIn = 3
	c.Careful.MinSourceLines = 0
	c.Careful.MinFanIn = 20
	c.Careful.TopFanInPercent = 1
	c.Careful.Shortcuts = []string{"tests_removed", "catch_all", "ci_loosened"}
	c.Notes.BigSourceLines = 400
	c.Careful.Paths = []string{"auth/**", "billing/**", "payments/**", "migrations/**", ".github/workflows/**", "security/**"}
	c.Shortcuts = map[string]bool{
		"tests_removed":         true,
		"skips":                 true,
		"catch_all":             true,
		"suppressions":          true,
		"special_case_comments": true,
		"ci_loosened":           true,
	}
	c.Strays.NudgeAfterDays = 3
	c.Strays.ArchiveAfterDays = 7
	c.Detect.LabelAt = 80
	c.Detect.Prior = 0.08
	c.People.Comment = "careful"
	return c
}

// Load reads cowboy.json over the defaults. A missing file is not an error.
func Load(file string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = Merge(&c, b)
	return c, err
}

// Merge applies a JSON document on top of c.
func Merge(c *Config, b []byte) error {
	var over Config
	if err := json.Unmarshal(b, &over); err != nil {
		return err
	}
	for k, v := range over.Agents {
		c.Agents[strings.ToLower(k)] = v
	}
	if over.Easy.MaxLines > 0 {
		c.Easy.MaxLines = over.Easy.MaxLines
	}
	if over.Easy.MaxDirs > 0 {
		c.Easy.MaxDirs = over.Easy.MaxDirs
	}
	if over.Easy.MaxFanIn > 0 {
		c.Easy.MaxFanIn = over.Easy.MaxFanIn
	}
	if over.Careful.MinSourceLines > 0 {
		c.Careful.MinSourceLines = over.Careful.MinSourceLines
	}
	if over.Careful.MinFanIn > 0 {
		c.Careful.MinFanIn = over.Careful.MinFanIn
	}
	if over.Careful.TopFanInPercent > 0 {
		c.Careful.TopFanInPercent = over.Careful.TopFanInPercent
	}
	if over.Careful.Paths != nil {
		c.Careful.Paths = over.Careful.Paths
	}
	if over.Careful.Shortcuts != nil {
		c.Careful.Shortcuts = over.Careful.Shortcuts
	}
	if over.Notes.BigSourceLines > 0 {
		c.Notes.BigSourceLines = over.Notes.BigSourceLines
	}
	for k, v := range over.Shortcuts {
		c.Shortcuts[k] = v
	}
	if over.Strays.NudgeAfterDays > 0 {
		c.Strays.NudgeAfterDays = over.Strays.NudgeAfterDays
	}
	if over.Strays.ArchiveAfterDays > 0 {
		c.Strays.ArchiveAfterDays = over.Strays.ArchiveAfterDays
	}
	if over.FanInIgnore != nil {
		c.FanInIgnore = over.FanInIgnore
	}
	if over.Detect.Enabled != nil {
		c.Detect.Enabled = over.Detect.Enabled
	}
	if over.Detect.SortAll != nil {
		c.Detect.SortAll = over.Detect.SortAll
	}
	switch over.People.Comment {
	case "careful", "always", "never":
		c.People.Comment = over.People.Comment
	}
	if over.Detect.LabelAt > 0 {
		c.Detect.LabelAt = over.Detect.LabelAt
	}
	if over.Detect.Prior > 0 && over.Detect.Prior < 1 {
		c.Detect.Prior = over.Detect.Prior
	}
	return nil
}

// MatchGlob reports whether p matches a glob where "**" spans directories.
func MatchGlob(glob, p string) bool {
	if strings.HasSuffix(glob, "/**") {
		prefix := strings.TrimSuffix(glob, "/**")
		return p == prefix || strings.HasPrefix(p, prefix+"/") || strings.Contains(p, "/"+prefix+"/")
	}
	if strings.Contains(glob, "**/") {
		suffix := glob[strings.Index(glob, "**/")+3:]
		if ok, _ := path.Match(suffix, path.Base(p)); ok {
			return true
		}
	}
	ok, _ := path.Match(glob, p)
	return ok
}
