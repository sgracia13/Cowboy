package evaluate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Replay inputs, as the evidence scripts produce them: sorted PRs
// (cowboy-sortlab), one outcomes.json per repo (cowboy-replay) and the
// collector's logs, whose "N PRs in window, R later reverted" lines give
// the sampling weights.

var windowRe = regexp.MustCompile(`^(\S+/\S+): (\d+) PRs in window, (\d+) later reverted`)

// LoadReplay reads a replayed sample as items: trouble = a revert the
// engine counted within 30 days of merge; careful = flagged; easy = safe.
func LoadReplay(sortedPath, replayDir string, logs []string) ([]Item, error) {
	window := map[string][2]int{}
	for _, p := range logs {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(string(b), "\n") {
			if m := windowRe.FindStringSubmatch(l); m != nil {
				a, _ := strconv.Atoi(m[2])
				r, _ := strconv.Atoi(m[3])
				window[m[1]] = [2]int{a, r}
			}
		}
	}
	f, err := os.Open(sortedPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type row struct {
		Repo     string `json:"repo"`
		Number   int    `json:"number"`
		Pen      string `json:"pen"`
		Lines    int    `json:"lines"`
		Files    int    `json:"files"`
		Reverted bool   `json:"reverted"`
	}
	byRepo := map[string][]row{}
	var repos []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<20), 64<<20)
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		if byRepo[r.Repo] == nil {
			repos = append(repos, r.Repo)
		}
		byRepo[r.Repo] = append(byRepo[r.Repo], r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	var items []Item
	for _, repo := range repos {
		rows := byRepo[repo]
		bad, err := replayReverts(replayDir, repo)
		if err != nil {
			return nil, err
		}
		w, ok := window[repo]
		if !ok {
			return nil, fmt.Errorf("no collect log line for %s", repo)
		}
		others := 0
		for _, r := range rows {
			if !r.Reverted {
				others++
			}
		}
		weight := float64(w[0]-w[1]) / float64(others)
		for _, r := range rows {
			it := Item{Repo: repo, Number: r.Number, W: weight, Stratum: "sampled", Bad: bad[r.Number],
				Flag: r.Pen == "careful", Safe: r.Pen == "easy", Lines: r.Lines, Files: r.Files}
			if r.Reverted {
				it.W, it.Stratum = 1, "reverted"
			}
			items = append(items, it)
		}
	}
	return items, nil
}

func replayReverts(dir, repo string) (map[int]bool, error) {
	b, err := os.ReadFile(dir + "/" + strings.ReplaceAll(repo, "/", "_") + "/outcomes.json")
	if err != nil {
		return nil, err
	}
	var st struct {
		Merges map[string]struct {
			Number   int       `json:"number"`
			MergedAt time.Time `json:"merged_at"`
		} `json:"merges"`
		Outcomes []struct {
			Number int       `json:"number"`
			Kind   string    `json:"kind"`
			Status string    `json:"status"`
			At     time.Time `json:"at"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	merged := map[int]time.Time{}
	for _, m := range st.Merges {
		merged[m.Number] = m.MergedAt
	}
	out := map[int]bool{}
	for _, o := range st.Outcomes {
		m, ok := merged[o.Number]
		if o.Kind == "revert" && (o.Status == "auto" || o.Status == "confirmed") && ok && o.At.Sub(m) <= 30*24*time.Hour {
			out[o.Number] = true
		}
	}
	return out, nil
}
