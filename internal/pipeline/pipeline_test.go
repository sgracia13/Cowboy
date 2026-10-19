package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/graph"
)

// TestGolden scores every sample PR in testdata/prs and checks the pen.
// PRs are scored in number order so doubles can see earlier PRs.
func TestGolden(t *testing.T) {
	root := "../../testdata"
	g, err := graph.Build(filepath.Join(root, "acme"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(root, "acme", "cowboy.json"))
	if err != nil {
		t.Fatal(err)
	}
	owners, _ := os.ReadFile(filepath.Join(root, "acme", "CODEOWNERS"))
	dirs, _ := os.ReadDir(filepath.Join(root, "prs"))
	var nums []int
	for _, d := range dirs {
		n, _ := strconv.Atoi(d.Name())
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var open []Decision
	for _, n := range nums {
		dir := filepath.Join(root, "prs", strconv.Itoa(n))
		var meta analyze.PRMeta
		b, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err := json.Unmarshal(b, &meta); err != nil {
			t.Fatal(err)
		}
		patch, _ := os.ReadFile(filepath.Join(dir, "pr.diff"))
		want, _ := os.ReadFile(filepath.Join(dir, "expect.txt"))
		in := Input{Meta: meta, Files: diff.Parse(string(patch)), Config: cfg, Graph: g, Codeowners: string(owners), OpenPRs: open, Now: time.Unix(0, 0)}
		d, ok := Run(in)
		got := "skip"
		if ok {
			got = string(d.Pen)
			if d.DoubleOf != 0 {
				got += "+double"
			}
			open = append(open, d)
		}
		if got != strings.TrimSpace(string(want)) {
			t.Errorf("PR #%d %q: got %s, want %s\nreasons: %+v", n, meta.Title, got, strings.TrimSpace(string(want)), d.Reasons)
		}
		// determinism: same input, same output, 100 times
		for i := 0; i < 100 && ok; i++ {
			again, _ := Run(in)
			if !reflect.DeepEqual(again, d) {
				t.Fatalf("PR #%d: result changed between runs", n)
			}
		}
	}
}

func TestBlastRadius(t *testing.T) {
	g, err := graph.Build("../../testdata/acme")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]int{
		"src/auth/session.ts":    38 + 1, // 38 features + its own test
		"src/webhooks/sender.ts": 7,      // 6 jobs + client
		"src/api/audit.ts":       3,
		"billing/totals.py":      5, // 4 reports + test
	}
	for f, want := range cases {
		if got, _ := g.FanIn(f); got != want {
			t.Errorf("%s: fan-in %d, want %d", f, got, want)
		}
	}
}

// Hosts refuse review requests to a PR's own author (found on a real repo).
func TestOwnersSkipTheAuthor(t *testing.T) {
	meta := analyze.PRMeta{Repo: "o/r", Number: 1, Author: "Sam", Commits: []string{"x\n\nCo-authored-by: Claude <noreply@anthropic.com>"}}
	files := []diff.File{{Path: "src/auth/a.ts", Status: "modified", Added: []string{"x"}}}
	d, ok := Run(Input{Meta: meta, Files: files, Config: config.Default(), Codeowners: "/src/auth/ @sam @maria", Now: time.Unix(0, 0)})
	if !ok || len(d.Signals.Owners) != 1 || d.Signals.Owners[0] != "@maria" {
		t.Fatalf("owners = %v, want [@maria]", d.Signals.Owners)
	}
}
