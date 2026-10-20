# results for the lines-budget test

Claims: CLAIMS-lines.md (commit 4371379), written with compare_lines.py
before any repo below was cloned. Repos chosen by its screen, in the
written order: elastic/elasticsearch (62 reverted PRs in the window),
microsoft/vscode (122), home-assistant/core (35). The first three
candidates all passed, so no others were cloned. All three count
(engine-found reverted PRs in the sample: 58, 112, 35). Rules: v0.7.0,
unchanged.

## short version: careful beats the two size rules, and loses to chance

Every method gets the same number of reviewed lines as Cowboy's careful
pen.

| claim | result | 95% range | verdict |
|---|---|---|---|
| 1. careful beats largest-first | careful catches 18.2% of reverted PRs, largest-first 0.0% (+18.2 points) | +11.5 to +23.9 | **passes** |
| 2. careful beats most-files-first | 18.2% vs 7.7% (+10.5 points) | +5.5 to +16.5 | **passes** |
| 3. careful beats chance | 0.46× the repos' reverts per line read | 0.30× to 0.70× | **fails the other way**: the whole range is below 1, so careful is shown to do *worse* than random per line |

Also reported (counts toward nothing): **smallest first** catches 97.3% of
reverted PRs with the same lines, against careful's 18.2% (−79.1 points,
range −84.7 to −66.0).

**Claims 1 and 2 pass, but they are not a win worth claiming.** With a
lines budget, "largest first" spends the whole budget on a handful of
huge PRs (3 in Elasticsearch, about 50 in the others), so it is close to
a degenerate alternative, and beating it says little. "Most files first"
has the same weakness. The
honest reading is claim 3 and smallest-first: for each line a reviewer
reads, the careful pen finds *fewer* reverted PRs than picking PRs at
random, and far fewer than reading the smallest PRs first.

## why: reverts happen per pull request, not per line

Pooled over the three repos, weighted to their real mix of PRs:

| PR size | share of PRs | share of lines | reverted within 30 days | reverts per 10,000 lines |
|---|---|---|---|---|
| under 10 lines | 25.8% | 0.3% | 0.53% | 11.80 |
| 10–49 | 23.5% | 1.4% | 0.70% | 2.78 |
| 50–199 | 23.5% | 6.2% | 0.46% | 0.43 |
| 200–999 | 20.8% | 22.7% | 0.85% | 0.19 |
| 1,000+ | 6.3% | 69.4% | 0.68% | 0.02 |

A pull request's chance of being reverted barely changes with size, while
lines change a thousandfold. So any budget counted in lines favors the
smallest PRs, and the careful pen, which leans toward big PRs (11–19% of
PRs but 32–49% of the lines), is at a disadvantage built into the
measure.

One caveat a skeptic should raise: reverts are only one kind of trouble,
and big PRs are harder to revert, so they may be fixed forward instead.
Counting only reverts may understate trouble in big PRs. This test can't
tell; "needed a fix" outcomes are too sparse in these repos to say.

## checks

An independent recomputation, written from the claims file without
reusing compare_lines.py, matched every pooled and per-repo figure to four
decimals, and its own bootstrap gave ranges within a point of these.
It also found:

- **Very large PRs.** Elasticsearch has PRs of 325,690 and 269,155 lines
  (the second is careful); weighted, that one PR is about 44% of
  Elasticsearch's budget. Capping every PR at 5,000 lines gives 0.55×
  chance per line instead of 0.46×; dropping PRs over 20,000 lines gives
  0.54×. The direction of every result holds.
- **Repeated PR numbers.** 9 extra rows across 4 PR numbers (several
  commits tagged with the same `(#N)`, likely backports). None had
  trouble; dropping them changes nothing.
- **Trouble outside the weighting strata.** 4 PRs the engine found
  reverted weren't in the collector's revert set (so they carry the
  larger weight), and 18 in the set weren't counted as trouble (most
  likely reverted after 30 days). Counting only PRs in both gives careful
  17.4% and 0.44× chance per line: same conclusions.
- In the data files, the per-repo field named `reverted` is weighted
  trouble, not a count of reverts.

**Timing.** CLAIMS-lines.md and compare_lines.py were committed at
20:14:51 (−04:00), pushed to GitHub straight away; the first repo was
cloned at 20:15:09. That is seconds, in the same session, and commit
times are set locally, so the pushed commit on GitHub is the outside
record. The early look at the spent repos (below) ran after the commit
and before the fresh data, and changed nothing.

## the pull-request-count test on the same repos (counts toward nothing)

The v3 claims, same budget as before (the same number of PRs):

| | result | 95% range |
|---|---|---|
| careful vs largest-first | 18.2% vs 18.0% of reverted PRs | −7.0 to +8.1 points |
| careful vs most-files | 18.2% vs 17.2% | −5.2 to +6.5 points |
| careful vs chance | 1.30× | 0.90× to 1.75× |
| easy vs the same share of smallest | −0.08 points | −0.20 to +0.02 |

The same picture as the three earlier rounds: careful leans the right way
and doesn't clear the bar; the easy pen holds.

## per repo (same lines as careful)

| repo | careful's share of PRs | of lines | careful | largest | most files | smallest | random |
|---|---|---|---|---|---|---|---|
| elastic/elasticsearch | 11.0% | 31.7% | 14.8% | 0.0% | 8.2% | 91.8% | 31.7% |
| home-assistant/core | 18.7% | 49.0% | 25.7% | 0.0% | 14.3% | 100% | 49.0% |
| microsoft/vscode | 10.7% | 45.5% | 17.6% | 0.0% | 5.5% | 99.2% | 45.5% |

## what this means

- Measured by lines read, Cowboy's careful pen does not earn its place:
  it beats rules that only look at size, and does worse than random
  choice. Careful takes 11–19% of PRs but 32–49% of the lines, so it is
  an expensive pen for what it finds.
- The finding with the most use to a team isn't about Cowboy's rules:
  in these repos a 10-line PR was about as likely to be reverted as a
  1,000-line one. If review time tracks lines read, a quick look at
  every small PR finds more of what later gets reverted than a deep look
  at a few big ones.
- That matches the one claim that has held in every round: the easy pen
  is as safe as the smallest PRs, no more and no less.

## also run on the spent repos (counts toward nothing)

next.js, Airflow and Kibana (the v3 data), with compare_lines.py after it
was committed: careful 25.7% vs largest-first 2.1% and most-files 4.3%,
0.50× chance per line (0.35× to 0.69×), smallest-first 91.4%. Same
picture.

## rerun

    python3 compare_lines.py sorted.jsonl replay elastic_elasticsearch.collect.log microsoft_vscode.collect.log home-assistant_core.collect.log

Aggregates: `data/fresh-lines.json`, `data/fresh-lines-count.json`,
`data/spent-lines.json`.
