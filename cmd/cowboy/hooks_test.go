package main

import "testing"

func TestPRLink(t *testing.T) {
	for _, c := range []struct{ kind, url, want string }{
		{"github", "", "https://github.com/o/r/pull/7"},
		{"github", "https://ghe.acme.dev/api/v3", "https://ghe.acme.dev/o/r/pull/7"},
		{"gitlab", "", "https://gitlab.com/o/r/-/merge_requests/7"},
		{"gitlab", "https://git.acme.dev/", "https://git.acme.dev/o/r/-/merge_requests/7"},
		{"forgejo", "https://code.acme.dev", "https://code.acme.dev/o/r/pulls/7"},
	} {
		if got := prLink(c.kind, c.url)("o/r", 7); got != c.want {
			t.Errorf("%s %s: %s", c.kind, c.url, got)
		}
	}
}
