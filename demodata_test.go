package cowboy

import (
	"io/fs"
	"testing"
)

// The demo runs from the copy built into the binary, so it must hold the
// sample pull requests, the sample repo and the history.
func TestDemoDataIsBuiltIn(t *testing.T) {
	for _, p := range []string{"testdata/prs", "testdata/acme", "testdata/history"} {
		if _, err := fs.Stat(Demo, p); err != nil {
			t.Errorf("%s missing from the built-in demo data: %v", p, err)
		}
	}
}
