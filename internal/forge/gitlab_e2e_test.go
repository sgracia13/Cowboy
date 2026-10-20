package forge_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

// The whole loop on GitLab: an agent's merge request is sorted, labelled
// and commented on; after merge, a revert on the default branch is traced
// back to the merge request it undid.
func TestGitLabEndToEnd(t *testing.T) {
	g, f := forge.NewFakeGitLab(t)
	st, _ := store.Open("")
	a := app.New(g, st, nil)
	repo := "grp/sub/app"
	st.AddRepo(repo)

	d, ok, err := a.HandlePR(repo, 7)
	if err != nil || !ok {
		t.Fatalf("sort: ok=%v err=%v", ok, err)
	}
	if d.Agent == "" {
		t.Errorf("the Claude co-author trailer should tag the agent: %+v", d.Signals.AgentHow)
	}
	if d.Pen != score.Careful {
		t.Errorf("a failing pipeline should send it to careful, got %s (%+v)", d.Pen, d.Reasons)
	}
	writes := strings.Join(f.Writes(), "\n")
	if !strings.Contains(writes, `"add_labels":"cowboy:careful`) {
		t.Errorf("no careful label written:\n%s", writes)
	}
	if !strings.Contains(writes, "/merge_requests/7/notes") || !strings.Contains(writes, "cowboy: careful") {
		t.Errorf("no comment posted:\n%s", writes)
	}

	// the catch-up sweep reads the open merge requests without errors
	if caught := a.Reconcile(); len(caught) > 2 {
		t.Errorf("sweep: %v", caught)
	}

	// sorted while it was open, then merged
	st.Put(pipeline.Decision{Repo: repo, Number: 8, Title: "Add retry", Pen: score.Normal, Agent: "claude-code", State: "merged"})
	oc, _ := outcome.Open("")
	w := &outcome.Watcher{Forge: g, Decisions: st, State: oc, Now: func() time.Time { return time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC) },
		Lookback: 60 * 24 * time.Hour, MaxPerRun: 100}
	w.RecordMerge(repo, 8)
	summary := w.Check(repo)
	m, ok := oc.Merge(repo, 8)
	if !ok || m.SHA != "5a5a5a5a8b8b8b8b" {
		t.Fatalf("merge not recorded: %+v (%s)", m, summary)
	}
	found := false
	for _, o := range oc.Outcomes(repo, 8) {
		found = found || o.Kind == outcome.Revert
	}
	if !found {
		t.Errorf("the revert of 5a5a5a5a8b8b8b8b should be traced to !8: %s", summary)
	}
}
