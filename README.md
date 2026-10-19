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

```sh
# with docker (image: ghcr.io/sgracia13/cowboy)
docker run -d -p 8080:8080 -v cowboy-data:/data \
  -e FORGE=github -e FORGE_TOKEN=... -e WEBHOOK_SECRET=... -e UI_PASSWORD=... \
  -e REPOS=you/your-repo ghcr.io/sgracia13/cowboy:latest

# or: docker compose up -d   (edit docker-compose.yml first)
```

| env | what |
|---|---|
| `FORGE` | `github`, `gitea` or `forgejo` |
| `FORGE_URL` | your forge (not needed for github.com) |
| `FORGE_TOKEN` | token that can read prs and write labels + comments |
| `WEBHOOK_SECRET` | the secret you set on the webhook |
| `REPOS` | `owner/repo,...` to sort all open prs at start (optional) |
| `REPO_PATHS` | `owner/repo=/path/to/checkout` to turn on blast radius (optional) |
| `UI_PASSWORD` | password for the dashboard (one shared login for the team) |
| `AUTH_PROXY_HEADER` | instead of a password: the header your SSO proxy sets, e.g. `X-Forwarded-User` |
| `UI_OPEN=1` | run the dashboard with no login (only if nothing else can reach it) |
| `DRY_RUN=1` | sort and show in the corral, write nothing to the forge |
| `CATCHUP_MINUTES` | how often the catch-up sweep runs (default 5) |
| `MODEL_URL` | your model server, e.g. `http://localhost:11434` (optional) |
| `MODEL_NAME` | e.g. `qwen2.5-coder:7b` (optional) |

then add a webhook on the repo or org:

- github: `https://cowboy.yourhost/webhook/github`, events: pull requests, check suites
- gitea / forgejo: `https://cowboy.yourhost/webhook/gitea`, events: pull request

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

## what it writes

only these, ever:

- labels: `cowboy:easy|normal|careful`, `cowboy:double`, `cowboy:stray`, `agent:<name>`
- one comment per pr, edited in place on every push (found by `<!-- cowboy -->`), with the model's notes if you turned it on
- a review request to the CODEOWNERS of careful prs

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
cowboy sign --secret s body.json                                 # signature header for testing webhooks
cowboy check-model                                               # is the model server answering?
```

## layout

```
cmd/cowboy          cli: demo, serve, score, tally, sign
internal/diff       unified diff parser
internal/analyze    agent detection, file classes, linked issues
internal/graph      import graph -> blast radius (go, js/ts, python, java)
internal/shortcuts  the six shortcut checks
internal/score      measure + decide (pure functions)
internal/pipeline   one pr in, one decision out (doubles, strays, comment)
internal/model      optional local model: scope check + brief (openai-compatible api)
internal/detect     undisclosed-agent check: matrix, extractors, per-author baselines, scorer
internal/forge      github + gitea/forgejo clients, and a demo forge
internal/app        the loop: fetch -> sort -> store -> label; live events; catch-up sweep
internal/webhook    signed webhook receivers
internal/store      decisions + corrections
internal/web        the corral ui
testdata/           sample repo + 10 sample prs with expected pens
```

`make test` runs everything: golden pens, determinism, blast radius, shortcuts, webhooks, the full loop and every page.

## prototype limits (vs. the build plan)

- store is a json file, not sqlite (this build has zero dependencies)
- imports are found with patterns, not tree-sitter; blast radius needs a local checkout via `REPO_PATHS`
- review requests skip teams (`@org/team`); users only
- gitea/forgejo label changes aren't read back as corrections yet (github's are); use the corral's move buttons
- the weekly tally goes to a slack-style webhook or email; teams/discord-native formats aren't built
- model notes are tested against a stand-in server; try them with your own model before trusting the wording
- the model's "off task" note is advice only; an opt-in rule to send off-task prs to careful isn't built
- the agent check's weights are hand-set (v0) and untested on real repos; spotting a removed co-author line needs pr edit history (not read yet); backfilling baselines from a repo's past prs isn't built, so histories start from the first close

license: agpl-3.0. free for any team, any size. no plans, no keys, no limits.
