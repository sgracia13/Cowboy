package fit

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// history: 900 PRs over 90 days; 1 in 10 touch risky/, and 1 in 4 of those
// had trouble; 1 in 100 of the rest did.
func history() []Example {
	var ex []Example
	for i := 0; i < 900; i++ {
		m := t0.Add(-90*24*time.Hour + time.Duration(i)*2*time.Hour)
		e := Example{Repo: "o/r", Number: i, Merged: m, W: 1}
		if i%10 == 0 {
			e.Facts = []string{"path:risky/**", "file:risky/x.go"}
			if i%40 == 0 {
				e.TroubleAt = m.Add(48 * time.Hour)
			}
		} else {
			e.Facts = []string{fmt.Sprintf("path:calm%d/**", i%7)}
			if i%100 == 1 {
				e.TroubleAt = m.Add(48 * time.Hour)
			}
		}
		ex = append(ex, e)
	}
	return ex
}

func TestFitFindsTheRiskyFolder(t *testing.T) {
	r := Fit("o/r", history(), t0, Defaults())
	if !r.Enough {
		t.Fatalf("not enough: %s", r.Note)
	}
	if len(r.Rules) == 0 || r.Rules[0].Fact != "file:risky/x.go" && r.Rules[0].Fact != "path:risky/**" {
		t.Fatalf("rules = %+v", r.Rules)
	}
	// the file and its folder cover the same PRs: only one is kept
	if len(r.Rules) != 1 {
		t.Errorf("want one rule (the other is redundant), got %+v", r.Rules)
	}
	if r.Share > 0.15 {
		t.Errorf("share %.2f over budget", r.Share)
	}
	got := r.Careful([]string{"path:risky/**", "file:risky/x.go"})
	if len(got) != 1 {
		t.Errorf("careful = %+v", got)
	}
	if len(r.Careful([]string{"path:calm1/**"})) != 0 {
		t.Error("calm PR flagged")
	}
}

func TestTroubleAfterTheFitDateIsUnknown(t *testing.T) {
	ex := history()
	// move every trouble past the fit date: nothing is known yet
	for i := range ex {
		if !ex[i].TroubleAt.IsZero() {
			ex[i].TroubleAt = t0.Add(time.Hour)
		}
	}
	r := Fit("o/r", ex, t0, Defaults())
	if r.Enough || r.Trouble != 0 {
		t.Errorf("trouble after the fit date counted: %+v", r)
	}
}

func TestNotEnoughHistory(t *testing.T) {
	ex := history()[:100]
	r := Fit("o/r", ex, t0, Defaults())
	if r.Enough || len(r.Rules) != 0 || r.Note == "" {
		t.Errorf("want not enough history, got %+v", r)
	}
}

func TestBudgetStopsAdding(t *testing.T) {
	o := Defaults()
	o.MaxShare = 0.05 // risky/ alone is 10%
	r := Fit("o/r", history(), t0, o)
	if len(r.Rules) != 0 {
		t.Errorf("rules past the budget: %+v", r.Rules)
	}
}

func TestDeterministic(t *testing.T) {
	a := Fit("o/r", history(), t0, Defaults())
	for i := 0; i < 20; i++ {
		b := Fit("o/r", history(), t0, Defaults())
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatal("fit changed between runs")
		}
	}
}

func TestCheckFit(t *testing.T) {
	var ex []Sized
	for _, e := range history() {
		ex = append(ex, Sized{Example: e, Lines: 10})
	}
	// a test month after the fit date with the same pattern
	for i := 0; i < 300; i++ {
		m := t0.Add(time.Duration(i) * 2 * time.Hour)
		e := Sized{Example: Example{Repo: "o/r", Number: 10000 + i, Merged: m, W: 1}, Lines: 10 + i%3}
		if i%10 == 0 {
			e.Facts = []string{"path:risky/**", "file:risky/x.go"}
			if i%20 == 0 {
				e.TroubleAt = m.Add(24 * time.Hour)
			}
		}
		ex = append(ex, e)
	}
	r, c := CheckFit("o/r", ex, t0, t0.Add(30*24*time.Hour), Defaults())
	if len(r.Rules) == 0 || c.Trouble == 0 || c.Fitted != c.Trouble || !c.Wins {
		t.Errorf("check = %+v (rules %+v)", c, r.Rules)
	}
}
