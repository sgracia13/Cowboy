# results for rules v3 on fresh repos

Claims: CLAIMS-v3.md (commit ec37336), written before any repo below was
cloned. Repos chosen by its screen: vercel/next.js (48 reverted PRs in the
window), apache/airflow (35), elastic/kibana (68). cal.com, mattermost,
react and langchainjs were screened out for too few reverts. All three
kept repos count (engine-found reverted PRs in the sample: 46, 32, 62).

## short version: careful still doesn't beat the simple alternatives

| claim | v3 result | 95% range | verdict |
|---|---|---|---|
| 1. careful beats size | careful holds 25.7% of reverted PRs, the largest PRs 20.7% (+5.0 points) | −3.5 to +13.0 | not shown |
| 2. careful beats spread | 25.7% vs 27.9% for most files touched (−2.1 points) | −9.6 to +6.3 | not shown |
| 3. careful beats chance | 1.08× the repos' revert rate | 0.78× to 1.39× | not shown |
| 4. easy at least as safe as smallest | −0.02 points | −0.13 to +0.10 | holds |

v2 on the same repos, for comparison (counts toward nothing):

| claim | v2 result | 95% range |
|---|---|---|
| 1. vs size | +8.6 points (32.1% vs 23.6%) | −1.4 to +17.3 |
| 2. vs spread | +2.1 points | −5.8 to +11.5 |
| 3. vs chance | 1.11× | 0.84× to 1.39× |
| 4. easy vs smallest | −0.02 points | −0.13 to +0.10 |

The v3 changes made the careful pen smaller in Airflow (26% → 14%) and
Kibana (32% → 29%), slightly larger in next.js (18% → 20%), and not better:
v3 does no better than v2 here.

## per repo (weighted rates, reverted within 30 days)

| repo | easy | normal | careful | all | sent to careful (v3) | careful lift |
|---|---|---|---|---|---|---|
| apache/airflow | 0.29% | 0.50% | 0.72% | 0.45% | 13.7% | 1.60× |
| elastic/kibana | 0.48% | 0.27% | 0.33% | 0.34% | 28.8% | 0.95× |
| vercel/next.js | 0.94% | 1.34% | 1.71% | 1.28% | 19.6% | 1.33× |

In Airflow and next.js the pens line up the way they should (easy lowest,
careful highest), though not by enough to clear the bar. In Kibana they
don't: its easy pen had the highest revert rate of the three, and its
easy rate was a little above the same share of smallest PRs (0.48% vs
0.42%; no per-repo range was computed). Claim 4 holds pooled; in Kibana
alone the point estimate went the wrong way.

## what this means

Across eight repos now (five tuned-on or spent, three fresh), the default
careful pen has not been shown to beat "biggest PRs first" or chance in
any pre-registered test. The easy-pen claim held pooled in every test,
though in Kibana alone the estimate went the wrong way.

## a note on timing

CLAIMS-v3.md was committed 19 seconds before the first clone. The
collector's screen-only switch (a few lines that stop after printing the
counts) was committed a minute after the screen started; the screen ran
that same code.

## rerun

    python3 compare.py sorted_v3.jsonl replay_v3 vercel_next.js.collect.log apache_airflow.collect.log elastic_kibana.collect.log

Aggregates: `data/fresh-v3.json`, `data/fresh-v2.json`.
