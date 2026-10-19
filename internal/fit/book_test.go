package fit

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBookLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learned.json")
	b, err := OpenBook(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	res := Result{Repo: "O/R", At: now, From: now.AddDate(0, 0, -90), Base: 0.05, Enough: true, Rules: []Rule{
		{Fact: "path:billing/**", N: 40, K: 12, Rate: 0.3, LiftLow: 4},
		{Fact: "file:auth.go", N: 10, K: 4, Rate: 0.4, LiftLow: 3},
	}}
	if n, _ := b.Suggest(res, map[string]float64{"file:auth.go": 0.5}, now); n != 2 {
		t.Fatalf("new suggestions = %d, want 2", n)
	}
	if len(b.Active("o/r")) != 0 {
		t.Fatal("a suggestion must not sort anything")
	}
	// a suggestion can't skip the trial
	if _, err := b.Move("o/r", "path:billing/**", Active, "ann", now); err == nil {
		t.Fatal("suggested -> active should be refused")
	}
	l, err := b.Move("o/r", "path:billing/**", Shadow, "ann", now)
	if err != nil || !l.ShadowSince.Equal(now) || l.By != "ann" {
		t.Fatalf("start trial: %+v %v", l, err)
	}
	if len(b.Active("o/r")) != 0 {
		t.Fatal("a rule on trial must not sort anything")
	}
	later := now.AddDate(0, 1, 0)
	if _, err := b.Move("o/r", "path:billing/**", Active, "bo", later); err != nil {
		t.Fatal(err)
	}
	act := b.Active("o/r")
	if len(act) != 1 || act[0].Fact != "path:billing/**" || !strings.Contains(act[0].Label, "learned from this repo's history") {
		t.Fatalf("active = %+v", act)
	}
	if _, err := b.Move("o/r", "file:auth.go", Rejected, "bo", later); err != nil {
		t.Fatal(err)
	}
	// a new fit keeps what people decided and doesn't re-suggest rejected rules
	res2 := res
	res2.Rules = append(append([]Rule{}, res.Rules...), Rule{Fact: "path:api/**", N: 30, K: 8, Rate: 0.27, LiftLow: 2})
	if n, _ := b.Suggest(res2, nil, later); n != 1 {
		t.Fatalf("new suggestions on refit = %d, want 1", n)
	}
	// reload from disk
	b2, err := OpenBook(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range b2.List("o/r") {
		got[l.Fact] = l.Status
	}
	want := map[string]string{"path:billing/**": Active, "file:auth.go": Rejected, "path:api/**": Suggested}
	for f, s := range want {
		if got[f] != s {
			t.Errorf("%s: %q, want %q (all: %v)", f, got[f], s, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("rules = %v", got)
	}
	// switching off keeps the trial record's start
	l, _ = b2.Move("o/r", "path:billing/**", Shadow, "cy", later)
	if l.Status != Shadow || !l.ShadowSince.Equal(now) || !l.ActiveSince.IsZero() {
		t.Errorf("stop using: %+v", l)
	}
	if _, err := b2.Move("o/r", "file:auth.go", Shadow, "cy", later); err == nil {
		t.Error("a rejected rule should stay rejected")
	}
}
