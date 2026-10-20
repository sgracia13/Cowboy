# claims for the lines-budget test, written before testing

Written October 19, 2026, before any repo below was cloned or looked at.
The commit that adds this file is the timestamp. Results go in
RESULTS-lines.md whichever way they come out.

## why this test, and what's honest about it

Every earlier test gave each method the same **number of pull requests**
to flag. On that measure, Cowboy's careful pen did not beat "largest
first", "most files touched" or chance (RESULTS-v3.md).

Review effort is closer to **lines read** than to pull requests counted:
flagging one 3,000-line PR costs a reviewer far more than flagging three
50-line ones. This test gives every method the same number of reviewed
lines instead.

This measure was chosen **after** the pull-request-count tests failed to
show a win. That is a reason to be suspicious of a positive result, so:
the rules are not changed for this test, the repos are new, the claims and
pass marks are written down here first, and the result is published next
to the earlier ones whichever way it goes.

## the rules under test

The default rules exactly as released in v0.7.0-preview (rules v3; commit
95cef35 for the app, the same sorting code as the v3 test). Nothing is
tuned for this test.

## which repos

None of the eight repos used so far (sentry, grafana, n8n, posthog,
supabase, next.js, airflow, kibana), and none of the four screened out
before (cal.com, mattermost, react, langchainjs).

Candidates, in this order: elastic/elasticsearch, microsoft/vscode,
home-assistant/core, apache/kafka, go-gitea/gitea,
huggingface/transformers, temporalio/temporal, microsoft/TypeScript.

Screen each in order exactly as CLAIMS-v3.md did: count PRs merged
November 1, 2025 to September 15, 2026 that a later `Revert "..."` commit
undid (collect_sort.py's screen; repos that don't squash-merge with a
`(#N)` suffix count 0). Keep a repo if that count is 30 or more. Stop at
the first three kept. The screen looks at revert counts only, never at
pens or sizes. If fewer than three pass, use those that do and say so.

## data and method

Collection, sorting, after-merge replay and weighting are the same as the
v3 test (collect_sort.py with every reverted PR plus a random 2,500 of the
rest, sortlab with a checkout for blast radius, cowboy-replay). "Had
trouble" = reverted within 30 days per the engine. A repo counts if it has
20 or more engine-found reverted PRs in its sample. Claims are judged on
the counted repos pooled.

**Budget.** B = the careful pen's reviewed lines: the weighted sum of
`lines` (the collector's changed-line count, the same number "largest
first" sorts by) over the PRs Cowboy put in careful.

**Alternatives, each given the same B lines:**

- **largest first**: PRs from the largest down, until their weighted lines
  reach B.
- **most files first**: PRs by files touched (ties: more lines first),
  until their weighted lines reach B.
- **random**: the expected result of picking PRs at random until B lines
  are reached, which catches B ÷ (all lines) of the PRs with trouble.

**Caught** = the weighted share of all PRs with trouble that a method's
picks contain.

Ranges are 95% from 2,000 bootstrap resamples, drawn within each repo and
within reverted / not reverted (seed 2026), as before. Script:
compare_lines.py, written before the data is collected.

## claims

1. **Careful beats largest-first**, for the same lines: the 95% range of
   (careful caught − largest-first caught) is above zero.
2. **Careful beats most-files-first**, for the same lines: the range of
   (careful caught − most-files caught) is above zero.
3. **Careful beats chance**, for the same lines: the range of careful's
   trouble per line reviewed ÷ the repos' trouble per line is above 1.

A claim passes only if its whole range clears the line. "Not shown" means
no demonstrated advantage, which is not the same as no effect.

## also reported, counting toward nothing

- **Smallest first** for the same lines. Under a lines budget, reviewing
  the smallest PRs first covers the most PRs, so it may be the toughest
  simple alternative. If it beats careful, the evidence page says so.
- The pull-request-count comparison (the v3 claims) on these repos.
- Careful's share of PRs and of lines, per repo.
