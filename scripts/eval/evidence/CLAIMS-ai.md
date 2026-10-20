# claims for the AI risk test, written before any model scored anything

Written October 20, 2026, before any real model scored a pull request.
The commit that adds this file is the timestamp. Results go in
RESULTS-ai.md whichever way they come out. **There are no results yet.**

## the question

Does a local model, reading only a pull request's title, description and
diff, find the pull requests that later had trouble better than Cowboy's
rules and simple alternatives, at the same review budget? And does it add
anything on top of the rules?

The model never decides a pen. This is an offline test on public history;
nothing in a live install reads these scores.

## what each method sees and flags

| method | sees at prediction time | flags |
|---|---|---|
| A. rules | the diff, file paths, the import graph (as `cmd/cowboy-sortlab`) | the careful pen |
| B. model | the merge message (title + description) and the diff, cut at 12,000 characters | its top-scored PRs: the same share of PRs as careful; second, the same lines read as careful |
| C. alternatives | lines and files changed | largest first, most files first, chance; smallest first for the lines budget |
| D. rules + model | the rules' pens and the model's scores | careful unchanged, plus the model's top 5% of PRs from those careful didn't flag |

The model is asked for an integer risk from 0 to 100 (`model.Risk`,
prompt version printed by `cowboy-ailab`). Ties are broken by a hash of
the PR, never by size. A reply without a valid integer score is a
failure: no score is guessed.

## outcome

Trouble = a revert within 30 days of merge, found in git history by
`cmd/cowboy-replay` (the same engine and rule as every earlier round). A
pull request with no revert is not proven safe; it's only not counted as
trouble. Follow-up fixes are too sparse in public repos to use.

## who is compared

- Every method is judged on exactly the same PRs: those with a model
  score. PRs the model failed on, or that weren't scored, are left out of
  every method and reported, with how many of them had trouble.
- PRs merged on or before the model's published training cutoff are left
  out of every method: the model may have seen them and their reverts.
  `cowboy-ailab compare` refuses to run without `-model-cutoff`.
- A repo counts only with 20 or more PRs with trouble among its scored,
  eligible PRs (as in every round). With no counted repo, every verdict is
  "not enough evidence yet".

## leakage controls

- The scoring step decodes only repo, number, merge time, title, message
  and diff from each collected PR. Outcome fields in the same file are
  never read (`TestScoreIgnoresOutcomeFields` checks the request sent is
  byte-identical with and without them).
- The prompt never includes the repo name, PR number or commit SHA.
- Scores and outcomes meet only in `cowboy-ailab compare`.
- Each score records the model asked for and the model the server says
  answered, the prompt version, temperature 0, seed 7, a SHA-256 of the
  exact input, its size, whether the diff was cut, the time taken, any
  error, and when it was produced.

## data

- **Development set**, for writing and changing the prompt: Sentry,
  Grafana, n8n (already used in earlier rounds). Any prompt change gives a
  new prompt version.
- **Test set**: three repos never used in any earlier round, picked by the
  collector's screen (most reverted PRs in the window) before any is
  scored, with the PR window entirely after the model's training cutoff.
- Before the test set is scored, a follow-up commit to this file records:
  the model and its exact build, its training cutoff, the prompt version,
  and the three repos. Nothing else in this file changes after that.

## claims and verdicts

Same bootstrap as every round: 2,000 draws, seed 2026, resampling within
repo and sampling stratum, 95% ranges.

1. **B outperforms** only if the whole range of (model recall − careful
   recall) at the same share is above 0, **and** the whole range of
   (model recall − chance) is above 0.
2. **D adds value** only if, at the extra 5%, the whole range of the
   model's extra catches minus each alternative's (largest, most files,
   random) is above 0, for all three.
3. Otherwise the verdict is "no demonstrated advantage", "did worse"
   (B: model − careful range below 0; D: model − random range below 0),
   or "not enough evidence yet".
4. Reported, not claims: model vs largest and most files at the same
   share; model vs careful and smallest first at the same lines read.
5. **Practical bar**: under 1% of attempted PRs fail to score. Cost is
   reported as median and 95th-percentile seconds per PR and total hours.

## what this can't show

Reverts are one kind of trouble; most reverts in some repos are for
breaking tests or internal builds, which a diff alone may not show. A model
that "did worse" or showed "no demonstrated advantage" here could still
help a reviewer in other ways (pointing at the right file); that isn't
tested. Results describe what went together, not what caused what.

## plumbing check (not evidence)

To check the pipeline end to end, the development set (7,721 PRs) was
scored by `scripts/eval/ai/hash_model.py`, a stand-in server whose "risk"
is a hash of the prompt: it knows nothing. As it should, it showed no
difference from chance (−0.7 points, likely −7.8 to +7.3), both headline
verdicts were "no demonstrated advantage", and the rules' and
alternatives' numbers matched `cowboy evaluate` on the same data exactly
(careful 38.1%, largest 32.7%, most files 37.8%, chance 30.1%, 214 PRs
with trouble). This says the measuring works; it says nothing about any
real model.

## rerun

    python3 scripts/eval/collect_sort.py REPO_DIR owner/name prs.jsonl CHECKOUT   # per repo, as before
    cowboy-sortlab -in prs.jsonl -out sorted.jsonl
    cowboy-replay  -sorted sorted.jsonl -patches prs.jsonl -git owner/name=/path/repo.git -out replay/
    cowboy-ailab score   -in prs.jsonl -out ai.jsonl -model-url http://localhost:11434 -model <model>
    cowboy-ailab compare -sorted sorted.jsonl -replay replay/ -logs a.log,b.log,c.log -ai ai.jsonl -model-cutoff YYYY-MM-DD
