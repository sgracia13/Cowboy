// Command cowboy-replay runs Cowboy's after-merge engine over real history
// that already happened, instead of waiting 30 days on a live repo. It reads
// PRs that were sorted by the current rules (scripts/eval sort data: pen,
// reasons, patch, merge time) and a local clone of each repo (commits on the
// default branch, release tags), then writes the same outcomes.json and
// data file a live Cowboy would have, and renders the outcomes page.
//
// What history can't give: CI results (so "broke main" can't be found),
// approvals, incidents, and people confirming suggestions. Those parts of the
// page stay empty; everything else is the real engine on real data.
//
//	cowboy-replay -sorted full_sorted_v2.jsonl -patches full.jsonl \
//	  -git getsentry/sentry=/path/sentry.git -out ./replay
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/audit"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/outcome"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/shortcuts"
	"github.com/sgracia13/Cowboy/internal/store"
	"github.com/sgracia13/Cowboy/internal/web"
)

type sorted struct {
	Repo      string         `json:"repo"`
	Number    int            `json:"number"`
	Title     string         `json:"title"`
	Msg       string         `json:"msg"`
	Author    string         `json:"author"`
	Merged    time.Time      `json:"merged"`
	Pen       score.Pen      `json:"pen"`
	Reasons   []score.Reason `json:"reasons"`
	FanIn     int            `json:"fan_in"`
	FanKnown  bool           `json:"fan_in_known"`
	Agent     bool           `json:"agent_label"`
	Bot       bool           `json:"bot"`
	Reverted  bool           `json:"reverted"`
	Shortcuts []struct {
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
	} `json:"shortcuts"`
}

type commit struct {
	sha, msg string
	at       time.Time
}

// gitHost is a git host made from a local clone and the sort data.
type gitHost struct {
	forge.Forge
	merges   map[int]forge.MergeInfo
	patches  map[int]string
	commits  []forge.Commit
	releases []forge.Release
	shaPR    map[string]int
}

func (g *gitHost) MergeInfo(_ string, n int) (forge.MergeInfo, error) {
	m, ok := g.merges[n]
	if !ok {
		return m, fmt.Errorf("#%d: no squash commit found", n)
	}
	return m, nil
}
func (g *gitHost) Commits(_ string, since time.Time) ([]forge.Commit, error) {
	var out []forge.Commit
	for _, c := range g.commits {
		if !c.At.Before(since) {
			out = append(out, c)
		}
	}
	return out, nil
}
func (g *gitHost) Releases(_ string, since time.Time) ([]forge.Release, error) {
	var out []forge.Release
	for _, r := range g.releases {
		if !r.At.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (g *gitHost) MergedSince(string, time.Time) ([]int, error) { return nil, nil }
func (g *gitHost) PRForCommit(_ string, sha string) (int, error) {
	for full, n := range g.shaPR {
		if strings.HasPrefix(full, sha) {
			return n, nil
		}
	}
	return 0, nil
}
func (g *gitHost) GetDiff(_ string, n int) (string, error) { return g.patches[n], nil }
func (g *gitHost) GetFile(string, string, string) (string, error) {
	return "", fmt.Errorf("no file contents in a replay")
}
func (g *gitHost) ChecksOK(string, string) (*bool, error) { return nil, nil } // CI history isn't in git

var squash = regexp.MustCompile(`\(#(\d+)\)\s*$`)
var releaseTag = regexp.MustCompile(`^(v?\d+\.\d+\.\d+|n8n@\d+\.\d+\.\d+)$`)

func gitLog(dir string, since time.Time) []commit {
	cmd := exec.Command("git", "-C", dir, "log", "HEAD", "--since="+since.Format("2006-01-02"), "--format=%H%x00%cI%x00%B%x1e")
	out, err := cmd.Output()
	if err != nil {
		log.Fatalf("git log %s: %v", dir, err)
	}
	var cs []commit
	for _, rec := range strings.Split(string(out), "\x1e") {
		parts := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, parts[1])
		cs = append(cs, commit{parts[0], parts[2], t.UTC()})
	}
	return cs
}

func gitTags(dir string) []forge.Release {
	out, err := exec.Command("git", "-C", dir, "for-each-ref", "refs/tags", "--format=%(refname:short)%00%(creatordate:iso-strict)").Output()
	if err != nil {
		log.Fatalf("git tags %s: %v", dir, err)
	}
	var rs []forge.Release
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		tag, when, ok := strings.Cut(l, "\x00")
		if !ok || !releaseTag.MatchString(tag) {
			continue
		}
		t, err := time.Parse(time.RFC3339, when)
		if err == nil {
			rs = append(rs, forge.Release{Tag: tag, At: t.UTC()})
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].At.Before(rs[j].At) })
	return rs
}

func main() {
	sortedPath := flag.String("sorted", "full_sorted_v2.jsonl", "sorted PRs (pens and reasons from the current rules)")
	patchPath := flag.String("patches", "full.jsonl", "the same PRs with their patches")
	out := flag.String("out", "replay", "where to write data files and pages")
	var gits multi
	flag.Var(&gits, "git", "owner/repo=/path/to/clone (repeat)")
	flag.Parse()

	byRepo := map[string][]sorted{}
	readJSONL(*sortedPath, func(b []byte) {
		var s sorted
		if json.Unmarshal(b, &s) == nil {
			byRepo[s.Repo] = append(byRepo[s.Repo], s)
		}
	})
	patches := map[string]string{}
	readJSONL(*patchPath, func(b []byte) {
		var p struct {
			Repo   string
			Number int
			Patch  string
		}
		if json.Unmarshal(b, &p) == nil {
			patches[fmt.Sprintf("%s#%d", p.Repo, p.Number)] = p.Patch
		}
	})
	agents := config.Default().Agents
	var pages []page
	for _, g := range gits {
		repo, dir, _ := strings.Cut(g, "=")
		prs := byRepo[repo]
		if len(prs) == 0 {
			log.Fatalf("no sorted PRs for %s", repo)
		}
		first := prs[0].Merged
		for _, p := range prs {
			if p.Merged.Before(first) {
				first = p.Merged
			}
		}
		commits := gitLog(dir, first.AddDate(0, 0, -7))
		head := commits[0].at
		h := &gitHost{Forge: forge.NewFake("", ""), merges: map[int]forge.MergeInfo{}, patches: map[int]string{}, shaPR: map[string]int{}, releases: gitTags(dir)}
		for _, c := range commits {
			h.commits = append(h.commits, forge.Commit{SHA: c.sha, Message: c.msg, At: c.at})
			title, _, _ := strings.Cut(c.msg, "\n")
			if m := squash.FindStringSubmatch(strings.TrimSpace(title)); m != nil {
				var n int
				fmt.Sscan(m[1], &n)
				h.shaPR[c.sha] = n
			}
		}
		prSHA := map[int]string{}
		for sha, n := range h.shaPR {
			prSHA[n] = sha
		}

		dirOut := filepath.Join(*out, strings.ReplaceAll(repo, "/", "_"))
		os.RemoveAll(dirOut)
		os.MkdirAll(dirOut, 0o755)
		st, _ := store.Open(filepath.Join(dirOut, "cowboy.json"))
		missing, agentPRs := 0, 0
		for _, p := range prs {
			sha := prSHA[p.Number]
			if sha == "" {
				missing++
				continue
			}
			patch := patches[fmt.Sprintf("%s#%d", repo, p.Number)]
			files := diff.Parse(patch)
			var paths []string
			for _, f := range files {
				paths = append(paths, f.Path)
			}
			h.merges[p.Number] = forge.MergeInfo{SHA: sha, MergedAt: p.Merged.UTC(), Title: p.Title, Files: paths}
			h.patches[p.Number] = patch
			agent := ""
			if p.Agent || p.Bot {
				agent, _ = analyze.DetectAgent(analyze.PRMeta{Author: p.Author, Body: p.Msg, Commits: []string{p.Msg}}, agents)
				if agent == "" {
					agent = "other-agent"
				}
				agentPRs++
			}
			sig := score.Signals{FanIn: p.FanIn, FanInKnown: p.FanKnown}
			for _, s := range p.Shortcuts {
				sig.Shortcuts = append(sig.Shortcuts, shortcutFlag(s.Kind, s.Detail))
			}
			st.Put(pipeline.Decision{Repo: repo, Number: p.Number, Title: p.Title, Body: p.Msg, Agent: agent, Pen: p.Pen, Reasons: p.Reasons,
				Signals: sig, State: "merged", DecidedAt: p.Merged.UTC(), Labels: []string{"cowboy:" + string(p.Pen)}})
		}
		oc, _ := outcome.Open(filepath.Join(dirOut, "outcomes.json"))
		al, _ := audit.Open(filepath.Join(dirOut, "audit.jsonl"))
		a := app.New(h, st, nil)
		a.Audit, a.Outcomes = al, oc
		w := &outcome.Watcher{Forge: h, Decisions: st, State: oc, Now: func() time.Time { return head }, OnEvent: a.OnOutcome,
			Lookback: head.Sub(first) + 30*24*time.Hour, MaxPerRun: 1 << 30}
		start := time.Now()
		summary := w.Check(repo)
		log.Printf("%s: %d PRs (%d agent), %d without a squash commit; %s in %s", repo, len(prs)-missing, agentPRs, missing, summary, time.Since(start).Round(time.Millisecond))

		// cross-check: reverts the engine found vs the earlier study's label
		found := map[int]bool{}
		for _, o := range oc.Outcomes(repo, 0) {
			if o.Kind == outcome.Revert {
				found[o.Number] = true
			}
		}
		both, onlyStudy, onlyEngine := 0, 0, 0
		for _, p := range prs {
			if prSHA[p.Number] == "" {
				continue
			}
			switch {
			case p.Reverted && found[p.Number]:
				both++
			case p.Reverted:
				onlyStudy++
			case found[p.Number]:
				onlyEngine++
			}
		}
		log.Printf("%s reverts: both %d, only the earlier study %d, only the engine %d", repo, both, onlyStudy, onlyEngine)

		rep := outcome.Build(st.List("", ""), oc, head, 30*24*time.Hour, map[string]int{repo: 20})
		for _, pr := range rep.ByPen {
			log.Printf("  %-8s %s", pr.Pen, pr.Rate)
		}
		pages = append(pages, page{repo: repo, file: strings.ReplaceAll(repo, "/", "_") + ".html", html: render(a, st, oc, al, head), rep: rep, prs: len(prs) - missing, agents: agentPRs, from: first, to: head,
			both: both, onlyStudy: onlyStudy, onlyEngine: onlyEngine})
	}
	writeSite(*out, pages)
}

type page struct {
	repo, file, html string
	rep              outcome.Report
	prs, agents      int
	from, to         time.Time
	both, onlyStudy  int
	onlyEngine       int
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func readJSONL(path string, f func([]byte)) {
	fh, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		f(sc.Bytes())
	}
}

// render asks the real dashboard for the outcomes page, as of the end of
// the history.
func render(a *app.App, st *store.Store, oc *outcome.State, al *audit.Log, now time.Time) string {
	s := web.New(a)
	s.Outcomes = &web.OutcomeSettings{State: oc, Window: 30 * 24 * time.Hour, Audit: al, Version: "replay", Now: func() time.Time { return now }}
	mux := http.NewServeMux()
	s.Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/outcomes", nil))
	return rec.Body.String()
}

func shortcutFlag(kind, detail string) shortcuts.Flag {
	return shortcuts.Flag{Kind: kind, Detail: detail}
}

var (
	prLink   = regexp.MustCompile(`href="/pr\?repo=([^&"]+)&(?:amp;)?n=(\d+)"`)
	formTag  = regexp.MustCompile(`(?s)<form[^>]*>.*?</form>`)
	navLink  = regexp.MustCompile(`href="/[a-z]*"`)
	cssLink  = regexp.MustCompile(`<link rel="stylesheet" href="/static/cowboy.css">`)
	iconLink = regexp.MustCompile(`<link rel="icon"[^>]*>`)
)

// static turns a dashboard page into a standalone file: inlined styles, PR
// links to GitHub, and no buttons (nobody can confirm anything in a replay).
func static(html, repo string) string {
	html = cssLink.ReplaceAllString(html, "<style>"+web.CSS()+"</style>")
	html = iconLink.ReplaceAllString(html, "")
	html = prLink.ReplaceAllStringFunc(html, func(m string) string {
		sm := prLink.FindStringSubmatch(m)
		r := strings.ReplaceAll(sm[1], "%2f", "/")
		r = strings.ReplaceAll(r, "%2F", "/")
		return fmt.Sprintf(`href="https://github.com/%s/pull/%s" target="_blank" rel="noopener"`, r, sm[2])
	})
	html = formTag.ReplaceAllString(html, "")
	html = navLink.ReplaceAllString(html, `href="index.html"`)
	// a note at the top, and tables that scroll on a phone
	html = strings.Replace(html, "</style>", ".sig{display:block;overflow-x:auto;max-width:100%}main{padding-left:max(16px,env(safe-area-inset-left,0px));padding-right:max(16px,env(safe-area-inset-right,0px))}.replay-note{background:#111;color:#ffd400;padding:10px max(16px,env(safe-area-inset-left,0px));font-weight:600}.replay-note a{color:#ffd400}</style>", 1)
	html = strings.Replace(html, "<main>", `<div class="replay-note">A replay of `+repo+`'s real history. Buttons are hidden: nobody can confirm anything here. <a href="index.html">All three repos</a></div><main>`, 1)
	return html
}

func writeSite(out string, pages []page) {
	var idx strings.Builder
	idx.WriteString(`<title>After-Merge Replay</title>
<meta name="claude:viewport-fit-sides" content="cover">
<style>` + web.CSS() + `
main{padding-left:max(16px,env(safe-area-inset-left,0px));padding-right:max(16px,env(safe-area-inset-right,0px))}
header.top{padding-left:max(16px,env(safe-area-inset-left,0px));padding-right:max(16px,env(safe-area-inset-right,0px))}
.rep{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,300px),1fr));gap:16px;align-items:start}
.rep .box{margin:0;min-width:0}
.sig{display:block;overflow-x:auto;max-width:100%;font-variant-numeric:tabular-nums}
.rep h2 a{color:inherit}
</style>
<header class="top"><span class="brand">cowboy</span><span class="where">a replay of real history</span></header>
<main>
<section class="hero"><h1>after merge, replayed</h1></section>
<p class="lead">cowboy's after-merge engine, run on pull requests that really merged into three open-source repos between november 2025 and september 2026. each pr was sorted by cowboy's current rules. then the engine read the real default-branch history and release tags, the same way it does on a live repo, and the dashboard drew the outcomes page as of mid-october 2026.</p>
<div class="rep">`)
	for _, p := range pages {
		os.WriteFile(filepath.Join(out, p.file), []byte(static(p.html, p.repo)), 0o644)
		fmt.Fprintf(&idx, `<section class="box"><h2><a href="%s">%s</a></h2><p>%d merged prs, %d by agents</p><table class="sig"><tr><th>pen</th><th>had trouble within 30 days</th></tr>`, p.file, p.repo, p.prs, p.agents)
		for _, r := range p.rep.ByPen {
			fmt.Fprintf(&idx, `<tr><td><b>%s</b></td><td>%s</td></tr>`, r.Pen, r.Rate)
		}
		fmt.Fprintf(&idx, `</table><p class="small">%d suggested fixes waiting for a person. reverts matched an independent earlier count on %d prs; %d only there, %d only here.</p><p><a href="%s">open the outcomes page</a></p></section>`,
			p.rep.Pending, p.both, p.onlyStudy, p.onlyEngine, p.file)
	}
	idx.WriteString(`</div>
<section class="box"><h2>what a replay can't see</h2><ul class="trail">
<li>ci results, so "broke main" can't be found</li>
<li>approvals and incidents, so the review comparison and incident links are empty</li>
<li>people confirming suggestions, so suggested fixes stay pending and don't count. on a live team someone confirms them; in a sample of 21 judged by title, about 7 in 10 looked right</li>
<li>file contents, so code survival isn't measured</li>
<li>every merge: the prs are a random sample of each repo's merges, so some reverts and fixes between unsampled prs are missed</li>
</ul>
<p class="small">n8n reverted one pr in ten months. its trouble shows up as follow-up fixes instead, which is why its rates are near zero until someone confirms them.</p></section>
</main>`)
	os.WriteFile(filepath.Join(out, "index.html"), []byte(idx.String()), 0o644)
}
