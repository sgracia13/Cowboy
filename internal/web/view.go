package web

import (
	"fmt"
	"math"
	"strings"

	"github.com/sgracia13/Cowboy/internal/evaluate"
	"github.com/sgracia13/Cowboy/internal/outcome"
)

// answerView is the top of the outcomes page: the verdict in one plain
// sentence, a few supporting lines, and the counts behind it.
type answerView struct {
	Say     string
	Also    []string
	Rated   int // merged PRs whose window has passed
	Trouble int // of those, PRs that had trouble
	Recent  int // merged PRs still inside the window
	Waiting int // guesses waiting for a person
}

func times2(f float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", f), "0"), ".0") + "×"
}

func makeAnswer(r outcome.Report, c evaluate.Result, waiting int) answerView {
	a := answerView{Rated: r.Matured, Recent: r.Recent, Waiting: waiting}
	for _, p := range r.ByPen {
		a.Trouble += p.Rate.Bad
	}
	switch {
	case r.Matured == 0:
		a.Say = "Nothing to rate yet."
		a.Also = append(a.Also, fmt.Sprintf("A pull request is rated once it has been merged for %d days, so a quiet start doesn't look cleaner than it is.", r.WindowDays))
		if r.Recent > 0 {
			a.Also = append(a.Also, fmt.Sprintf("%d merged pull requests are still inside that window.", r.Recent))
		}
		return a
	case len(c.Counted) == 0:
		most, where := 0, ""
		for _, x := range c.Repos {
			if x.BadCount > most {
				most, where = x.BadCount, x.Repo
			}
		}
		a.Say = "Not enough trouble yet to judge the rules."
		line := fmt.Sprintf("A fair comparison needs at least %d pull requests with trouble in a repo.", evaluate.MinEvents)
		if where != "" {
			line += fmt.Sprintf(" The most so far is %d, in %s.", most, where)
		}
		a.Also = append(a.Also, line, "Until then, any difference between the pens could be luck, so this page doesn't claim one.")
		return a
	}
	lift := c.Lift.Value
	switch c.Verdict["chance"] {
	case evaluate.Outperformed:
		a.Say = fmt.Sprintf("Careful pull requests had trouble %s as often as average. That's more than chance would explain.", times2(lift))
	case evaluate.Worse:
		a.Say = fmt.Sprintf("Careful pull requests had trouble less often than average (%s). The careful pen isn't finding the risky ones.", times2(lift))
	default:
		a.Say = fmt.Sprintf("Careful pull requests had trouble %s as often as average. That's not enough to say the rules beat a random pick.", times2(lift))
	}
	size, files := c.Verdict["size"] == evaluate.Outperformed, c.Verdict["files"] == evaluate.Outperformed
	switch {
	case size && files:
		a.Also = append(a.Also, "They also caught more than picking the largest pull requests, or the ones touching the most files.")
	case size:
		a.Also = append(a.Also, "They caught more than picking the largest pull requests, but not more than picking the ones touching the most files.")
	case files:
		a.Also = append(a.Also, "They caught more than picking the pull requests touching the most files, but not more than picking the largest.")
	default:
		a.Also = append(a.Also, "They didn't catch more than simply picking the largest pull requests, or the ones touching the most files.")
	}
	switch c.Verdict["safe"] {
	case evaluate.Worse:
		a.Also = append(a.Also, "The easy pen had more trouble than the same share of the smallest pull requests.")
	case evaluate.Outperformed:
		a.Also = append(a.Also, "The easy pen had less trouble than the same share of the smallest pull requests.")
	case evaluate.NoAdvantage:
		a.Also = append(a.Also, "The easy pen was as safe as the same share of the smallest pull requests.")
	}
	return a
}

// barView is one method in the "same budget" comparison.
type barView struct {
	Label string
	Value float64 // share of PRs with trouble it caught
	Width float64 // percent of the chart's width
	Us    bool
}

type barsView struct {
	Bars []barView
	Max  float64 // the axis end
}

// niceMax is a round axis end a little above x.
func niceMax(x float64) float64 {
	for _, s := range []float64{0.01, 0.02, 0.03, 0.05, 0.08, 0.1, 0.15, 0.2, 0.25, 0.3, 0.4, 0.5, 0.6, 0.8, 1} {
		if s >= x*1.05 {
			return s
		}
	}
	return 1
}

func makeBars(c evaluate.Result) barsView {
	rows := []barView{
		{Label: "Cowboy's careful pen", Value: c.Recall["rule"], Us: true},
		{Label: "Largest first", Value: c.Recall["size"]},
		{Label: "Most files first", Value: c.Recall["spread"]},
		{Label: "Picked at random", Value: c.Recall["chance"]},
	}
	top := 0.0
	for _, r := range rows {
		top = math.Max(top, r.Value)
	}
	v := barsView{Max: niceMax(top)}
	for _, r := range rows {
		r.Width = 100 * r.Value / v.Max
		v.Bars = append(v.Bars, r)
	}
	return v
}

// dotView is a rate with its likely range, placed on a shared axis.
type dotView struct {
	Label            string
	Rate             outcome.Rate
	Mark, Low, Width float64 // percent positions
}

type dotsView struct {
	Rows   []dotView
	Max    float64
	Avg    float64 // the overall rate, drawn as a dashed line
	AvgPos float64
}

func makeDots(labels []string, rates []outcome.Rate, avg float64) dotsView {
	top := avg
	for _, r := range rates {
		if r.N > 0 {
			top = math.Max(top, r.High)
		}
	}
	v := dotsView{Max: niceMax(top), Avg: avg}
	pos := func(x float64) float64 { return math.Min(100, 100*x/v.Max) }
	v.AvgPos = pos(avg)
	for i, r := range rates {
		d := dotView{Label: labels[i], Rate: r}
		if r.N > 0 {
			d.Mark, d.Low = pos(r.P), pos(r.Low)
			d.Width = pos(r.High) - d.Low
		}
		v.Rows = append(v.Rows, d)
	}
	return v
}

// pctRate formats a rate for a reader: one decimal under 10%.
func pctRate(f float64) string {
	if f > 0 && f < 0.1 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", f*100), ".0") + "%"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

// capFirst capitalizes the first letter (labels stored lowercase).
func capFirst(s string) string {
	if s == "" {
		return s
	}
	if strings.HasPrefix(s, "ci ") {
		return "CI " + s[3:]
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// checkLabel names a reason's check for people.
func checkLabel(c string) string {
	switch c {
	case "sensitive":
		return "Sensitive path"
	case "blast_radius":
		return "Blast radius"
	case "shortcut":
		return "Shortcut"
	case "checks":
		return "Checks"
	case "file_class":
		return "File types"
	case "size":
		return "Size"
	case "fitted":
		return "Fitted rule"
	}
	return capFirst(strings.ReplaceAll(c, "_", " "))
}

// shortcutLabel names a shortcut kind for people.
func shortcutLabel(k string) string {
	switch k {
	case "catch_all":
		return "Catch-all"
	case "ci_loosened":
		return "CI loosened"
	case "skips":
		return "Skipped tests"
	case "special_case_comments":
		return "Test-only special case"
	case "suppressions":
		return "Warning silencer"
	case "tests_removed":
		return "Tests removed"
	}
	return capFirst(strings.ReplaceAll(k, "_", " "))
}
