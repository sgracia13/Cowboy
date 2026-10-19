// Command cowboy sorts AI-agent pull requests into easy, normal and careful.
//
//	cowboy demo                     sample ranch on http://localhost:8080, no setup
//	cowboy serve                    real server (env vars below)
//	cowboy score --diff f --meta m  sort one PR from files and print why
//	cowboy tally                    print this week's tally as markdown
//	cowboy sign --secret s file     print the signature header for a webhook body
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works in the slim Docker image

	"github.com/sgracia13/Cowboy/internal/analyze"
	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/config"
	"github.com/sgracia13/Cowboy/internal/detect"
	"github.com/sgracia13/Cowboy/internal/diff"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/model"
	"github.com/sgracia13/Cowboy/internal/pipeline"
	"github.com/sgracia13/Cowboy/internal/report"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
	"github.com/sgracia13/Cowboy/internal/web"
	"github.com/sgracia13/Cowboy/internal/webhook"
)

// tallyNote says where the weekly tally goes, for the dashboard.
var tallyNote string

// ghSetup is the "Connect GitHub" flow (GitHub App mode only), and hooks
// the webhook handler, so finishing setup can set the new app's secret.
var (
	ghSetup *web.GitHubSetup
	hooks   *webhook.Handler
)

// version is set at release time (-ldflags "-X main.version=v0.1.0").
var version = "dev"

const usage = `cowboy: sorts ai-agent pull requests into easy, normal and careful.

  cowboy demo                      run a sample ranch on http://localhost:8080
  cowboy serve                     run for real (see env below)
  cowboy score --diff F --meta M   sort one pr from files and print why
  cowboy tally [--send]            print this week's tally, or send it now
  cowboy sign --secret S FILE      print a webhook signature for FILE
  cowboy check-model               check the local model server answers
  cowboy version                   print the version

env for serve:
  on github, leave FORGE_TOKEN empty and open the dashboard: "create the github app"
  sets everything up (PUBLIC_URL = the https address github can reach, for webhooks).

  FORGE           github | gitea | forgejo
  FORGE_URL       base url (required for gitea/forgejo; github enterprise api url)
  FORGE_TOKEN     token with pr read + label/comment write
  WEBHOOK_SECRET  shared secret for webhook signatures
  UI_PASSWORD     dashboard password (or AUTH_PROXY_HEADER behind an SSO proxy)
  REPO_PATHS      optional "owner/repo=/path/to/checkout,...": read blast radius from a local copy
                  (otherwise cowboy downloads the default branch from the git host)
  BLAST_RADIUS    off = don't download repos for blast radius
  BLAST_RADIUS_MAX_MB  biggest archive to download (default 300)
  REPOS           optional "owner/repo,..." to sort all open prs at start
  DATA            store file (default ./data/cowboy.json)
  ADDR            listen address (default :8080)
  DRY_RUN         1 = sort and store, never write to the forge

optional weekly tally (sent Mondays at 09:00 server time by default):
  TALLY_SLACK_WEBHOOK  a Slack incoming webhook (or Mattermost, or any {"text": ...} webhook)
  TALLY_EMAIL_TO       comma-separated addresses; needs SMTP_HOST, SMTP_PORT (587),
                       SMTP_USER, SMTP_PASSWORD, SMTP_FROM
  TALLY_DAY            e.g. friday      TALLY_HOUR  0-23      TZ  e.g. Europe/Berlin
  PUBLIC_URL           your dashboard address, linked from the post

optional local model (any OpenAI-compatible server: ollama, vllm, llama.cpp, lm studio):
  MODEL_URL       e.g. http://localhost:11434
  MODEL_NAME      e.g. qwen2.5-coder:7b
  MODEL_KEY       only if your server wants one
  with no MODEL_URL, cowboy sorts with rules only. the model never changes the pen.
`

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "demo":
		demo(os.Args[2:])
	case "serve":
		serve()
	case "score":
		scoreCmd(os.Args[2:])
	case "tally":
		tallyCmd(os.Args[2:])
	case "check-model":
		checkModel()
	case "version", "--version", "-v":
		fmt.Println("cowboy", version)
	case "sign":
		signCmd(os.Args[2:])
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parsePaths(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if repo, path, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			m[repo] = path
		}
	}
	return m
}

// run wires webhooks, the worker, the sweeps and the UI, then serves.
// start runs once in the background with the webhook queue (the demo uses
// it to play PRs in as if they were arriving).
func run(a *app.App, addr, secret string, catchUp time.Duration, auth web.Auth, start func(queue chan<- webhook.Job)) {
	queue := make(chan webhook.Job, 256)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { // the worker; on stop it finishes what's queued, then exits
		defer close(done)
		for {
			select {
			case j := <-queue:
				handleJob(a, j)
			case <-stop:
				for {
					select {
					case j := <-queue:
						handleJob(a, j)
					default:
						return
					}
				}
			}
		}
	}()
	go safely("startup", func() {
		if start != nil {
			start(queue)
		}
	})
	go func() { // catch-up sweep: missed webhooks, missed pushes, missed closes
		for range time.Tick(catchUp) {
			safely("catch-up sweep", func() {
				for _, m := range a.Reconcile() {
					log.Print(m)
				}
			})
		}
	}()
	go func() { // stray sweep
		for range time.Tick(time.Hour) {
			safely("stray sweep", func() {
				for _, m := range a.Sweep() {
					log.Print(m)
				}
			})
		}
	}()

	mux := http.NewServeMux()
	h := &webhook.Handler{Secret: secret, Queue: queue}
	hooks = h
	mux.HandleFunc("/webhook/github", h.GitHub)
	mux.HandleFunc("/webhook/gitea", h.Gitea)
	mux.HandleFunc("/webhook/forgejo", h.Gitea)
	ui := web.New(a)
	ui.SignOut = auth.Password != "" && auth.ProxyHeader == ""
	ui.TallyNote = tallyNote
	ui.Setup = ghSetup
	ui.Routes(mux)
	base, cancelBase := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:              addr,
		BaseContext:       func(net.Listener) context.Context { return base },
		Handler:           auth.Wrap(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// no WriteTimeout: /events is a long-lived stream
		MaxHeaderBytes: 64 << 10,
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Print("shutting down: finishing queued work")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cancelBase()      // ends open live streams (dashboard tabs reconnect later)
		srv.Shutdown(ctx) // stop taking webhooks, wait for ones in flight
		close(stop)
		select {
		case <-done:
		case <-ctx.Done():
			log.Print("shutdown: gave up waiting; the catch-up sweep will finish the rest on next start")
		}
		os.Exit(0)
	}()
	log.Printf("cowboy listening on %s (catch-up sweep every %s)", addr, catchUp)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	select {} // Shutdown in progress: the signal goroutine exits the process
}

// trackRepos follows the GitHub App being installed on, or removed from, repos.
func trackRepos(a *app.App, add, remove []string) {
	gh, _ := a.Forge.(*forge.GitHub)
	for _, r := range remove {
		a.Store.RemoveRepo(r)
		if gh != nil && gh.App() != nil {
			gh.App().Forget(r)
		}
		log.Printf("%s: the GitHub App was removed; no longer sorting it", r)
	}
	for _, r := range add {
		a.Store.AddRepo(r)
		log.Printf("%s: the GitHub App was installed; sorting its open pull requests", r)
	}
	if len(add) > 0 {
		for _, m := range a.Reconcile() {
			log.Print(m)
		}
	}
}

// handleJob runs one webhook job. A panic is logged and the worker carries
// on, so one strange PR can't stop Cowboy sorting the rest.
func handleJob(a *app.App, j webhook.Job) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("%s#%d: recovered from a crash: %v (the catch-up sweep will retry it)", j.Repo, j.Number, r)
		}
	}()
	switch {
	case len(j.Track) > 0 || len(j.Untrack) > 0:
		trackRepos(a, j.Track, j.Untrack)
	case j.Unflag:
		if err := a.ClearAgent(j.Repo, j.Number, j.By); err == nil {
			log.Printf("%s#%d marked not-an-agent by %s", j.Repo, j.Number, j.By)
		}
	case j.Repen != "":
		if err := a.Correct(j.Repo, j.Number, score.Pen(j.Repen), j.By); err == nil {
			log.Printf("%s#%d moved to %s by %s", j.Repo, j.Number, j.Repen, j.By)
		}
	case j.Closed:
		a.Closed(j.Repo, j.Number, j.Merged)
		log.Printf("%s#%d %s", j.Repo, j.Number, map[bool]string{true: "merged", false: "closed"}[j.Merged])
	default:
		d, ok, err := a.HandlePR(j.Repo, j.Number)
		switch {
		case err != nil:
			log.Printf("%s#%d error: %v", j.Repo, j.Number, err)
		case !ok:
			log.Printf("%s#%d skipped (not an agent pr)", j.Repo, j.Number)
		default:
			who := d.Agent
			if who == "" {
				who = "person"
			}
			log.Printf("%s#%d -> %s (%s)", j.Repo, j.Number, d.Pen, who)
		}
	}
}

// safely runs f, logging a panic instead of crashing the process.
func safely(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("%s: recovered from a crash: %v", what, r)
		}
	}()
	f()
}

// checkAccess makes sure the token can see each repo before serving, so a
// wrong token or missing permission shows up at start, not as silence.
func checkAccess(f forge.Forge, repos []string) {
	for _, repo := range repos {
		open, err := f.ListOpenPRs(repo)
		if err != nil {
			log.Printf("WARNING: can't list pull requests for %s: %v", repo, err)
			log.Printf("         check FORGE_TOKEN: it needs read access to pull requests and write access to issues/labels on %s", repo)
			continue
		}
		log.Printf("%s: token works, %d open pull request(s)", repo, len(open))
	}
}

// authFromEnv reads the dashboard login settings.
func authFromEnv(addr string) web.Auth {
	a := web.Auth{Password: os.Getenv("UI_PASSWORD"), ProxyHeader: os.Getenv("AUTH_PROXY_HEADER"), Open: os.Getenv("UI_OPEN") == "1"}
	if d, err := strconv.Atoi(os.Getenv("UI_SESSION_DAYS")); err == nil {
		a.SessionDays = d
	}
	if !a.Enabled() && !a.Open {
		if web.IsLocal(addr) {
			a.Open = true // only this machine can reach it
		} else {
			log.Fatalf("the dashboard on %s would be open to anyone who can reach it.\n"+
				"Set UI_PASSWORD, or AUTH_PROXY_HEADER if Cowboy sits behind an SSO proxy,\n"+
				"or ADDR=127.0.0.1:8080 to keep it on this machine, or UI_OPEN=1 if you're sure.", addr)
		}
	}
	switch {
	case a.ProxyHeader != "":
		log.Printf("dashboard: signed in through your proxy (%s)", a.ProxyHeader)
	case a.Password != "":
		log.Printf("dashboard: password login on")
	default:
		log.Printf("dashboard: no login (UI_OPEN=1 or local address)")
	}
	return a
}

// githubApp sets up GitHub App mode: it loads saved app credentials (or
// GITHUB_APP_ID + GITHUB_APP_PRIVATE_KEY_FILE), or prepares the setup page.
// It returns the webhook secret to use.
func githubApp(a *app.App, gh *forge.GitHub, secret string) (string, error) {
	api := os.Getenv("FORGE_URL")
	webBase := "https://github.com"
	if api != "" {
		webBase = strings.TrimSuffix(strings.TrimRight(api, "/"), "/api/v3")
	}
	path := filepath.Join(filepath.Dir(env("DATA", "data/cowboy.json")), "github-app.json")
	creds, ok, err := forge.LoadAppCreds(path)
	if err != nil {
		return "", err
	}
	if !ok && os.Getenv("GITHUB_APP_ID") != "" {
		id, err := strconv.ParseInt(os.Getenv("GITHUB_APP_ID"), 10, 64)
		if err != nil {
			return "", fmt.Errorf("GITHUB_APP_ID %q isn't a number", os.Getenv("GITHUB_APP_ID"))
		}
		key, err := os.ReadFile(os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"))
		if err != nil {
			return "", fmt.Errorf("GITHUB_APP_ID needs GITHUB_APP_PRIVATE_KEY_FILE: %v", err)
		}
		creds, ok = forge.AppCreds{ID: id, PEM: string(key), Name: "app " + os.Getenv("GITHUB_APP_ID")}, true
	}
	use := func(c forge.AppCreds) error {
		auth, err := forge.NewAppAuth(api, c)
		if err != nil {
			return err
		}
		gh.UseApp(auth)
		return nil
	}
	if ok {
		if err := use(creds); err != nil {
			return "", fmt.Errorf("%s: %v", path, err)
		}
		if secret == "" {
			secret = creds.WebhookSecret
		}
		log.Printf("github: signed in as the GitHub App %q", creds.Name)
	} else {
		dash := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")
		if dash == "" {
			addr := env("ADDR", ":8080")
			if strings.HasPrefix(addr, ":") {
				addr = "localhost" + addr
			}
			dash = "http://" + addr
		}
		log.Printf("github: not connected yet. Open %s/setup and press \"create the github app\"", dash)
	}
	ghSetup = &web.GitHubSetup{
		WebBase:   webBase,
		PublicURL: os.Getenv("PUBLIC_URL"),
		Status: func() web.SetupStatus {
			app := gh.App()
			if app == nil {
				return web.SetupStatus{}
			}
			c := app.Creds
			return web.SetupStatus{Connected: true, AppName: c.Name, AppURL: c.HTMLURL, InstallURL: c.InstallURL(), Repos: a.Store.Repos()}
		},
		Finish: func(code string) (forge.AppCreds, error) {
			c, err := forge.ConvertManifest(strings.TrimRight(firstNonEmpty(api, "https://api.github.com"), "/"), code)
			if err != nil {
				return c, err
			}
			if err := forge.SaveAppCreds(path, c); err != nil {
				return c, fmt.Errorf("saving the app's credentials: %v", err)
			}
			if err := use(c); err != nil {
				return c, err
			}
			if hooks != nil && os.Getenv("WEBHOOK_SECRET") == "" {
				hooks.SetSecret(c.WebhookSecret)
			}
			return c, nil
		},
		Sync: func() (int, error) { return syncAppRepos(a, gh), nil },
	}
	return secret, nil
}

// syncAppRepos tracks the repos the GitHub App is installed on, stops
// tracking ones it was removed from, and sorts what's open.
func syncAppRepos(a *app.App, gh *forge.GitHub) int {
	repos, err := gh.App().Repos()
	if err != nil {
		log.Printf("github: can't list the app's repos: %v", err)
		return 0
	}
	have, tracked := map[string]bool{}, map[string]bool{}
	for _, r := range repos {
		have[strings.ToLower(r)] = true
	}
	var added, gone []string
	for _, r := range a.Store.Repos() {
		tracked[strings.ToLower(r)] = true
		if !have[strings.ToLower(r)] {
			gone = append(gone, r)
		}
	}
	for _, r := range repos {
		if !tracked[strings.ToLower(r)] {
			added = append(added, r)
		}
	}
	trackRepos(a, added, gone)
	if len(repos) == 0 {
		log.Printf("github: the app isn't installed on any repos yet; pick some at %s", gh.App().Creds.InstallURL())
	}
	return len(repos)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func serve() {
	f, err := forge.New(env("FORGE", "github"), os.Getenv("FORGE_URL"), os.Getenv("FORGE_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	secret := os.Getenv("WEBHOOK_SECRET")
	st, err := store.Open(env("DATA", "data/cowboy.json"))
	if err != nil {
		log.Fatal(err)
	}
	a := app.New(f, st, parsePaths(os.Getenv("REPO_PATHS")))
	// GitHub with no token: sign in as a GitHub App, made with one click on /setup.
	gh, _ := f.(*forge.GitHub)
	appMode := gh != nil && os.Getenv("FORGE_TOKEN") == ""
	if appMode {
		if s, err := githubApp(a, gh, secret); err != nil {
			log.Fatal(err)
		} else {
			secret = s
		}
	} else if secret == "" {
		log.Fatal("WEBHOOK_SECRET is required")
	}
	a.DryRun = os.Getenv("DRY_RUN") == "1"
	a.ArchiveGraphs = !strings.EqualFold(os.Getenv("BLAST_RADIUS"), "off")
	a.ArchiveLimit = 300 << 20
	if v, err := strconv.Atoi(os.Getenv("BLAST_RADIUS_MAX_MB")); err == nil && v >= 0 {
		a.ArchiveLimit = int64(v) << 20
	}
	if a.ArchiveGraphs {
		log.Printf("blast radius: on, read from the git host (archives up to %d MB; nothing is kept on disk)", a.ArchiveLimit>>20)
	}
	bpath := filepath.Join(filepath.Dir(env("DATA", "data/cowboy.json")), "baselines.json")
	if bs, err := detect.OpenBaselines(bpath, 90); err == nil {
		a.Baselines = bs
	} else {
		log.Printf("baselines: %v (undisclosed-agent check off)", err)
	}
	if m := modelFromEnv(); m != nil {
		a.Model = m
		a.StartNotes()
		log.Printf("local model: %s at %s", m.Model, m.BaseURL)
	} else {
		log.Printf("no MODEL_URL set: sorting with rules only")
	}
	catchUp := 5 * time.Minute
	if v, err := strconv.Atoi(os.Getenv("CATCHUP_MINUTES")); err == nil && v > 0 {
		catchUp = time.Duration(v) * time.Minute
	}
	repos := strings.FieldsFunc(os.Getenv("REPOS"), func(r rune) bool { return r == ',' || r == ' ' })
	addr := env("ADDR", ":8080")
	auth := authFromEnv(addr)
	if rc, ok, err := report.FromEnv(os.Getenv); err != nil {
		log.Fatalf("weekly tally: %v", err)
	} else if ok {
		sch := report.NewScheduler(rc, a, env("DATA", "data/cowboy.json"))
		tallyNote = fmt.Sprintf("Sent to the %s, %s. Next: %s.", rc.Where(), rc.When(), rc.Next(time.Now()).Format("Mon Jan 2"))
		log.Printf("weekly tally: %s, %s", rc.Where(), rc.When())
		go safely("weekly tally", func() { sch.Run(context.Background()) })
	} else {
		tallyNote = "Not sent anywhere yet. Set TALLY_SLACK_WEBHOOK or TALLY_EMAIL_TO to get it every week."
	}
	if !appMode {
		checkAccess(f, repos)
	}
	run(a, addr, secret, catchUp, auth, func(chan<- webhook.Job) {
		for _, repo := range repos {
			st.AddRepo(repo)
		}
		if appMode && gh.App() != nil {
			syncAppRepos(a, gh)
		}
		for _, m := range a.Reconcile() { // sorts everything already open
			log.Print(m)
		}
		for _, m := range a.Sweep() {
			log.Print(m)
		}
	})
}

// findTestdata looks for testdata next to the binary or in the working dir.
func findTestdata() string {
	cands := []string{"testdata"}
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(exe), "testdata"), filepath.Join(filepath.Dir(exe), "..", "testdata"))
	}
	for _, c := range cands {
		if _, err := os.Stat(filepath.Join(c, "prs")); err == nil {
			return c
		}
	}
	log.Fatal("can't find testdata/ (run from the cowboy folder)")
	return ""
}

func demo(args []string) {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	addr := fs.String("addr", env("ADDR", ":8080"), "listen address")
	noServe := fs.Bool("once", false, "sort the sample prs, print, and exit")
	drip := fs.Duration("drip", 4*time.Second, "time between sample prs arriving (0 = all at once)")
	modelURL := fs.String("model-url", os.Getenv("MODEL_URL"), "use a real local model server instead of the demo's prewritten answers")
	modelName := fs.String("model-name", env("MODEL_NAME", "qwen2.5-coder:7b"), "model name on that server")
	noModel := fs.Bool("no-model", false, "rules only, no model notes")
	agentCheck := fs.Bool("agent-check", false, "also run the experimental check for unmarked agent pull requests")
	fs.Parse(args)

	td := findTestdata()
	fake := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	dataFile := filepath.Join(os.TempDir(), "cowboy-demo.json")
	os.Remove(dataFile)
	st, _ := store.Open(dataFile)
	a := app.New(fake, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	// The experimental check is off in the demo unless asked for: its
	// sample authors are made up, and on real repos it catches little.
	// Without it, the sample PRs from people are sorted quietly like any other.
	if *agentCheck {
		bs, _ := detect.OpenBaselines("", 90)
		if err := detect.SeedDir(bs, fake.Name(), filepath.Join(td, "history"), time.Now()); err != nil {
			log.Printf("demo history: %v", err)
		}
		a.Baselines = bs
	}
	switch {
	case *noModel:
	case *modelURL != "":
		a.Model = model.New(*modelURL, *modelName, os.Getenv("MODEL_KEY"))
	default:
		a.Model = model.Demo{Dir: filepath.Join(td, "prs")}
	}
	const repo = "acme/app"
	st.AddRepo(repo)
	secret := env("WEBHOOK_SECRET", "demo-secret")
	url := "http://localhost" + portOf(*addr)

	if *noServe || *drip == 0 {
		sortAll(a, fake, repo)
		if *noServe {
			fmt.Println()
			return
		}
		fmt.Printf("\n  open %s\n  webhook secret: %s  (try: ./scripts/send-webhook.sh 512)\n\n", url, secret)
		run(a, *addr, secret, 20*time.Second, web.Auth{Open: true}, nil)
		return
	}

	// Live demo: PRs arrive one by one, like agents working overnight.
	// One webhook is "lost" on purpose so the catch-up sweep can find it.
	const lost = 521
	fake.HideAll()
	wait := 8 * time.Second
	fmt.Printf("\n  cowboy demo · live\n\n  open %s now. agent prs start arriving in %s, one every %s.\n", url, wait, *drip)
	fmt.Printf("  the webhook for #%d gets lost on purpose; watch the catch-up sweep find it.\n", lost)
	if a.Model != nil {
		fmt.Printf("  model notes: %s\n", a.Model.Name())
	}
	fmt.Printf("  webhook secret: %s  (try: ./scripts/send-webhook.sh 512)\n\n", secret)
	run(a, *addr, secret, 15*time.Second, web.Auth{Open: true}, func(queue chan<- webhook.Job) {
		time.Sleep(wait)
		for _, n := range fake.All() {
			fake.Open(n)
			if n == lost {
				a.Announce(repo, n, fmt.Sprintf("#%d opened, but its webhook was lost (on purpose, for the demo)", n))
				log.Printf("%s#%d opened; webhook dropped on purpose", repo, n)
			} else {
				queue <- webhook.Job{Repo: repo, Number: n}
			}
			time.Sleep(*drip)
		}
		for _, m := range a.Sweep() {
			log.Print(m)
		}
	})
}

// sortAll sorts every sample PR at once and prints a table.
func sortAll(a *app.App, fake *forge.Fake, repo string) {
	open, _ := fake.ListOpenPRs(repo)
	log.SetOutput(io.Discard) // keep the table readable
	fmt.Print("\n  cowboy demo · sorting the sample ranch (acme/app)\n\n")
	for _, o := range open {
		n := o.Number
		d, ok, err := a.HandlePR(repo, n)
		switch {
		case err != nil:
			fmt.Printf("  #%d  error %v\n", n, err)
		case !ok:
			fmt.Printf("  #%d  %-8s  no agent found (checked), left alone\n", n, "-")
		default:
			extra := ""
			if d.DoubleOf != 0 {
				extra = fmt.Sprintf("  double of #%d", d.DoubleOf)
			}
			if d.Notes.Drift() {
				extra += "  model: off task?"
			}
			if d.Detection != nil && d.Detection.Label == "likely agent" {
				extra += fmt.Sprintf("  no agent label, caught by agent check (%d)", d.Detection.Score)
			}
			who := d.Agent
			if who == "" {
				who = "person"
			}
			fmt.Printf("  #%d  %-8s  %-14s %s%s\n", n, d.Pen, who, trunc(d.Title, 38), extra)
		}
	}
	for _, m := range a.Sweep() {
		fmt.Println("  " + m)
	}
	log.SetOutput(os.Stderr)
}

func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return addr
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func scoreCmd(args []string) {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	diffF := fs.String("diff", "", "unified diff file (git diff / .diff url)")
	metaF := fs.String("meta", "", "pr meta json (author, title, body, commits)")
	repoP := fs.String("repo", "", "local checkout, for blast radius and CODEOWNERS")
	cfgF := fs.String("config", "", "cowboy.json (default: <repo>/cowboy.json)")
	asJSON := fs.Bool("json", false, "print the full decision as json")
	modelURL := fs.String("model-url", os.Getenv("MODEL_URL"), "local model server for notes (optional)")
	modelName := fs.String("model-name", env("MODEL_NAME", "qwen2.5-coder:7b"), "model name on that server")
	fs.Parse(args)
	if *diffF == "" || *metaF == "" {
		fs.Usage()
		os.Exit(2)
	}
	patch, err := os.ReadFile(*diffF)
	if err != nil {
		log.Fatal(err)
	}
	var meta analyze.PRMeta
	mb, err := os.ReadFile(*metaF)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(mb, &meta); err != nil {
		log.Fatal(err)
	}
	if meta.Repo == "" {
		meta.Repo = "local/repo"
	}
	cfg := config.Default()
	cfgPath := *cfgF
	if cfgPath == "" && *repoP != "" {
		cfgPath = filepath.Join(*repoP, "cowboy.json")
	}
	if cfgPath != "" {
		if c, err := config.Load(cfgPath); err == nil {
			cfg = c
		}
	}
	owners := ""
	paths := map[string]string{}
	if *repoP != "" {
		paths[meta.Repo] = *repoP
		for _, p := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
			if b, err := os.ReadFile(filepath.Join(*repoP, p)); err == nil {
				owners = string(b)
				break
			}
		}
	}
	st, _ := store.Open(filepath.Join(os.TempDir(), "cowboy-score-"+strconv.Itoa(os.Getpid())+".json"))
	a := app.New(nil, st, paths)
	d, ok := a.ScoreLocal(meta, string(patch), owners, cfg)
	if ok && *modelURL != "" {
		n := model.New(*modelURL, *modelName, os.Getenv("MODEL_KEY")).Notes(model.Ask{Repo: meta.Repo, Number: meta.Number, Title: meta.Title, Body: meta.Body, Files: diff.Parse(string(patch)), HeadSHA: d.HeadSHA})
		n.HeadSHA = d.HeadSHA
		d.Notes = &n
		d.Comment = pipeline.FullComment(d)
	}
	if !ok {
		fmt.Println("not an agent pr: cowboy leaves it alone. (add the login under \"agents\" in cowboy.json)")
		os.Exit(0)
	}
	if *asJSON {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		enc.Encode(d)
		os.Stdout.Write(buf.Bytes())
		return
	}
	fmt.Println(d.Comment)
}

func tallyCmd(args []string) {
	fs := flag.NewFlagSet("tally", flag.ExitOnError)
	send := fs.Bool("send", false, "send the last 7 days now, to TALLY_SLACK_WEBHOOK and/or TALLY_EMAIL_TO")
	fs.Parse(args)
	st, err := store.Open(env("DATA", "data/cowboy.json"))
	if err != nil {
		log.Fatal(err)
	}
	a := app.New(nil, st, nil)
	if !*send {
		fmt.Print(app.TallyMarkdown(a.Tally(time.Now().AddDate(0, 0, -7))))
		return
	}
	rc, ok, err := report.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if !ok {
		log.Fatal("nowhere to send it: set TALLY_SLACK_WEBHOOK and/or TALLY_EMAIL_TO")
	}
	r := report.Build(a, time.Now(), rc.Link)
	fmt.Print(r.Text())
	errs := report.Send(context.Background(), rc, r, nil)
	for d, err := range errs {
		log.Printf("%s: %v", d, err)
	}
	if len(errs) > 0 {
		os.Exit(1)
	}
	fmt.Printf("sent to the %s\n", rc.Where())
}

func signCmd(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	secret := fs.String("secret", env("WEBHOOK_SECRET", "demo-secret"), "webhook secret")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(webhook.Sign(*secret, b))
}

func modelFromEnv() *model.Client {
	u := os.Getenv("MODEL_URL")
	if u == "" {
		return nil
	}
	return model.New(u, env("MODEL_NAME", "qwen2.5-coder:7b"), os.Getenv("MODEL_KEY"))
}

func checkModel() {
	m := modelFromEnv()
	if m == nil {
		fmt.Println("MODEL_URL isn't set. example: MODEL_URL=http://localhost:11434 MODEL_NAME=qwen2.5-coder:7b cowboy check-model")
		os.Exit(1)
	}
	start := time.Now()
	reply, err := m.Ping()
	if err != nil {
		fmt.Println("not working:", err)
		os.Exit(1)
	}
	fmt.Printf("ok: %s at %s answered in %.1fs (%q)\n", m.Model, m.BaseURL, time.Since(start).Seconds(), trunc(strings.TrimSpace(reply), 40))
}
