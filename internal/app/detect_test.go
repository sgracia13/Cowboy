package app

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/detect"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/store"
)

func detectApp(t *testing.T) (*App, *forge.Fake, *store.Store, *detect.FileBaselines) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	now := time.Date(2026, 10, 18, 18, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	bs, _ := detect.OpenBaselines(filepath.Join(t.TempDir(), "b.json"), 90)
	bs.Now = a.Now
	if err := detect.SeedDir(bs, f.Name(), filepath.Join(td, "history"), now); err != nil {
		t.Fatal(err)
	}
	a.Baselines = bs
	return a, f, st, bs
}

func TestUndisclosedAgentCaught(t *testing.T) {
	a, f, st, _ := detectApp(t)
	for _, n := range f.All() {
		if _, _, err := a.HandlePR("acme/app", n); err != nil {
			t.Fatal(n, err)
		}
	}
	for _, n := range []int{540, 542} {
		d, ok := st.Get("acme/app", n)
		if !ok || d.Detection == nil || d.Detection.Label != "likely agent" {
			t.Fatalf("#%d not caught: %+v", n, d.Detection)
		}
		if !slices.Contains(f.Labels[n], "agent:likely") {
			t.Errorf("#%d forge labels %v, want agent:likely", n, f.Labels[n])
		}
		if !strings.Contains(f.Comments[n], "agent check") {
			t.Errorf("#%d comment lacks the agent check block", n)
		}
	}
	if d, _ := st.Get("acme/app", 540); d.Agent != "codex?" {
		t.Errorf("540 agent guess %q, want codex? (from the codex/ branch)", d.Agent)
	}
	for _, n := range []int{520, 541} {
		if _, ok := st.Get("acme/app", n); ok {
			t.Errorf("human #%d should be left alone", n)
		}
		if _, ok := f.Labels[n]; ok {
			t.Errorf("human #%d got labels %v", n, f.Labels[n])
		}
	}
	// disclosed agents skip the check entirely
	if d, _ := st.Get("acme/app", 507); d.Detection != nil {
		t.Error("disclosed #507 should not run the check")
	}
}

func TestSortAll(t *testing.T) {
	a, f, st, _ := detectApp(t)
	a.Forge = sortAllForge{f}
	d, ok, err := a.HandlePR("acme/app", 541)
	if err != nil || !ok {
		t.Fatalf("sort_all should sort the human PR: ok=%v err=%v", ok, err)
	}
	if d.Agent != "" || slices.ContainsFunc(f.Labels[541], func(l string) bool { return strings.HasPrefix(l, "agent:") }) {
		t.Errorf("human PR got an agent: %q %v", d.Agent, f.Labels[541])
	}
	if _, ok := st.Get("acme/app", 541); !ok {
		t.Error("not stored")
	}
}

// sortAllForge serves a cowboy.json with sort_all on.
type sortAllForge struct{ *forge.Fake }

func (s sortAllForge) GetFile(repo, path, ref string) (string, error) {
	if path == "cowboy.json" {
		return `{"detect": {"sort_all": true}}`, nil
	}
	return s.Fake.GetFile(repo, path, ref)
}

func TestNotAnAgentSticks(t *testing.T) {
	a, f, st, bs := detectApp(t)
	a.HandlePR("acme/app", 542)
	if err := a.ClearAgent("acme/app", 542, "priya"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.Labels[542], "agent:likely") {
		t.Fatalf("label still on: %v", f.Labels[542])
	}
	if !strings.Contains(f.Comments[542], "priya removed the agent:likely label") {
		t.Errorf("comment not updated:\n%s", f.Comments[542])
	}
	// a re-sort of the same PR keeps it as human work, in the corral
	d, ok, _ := a.HandlePR("acme/app", 542)
	if !ok || d.AgentCleared != "priya" || slices.Contains(f.Labels[542], "agent:likely") {
		t.Fatalf("correction lost on re-sort: ok=%v cleared=%q labels=%v", ok, d.AgentCleared, f.Labels[542])
	}
	if b, _ := bs.Get("demo", "priya"); b.CorrectionsAgainst != 1 {
		t.Errorf("corrections = %d, want 1", b.CorrectionsAgainst)
	}
	_ = st
}

func TestClosingTeachesTheBaseline(t *testing.T) {
	a, f, _, bs := detectApp(t)
	before, _ := bs.Get("demo", "sam")
	a.HandlePR("acme/app", 541)
	f.Close(541, true)
	a.Closed("acme/app", 541, true)
	after, _ := bs.Get("demo", "sam")
	if after.SamplePRs != before.SamplePRs+1 {
		t.Fatalf("sam samples %d -> %d, want +1", before.SamplePRs, after.SamplePRs)
	}
	// a labelled agent PR joins history as agent work, not as "normal for priya"
	pb, _ := bs.Get("demo", "priya")
	a.HandlePR("acme/app", 542)
	a.Closed("acme/app", 542, true)
	pa, _ := bs.Get("demo", "priya")
	if pa.SamplePRs != pb.SamplePRs || pa.DisclosedAgentShare <= pb.DisclosedAgentShare {
		t.Fatalf("agent PR leaked into priya's human baseline: %d->%d share %.2f->%.2f", pb.SamplePRs, pa.SamplePRs, pb.DisclosedAgentShare, pa.DisclosedAgentShare)
	}
}
