# results for fitted rules

Claims and method: CLAIMS-fit.md (commit 9751e9f), written before the fit
code existed. Code: internal/fit (commit a24626f), run with
cmd/cowboy-fitlab and scored with compare_fit.py. Repos: vercel/next.js,
apache/airflow, elastic/kibana; test months February to August 2026, each
fitted on the 90 days before it.

## short version: better than the defaults on a smaller pen, but not proven

| claim | result | 95% range | verdict |
|---|---|---|---|
| 1. fitted beats biggest-first | fitted pen held 17% of reverted PRs, the same share of largest PRs 5% (+12 points) | +4 to +18 points | holds, but see below |
| 2. fitted beats chance | fitted PRs reverted 1.37× as often as all PRs | 0.87× to 1.93× | not shown |
| 3. fitted beats the defaults | lift 1.37× vs 1.10× for the v3 defaults (+0.27) | −0.24 to +0.81 | not shown |
| 4. fitted pen under 20% in every repo | 6.3% airflow, 14.9% kibana, 11.5% next.js | | holds |

100 reverted PRs across 21 repo-months. The fit had enough history and
found rules in 15 of the 21; the other 6 fell back to the defaults.

**Claim 1 holds for a weak reason.** On these repos the largest PRs were
reverted *less* often than average: the biggest 12% of PRs held only 5% of
the reverts, below the 12% chance would give. Beating biggest-first here
mostly says size is a poor signal in these repos, not that fitting is a
strong one. The honest comparison is claim 2 (chance), and that is not
shown.

## per repo

| repo | reverted PRs | fitted share | fitted lift | default share | default lift |
|---|---|---|---|---|---|
| apache/airflow | 18 | 6.3% | 3.53× | 13.9% | 1.60× |
| elastic/kibana | 52 | 14.9% | 1.29× | 29.0% | 1.06× |
| vercel/next.js | 30 | 11.5% | 0.87× | 21.3% | 1.10× |

## what the fit picked

- **Kibana:** `.buildkite/**` (its CI configuration) in four of seven
  months. It carried over: in the test months, CI-config PRs were reverted
  about 3.5× as often as the repo's average (5 of about 415 weighted PRs).
- **Airflow:** `task-sdk/**` and `providers/google/**`, folders under
  heavy change. Each caught one later revert.
- **next.js:** mostly single files (`base-server.ts`, `build/index.ts`,
  `app-render.tsx`) with 3–5 reverts each. These did not carry over: last
  quarter's trouble files weren't next month's.

The same test on Sentry, Grafana and n8n (counts toward nothing): fitted
lift 0.89× pooled, below the defaults' 1.17×. In Sentry, where it fitted
every month, it chased last quarter's hot spots (a feature area under
heavy development) that had moved on by the next month.

## what this means

Fitting helped where a repo has a stable kind of trouble (Kibana's CI
config) and did nothing where trouble moves around with whatever is under
active development (next.js, Sentry). Across the pooled test it made a
pen half the size of the defaults' with a higher hit rate, but the
difference is within noise, so it can't be claimed.

## rerun

    cowboy-fitlab -sorted sorted_v3.jsonl -replay replay_v3 -collectlog ... -out fresh
    python3 compare_fit.py fresh.prs.jsonl

Aggregates: `data/fresh-fit.json`.
