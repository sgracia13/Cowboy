package score

import (
	"strings"
	"testing"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/diff"
)

func lines(n int, s string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// Silencers and skips are shown as notes but don't make a PR careful.
func TestNotesDontChangeThePen(t *testing.T) {
	cfg := config.Default()
	files := []diff.File{{Path: "src/a.ts", Status: "modified", Added: []string{"x() // eslint-disable-line"}}}
	s := Measure(analyze.PRMeta{}, files, cfg, nil)
	pen, reasons := Decide(s, cfg)
	if pen != Easy {
		t.Fatalf("pen = %s, want easy", pen)
	}
	found := false
	for _, r := range reasons {
		if r.Effect == "note" && strings.Contains(r.Detail, "silencer") {
			found = true
		}
	}
	if !found {
		t.Fatalf("silencer note missing: %+v", reasons)
	}
	if c := Comment(pen, s, reasons, nil); !strings.Contains(c, "worth a look: adds a warning silencer") {
		t.Fatalf("comment missing note:\n%s", c)
	}
}

// Catch-alls still make a PR careful.
func TestCatchAllIsCareful(t *testing.T) {
	cfg := config.Default()
	files := []diff.File{{Path: "app/a.py", Status: "modified", Added: []string{"    except Exception:", "        pass"}}}
	if pen, _ := Decide(Measure(analyze.PRMeta{}, files, cfg, nil), cfg); pen != Careful {
		t.Fatalf("pen = %s, want careful", pen)
	}
}

// Big PRs get a note, not careful, unless a repo turns size back on.
func TestBigPRGetsANote(t *testing.T) {
	cfg := config.Default()
	files := []diff.File{{Path: "src/big.go", Status: "modified", Added: lines(500, "x := 1")}}
	pen, reasons := Decide(Measure(analyze.PRMeta{}, files, cfg, nil), cfg)
	if pen != Normal {
		t.Fatalf("pen = %s, want normal", pen)
	}
	if !strings.Contains(reasons[len(reasons)-1].Detail+reasonsText(reasons), "too big to review well") {
		t.Fatalf("big-PR note missing: %+v", reasons)
	}
	if err := config.Merge(&cfg, []byte(`{"careful":{"min_source_lines":400}}`)); err != nil {
		t.Fatal(err)
	}
	if pen, _ := Decide(Measure(analyze.PRMeta{}, files, cfg, nil), cfg); pen != Careful {
		t.Fatalf("with min_source_lines set, pen = %s, want careful", pen)
	}
}

func reasonsText(rs []Reason) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.Detail + "\n")
	}
	return b.String()
}
