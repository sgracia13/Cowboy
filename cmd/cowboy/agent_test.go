package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/mcp"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/store"
)

func gitRepo(t *testing.T) string {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	g := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	w := func(name, body string) {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	g("init", "-q", "-b", "main")
	g("remote", "add", "origin", "git@github.com:acme/app.git")
	w("cowboy.json", `{"careful":{"paths":["billing/**"]},"shortcuts":{"catch_all":false}}`)
	w("pay/a_test.go", "package pay\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n")
	g("add", ".")
	g("commit", "-q", "-m", "start")
	g("checkout", "-q", "-b", "work")
	w("pay/a_test.go", "package pay\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { t.Skip(\"flaky\") }\n")
	w("billing/charge.go", "package billing\n\nfunc Charge() {}\n")
	return dir
}

// call drives the real tools through the MCP server, as an agent would.
func call(t *testing.T, tool string, args map[string]any) map[string]any {
	t.Helper()
	a, _ := json.Marshal(args)
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, a) + "\n"
	var out strings.Builder
	s := &mcp.Server{Name: "cowboy", Version: "test", Tools: agentTools()}
	if err := s.Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var last map[string]any
	for sc.Scan() {
		json.Unmarshal(sc.Bytes(), &last)
	}
	res, _ := last["result"].(map[string]any)
	if res == nil {
		t.Fatalf("%s: %v", tool, last)
	}
	return res
}

func text(res map[string]any) string {
	return res["content"].([]any)[0].(map[string]any)["text"].(string)
}

func TestToolsList(t *testing.T) {
	names := map[string]bool{}
	for _, tl := range agentTools() {
		names[tl.Name] = true
		if tl.Annotations["readOnlyHint"] != true {
			t.Errorf("%s should be marked read-only", tl.Name)
		}
	}
	for _, n := range []string{"check_changes", "repo_rules", "agent_guide", "file_history"} {
		if !names[n] {
			t.Errorf("missing tool %s", n)
		}
	}
}

func TestCheckChangesTool(t *testing.T) {
	dir := gitRepo(t)
	res := call(t, "check_changes", map[string]any{"path": dir, "blast_radius": false})
	if res["isError"] == true {
		t.Fatal(text(res))
	}
	txt := text(res)
	for _, want := range []string{"acme/app", "Careful", "billing/charge.go", "skips in pay/a_test.go"} {
		if !strings.Contains(txt, want) {
			t.Errorf("check_changes lacks %q:\n%s", want, txt)
		}
	}
	if sc, _ := res["structuredContent"].(map[string]any); sc["pen"] != "careful" {
		t.Errorf("structured result: %v", res["structuredContent"])
	}
	if bad := call(t, "check_changes", map[string]any{"path": t.TempDir()}); bad["isError"] != true {
		t.Error("a folder that isn't a checkout should be a tool error")
	}
}

func TestRulesAndGuideTools(t *testing.T) {
	dir := gitRepo(t)
	t.Setenv("DATA", filepath.Join(t.TempDir(), "none", "cowboy.json")) // no history here
	r := text(call(t, "repo_rules", map[string]any{"path": dir}))
	if !strings.Contains(r, "billing/**") || !strings.Contains(r, "cowboy.json on the base branch") {
		t.Errorf("repo_rules:\n%s", r)
	}
	g := text(call(t, "agent_guide", map[string]any{"path": dir}))
	if !strings.Contains(g, "## Working in acme/app") || !strings.Contains(g, "`billing/**`") || strings.Contains(g, "catch-alls that swallow") || !strings.Contains(g, "cowboy check") {
		t.Errorf("agent_guide should follow the repo's rules (catch-all check off):\n%s", g)
	}
}

func TestFileHistory(t *testing.T) {
	dir := gitRepo(t)
	t.Setenv("DATA", filepath.Join(t.TempDir(), "none", "cowboy.json"))
	if txt := text(call(t, "file_history", map[string]any{"path": dir, "file": "billing/charge.go"})); !strings.Contains(txt, "No Cowboy history on this machine") {
		t.Errorf("no data: %s", txt)
	}
	if res := call(t, "file_history", map[string]any{"path": dir}); res["isError"] != true {
		t.Error("file is required")
	}
	// with history: two merges touched billing/, one was reverted
	data := filepath.Join(t.TempDir(), "cowboy.json")
	store.Open(data)
	oc, _ := outcome.Open(filepath.Join(filepath.Dir(data), "outcomes.json"))
	now := time.Now().UTC()
	oc.PutMerge(outcome.Merge{Repo: "acme/app", Number: 7, Title: "charge in cents", MergedAt: now.Add(-40 * 24 * time.Hour), Files: []string{"billing/charge.go"}})
	oc.PutMerge(outcome.Merge{Repo: "acme/app", Number: 8, Title: "rename", MergedAt: now.Add(-20 * 24 * time.Hour), Files: []string{"billing/x.go"}})
	oc.PutMerge(outcome.Merge{Repo: "other/repo", Number: 9, MergedAt: now, Files: []string{"billing/charge.go"}})
	oc.Add(outcome.Outcome{Repo: "acme/app", Number: 7, Kind: outcome.Revert, Status: outcome.Auto, At: now.Add(-38 * 24 * time.Hour), Evidence: "r"})
	t.Setenv("DATA", data)
	res := call(t, "file_history", map[string]any{"path": dir, "file": "billing/charge.go"})
	if txt := text(res); !strings.Contains(txt, "1 merged pull requests touched billing/charge.go; 1 had trouble") || !strings.Contains(txt, "#7 charge in cents") || !strings.Contains(txt, "revert") {
		t.Errorf("file:\n%s", txt)
	}
	if txt := text(call(t, "file_history", map[string]any{"path": dir, "file": "billing/"})); !strings.Contains(txt, "2 merged pull requests touched billing/; 1 had trouble") {
		t.Errorf("folder:\n%s", txt)
	}
}
