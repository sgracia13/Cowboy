# claims for the practices test, written before testing

Written October 20, 2026, before any practice was measured on any repo.
The commit that adds this file is the timestamp. Results go in
RESULTS-practices.md whichever way they come out.

## the question

Do the habits engineers use to keep changes safe actually go with fewer
reverted pull requests? If some do, Cowboy can show a team how often it
uses the habits that hold up in its own repo, without asking who (or
what) wrote the code.

This asks which habits *go with* fewer reverts, not which *cause* fewer:
careful engineers do several careful things at once, and none of this
can separate them.

## the practices (all read from git history)

Among merged pull requests that change source code (the collector's file
classes: at least one source file):

1. **Tests with code.** The pull request also changes at least one test
   file.
2. **No shortcuts.** None of Cowboy's shortcut checks fired: no silenced
   warnings, skipped tests, catch-alls that hide errors, loosened CI,
   removed test files or asserts, or test-only special cases.
3. **Links an issue.** The merge commit's message, past its first line,
   names an issue: "fixes/closes/resolves #N", a bare "#N" other than the
   pull request's own number, or a link to an issue tracker
   (`/issues/N`, a Jira key like ABC-123).

Reviews, approvals and checks passing before merge aren't in git history,
so they can't be tested this round.

Practice 3 depends on how each repo writes merge messages (some keep the
pull request description, some don't), so it is measured only in repos
where at least 20% of source pull requests link an issue; elsewhere it's
reported as "not measurable here".

## which repos

Fresh: none of the eleven used so far, and none screened out before.
Candidates, in this order: apache/kafka, go-gitea/gitea,
huggingface/transformers, temporalio/temporal, microsoft/TypeScript,
grafana/loki, hashicorp/terraform-provider-aws, prefecthq/prefect.
Screen exactly as CLAIMS-lines.md: keep a repo with 30 or more reverted
pull requests merged November 1, 2025 to September 15, 2026; stop at the
first three kept; the screen looks at revert counts only.

The six repos already collected (next.js, Airflow, Kibana,
Elasticsearch, VS Code, Home Assistant) are reported too, counting toward
nothing: their practices were never looked at, but the repos were used
for other claims.

## method

Same collection, sorting, after-merge replay and weighting as before
(collect_sort.py with every reverted pull request plus a random 2,500 of
the rest; sortlab; cowboy-replay). "Reverted" = reverted within 30 days
per the engine. A repo counts if it has 20 or more engine-found reverted
pull requests among its source-changing pull requests.

**Size is held fixed.** Pull requests that follow a practice can differ
in size from those that don't, so each comparison is made within size
bands (changed lines: under 10, 10–49, 50–199, 200–999, 1,000+) and
within each repo, then combined: a Mantel-Haenszel risk ratio (revert
rate with the practice ÷ without), weighted to each repo's real mix of
pull requests.

Ranges are 95% from 2,000 bootstrap resamples drawn within each repo and
within reverted / not reverted (seed 2026). Script: compare_practices.py,
written before the data is collected.

## claims

For each practice, on the counted fresh repos pooled:

- **Holds:** the whole 95% range of the size-adjusted risk ratio is
  below 1 (pull requests with the practice were reverted less often).
- **Did worse:** the whole range is above 1.
- **No demonstrated difference:** otherwise.

A practice that holds is a candidate for Cowboy's practice report. One
that doesn't is reported as such, not dropped quietly.

## also reported, counting toward nothing

- Each practice per repo, and its share of source pull requests.
- The same on the six spent repos.
- The size-unadjusted risk ratio, to show what adjusting changed.

## amendment 1 (October 20, 2026, after the screen, before any practice was measured)

None of the eight candidates passed the screen: reverted pull requests in
the window were kafka 4, gitea 2, transformers 15, temporal 14,
TypeScript 1, loki 1, terraform-provider-aws 0, prefect 0. The screen
looks at revert counts only; no practice was measured on any of them.

So the candidate list is extended, in this order, with repos chosen for
busy history and squash merges (nothing else about them was looked at):
pytorch/pytorch, ray-project/ray, elastic/beats, mastodon/mastodon,
microsoft/playwright, langchain-ai/langchain, apache/superset,
mlflow/mlflow. Same screen, same rule: keep 30 or more, stop at the first
three kept, use whatever passes. Nothing else in this file changes.
