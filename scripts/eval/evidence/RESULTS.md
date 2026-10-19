# results

The claims are in CLAIMS.md, committed (b9251e0) before anything was run.
This file reports what came out, including the claims that failed.

## short version

| claim | result | 95% range | verdict |
|---|---|---|---|
| 1. careful beats size | careful holds 31.8% of reverted PRs, the largest PRs 24.7% (+7.1 points) | −1.9 to +17.1 points | **not shown** |
| 2. careful beats spread | 31.8% vs 27.1% for the most files touched (+4.7 points) | −3.9 to +12.6 points | **not shown** |
| 3. careful beats chance | careful PRs are reverted 1.34× as often as the repo overall | 1.07× to 1.63× | holds |
| 4. easy at least as safe as smallest | easy's revert rate is 0.14 points *lower* than the same share of smallest PRs | −0.29 to +0.01 points | holds |

Counted: getsentry/sentry (174 reverted PRs in the sample) and
grafana/grafana (40). n8n-io/n8n had 1 reverted PR in the sample, so it is
reported but not counted, as CLAIMS.md said.

What this supports, on the repos the rules were tuned on: Cowboy's careful
pen is where reverts gather more than chance would put them, and its easy
pen is at least as safe as simply picking the smallest PRs. On two repos
the rules never saw, only the easy-pen claim held (see "held-out"). What it does not support, yet: that the rules do
better than a plain size or file-count cutoff at finding reverts. The
direction favours Cowboy in both repos, but the ranges include zero.

## real-world rates per pen

Weighted back to each repo's actual mix of PRs (see "deviation" below).

| repo | easy | normal | careful | all PRs |
|---|---|---|---|---|
| getsentry/sentry | 0.78% | 1.15% | 1.94% | 1.18% |
| grafana/grafana | 0.33% | 0.49% | 0.56% | 0.47% |

Reverts are rare in both repos. The careful pen's rate is about 2.5× the
easy pen's in Sentry and 1.7× in Grafana.

## deviation from the written plan

CLAIMS.md says the PRs were "sampled at random". They were not: the
collection script keeps every PR that was later reverted plus a random
sample of the rest. Compared naively, this makes reverts look about five
times more common than they are and inflates every rate.

Found after the claims were committed and the first (unweighted) run was
done. The fix: each non-reverted PR counts as (PRs in window − reverted) /
(non-reverted PRs sampled) PRs, and each reverted PR counts once:

| repo | PRs in window | reverted (all kept) | non-reverted sampled | weight |
|---|---|---|---|---|
| getsentry/sentry | 16,180 | 172 | 2,500 | 6.40 |
| grafana/grafana | 9,702 | 48 | 2,500 | 3.86 |
| n8n-io/n8n | 7,941 | 0 (1 kept by the collector's broader match) | 2,500 | 3.18 |

The collector's revert match and the after-merge engine's differ slightly
(the engine finds 174 in the Sentry sample, 40 in Grafana's, 1 in n8n's);
the claims are scored on the engine's, as CLAIMS.md says.

"Flag the same number of PRs" becomes "flag the same weighted share of the
repo's PRs". Chance is the share itself (the expected value of the 10,000
random draws CLAIMS.md describes), not a simulation. The bootstrap resamples
reverted and non-reverted PRs separately (2,000 draws, seed 2026).

The unweighted run (appendix) said claim 1 held. The weighted run says it
does not. The weighted run is the right one, and this page reports it.

## what this doesn't show

- **In-sample.** The v2 rules were adjusted while looking at these same
  three repos. On two repos they never saw (below), claims 1–3 did not
  replicate.
- **Reverts only.** Breaks, follow-up fixes and incidents need data a public
  repo doesn't keep (check runs on old commits, a team's confirmations, a
  monitoring tool).
- **No live team.** Nobody reviewed differently because of a pen. This
  measures whether the pens point at the risky PRs, not whether a team
  using Cowboy ships fewer reverts.
- **Causation.** A careful PR may be reverted more because it was risky, or
  because it got more scrutiny after merge. Rates describe; they don't
  explain.

## held-out replication: it did not replicate

PostHog/posthog and supabase/supabase, collected and scored the same way
(same rules, same scripts, same weighting), never looked at while writing
the rules. Under the CLAIMS.md threshold only Supabase counts (25 reverted
PRs in the sample); PostHog has 18 and is reported only.

| claim | supabase/supabase | 95% range | verdict |
|---|---|---|---|
| 1. careful vs size | careful holds 12% of reverted PRs, the largest PRs 16% (−4 points) | −24 to +13 points | not shown |
| 2. careful vs spread | 12% vs 12% (0 points) | −19 to +19 points | not shown |
| 3. careful vs chance | 1.21× the repo's rate | 0× to 2.59× | not shown |
| 4. easy vs smallest | −0.05 points | −0.15 to +0.05 points | holds |

Real-world rates per pen (weighted):

| repo | easy | normal | careful | all PRs | share sent to careful |
|---|---|---|---|---|---|
| PostHog/posthog (not counted) | 0.05% | 0.13% | 0.08% | 0.10% | 38% |
| supabase/supabase | 0.48% | 0.46% | 0.58% | 0.48% | 10% |

What this says: on repos the rules were not tuned on, the careful pen did
not gather reverts better than chance or than a size cutoff. In PostHog the
careful pen was below the repo average and held 38% of all PRs, which is
too many to be a useful "look here". The easy pen held up in both: it was
never worse than the same share of smallest PRs.

The honest reading of everything so far: the easy pen is a safe place to
skim; the careful pen is not yet shown to beat a plain size rule, and
whether it beats chance depends on the repo. The next step is rules that
travel better, checked on fresh held-out repos again. The first lead: in
PostHog, the "adds a catch-all that may hide errors" shortcut fired on 257
of 968 careful PRs (Python `except Exception` is common there), and the
"warning silencer" shortcut fired on routine `# noqa` and `# type: ignore`
comments. Those two
rules alone fill much of the pen.

## appendix: the unweighted run

Kept for the record. Sentry + Grafana pooled, every sampled PR counted once
(5,220 PRs, 214 reverted, careful = 1,350):

| claim | result | 95% range |
|---|---|---|
| 1. vs size | 75 vs 50 reverted PRs (+25) | +9 to +43 |
| 2. vs spread | 75 vs 62 (+13) | −3 to +30 |
| 3. vs chance | 1.36× | 1.13× to 1.60× |
| 4. easy vs smallest | −0.65 points | −1.42 to +0.06 |

## rerun

    python3 scripts/eval/evidence/compare.py SORTED.jsonl REPLAY_DIR COLLECT_LOGS... > results.json

SORTED.jsonl comes from collect_sort.py and sortlab; REPLAY_DIR from
cowboy-replay (its outcomes.json per repo); the collect logs carry the
"N PRs in window, R later reverted" lines the weights need.
