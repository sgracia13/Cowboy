# claims for rules v3, written before testing them

Written October 19, 2026, after the rules changed (commit a8db2ea) and
before any repo below was cloned or looked at. The commit that adds this
file is the timestamp. Results go in RESULTS-v3.md whichever way they come
out.

## what changed (v2 → v3)

- Blast radius: a changed file must be among the repo's 1% most-imported
  files, as well as having 20+ importers.
- Catch-all: a broad catch counts only when its body does nothing with the
  error (pass, continue, return nothing, an empty block) or is a bare
  Python `except:`.

Both were chosen from what each rule says it does. The five repos used so
far (sentry, grafana, n8n, posthog, supabase) informed the changes and are
spent: they are reported for the record but count toward nothing.

## which repos

Candidates, in this order: vercel/next.js, calcom/cal.com, apache/airflow,
mattermost/mattermost, facebook/react, langchain-ai/langchainjs,
elastic/kibana.

Screen each in order on its default-branch history only: count PRs merged
November 1, 2025 to September 15, 2026 that a later `Revert "..."` commit
undid (collect_sort.py's count). Keep a repo if that count is 30 or more.
Stop at the first three kept. The screen looks at revert counts only,
never at pens. If fewer than three pass, use those that do and say so.

## data and method

Same as CLAIMS.md and the weighted method in RESULTS.md: collect_sort.py
(every reverted PR plus a random 2,500 of the rest), sortlab with a
checkout for blast radius, cowboy-replay for after-merge outcomes, then
compare.py. "Had trouble" = reverted within 30 days per the engine. A
repo counts toward the claims if it has 20+ engine-found reverted PRs in
its sample. Claims are judged on the pooled kept repos.

## claims (same pass marks as before)

1. Careful beats size: the 95% range of (careful's share of reverted PRs
   − the same share of largest PRs) is above zero. This is the one that
   decides whether the careful pen does something a size rule doesn't.
2. Careful beats spread: same, against most files touched.
3. Careful beats chance: the 95% range of careful's revert rate ÷ the
   repos' rate is above 1.
4. Easy is at least as safe as smallest: the 95% range of the difference
   in revert rate includes zero or is below it.

## also reported, counting toward nothing

- v2 rules on the same fresh repos, side by side.
- v3 rules on the five spent repos.
- Careful's share of PRs per repo.

If a claim fails, the evidence page says so.
