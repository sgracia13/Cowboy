# cowboy

sorts ai-agent pull requests into **easy**, **normal** and **careful**, with the reasons written down.
**rules decide the pen. a local model explains.** runs on your server. never merges, closes or reviews. your code never leaves.

**free. all of it. whatever's free today stays free.**

![the corral filling in live](docs/live-demo.gif)

> **preview release.** sorting, labels, doubles, strays, the live corral and the github integration are tested on real repos. forgejo/gitea support and the local model notes are in preview.

## try it in one minute (no setup)

```sh
curl -fsSL https://github.com/sgracia13/Cowboy/releases/latest/download/install.sh | sh
cowboy demo
```

on windows, download `cowboy-windows-amd64.exe` from [releases](https://github.com/sgracia13/Cowboy/releases) and run `cowboy-windows-amd64.exe demo`. have go? `go run github.com/sgracia13/Cowboy/cmd/cowboy@latest demo`.

open http://localhost:8080 right away. the corral starts empty, then the sample repo's prs arrive one every 4 seconds, as if agents (and a couple of people) were opening them overnight. each one drops into its pen live, no refresh. the webhook for #521 is dropped on purpose, so you can watch the **catch-up sweep** find it a few seconds later.

want everything at once instead? `demo --drip 0`. just the table in the terminal? `demo --once`.

where they land:

| pr | pen | why |
|---|---|---|
| #498 bump eslint config | easy | 2 lines, config only, checks green |
| #512 fix typo in readme | easy | docs only |
| #521 add tests for features | easy | tests only, no source touched |
| #503 paginate audit log | normal | 79 lines, small blast radius · **stray** (agent went quiet) |
| #506 retry webhook client | normal | 10 lines, 7 files depend on it |
| #509 retry webhook sender | normal | **double** of #506 (same issue #77) |
| #507 move auth to session store | careful | auth path, 39 files depend on it, removed an assert (the eslint silencer is a note) |
| #515 rewrite invoice totals | careful | billing path, `except Exception` (the "special case for test" comment is a note) |
| #530 speed up ci | careful | loosens ci (`|| true`) |
| #520, #542 (people) | easy | sorted quietly: a pen label, no agent label, no comment |
| #540, #541 (people) | normal | the same, in normal |

the demo also shows **local model notes** from prewritten answers, so you can see the feature without a model server. look at #506: the rules say normal, but the model notices the agent also changed `send()` for every caller, which the issue never asked for. #530 gets flagged the same way.

click any pr to see what was measured and the one comment cowboy would post, and to move it to another pen (that gets counted in the tally). the link **see it on the demo git host** shows the pr the way github or forgejo would: just the labels, that comment and a review request.

send it a real signed webhook while the demo runs:

```sh
./scripts/send-webhook.sh 512
```

## run it for real

### on github: connect with one click (preview)

```sh
# docker (image: ghcr.io/sgracia13/cowboy)
docker run -d -p 8080:8080 -v cowboy-data:/data \
  -e UI_PASSWORD=a-dashboard-password \
  -e PUBLIC_URL=https://cowboy.your-domain.com \
  ghcr.io/sgracia13/cowboy:latest

# or the binary, on your own machine (no PUBLIC_URL needed)
cowboy serve
```

open the dashboard, press **create the github app**, confirm on github, pick your repos, and you're back in the corral with your open prs sorted. no token, no webhook and no secret to copy.

- cowboy asks github to create a **private github app** for your server (github's app-manifest flow). the app's id, private key and webhook secret are saved in `github-app.json` next to the data file, readable only by cowboy's user.
- it asks for: pull requests read/write, issues read/write (labels, the comment), contents read (cowboy.json, CODEOWNERS), checks and metadata read.
- cowboy signs in with **short-lived tokens** (one hour, per installation), not a long-lived personal token.
- repos you add or remove on github are picked up automatically.
- with a `PUBLIC_URL` github can reach over https, webhooks are turned on and prs are sorted the moment they change. on localhost or a private address the app is created with webhooks off and the catch-up sweep checks every few minutes instead.
- an org: type its name on the setup page to create the app under the org. github enterprise: set `FORGE_URL` to your api address (`https://ghe.your-co.com/api/v3`).
- made an app by hand? set `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY_FILE` and `WEBHOOK_SECRET` instead.

this has been tested end to end against a stand-in for github's api, not yet against github.com itself.

### with a token (github, forgejo, gitea)

```sh
docker run -d -p 8080:8080 -v cowboy-data:/data \
  -e FORGE=github -e FORGE_TOKEN=... -e WEBHOOK_SECRET=... -e UI_PASSWORD=... \
  -e REPOS=you/your-repo ghcr.io/sgracia13/cowboy:latest
```

setting `FORGE_TOKEN` keeps the old way: a token, `REPOS`, and a webhook you add yourself:

- github: `https://cowboy.yourhost/webhook/github`, events: pull requests, check suites
- gitea / forgejo: `https://cowboy.yourhost/webhook/gitea`, events: pull request

### settings

| env | what |
|---|---|
| `FORGE` | `github` (default), `gitea` or `forgejo` |
| `FORGE_URL` | your forge (not needed for github.com) |
| `PUBLIC_URL` | the https address github can reach this cowboy at (github app setup, links in the tally) |
| `FORGE_TOKEN` | token that can read prs and write labels + comments (leave empty on github to use the app) |
| `WEBHOOK_SECRET` | the secret you set on the webhook (the github app brings its own) |
| `REPOS` | `owner/repo,...` to sort all open prs at start (token mode) |
| `BLAST_RADIUS` | `off` = don't download repos for blast radius (default on) |
| `BLAST_RADIUS_MAX_MB` | biggest repo archive to download (default 300) |
| `REPO_PATHS` | `owner/repo=/path/to/checkout`: read blast radius from a local copy instead (optional) |
| `UI_PASSWORD` | password for the dashboard (one shared login for the team) |
| `AUTH_PROXY_HEADER` | instead of a password: the header your SSO proxy sets, e.g. `X-Forwarded-User` |
| `UI_OPEN=1` | run the dashboard with no login (only if nothing else can reach it) |
| `DRY_RUN=1` | sort and show in the corral, write nothing to the forge |
| `CATCHUP_MINUTES` | how often the catch-up sweep runs (default 5) |
| `MODEL_URL` | your model server, e.g. `http://localhost:11434` (optional) |
| `MODEL_NAME` | e.g. `qwen2.5-coder:7b` (optional) |

## turn on the local model (optional)

cowboy can use an open-weight model on your own server to add two notes to each agent pr:

- **on task?** did the agent change only what the issue asked for, or did it wander?
- **brief** three plain lines on what changed, and the one or two spots to look at first

the model **never picks the pen**. same pr, same pen, with or without it. notes that name a file that isn't in the pr are thrown out.

it works with anything that speaks the openai chat api: ollama, vllm, llama.cpp server, lm studio, localai.

```sh
# example with ollama on the same machine
ollama pull qwen2.5-coder:7b
MODEL_URL=http://localhost:11434 MODEL_NAME=qwen2.5-coder:7b cowboy check-model
MODEL_URL=http://localhost:11434 MODEL_NAME=qwen2.5-coder:7b cowboy demo   # demo with your real model
```

then add `MODEL_URL` and `MODEL_NAME` to `serve`. labels land first; notes follow when the model finishes (seconds on a gpu, under a minute on a cpu for a 7b model). no `MODEL_URL` = rules only, everything else works.

## agents that don't say so (experimental)

> **how well it works today:** tested on 12,300 real prs (sentry, n8n, grafana) with their agent labels removed, the behaviour and text signals alone caught almost none of the hidden agent prs, and flagged almost no humans. agent branch names (`claude/`, `codex/`…) are reliable but rare. treat the score as a hint, not a verdict; the test tools are in `scripts/eval/` if you want to run them on your own history.

a lot of agent work arrives from people's own accounts with no bot name, no co-author line and no signature. cowboy checks every pr that doesn't declare an agent and scores how likely it is that one wrote it, 0 to 100, with a range.

it looks at three layers, and compares each person to **their own history**, not to everyone:

| layer | examples | weight |
|---|---|---|
| deterministic | agent branch names (`codex/`, `cursor/`…), agent instruction files | near-proof |
| behaviour | lines per minute vs this author's usual top pace, machine-regular commit gaps, push-and-open in one step, 3 a.m. bursts from a 9-to-5 person | capped |
| text and code | pr templates this author never uses, stock phrases, no typos from someone who usually makes them, comments that restate code, defensive checks everywhere | capped, weakest |

the evidence adds up in log-odds with a ceiling per layer, so **being fast is never enough on its own**. the `agent:likely` label goes on only when the **low end** of the range clears 80, and it never changes the pen. anyone can remove the label (or press **not an agent**): the pr stays as human work and cowboy asks for more evidence on that author's prs from then on.

the check is off in the demo, because its sample authors are made up. to see how it reasons, run `cowboy demo --agent-check`: it flags **#540** (a codex/ branch and a 3 a.m. burst) and **#542**, and leaves **#541** alone, sam's fast codemod, because that pace is normal for sam. that's a demonstration on invented histories, not evidence it works on real repos.

```json
// cowboy.json
"detect": {
  "sort_all": true,   // put every pr in a pen, agent or not (default true)
  "label_at": 80,     // the low end of the range must reach this
  "prior": 0.08       // expected share of undisclosed agent prs
}
```

histories live in `baselines.json` next to the store and grow as prs close. a new author starts "cold": half-weight behaviour and a wider range, so they need more evidence before a label. the v0 weights are set by hand (see `internal/detect/score.go`); fit them on your own history before relying on the numbers.

## live, and nothing slips through

- **live corral.** the dashboard keeps a connection open to cowboy (server-sent events). when a pr is sorted, moved, closed or gets model notes, its pen redraws in place. the yellow dot in the header means it's connected.
- **catch-up sweep.** webhooks get lost (restarts, network blips). every 5 minutes cowboy lists each repo's open prs and sorts any pr it missed or whose code changed, and marks prs that were merged or closed. when nothing changed it costs one api call per repo. (with `sort_all` off, prs that aren't agent work are remembered by commit, so they aren't fetched again.)
- **trail.** the trail page lists everything cowboy did, newest first.

## the weekly tally in slack or email

every week cowboy can post the tally (prs per agent and for people, by pen, plus doubles, strays and how often your team moved one) to a chat channel, by email, or both. nothing is sent unless you set one of these:

```sh
TALLY_SLACK_WEBHOOK=https://hooks.slack.com/services/...   # a slack incoming webhook; mattermost works too
TALLY_EMAIL_TO=team@your-co.com,lead@your-co.com
SMTP_HOST=smtp.your-co.com SMTP_PORT=587 SMTP_USER=... SMTP_PASSWORD=... SMTP_FROM=cowboy@your-co.com
TALLY_DAY=monday TALLY_HOUR=9 TZ=America/New_York          # the defaults are monday 09:00 server time
PUBLIC_URL=https://cowboy.your-co.com                        # optional: links the post to your dashboard
```

it covers the 7 days before the send time. cowboy remembers what it sent (`tally-sent.json` next to the data file), so a restart never posts twice; a failed send is retried every 30 minutes, and a post more than a day late is skipped rather than sent stale. the first post goes out at the next scheduled time. to try it now: `cowboy tally --send`.

## safe to run on a server

- **dashboard login.** set `UI_PASSWORD`, or `AUTH_PROXY_HEADER` behind an sso proxy. cowboy refuses to start with an open dashboard on a public address. webhooks and `/healthz` stay outside the login; webhooks are checked by signature.
- **no cross-site clicks.** dashboard buttons only accept posts from the dashboard itself, and pages send strict security headers.
- **rate limits.** github's rate limits are waited out (up to a minute); longer limits are left to the next catch-up sweep.
- **repeat webhooks** (same delivery id) are handled once.
- **one bad pr can't stop it.** a crash while sorting one pr is logged and cowboy carries on; the sweep retries it.
- **clean shutdown.** on stop, cowboy finishes queued work first. the store is written to disk safely, so a crash never leaves it half-written.
- **startup check.** cowboy checks the token can read each repo in `REPOS` and says so in the log.

## after merge: did the pens get it right?

cowboy follows every merged pr for 30 days (`OUTCOME_WINDOW_DAYS`) and ties what happened back to the pen it gave. the **outcomes** page shows it.

what counts as trouble:

| outcome | how cowboy knows | counts |
|---|---|---|
| reverted | a revert commit on the default branch names the pr (github's revert button, `git revert`, a revert branch, or the pr's title) | straight away |
| broke main | checks failed on the merge commit after the pr itself was green | straight away |
| needed a fix | within two weeks, a pr titled as a fix names this one, or rewrites 3+ of the lines it added (lines common to many prs don't count) | once a person confirms |
| incident | an alert from sentry, pagerduty or any tool, matched by release and by files in the error | once a person confirms |

anything that counts can be marked "not a problem". cowboy also records which release each pr shipped in, who approved it, and how much of its code is still there after 30 and 90 days (from fingerprints of a sample of its added lines; no code is kept).

the outcomes page has:

- **by pen**: how often easy, normal and careful prs had trouble, with a range, so 2 of 10 doesn't look like a fact. only prs merged at least 30 days ago are rated.
- **rule report card**: for each careful rule, how often its prs had trouble; and careful prs approved before merge vs not.
- **suggested rule changes**: only when the numbers clearly point somewhere (a folder whose easy prs keep breaking, a blast-radius line that splits better, a rule that catches nothing). cowboy never changes rules itself; you edit `cowboy.json`.
- **scorecards**: each agent (and people) by area of the code, and by month.
- **easy, but had trouble**: what slipped through.

connect incidents (each is optional):

```sh
SENTRY_CLIENT_SECRET=...   # a sentry internal integration with an issue alert action -> https://cowboy.your-co.com/webhook/sentry
PAGERDUTY_SECRET=...       # a pagerduty v3 webhook subscription (incident.triggered) -> /webhook/pagerduty
INCIDENT_SECRET=...        # anything else: POST json to /webhook/incident, signed (X-Cowboy-Signature: sha256=…) or with Authorization: Bearer <secret>
INCIDENT_REPOS=web-frontend=acme/web   # when a tool's project or service name isn't the repo (one repo needs nothing)
```

```json
{"id": "INC-12", "title": "Checkout errors", "severity": "sev2", "started_at": "2026-10-19T14:02:00Z",
 "url": "https://…", "repo": "acme/web", "release": "v2.3.0", "files": ["src/pay/api.ts"]}
```

cowboy suggests up to three likely causes per incident (shipped in that release, or just before it started; and changed files that show up in the error) and someone on the team confirms or rejects them. it never uses the pen to pick a cause, so the pens are judged fairly.

**the audit record.** every sort, move, merge, approval, outcome and confirmation is appended to `audit.jsonl` next to the data file. each entry carries the hash of the one before, so `cowboy audit verify` finds any entry that was changed or removed. the weekly tally post includes the latest fingerprint: a copy outside the server to check against. export it with `cowboy audit export --format csv` or from the outcomes page.

**the benchmark.** `cowboy benchmark export` writes counts only (pens, agents, rules, outcome kinds; no repo names, paths, titles or people). read it, and share it only if you want to. `cowboy benchmark report a.json b.json …` pools several teams' files into one report. nothing is ever sent anywhere by cowboy.

**how well revert detection works**: on two years of n8n, grafana and sentry history it traced 70%, 87% and 98.8% of reverts to the pr they undid; the rest name no pr, title or commit. details: [`scripts/eval/outcomes`](scripts/eval/outcomes).

**don't wait a month.** on a fresh install the outcomes page offers **rate the last 90 days** (or run `cowboy backfill --days 90`): cowboy sorts the prs that already merged, the way it would have, and checks what happened to them. nothing is written to your git host, it uses today's rules and import graph, and these prs stay out of the tally.

**replayed on real history.** `cmd/cowboy-replay` runs the same engine on 7,721 real merged prs from sentry, grafana and n8n (november 2025 to september 2026). reverts it found matched an independent earlier count on 213 prs (8 only there, 5 only here). weighted back to sentry's real mix of prs, 1.94% of careful prs were reverted within 30 days against 0.78% of easy ones. suggested fixes looked right by title in about 7 in 10 of a sample, which is why a person confirms them. those are the repos the rules were tuned on. against simply flagging the biggest prs, careful's lead is within noise there, and on supabase (never used for tuning) careful did no better than size or chance; the easy pen held up in both. the claims (written down first) and results: [`scripts/eval/evidence`](scripts/eval/evidence).

`OUTCOMES=off` turns this off. it needs a host that reports history (github, forgejo, gitea).

## what it writes

only these, ever:

- labels: `cowboy:easy|normal|careful`, `cowboy:double`, `cowboy:stray`, `agent:<name>`
- one comment per pr, edited in place on every push (found by `<!-- cowboy -->`), with the model's notes if you turned it on
- a review request to the CODEOWNERS of careful prs

## blast radius

"20+ files depend on this" needs to know who imports what. cowboy works that out by itself:

- **no local copy needed.** cowboy downloads the default branch as a `.tar.gz` from your git host (the same api it already uses; the github app's *contents: read* covers it), reads the imports while it streams in, and keeps only the list of who imports what. no code is written to disk. it checks hourly whether the default branch moved and downloads again only if it did. a pull request is sorted with the last graph while a new one builds.
- **real parsing.** go is read with go's own parser. javascript/typescript and python are read by scanners that handle imports split over many lines and skip comments, strings, template literals and regular expressions. imports are matched to files using `go.mod`, tsconfig/jsconfig `paths` and `baseUrl` (with `extends`), workspace packages (`package.json` names, `exports` maps, `workspace:` aliases), and python package roots like `src/`.
- **checked against the real tools.** on n8n, grafana and sentry, it found 100.0% of the imports the typescript compiler and python's `ast` find, picked the same file as typescript 99.9–100% of the time, and caught 100% of the files 20+ files import (the old pattern version caught 1–60%). details and scripts: [`scripts/eval/graphcheck`](scripts/eval/graphcheck).
- **big repos.** archives over 300 MB are skipped (`BLAST_RADIUS_MAX_MB`); n8n, grafana and sentry are 50–55 MB and take 3–7 seconds to read. files over 2 MB (bundles, generated code) aren't read.
- `REPO_PATHS=owner/repo=/path` still works if you'd rather keep a checkout, and `BLAST_RADIUS=off` turns downloads off. the rules page shows how blast radius is working for each repo.

## the rules

**every pr is sorted, not just agent prs.** the rules look at the code, not who wrote it, and unmarked agent work can't be spotted reliably, so sorting everything is how nothing risky goes unsorted. prs from people are kept quiet: they get a pen label, no agent label, never a double or stray label, and a comment only when they land in careful. change that with `"people": {"comment": "always"}` or `"never"`, or go back to agent prs only with `"detect": {"sort_all": false}`.

defaults below. override per repo with `cowboy.json` on the default branch (a pr can't loosen its own rules). see `testdata/acme/cowboy.json`.

- **careful** if any: sensitive path (`auth/**`, `billing/**`, `payments/**`, `migrations/**`, `.github/workflows/**`, `security/**`) · 20+ files import a changed file · a catch-all that may hide errors · ci loosened · asserts lost across the pr (not counting a feature removed with its tests) · checks failing
- **easy** if all: ≤50 lines, ≤2 folders, ≤3 importers, checks ok. or: no source code at all and ≤600 lines
- **normal** otherwise
- **worth a look** (shown on the pr, never changes the pen): warning silencers, skipped tests, "special case for test" comments, 400+ source lines ("too big to review well")

these rules were checked against 7,500 merged prs from sentry, grafana and n8n: prs sorted careful were reverted 2.5x as often as easy ones. silencers, skips and size didn't predict reverts, so they're notes. a repo can make any shortcut count again with `"careful": {"shortcuts": [...]}` or size with `"min_source_lines": 400`.
- **double**: same linked issue, or 80%+ same files as an older open agent pr
- **stray**: no commits 3+ days after changes were requested (7+ = suggest closing)

same input, same pen, every time (tested 100× per sample pr).

## other commands

```sh
cowboy score --diff my.diff --meta meta.json --repo ./checkout   # sort one pr offline, print the comment
cowboy tally                                                     # this week's per-agent table as markdown
cowboy tally --send                                              # send the last 7 days to slack/email now
cowboy outcomes [--check]                                        # after merge, by pen (--check reads the git host now)
cowboy backfill --days 90                                        # rate prs that already merged (writes nothing to the host)
cowboy audit verify                                              # was the audit record changed?
cowboy audit export --format csv --since 2026-01-01              # the record, for an auditor
cowboy benchmark export > ours.json                              # counts only, to share if you want
cowboy sign --secret s body.json                                 # signature header for testing webhooks
cowboy check-model                                               # is the model server answering?
```

## layout

```
cmd/cowboy          cli: demo, serve, score, tally, sign
internal/diff       unified diff parser
internal/analyze    agent detection, file classes, linked issues
internal/graph      import graph -> blast radius (go, js/ts, python, java/kotlin)
internal/shortcuts  the six shortcut checks
internal/score      measure + decide (pure functions)
internal/pipeline   one pr in, one decision out (doubles, strays, comment)
internal/model      optional local model: scope check + brief (openai-compatible api)
internal/detect     undisclosed-agent check: matrix, extractors, per-author baselines, scorer
internal/forge      github + gitea/forgejo clients, and a demo forge
internal/app        the loop: fetch -> sort -> store -> label; live events; catch-up sweep
internal/webhook    signed webhook receivers
internal/store      decisions + corrections
internal/outcome    after merge: reverts, broken main, fixes, incidents, survival; report card, scorecards, benchmark
internal/audit      the hash-chained audit record
internal/web        the corral ui
testdata/           sample repo + 10 sample prs with expected pens
```

`make test` runs everything: golden pens, determinism, blast radius, shortcuts, webhooks, the full loop and every page.

## prototype limits (vs. the build plan)

- store is a json file, not sqlite (this build has zero dependencies)
- blast radius for java and kotlin reads imports only (files in the same package use each other without one), and hasn't been checked against a reference yet
- review requests skip teams (`@org/team`); users only
- after-merge tracking has been run against a stand-in for github, sentry and pagerduty, and revert detection against real history; not yet on a live repo for 30 days
- releases are matched to prs by time (merged before the release was published), not by walking the git history
- gitea/forgejo label changes aren't read back as corrections yet (github's are); use the corral's move buttons
- the weekly tally goes to a slack-style webhook or email; teams/discord-native formats aren't built
- model notes are tested against a stand-in server; try them with your own model before trusting the wording
- the model's "off task" note is advice only; an opt-in rule to send off-task prs to careful isn't built
- the agent check's weights are hand-set (v0) and untested on real repos; spotting a removed co-author line needs pr edit history (not read yet); backfilling baselines from a repo's past prs isn't built, so histories start from the first close

license: agpl-3.0. free for any team, any size. no plans, no keys, no limits.
