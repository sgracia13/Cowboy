package evaluate

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestMatchesCompareScript checks this package against compare.py on a
// real replay: point estimates must match exactly, and the bootstrap
// ranges (a different random generator) closely. Set COWBOY_EVIDENCE to a
// directory holding sorted_v3.jsonl, replay_v3/, the three collect logs and
// v3.json (compare.py's output); skipped otherwise.
func TestMatchesCompareScript(t *testing.T) {
	dir := os.Getenv("COWBOY_EVIDENCE")
	if dir == "" {
		t.Skip("COWBOY_EVIDENCE not set")
	}
	logs, _ := filepath.Glob(filepath.Join(dir, "*.collect.log"))
	items, err := LoadReplay(filepath.Join(dir, "sorted_v3.jsonl"), filepath.Join(dir, "replay_v3"), logs)
	if err != nil {
		t.Fatal(err)
	}
	got := Evaluate(items, Defaults())
	var want struct {
		Counted []string `json:"counted"`
		Pooled  struct {
			Recall  map[string]float64 `json:"recall"`
			DSize   float64            `json:"d_size"`
			DSpread float64            `json:"d_spread"`
			Lift    float64            `json:"lift"`
			DEasy   float64            `json:"d_easy"`
			DSizeCI []float64          `json:"d_size_ci"`
			DSprCI  []float64          `json:"d_spread_ci"`
			LiftCI  []float64          `json:"lift_ci"`
			DEasyCI []float64          `json:"d_easy_ci"`
		} `json:"pooled"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &want)
	if len(got.Counted) != len(want.Counted) {
		t.Fatalf("counted %v, want %v", got.Counted, want.Counted)
	}
	exact := func(name string, g, w float64) {
		if math.Abs(g-w) > 1e-9 {
			t.Errorf("%s = %.12f, compare.py %.12f", name, g, w)
		}
	}
	exact("recall rule", got.Recall["rule"], want.Pooled.Recall["cowboy"])
	exact("recall size", got.Recall["size"], want.Pooled.Recall["size"])
	exact("recall spread", got.Recall["spread"], want.Pooled.Recall["spread"])
	exact("recall chance", got.Recall["chance"], want.Pooled.Recall["chance"])
	exact("vs size", got.VsSize.Value, want.Pooled.DSize)
	exact("vs files", got.VsFiles.Value, want.Pooled.DSpread)
	exact("lift", got.Lift.Value, want.Pooled.Lift)
	exact("safe", got.SafeVs.Value, want.Pooled.DEasy)
	near := func(name string, g Range, w []float64, tol float64) {
		if math.Abs(g.Low-w[0]) > tol || math.Abs(g.High-w[1]) > tol {
			t.Errorf("%s range %.4f..%.4f, compare.py %.4f..%.4f", name, g.Low, g.High, w[0], w[1])
		}
	}
	near("vs size", got.VsSize, want.Pooled.DSizeCI, 0.02)
	near("vs files", got.VsFiles, want.Pooled.DSprCI, 0.02)
	near("lift", got.Lift, want.Pooled.LiftCI, 0.06)
	near("safe", got.SafeVs, want.Pooled.DEasyCI, 0.0005)
	t.Logf("verdicts: %v", got.Verdict)
}

func item(n int, bad, flag, safe bool, lines int) Item {
	return Item{Repo: "o/r", Number: n, W: 1, Bad: bad, Flag: flag, Safe: safe, Lines: lines, Files: 1 + lines/50}
}

func TestInsufficient(t *testing.T) {
	var items []Item
	for i := 0; i < 500; i++ {
		items = append(items, item(i, i%100 == 0, i%10 == 0, i%3 == 0, i))
	}
	r := Evaluate(items, Defaults())
	for k, v := range r.Verdict {
		if v != Insufficient {
			t.Errorf("%s = %s with 5 events", k, v)
		}
	}
}

func TestOutperformedAndNoAdvantage(t *testing.T) {
	// a rule that flags 10% of PRs and holds most of the trouble
	var good, coin []Item
	for i := 0; i < 3000; i++ {
		flag := i%10 == 0
		bad := (flag && i%30 == 0) || i%400 == 7
		good = append(good, item(i, bad, flag, i%10 == 5, (i*7919)%500))
		// a rule unrelated to trouble
		coin = append(coin, item(i, i%40 == 3, i%10 == 1, i%10 == 5, (i*7919)%500))
	}
	if v := Evaluate(good, Defaults()).Verdict["chance"]; v != Outperformed {
		t.Errorf("good rule vs chance: %s", v)
	}
	if v := Evaluate(coin, Defaults()).Verdict["chance"]; v != NoAdvantage && v != Worse {
		t.Errorf("coin-flip rule vs chance: %s", v)
	}
}

func TestDeterministic(t *testing.T) {
	var items []Item
	for i := 0; i < 2000; i++ {
		items = append(items, item(i, i%37 == 0, i%9 == 0, i%4 == 0, (i*31)%700))
	}
	a, _ := json.Marshal(Evaluate(items, Defaults()))
	b, _ := json.Marshal(Evaluate(items, Defaults()))
	if string(a) != string(b) {
		t.Error("same input, different result")
	}
}
