// Package app is Cowboy's loop: fetch a PR from the forge, sort it, store the
// decision, and write labels and one comment back. Webhooks, the sweep and
// the web UI all go through here.
package app

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/detect"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/graph"
	"github.com/sgracia13/Cowboy/internal/model"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

const marker = "<!-- cowboy -->"

// Cowboy's own labels. Anything else on a PR is left alone.
var ownLabels = []string{"cowboy:easy", "cowboy:normal", "cowboy:careful", "cowboy:double", "cowboy:stray", "agent:likely"}

// App holds everything the loop needs.
type App struct {
	Forge     forge.Forge
	Store     *store.Store
	RepoPaths map[string]string // repo -> local checkout, for blast radius
	DryRun    bool              // sort and store, but write nothing to the forge
	Now       func() time.Time

	// Model is the optional local model. nil = rules only.
	Model model.Model
	// AsyncNotes runs the model off the sorting path (serve). The demo runs inline.
	AsyncNotes bool

	// Baselines holds each author's history for the undisclosed-agent
	// check. nil turns the check off.
	Baselines *detect.FileBaselines
	notesQ    chan notesJob

	mu      sync.Mutex
	graphs  map[string]*graph.Graph
	locks   map[string]*sync.Mutex
	events  *hub
	skipped map[string]bool
	checks  map[string]checked
}

// New builds an App.
func New(f forge.Forge, s *store.Store, repoPaths map[string]string) *App {
	if repoPaths == nil {
		repoPaths = map[string]string{}
	}
	return &App{Forge: f, Store: s, RepoPaths: repoPaths, Now: time.Now,
		graphs: map[string]*graph.Graph{}, locks: map[string]*sync.Mutex{}}
}

// one PR at a time per repo, so doubles see each other in order
func (a *App) repoLock(repo string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locks[repo] == nil {
		a.locks[repo] = &sync.Mutex{}
	}
	return a.locks[repo]
}

// Graph returns the import graph for a repo, or nil if no local checkout.
func (a *App) Graph(repo string) *graph.Graph {
	a.mu.Lock()
	defer a.mu.Unlock()
	if g, ok := a.graphs[repo]; ok {
		return g
	}
	path, ok := a.RepoPaths[repo]
	if !ok {
		return nil
	}
	g, err := graph.Build(path)
	if err != nil {
		log.Printf("graph %s: %v", repo, err)
		return nil
	}
	a.graphs[repo] = g
	return g
}

// RefreshGraph drops the cached graph (call after the default branch moves).
func (a *App) RefreshGraph(repo string) {
	a.mu.Lock()
	delete(a.graphs, repo)
	a.mu.Unlock()
}

// Config loads the repo's rule book from the default branch. Reading it from
// the PR head would let a PR loosen its own rules.
func (a *App) Config(repo string) config.Config {
	cfg := config.Default()
	for _, name := range []string{"cowboy.json", ".cowboy.json"} {
		body, err := a.Forge.GetFile(repo, name, "")
		if err == nil && body != "" {
			if err := config.Merge(&cfg, []byte(body)); err != nil {
				log.Printf("%s/%s: %v (using defaults)", repo, name, err)
			}
			break
		}
	}
	return cfg
}

// HandlePR fetches, sorts, stores and labels one PR.
// It returns ok=false when the PR is not from an agent.
func (a *App) HandlePR(repo string, n int) (pipeline.Decision, bool, error) {
	l := a.repoLock(repo)
	l.Lock()
	defer l.Unlock()

	meta, state, err := a.Forge.GetPR(repo, n)
	if err != nil {
		return pipeline.Decision{}, false, fmt.Errorf("get PR: %w", err)
	}
	if state != "open" {
		if d, ok := a.Store.Get(repo, n); ok && d.State == "open" {
			a.Closed(repo, n, state == "merged")
		}
		return pipeline.Decision{}, false, nil
	}
	patch, err := a.Forge.GetDiff(repo, n)
	if err != nil {
		return pipeline.Decision{}, false, fmt.Errorf("get diff: %w", err)
	}
	if meta.ChecksOK == nil && meta.HeadSHA != "" {
		meta.ChecksOK, _ = a.Forge.ChecksOK(repo, meta.HeadSHA)
	}
	owners := ""
	for _, p := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS", ".gitea/CODEOWNERS"} {
		if owners, _ = a.Forge.GetFile(repo, p, ""); owners != "" {
			break
		}
	}
	return a.sort(meta, patch, owners)
}

type notesJob struct {
	d     pipeline.Decision
	files []diff.File
}

// StartNotes starts the background worker for model notes. A slow model
// (CPU-only box) then never delays sorting: labels land first, notes follow.
func (a *App) StartNotes() {
	if a.Model == nil || a.notesQ != nil {
		return
	}
	a.AsyncNotes = true
	a.notesQ = make(chan notesJob, 128)
	go func() {
		for j := range a.notesQ {
			a.addNotes(j.d, j.files)
		}
	}()
}

func (a *App) queueNotes(d pipeline.Decision, files []diff.File) {
	if a.Model == nil || d.Notes != nil {
		return
	}
	if a.AsyncNotes && a.notesQ != nil {
		select {
		case a.notesQ <- notesJob{d, files}:
		default:
			log.Printf("%s#%d: model busy, skipped notes", d.Repo, d.Number)
		}
		return
	}
	a.addNotes(d, files)
}

func (a *App) addNotes(d pipeline.Decision, files []diff.File) {
	n := a.Model.Notes(model.Ask{Repo: d.Repo, Number: d.Number, Title: d.Title, Body: d.Body, Files: files, HeadSHA: d.HeadSHA})
	cur, ok := a.Store.Get(d.Repo, d.Number)
	if !ok || cur.HeadSHA != d.HeadSHA {
		return // a newer push arrived; its own notes are coming
	}
	cur.Notes = &n
	a.Store.Put(cur)
	if n.Error != "" {
		log.Printf("%s#%d model: %s", d.Repo, d.Number, n.Error)
	} else {
		log.Printf("%s#%d model notes in %.1fs (on task: %s)", d.Repo, d.Number, n.Seconds, onTask(n.OnTask))
	}
	if n.Error == "" {
		a.publish("notes", d.Repo, d.Number, "", "#%d local model notes added (on task: %s)", d.Number, onTask(n.OnTask))
	}
	if !a.DryRun {
		a.Forge.UpsertComment(d.Repo, d.Number, marker, pipeline.FullComment(cur))
	}
}

func onTask(b *bool) string {
	if b == nil {
		return "unclear"
	}
	if *b {
		return "yes"
	}
	return "no"
}

// ModelName is shown in the UI.
func (a *App) ModelName() string {
	if a.Model == nil {
		return ""
	}
	return a.Model.Name()
}

// ScoreLocal sorts a PR given directly (used by `cowboy score`); no forge writes.
func (a *App) ScoreLocal(meta analyze.PRMeta, patch, owners string, cfg config.Config) (pipeline.Decision, bool) {
	in := pipeline.Input{Meta: meta, Files: diff.Parse(patch), Config: cfg,
		Graph: a.Graph(meta.Repo), Codeowners: owners, OpenPRs: a.Store.List(meta.Repo, "open"), Now: a.Now().UTC()}
	return pipeline.Run(in)
}

// classify runs the pipeline, first checking undisclosed PRs for an agent.
func (a *App) classify(meta analyze.PRMeta, patch, owners string, cfg config.Config) (pipeline.Decision, *detect.Assessment, bool) {
	files := diff.Parse(patch)
	in := pipeline.Input{Meta: meta, Files: files, Config: cfg,
		Graph: a.Graph(meta.Repo), Codeowners: owners, OpenPRs: a.Store.List(meta.Repo, "open"), Now: a.Now().UTC(),
		SortAll: cfg.Detect.SortAll}
	var as *detect.Assessment
	if agent, _ := analyze.DetectAgent(meta, lowerKeys(cfg.Agents)); agent == "" {
		old, had := a.Store.Get(meta.Repo, meta.Number)
		cleared := had && old.AgentCleared != ""
		if ch, ok := a.detectAgent(meta, files, cfg); ok {
			as = &ch.a
			if ch.a.Label == "likely agent" && !cleared {
				name := detect.GuessAgent(ch.m)
				if name == "" {
					name = "unknown agent"
				} else {
					name += "?"
				}
				in.AgentOverride = name
				in.AgentHow = fmt.Sprintf("not disclosed; agent check %d, range %d-%d", ch.a.Score, ch.a.Low, ch.a.High)
				in.AgentLabel = "agent:likely"
			}
		}
		if cleared {
			in.SortAll = true // a person said it's human work; keep it in the corral
		}
	}
	d, ok := pipeline.Run(in)
	if ok {
		d.Detection = as
	}
	return d, as, ok
}

func (a *App) sort(meta analyze.PRMeta, patch, owners string) (pipeline.Decision, bool, error) {
	cfg := a.Config(meta.Repo)
	d, as, ok := a.classify(meta, patch, owners, cfg)
	if !ok {
		if !a.isSkipped(meta.Repo, meta.Number, meta.HeadSHA) {
			switch {
			case as != nil && as.Abstained:
				a.publish("skipped", meta.Repo, meta.Number, "", "#%d has no agent marks and too little to check; left alone", meta.Number)
			case as != nil:
				a.publish("skipped", meta.Repo, meta.Number, "", "#%d checked for an undisclosed agent: looks human (%d), left alone", meta.Number, as.Score)
			default:
				a.publish("skipped", meta.Repo, meta.Number, "", "#%d was written by a person, left alone", meta.Number)
			}
		}
		a.skip(meta.Repo, meta.Number, meta.HeadSHA)
		return d, false, nil
	}
	old, had := a.Store.Get(meta.Repo, meta.Number)
	if had && old.Corrected != "" && old.HeadSHA == d.HeadSHA {
		d.Corrected = old.Corrected
		d.Labels[0] = "cowboy:" + string(d.Corrected)
	}
	if had && old.Stray {
		d.Stray, d.Labels = true, append(d.Labels, "cowboy:stray")
	}
	if had && old.Notes != nil && old.HeadSHA == d.HeadSHA {
		d.Notes = old.Notes
	}
	if err := a.Store.Put(d); err != nil {
		return d, true, err
	}
	if err := a.write(d, !had || old.Pen != d.Pen); err != nil {
		return d, true, err
	}
	verb := "sorted"
	if had && old.HeadSHA != d.HeadSHA {
		verb = "re-sorted after new commits"
	} else if had {
		verb = "re-sorted"
	}
	pen := d.Pen
	if d.Corrected != "" {
		pen = d.Corrected
	}
	who := d.Agent
	if who == "" {
		who = "no agent"
	}
	if d.Detection != nil && d.Detection.Label == "likely agent" && d.AgentCleared == "" {
		a.publish("caught", d.Repo, d.Number, string(pen), "#%d has no agent label but looks agent-written (%d): %s %s", d.Number, d.Detection.Score, verb, pen)
	} else {
		a.publish("sorted", d.Repo, d.Number, string(pen), "#%d %s %s (%s)", d.Number, verb, pen, who)
	}
	a.queueNotes(d, diff.Parse(patch))
	if got, ok := a.Store.Get(d.Repo, d.Number); ok {
		d = got
	}
	return d, true, nil
}

// write puts labels and the comment on the forge. Cowboy never merges,
// closes, approves or leaves review comments.
func (a *App) write(d pipeline.Decision, penChanged bool) error {
	if a.DryRun {
		return nil
	}
	if err := a.Forge.SetLabels(d.Repo, d.Number, d.Labels, minus(ownLabels, d.Labels)); err != nil {
		return fmt.Errorf("labels: %w", err)
	}
	if err := a.Forge.UpsertComment(d.Repo, d.Number, marker, pipeline.FullComment(d)); err != nil {
		return fmt.Errorf("comment: %w", err)
	}
	pen := d.Pen
	if d.Corrected != "" {
		pen = d.Corrected
	}
	if pen == score.Careful && penChanged && len(d.Signals.Owners) > 0 {
		if err := a.Forge.RequestReview(d.Repo, d.Number, d.Signals.Owners); err != nil {
			log.Printf("review request %s#%d: %v", d.Repo, d.Number, err)
		}
	}
	return nil
}

// Correct records a human moving a PR to another pen and relabels it.
func (a *App) Correct(repo string, n int, to score.Pen, by string) error {
	d, err := a.Store.Correct(repo, n, to, by)
	if err != nil {
		return err
	}
	d.Labels[0] = "cowboy:" + string(to)
	a.Store.Put(d)
	a.publish("moved", repo, n, string(to), "#%d moved to %s by %s", n, to, by)
	if a.DryRun {
		return nil
	}
	return a.Forge.SetLabels(repo, n, d.Labels, minus(ownLabels, d.Labels))
}

// Closed marks a PR merged or closed.
func (a *App) Closed(repo string, n int, merged bool) {
	state := "closed"
	if merged {
		state = "merged"
	}
	d, had := a.Store.Get(repo, n)
	a.Store.SetState(repo, n, state)
	a.learn(repo, n)
	if had && d.State == "open" {
		a.publish("closed", repo, n, "", "#%d %s", n, state)
	}
}

// Sweep checks open PRs for strays. Run it on a timer.
func (a *App) Sweep() (nudged []string) {
	now := a.Now().UTC()
	for _, repo := range a.Store.Repos() {
		cfg := a.Config(repo)
		for _, d := range a.Store.List(repo, "open") {
			act, err := a.Forge.Activity(repo, d.Number)
			if err != nil {
				continue
			}
			was := d.Stray
			nudge, archive := pipeline.StrayCheck(&d, cfg, act.LastChangesRequested, act.LastCommit, now)
			if d.Stray == was {
				continue
			}
			if !d.Stray {
				d.Labels = minus(d.Labels, []string{"cowboy:stray"})
			}
			a.Store.Put(d)
			if d.Stray {
				a.publish("stray", repo, d.Number, "", "#%d is a stray: no commits since changes were requested", d.Number)
			}
			if nudge {
				msg := fmt.Sprintf("%s#%d stray: no new commits since changes were requested", repo, d.Number)
				if archive {
					msg += " (consider closing)"
				}
				nudged = append(nudged, msg)
			}
			if !a.DryRun {
				a.Forge.SetLabels(repo, d.Number, d.Labels, minus(ownLabels, d.Labels))
			}
		}
	}
	return nudged
}

// AgentTally is one row of the Monday tally.
type AgentTally struct {
	Agent                                      string
	Total, Easy, Normal, Careful               int
	Doubles, Strays, Merged, Closed, Corrected int
	Shortcuts                                  int
}

// Tally counts decisions per agent since a time.
func (a *App) Tally(since time.Time) []AgentTally {
	rows := map[string]*AgentTally{}
	for _, d := range a.Store.List("", "") {
		if d.DecidedAt.Before(since) {
			continue
		}
		r := rows[d.Agent]
		if r == nil {
			r = &AgentTally{Agent: d.Agent}
			rows[d.Agent] = r
		}
		r.Total++
		switch d.Pen {
		case score.Easy:
			r.Easy++
		case score.Normal:
			r.Normal++
		case score.Careful:
			r.Careful++
		}
		if d.DoubleOf != 0 {
			r.Doubles++
		}
		if d.Stray {
			r.Strays++
		}
		if d.Corrected != "" {
			r.Corrected++
		}
		if len(d.Signals.Shortcuts) > 0 {
			r.Shortcuts++
		}
		switch d.State {
		case "merged":
			r.Merged++
		case "closed":
			r.Closed++
		}
	}
	var out []AgentTally
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

// TallyMarkdown renders the tally for chat or email.
func TallyMarkdown(rows []AgentTally) string {
	var b strings.Builder
	b.WriteString("## cowboy tally\n\n| agent | prs | easy | normal | careful | doubles | strays | shortcuts | re-penned |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d |\n", r.Agent, r.Total, r.Easy, r.Normal, r.Careful, r.Doubles, r.Strays, r.Shortcuts, r.Corrected)
	}
	return b.String()
}

func minus(xs, drop []string) []string {
	m := map[string]bool{}
	for _, d := range drop {
		m[d] = true
	}
	var out []string
	for _, x := range xs {
		if !m[x] {
			out = append(out, x)
		}
	}
	return out
}
