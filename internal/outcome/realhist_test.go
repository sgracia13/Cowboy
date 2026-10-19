package outcome

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Revert detection on real history: COWBOY_GITLOG=file (see scripts/eval/outcomes).
func TestRealHistory(t *testing.T) {
	p := os.Getenv("COWBOY_GITLOG")
	if p == "" {
		t.Skip()
	}
	b, _ := os.ReadFile(p)
	recs := strings.Split(string(b), "\x1e")
	shaPR := map[string]int{}
	squash := regexp.MustCompile(`\(#(\d+)\)\s*$`)
	titlePR := map[string]int{}
	type c struct{ sha, msg string }
	var cs []c
	for _, r := range recs {
		r = strings.TrimLeft(r, "\n")
		sha, msg, ok := strings.Cut(r, "\x00")
		if !ok {
			continue
		}
		cs = append(cs, c{sha, msg})
		title, _, _ := strings.Cut(msg, "\n")
		if m := squash.FindStringSubmatch(strings.TrimSpace(title)); m != nil {
			n := 0
			for _, ch := range m[1] {
				n = n*10 + int(ch-'0')
			}
			shaPR[sha] = n
			titlePR[strings.ToLower(strings.Join(strings.Fields(squash.ReplaceAllString(strings.TrimSpace(title), "")), " "))] = n
		}
	}
	titled, recognized, mapped, viaSHA, unresolved, viaTitle := 0, 0, 0, 0, 0, 0
	var misses []string
	for _, x := range cs {
		title, _, _ := strings.Cut(x.msg, "\n")
		looks := strings.HasPrefix(strings.TrimSpace(title), "Revert")
		if looks {
			titled++
		}
		refs, ok := ParseRevert(x.msg)
		if !ok {
			if looks && !strings.HasPrefix(title, `Revert "Revert`) {
				misses = append(misses, "not recognized: "+title)
			}
			continue
		}
		recognized++
		got := false
		if len(refs) == 0 {
			if tt := RevertedTitle(x.msg); tt != "" {
				if n := titlePR[strings.ToLower(strings.Join(strings.Fields(tt), " "))]; n > 0 && n != OwnNumber(x.msg) {
					got = true
					viaTitle++
				}
			}
		}
		for _, r := range refs {
			if r.PR > 0 {
				got = true
			} else {
				for full, n := range shaPR {
					if strings.HasPrefix(full, r.SHA) && n > 0 && n != OwnNumber(x.msg) {
						got = true
						viaSHA++
						break
					}
				}
			}
		}
		if got && mapped%25 == 3 {
			t.Logf("sample: %s", title)
			for _, r := range refs {
				target := r.PR
				if target == 0 {
					for full, n := range shaPR {
						if strings.HasPrefix(full, r.SHA) {
							target = n
						}
					}
				}
				for full, n := range shaPR {
					if n == target {
						for _, y := range cs {
							if y.sha == full {
								tt, _, _ := strings.Cut(y.msg, "\n")
								t.Logf("   -> #%d %s", n, tt)
							}
						}
					}
				}
			}
		}
		if got {
			mapped++
		} else {
			unresolved++
			if len(misses) < 40 {
				misses = append(misses, "no PR: "+title)
			}
		}
	}
	t.Logf("commits %d, titles starting Revert %d, recognized %d, traced to a PR %d (%d via SHA, %d via title), not traced %d", len(cs), titled, recognized, mapped, viaSHA, viaTitle, unresolved)
	for i, m := range misses {
		if i < 12 {
			t.Log(m)
		}
	}
}
