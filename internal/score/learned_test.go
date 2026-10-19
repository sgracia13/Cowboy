package score

import (
	"testing"

	"github.com/sgracia13/Cowboy/internal/config"
)

// A promoted learned rule adds to the default rules; it doesn't replace them.
func TestLearnedRulesAddToDefaults(t *testing.T) {
	cfg := config.Default()
	cfg.Learned = []config.FittedRule{{Fact: "path:billing/**", Label: "touches billing/**: learned"}}
	small := Signals{Files: 1, LinesChanged: 5, Dirs: 1, Classes: map[string]int{"source": 1}, Paths: []string{"billing/charge.go"}}
	pen, reasons := Decide(small, cfg)
	if pen != Careful {
		t.Fatalf("a PR matching a learned rule should be careful, got %s", pen)
	}
	found := false
	for _, r := range reasons {
		found = found || (r.Check == "learned" && r.Effect == "careful")
	}
	if !found {
		t.Errorf("no learned reason: %+v", reasons)
	}
	other := small
	other.Paths = []string{"web/page.go"}
	if pen, _ := Decide(other, cfg); pen == Careful {
		t.Error("a PR the learned rule doesn't match shouldn't be careful")
	}
	// the default sensitive-path trigger still sends to careful
	sens := other
	sens.Sensitive = []string{"web/page.go (web/**)"}
	if pen, _ := Decide(sens, cfg); pen != Careful {
		t.Error("default triggers must keep working next to learned rules")
	}
}
