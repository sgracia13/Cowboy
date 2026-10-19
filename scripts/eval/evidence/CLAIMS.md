# claims, written before running the comparison

Written October 19, 2026, before the comparison below was run. The commit
that adds this file is the timestamp. Results go in RESULTS.md, whichever
way they come out.

## data

7,721 merged pull requests sampled at random from getsentry/sentry,
grafana/grafana and n8n-io/n8n (merged November 2025 to September 2026),
each sorted by Cowboy's current rules (v2: careful = sensitive path, 20+
importers, careful shortcuts, failing checks; size is a note, not a rule).
"Had trouble" = reverted within 30 days of merge, as found by Cowboy's
after-merge engine from each repo's default-branch history. Suggested
fixes are left out (unconfirmed).

A repo with fewer than 20 reverted PRs in the sample is reported but not
counted toward the claims (too few events to say anything).

## the alternatives

In each repo, flag the same number of PRs Cowboy put in careful, chosen by:

- size: the most lines changed
- spread: the most files touched
- chance: at random (10,000 draws)

and for the easy pen, the same number of PRs with the fewest lines changed.

## claims

1. Careful beats size. Cowboy's careful pen contains more of the reverted
   PRs than the same number of largest PRs. Clear if the 95% bootstrap
   interval of the difference (pooled over counted repos) is above zero.
2. Careful beats spread. Same, against the most files touched.
3. Careful beats chance. Careful's revert rate is above the repo's overall
   rate, with the 95% interval of the ratio above 1.
4. Easy is at least as safe as smallest. Easy's revert rate is no higher
   than the same number of smallest PRs (interval of the difference
   includes or is below zero).

If a claim fails, the page says so.
