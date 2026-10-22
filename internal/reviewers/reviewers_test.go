package reviewers

import (
	"strings"
	"testing"
)

func TestName(t *testing.T) {
	for login, want := range map[string]string{
		"cursor[bot]": "Cursor Bugbot", "greptile-apps[bot]": "Greptile", "coderabbitai[bot]": "CodeRabbit",
		"Copilot": "Copilot", "copilot-pull-request-reviewer[bot]": "Copilot", "vercel[bot]": "Vercel Agent",
		"github-actions[bot]": "", "dependabot[bot]": "", "codecov[bot]": "", "maria": "", "cursoragent": "",
	} {
		if got := Name(login); got != want {
			t.Errorf("%s: %q, want %q", login, got, want)
		}
	}
	Add("acme-review[bot]=Acme review, other-bot[bot]")
	if Name("acme-review[bot]") != "Acme review" || Name("Other-Bot[bot]") != "other-bot" {
		t.Errorf("extra reviewers: %q %q", Name("acme-review[bot]"), Name("other-bot[bot]"))
	}
}

func TestSummary(t *testing.T) {
	in := "_⚠️ Potential issue_ | _🔴 Critical_\n\n**Three separate events table scans** when the pre-filter is active.\n\n<details><summary>Prompt for AI</summary>long hidden text</details>\n\n```suggestion\ncode here\n```\n![badge](https://x/y.svg) See [the docs](https://example.com)."
	got := Summary(in, 240)
	for _, bad := range []string{"details", "hidden", "code here", "badge", "**", "https://", "Potential issue"} {
		if strings.Contains(got, bad) {
			t.Errorf("summary kept %q: %q", bad, got)
		}
	}
	if !strings.HasPrefix(got, "Critical") && !strings.Contains(got, "Three separate events table scans when the pre-filter is active.") {
		t.Errorf("summary lost the point: %q", got)
	}
	for in, want := range map[string]string{
		"**Bug:** nil map write":              "nil map write",
		"Issue with retries: they never stop": "Issue with retries: they never stop",
		"🟡 Nit · Low-level · cast is a bug":   "Nit · Low-level · cast is a bug",
	} {
		if got := Summary(in, 240); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	long := Summary(strings.Repeat("word ", 200), 50)
	if len([]rune(long)) > 51 || !strings.HasSuffix(long, "…") {
		t.Errorf("not cut: %q", long)
	}
}
