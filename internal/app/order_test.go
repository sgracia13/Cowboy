package app

import (
	"path/filepath"
	"testing"

	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

// newestFirst lists open PRs newest first, the way GitHub does.
type newestFirst struct{ *forge.Fake }

func (n newestFirst) ListOpenPRs(repo string) ([]forge.OpenPR, error) {
	open, err := n.Fake.ListOpenPRs(repo)
	for i, j := 0, len(open)-1; i < j; i, j = i+1, j-1 {
		open[i], open[j] = open[j], open[i]
	}
	return open, err
}

// Found on a real repo: when the host lists newest first, the sweep must
// still mark the newer PR as the double of the older one.
func TestSweepFindsDoublesWhateverTheOrder(t *testing.T) {
	td := "../../testdata"
	f := newestFirst{forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))}
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	st.AddRepo("acme/app")
	a.Reconcile()
	if d, _ := st.Get("acme/app", 509); d.DoubleOf != 506 {
		t.Fatalf("509 double_of = %d, want 506", d.DoubleOf)
	}
}

// A person's PR gets a comment only when it lands in careful; agent PRs
// always do.
func TestCommentsOnPeopleOnlyWhenCareful(t *testing.T) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := New(f, st, nil)
	person := func(pen string) pipeline.Decision {
		return pipeline.Decision{Repo: "acme/app", Number: 1, Pen: score.Pen(pen)}
	}
	if a.startComment(person("easy"), score.Easy) || a.startComment(person("normal"), score.Normal) {
		t.Error("a person's easy or normal PR should get no comment")
	}
	if !a.startComment(person("careful"), score.Careful) {
		t.Error("a person's careful PR should get a comment")
	}
	agent := person("easy")
	agent.Agent = "codex"
	if !a.startComment(agent, score.Easy) {
		t.Error("agent PRs always get a comment")
	}
}
