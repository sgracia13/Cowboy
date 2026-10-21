package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgracia13/Cowboy/internal/score"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo: main has cowboy.json (auth/** is careful) and a small app; the
// work branch is checked out.
func repo(t *testing.T) string {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "cowboy.json", `{"careful":{"paths":["auth/**"]}}`)
	write(t, dir, "lib/a.go", "package lib\n\nfunc A() int { return 1 }\n")
	write(t, dir, "lib/a_test.go", "package lib\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "start")
	run(t, dir, "checkout", "-q", "-b", "work")
	return dir
}

func TestNothingChanged(t *testing.T) {
	dir := repo(t)
	r, err := Run(Options{Dir: dir, Base: "main", NoGraph: true})
	if err != nil || !r.Empty || !strings.Contains(r.Text(), "nothing to check") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestSmallChangeIsEasy(t *testing.T) {
	dir := repo(t)
	write(t, dir, "lib/a.go", "package lib\n\n// A is one.\nfunc A() int { return 1 }\n")
	r, err := Run(Options{Dir: dir, Base: "main", NoGraph: true})
	if err != nil || r.Pen != score.Easy || len(r.Shortcuts) != 0 {
		t.Fatalf("pen %s shortcuts %v err %v", r.Pen, r.Shortcuts, err)
	}
}

// Committed and uncommitted changes and new untracked files all count, a
// skipped test is flagged, and a sensitive path makes it careful.
func TestShortcutsAndCareful(t *testing.T) {
	dir := repo(t)
	write(t, dir, "auth/session.go", "package auth\n\nfunc Login() bool { return true }\n") // untracked
	write(t, dir, "lib/a_test.go", "package lib\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tt.Skip(\"later\")\n\tif A() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	run(t, dir, "commit", "-q", "-am", "skip it")
	write(t, dir, "lib/a.go", "package lib\n\nfunc A() int { return 2 }\n") // uncommitted
	r, err := Run(Options{Dir: dir, Base: "main", NoGraph: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Pen != score.Careful || r.Files != 3 {
		t.Errorf("pen %s, files %d: %+v", r.Pen, r.Files, r)
	}
	found := false
	for _, s := range r.Shortcuts {
		if s.Kind == "skips" && s.File == "lib/a_test.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("the skipped test wasn't flagged: %+v", r.Shortcuts)
	}
	txt := r.Text()
	if strings.Count(txt, "t.Skip") != 1 {
		t.Errorf("each shortcut should be listed once:\n%s", txt)
	}
	for _, want := range []string{"Careful", "auth/session.go", "skips in lib/a_test.go", "cowboy.json on the base branch"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
}

// A change can't loosen its own rules: cowboy.json is read from the base.
func TestRulesComeFromTheBase(t *testing.T) {
	dir := repo(t)
	write(t, dir, "cowboy.json", `{"careful":{"paths":[]}}`)
	write(t, dir, "auth/x.go", "package auth\n")
	r, err := Run(Options{Dir: dir, Base: "main", NoGraph: true})
	if err != nil || r.Pen != score.Careful {
		t.Fatalf("loosening cowboy.json in the change itself must not count: %s %v", r.Pen, err)
	}
}

func TestNotACheckout(t *testing.T) {
	if _, err := Run(Options{Dir: t.TempDir()}); err == nil {
		t.Error("want an error outside a git checkout")
	}
}
