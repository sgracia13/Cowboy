package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/evaluate"
	"github.com/sgracia13/Cowboy/internal/model"
)

const patch = "diff --git a/lib/a.go b/lib/a.go\n--- a/lib/a.go\n+++ b/lib/a.go\n@@ -1 +1,2 @@\n-x\n+y\n+z\n"

func prLine(n int, extra map[string]any) string {
	m := map[string]any{"repo": "o/r", "number": n, "merged": "2026-03-01T10:00:00Z", "title": "fix retry",
		"msg": "fix retry (#1)\n\nRetries the call.", "patch": patch}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b) + "\n"
}

type recorder struct{ asks []model.Ask }

func (r *recorder) Risk(a model.Ask) model.Risk {
	r.asks = append(r.asks, a)
	s := 10
	return model.Risk{Score: &s, PromptVersion: model.RiskPromptVersion, Model: "fake"}
}

// The model must see the same thing whether or not the PR was later
// reverted: outcome fields in the input are never decoded.
func TestScoreIgnoresOutcomeFields(t *testing.T) {
	plain := prLine(1, nil)
	labelled := prLine(1, map[string]any{"reverted": true, "reverted_after_days": 2, "followups": 3, "in_sample": false})
	var a, b recorder
	score(strings.NewReader(plain), io.Discard, map[string]bool{}, &a, 0, nil)
	score(strings.NewReader(labelled), io.Discard, map[string]bool{}, &b, 0, nil)
	if len(a.asks) != 1 || !reflect.DeepEqual(a.asks, b.asks) {
		t.Fatalf("outcome fields changed what the model saw:\n%+v\n%+v", a.asks, b.asks)
	}
	if a.asks[0].Title != "fix retry" || a.asks[0].Body != "\nRetries the call." || len(a.asks[0].Files) != 1 {
		t.Errorf("ask: %+v", a.asks[0])
	}
	// and over the wire, with the real client: byte-identical requests
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(x))
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": `{"risk": 5}`}}}})
	}))
	defer srv.Close()
	c := model.New(srv.URL, "m", "")
	score(strings.NewReader(plain), io.Discard, map[string]bool{}, c, 0, nil)
	score(strings.NewReader(labelled), io.Discard, map[string]bool{}, c, 0, nil)
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatal("requests differ between a reverted and a non-reverted copy of the same PR")
	}
	var req struct {
		Messages []struct{ Role, Content string }
	}
	json.Unmarshal([]byte(bodies[0]), &req)
	if len(req.Messages) != 2 || strings.Contains(req.Messages[1].Content, "revert") {
		t.Error("the PR text sent to the model mentions a revert it can't have known about")
	}
}

func TestScoreWritesProvenanceAndResumes(t *testing.T) {
	in := prLine(1, nil) + prLine(2, nil) + prLine(3, nil)
	var out bytes.Buffer
	done := map[string]bool{}
	r := &recorder{}
	if n, err := score(strings.NewReader(in), &out, done, r, 2, nil); err != nil || n != 2 {
		t.Fatalf("pilot of 2: %d %v", n, err)
	}
	if n, _ := score(strings.NewReader(in), &out, done, r, 0, nil); n != 1 {
		t.Fatalf("resume should score only the third PR, scored %d", n)
	}
	got, runs, err := readScores(&out)
	if err != nil || len(got) != 3 || len(runs) != 1 {
		t.Fatalf("read back: %d %v %v", len(got), runs, err)
	}
	s := got[key("o/r", 2)]
	if s.Number != 2 || s.Merged.IsZero() || s.Score == nil || s.PromptVersion != model.RiskPromptVersion {
		t.Errorf("line: %+v", s)
	}
}

func TestReadScoresLastLineWins(t *testing.T) {
	five := 5
	lines := []Scored{
		{Repo: "o/r", Number: 1, Risk: model.Risk{Error: "timeout", Model: "m", PromptVersion: "p"}},
		{Repo: "o/r", Number: 1, Risk: model.Risk{Score: &five, Model: "m", PromptVersion: "p"}},
	}
	var b bytes.Buffer
	for _, l := range lines {
		json.NewEncoder(&b).Encode(l)
	}
	got, _, _ := readScores(&b)
	if s := got[key("o/r", 1)]; s.Score == nil || *s.Score != 5 {
		t.Errorf("a retry should replace the failed line: %+v", s)
	}
}

// fixture writes a replay for one repo: n PRs, every 20th reverted 3 days
// after merge, PRs 1–100 merged in 2024 (before a 2025 cutoff), the rest
// in 2026.
func fixture(t *testing.T, n int) (sorted, replay, logf string, reverted map[int]bool) {
	dir := t.TempDir()
	reverted = map[int]bool{}
	var rows bytes.Buffer
	merges := map[string]any{}
	var outs []any
	for i := 1; i <= n; i++ {
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		if i <= 100 {
			at = time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		}
		rev := i%20 == 0
		reverted[i] = rev
		pen := "normal"
		if i%5 == 1 { // careful never holds a reverted PR here
			pen = "careful"
		}
		json.NewEncoder(&rows).Encode(map[string]any{"repo": "o/r", "number": i, "pen": pen, "lines": 10 + i%300, "files": 1 + i%7, "reverted": rev, "merged": at})
		merges[fmt.Sprint(i)] = map[string]any{"number": i, "merged_at": at}
		if rev {
			outs = append(outs, map[string]any{"number": i, "kind": "revert", "status": "auto", "at": at.Add(72 * time.Hour)})
		}
	}
	sorted = filepath.Join(dir, "sorted.jsonl")
	os.WriteFile(sorted, rows.Bytes(), 0o644)
	replay = filepath.Join(dir, "replay")
	os.MkdirAll(filepath.Join(replay, "o_r"), 0o755)
	b, _ := json.Marshal(map[string]any{"merges": merges, "outcomes": outs})
	os.WriteFile(filepath.Join(replay, "o_r", "outcomes.json"), b, 0o644)
	logf = filepath.Join(dir, "o_r.log")
	nrev := 0
	for _, r := range reverted {
		if r {
			nrev++
		}
	}
	os.WriteFile(logf, []byte(fmt.Sprintf("o/r: %d PRs in window, %d later reverted\n", n, nrev)), 0o644)
	return
}

func load(t *testing.T, sorted, replay, logf string) ([]evaluate.Item, map[string]time.Time) {
	items, err := evaluate.LoadReplay(sorted, replay, []string{logf})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(sorted)
	defer f.Close()
	merged, err := mergedTimes(f)
	if err != nil {
		t.Fatal(err)
	}
	return items, merged
}

var cutoff = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
var fast = evaluate.Options{Draws: 300, Seed: 2026, MinEvents: evaluate.MinEvents}

// Scores join to the replay's outcomes by repo and number: an oracle built
// from the revert list must come out on top, which only happens if every
// score lands on the right PR.
func TestCompareLinksScoresToOutcomes(t *testing.T) {
	sorted, replay, logf, reverted := fixture(t, 1200)
	items, merged := load(t, sorted, replay, logf)
	scores := map[string]Scored{}
	for i := 1; i <= 1200; i++ {
		s := 1
		if reverted[i] {
			s = 99
		}
		scores[key("o/r", i)] = Scored{Repo: "o/r", Number: i, Risk: model.Risk{Score: &s, Model: "m", PromptVersion: "p"}}
	}
	rep, err := runReport(items, merged, scores, map[string]int{"m | p": 1200}, cutoff, fast)
	if err != nil {
		t.Fatal(err)
	}
	p := rep.Population[0]
	if p.BeforeCut != 100 || p.BeforeCutBad != 5 || p.Scored != 1100 || p.Failed != 0 || p.NotScored != 0 || !rep.Complete {
		t.Fatalf("population: %+v", p)
	}
	if rep.Result.Events != 55 || rep.Result.Verdict["B"] != evaluate.Outperformed || rep.Result.Verdict["D"] != evaluate.Outperformed {
		t.Fatalf("oracle via the join: events %d, verdicts %+v", rep.Result.Events, rep.Result.Verdict)
	}
	if !strings.Contains(text(rep), "B, the model ranks instead of the rules: outperformed") {
		t.Errorf("report:\n%s", text(rep))
	}
}

// Failures and missing scores are left out of every method and counted;
// the practical bar fails at 1% or more.
func TestCompareMissingAndFailed(t *testing.T) {
	sorted, replay, logf, reverted := fixture(t, 1200)
	items, merged := load(t, sorted, replay, logf)
	scores := map[string]Scored{}
	for i := 101; i <= 1200; i++ {
		if i > 1150 { // the last 50: never scored
			continue
		}
		r := model.Risk{Model: "m", PromptVersion: "p", Seconds: 2}
		if i%40 == 0 { // fails, and every one of these was reverted
			r.Error = "timeout"
		} else {
			s := i % 100
			r.Score = &s
		}
		scores[key("o/r", i)] = Scored{Repo: "o/r", Number: i, Risk: r}
	}
	rep, err := runReport(items, merged, scores, map[string]int{"m | p": 1}, cutoff, fast)
	if err != nil {
		t.Fatal(err)
	}
	p := rep.Population[0]
	failedBad := 0
	for i := 101; i <= 1150; i++ {
		if i%40 == 0 && reverted[i] {
			failedBad++
		}
	}
	if p.NotScored != 50 || p.Failed != 26 || p.FailedBad != failedBad || rep.Complete || rep.Bar != "not met" {
		t.Fatalf("population %+v complete %v bar %s", p, rep.Complete, rep.Bar)
	}
	if rep.Result.PRs != p.Scored {
		t.Errorf("the comparison must use only scored PRs: %d vs %d", rep.Result.PRs, p.Scored)
	}
	out := text(rep)
	for _, want := range []string{"incomplete", "failed PRs had trouble", "not met"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestCompareRefusesMixedRuns(t *testing.T) {
	if _, err := runReport(nil, nil, nil, map[string]int{"m | p1": 3, "m | p2": 4}, cutoff, fast); err == nil || !strings.Contains(err.Error(), "mixes runs") {
		t.Errorf("want a refusal, got %v", err)
	}
}

func TestCompareNotEnoughEvidence(t *testing.T) {
	sorted, replay, logf, _ := fixture(t, 400) // 15 reverts after the cutoff
	items, merged := load(t, sorted, replay, logf)
	scores := map[string]Scored{}
	for i := 1; i <= 400; i++ {
		s := 50
		scores[key("o/r", i)] = Scored{Repo: "o/r", Number: i, Risk: model.Risk{Score: &s, Model: "m", PromptVersion: "p"}}
	}
	rep, _ := runReport(items, merged, scores, map[string]int{"m | p": 1}, cutoff, fast)
	if rep.Result.Verdict["B"] != evaluate.Insufficient || !strings.Contains(text(rep), "not enough evidence yet") {
		t.Errorf("report:\n%s", text(rep))
	}
}
