# claims for fitted rules, written before building or running the fit

Written October 19, 2026, before `cowboy fit` existed and before any fit was
run on any repo. The commit that adds this file is the timestamp. Results go
in RESULTS-fit.md whichever way they come out.

## the idea

Instead of one set of careful rules for every repo, Cowboy looks at a
repo's own recent history (which merged PRs later had trouble) and picks
the few facts that went with trouble there. The rules it picks are fixed
and shown with their numbers until the next fit. If there isn't enough
history, it says so and keeps the default rules.

## how a fit works (the method being tested)

Facts about each PR, all things Cowboy already measures:

- `path:<dir>/**` for each directory prefix, one to four levels deep, of
  each changed source or config file
- `file:<path>` for each changed source file
- `sensitive`, `blast` (the v3 blast-radius rule), `shortcut:<kind>` for
  each of the six shortcut kinds, `agent` (agent-authored), `big` (400+
  source lines)

Fitting, on the PRs merged in the 90 days before the fit date:

1. A PR "had trouble" if it was reverted within 30 days of merge, counting
   only reverts that happened before the fit date (what a live repo would
   know).
2. If fewer than 8 PRs had trouble: not enough history, keep the defaults.
3. For each fact: PRs with it (n) and those with trouble (k). Skip it if
   k < 3. Its cautious rate is the lower end of a one-sided 90% Wilson
   interval; its cautious lift is that ÷ the repo's trouble rate. Keep it
   if the cautious lift is 1.5 or more.
4. Take kept facts in order of cautious lift (ties: more trouble, then
   name), skipping any whose PRs are all already covered, and stop adding
   once the chosen rules would cover more than 15% of PRs. At most 10.
5. If nothing is kept: keep the defaults.

A fitted pen is: careful if a PR has any chosen fact (or failing checks).
It replaces the default careful rules for that repo until the next fit.

## the test: walk forward, month by month

Repos: vercel/next.js, apache/airflow, elastic/kibana (chosen by the v3
screen in CLAIMS-v3.md, before any pen was looked at). Same collection,
replay and weighting as CLAIMS-v3.md.

For each test month February through August 2026, fit on the 90 days
before the month starts, then sort that month's PRs with the fitted rules.
Pool every test month of every repo. "Had trouble" in a test month =
reverted within 30 days, per the after-merge engine.

## claims

1. Fitted beats biggest-first. The fitted careful pen holds more of the
   later-reverted PRs than the same share of largest PRs (per repo-month).
   Clear if the 95% range of the difference is above zero.
2. Fitted beats chance. The fitted pen's revert rate ÷ the overall rate has
   a 95% range above 1.
3. Fitted beats the defaults. The fitted pen's lift minus the v3 default
   careful pen's lift, same PRs, has a 95% range above zero.
4. The fitted pen stays small: under 20% of PRs in every repo.

Ranges: bootstrap, 2,000 draws, seed 2026, resampling test PRs within each
repo × month × reverted group. The fitted rules are held fixed in the
bootstrap, so the ranges don't include the wobble of refitting.

## also reported, counting toward nothing

- How many repo-months had enough history to fit.
- The rules chosen each month, with their numbers.
- The same test on the five spent repos.

If a claim fails, the evidence page says so.
