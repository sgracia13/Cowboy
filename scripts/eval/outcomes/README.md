# revert detection on real history

Cowboy counts a revert against the PR it undid. It reads that from the
commit message on the default branch: GitHub's revert button (`Revert "Title
(#123)"`, `Reverts owner/repo#123`), `git revert` ("This reverts commit
abc1234"), a revert branch (`revert-123-…`), a PR named in the revert title
("(#123)", "from #123"), or, when nothing else names it, the quoted title
matched against earlier PR titles. Only commits whose title says revert count:
a squash commit's body can list a commit reverted inside the PR's own branch,
which isn't a revert of a merged PR, and a commit never counts against its own
PR.

## results (October 19, 2026)

Every commit on the default branch from October 2024, from the n8n-io/n8n,
grafana/grafana and getsentry/sentry histories.

| | n8n | grafana | sentry |
|---|---|---|---|
| commits | 13,945 | 20,605 | 38,402 |
| reverts | 10 | 124 | 520 |
| traced to the PR they undid | 7 (70%) | 108 (87%) | 514 (98.8%) |

A sample of traced reverts was checked by hand: each pointed at the PR whose
title it quoted. The ones not traced are free-text reverts that name no PR,
title or commit ("revert 1.26", "Revert setting extrapolation mode to …").

## run it

```sh
git -C sentry log HEAD --since=2024-10-01 --format='%H%x00%B%x1e' > sentry.gitlog
COWBOY_GITLOG=sentry.gitlog go test ./internal/outcome -run RealHistory -v
```
