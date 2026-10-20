# results for the practices test

Claims: CLAIMS-practices.md (commit 34cf751), written with
compare_practices.py before any repo below was cloned. None of the first
eight candidates passed the screen, so amendment 1 (commit 68deca3,
written before any practice was measured) extended the list. The first
three that passed: pytorch/pytorch (1,614 reverted pull requests in the
window), ray-project/ray (33), microsoft/playwright (52). All three count.

## short version: as pre-registered, one practice holds and two did worse; after checks, almost nothing survives

Among merged pull requests that change source code, size held fixed:

| practice | risk ratio (with ÷ without) | 95% range | verdict |
|---|---|---|---|
| no shortcuts | 0.76× | 0.67× to 0.88× | **holds**: reverted less often |
| tests with code | 1.38× | 1.19× to 1.61× | **did worse**: reverted more often |
| links an issue (PyTorch and Ray only) | 1.18× | 1.05× to 1.34× | **did worse** |

Those are the numbers exactly as the claims file defined them. An
independent recomputation reproduced all three, then found two flaws in
how they were measured (see "checks"). Corrected, **no shortcuts barely
holds (0.87×, 0.76× to 1.00×)** and rests mostly on one kind of
shortcut, warning silencers; **links an issue shows no difference
(1.00×)**; tests with code still goes with more reverts, in PyTorch only.

**PyTorch decides the pooled numbers.** It supplies 1,629 of the 1,699
reverted pull requests among source changes. Its merge bot reverts fast:
most revert messages give the reason as breaking tests or internal builds.
A pull request that changes tests is likelier to break tests, so in
PyTorch "tests with code" goes with *more* reverts. That says more about
what PyTorch reverts for than about whether writing tests is a good
habit.

Without PyTorch (Playwright and Ray; not in the claims, reported for
honesty): no practice reaches a verdict. No shortcuts points the same way
(0.59×, range 0.34× to 1.09×); tests with code 1.08× (0.64× to 2.12×);
links an issue 1.01× (Ray only).

## per repo (weighted revert rate, with the practice vs without)

| repo | source PRs | reverted | no shortcuts | tests with code | links an issue |
|---|---|---|---|---|---|
| pytorch/pytorch | 3,380 | 1,629 | 18.4% vs 32.6% | 26.4% vs 14.2% | 23.7% vs 19.8% |
| microsoft/playwright | 1,575 | 42 | 2.1% vs 5.0% | 3.0% vs 2.0% | not measurable (2% link) |
| ray-project/ray | 1,976 | 28 | 0.9% vs 1.0% | 0.9% vs 0.9% | 1.0% vs 0.9% |

Shortcuts went with more reverts in PyTorch and Playwright, and made no
difference in Ray.

## the six spent repos (counting toward nothing)

next.js, Airflow and Kibana; Elasticsearch, VS Code and Home Assistant,
pooled in two groups of three:

| practice | group 1 | group 2 |
|---|---|---|
| no shortcuts | 1.54× (0.86× to 3.63×) | 1.28× (0.70× to 2.83×) |
| tests with code | 1.36× (0.88× to 2.26×) | 0.85× (0.55× to 1.32×) |
| links an issue | 1.02× (0.61× to 1.65×) | 0.47× (0.17× to 0.94×), Elasticsearch only |

None of the "no shortcuts" estimates there clears the line, and both lean
the other way.

## checks

An independent recomputation, written from the claims file without
reusing compare_practices.py, matched all three pooled ratios and ranges
(to about 0.01), and found:

1. **Relands were counted as reverted.** PyTorch often merges the same
   pull request twice (reverted, then relanded). The replay keeps one
   merge time per number, the last one, and the 30-day check allowed a
   revert from before it, so 175 relands that were never reverted were
   counted as reverted, each carrying PyTorch's sampling weight (4.76).
   They have shortcuts more often (39% vs 19%), mostly the same patch as
   the original. Matching every row to its own merge:
   - no shortcuts: **0.87× (0.76× to 1.00×)**: still holds, barely
   - tests with code: 1.38× (1.20× to 1.58×): unchanged
   - links an issue: 1.28× (1.13× to 1.44×)
2. **"Links an issue" mostly measured stacks.** In PyTorch, most of the
   bare "#N" references are ghstack lines ("ghstack dependencies: #N")
   and stack lists, pointing at other pull requests, not issues. With
   those removed, PyTorch links in 31% of pull requests and the ratio is
   **1.00× (0.88× to 1.14×)**: no difference. Keyword, issue-URL and Jira
   links alone: 1.12× (0.97× to 1.29×).
3. **Which shortcuts.** Warning silencers (10.8% of PyTorch's source pull
   requests) and skipped tests (8.2%) carry the result; the other kinds
   are about 1% each and change nothing. Without silencers and skips, no
   shortcuts is 0.89× (0.70× to 1.23×). With relands fixed, silencers
   alone remain: 1.22× (1.03× to 1.43×) as often reverted. Most are
   type-checker ignores in larger, typing-heavy Python changes, which
   the size bands only partly account for.
4. **The flags come from the pull requests themselves.** In 80 sampled
   PyTorch rows, every skip, silencer and special-case flag checked
   (117 of 117) was in that pull request's own merge commit, not in its
   revert.
5. **Weighting decides a lot.** Unweighted, the ratios would be 1.22×
   (tests with code) and 0.88× (no shortcuts); weighting is right for
   this sampling design, but the results lean on PyTorch's weight.
6. Duplicate pull request numbers share a patch, and the bootstrap
   doesn't cluster them, so the ranges are a little too narrow.

The reland flaw sits in how the scripts match outcomes to merges. In
round 4, Elasticsearch, VS Code and Home Assistant had 9 repeated numbers,
none with trouble; the earlier rounds haven't been rechecked for it. The
scripts should match each row to its own merge from now on.

## what this means (after the checks)

- Across nine repos, **one habit barely holds and only in PyTorch**:
  pull requests without shortcuts (in practice, without warning
  silencers) were reverted somewhat less often, 0.87× with a range that
  just clears 1. Nothing else visible in git history holds up.
- **Tests with code went with more reverts in PyTorch**, where most
  reverts are for breaking tests or internal builds. That is a fact
  about what PyTorch reverts for, not a reason to write fewer tests.
- So a cross-team "practices score" is not supported. What the data
  does support is the per-repo version: show a team how often it uses
  each habit, and claim a habit holds *in that repo* only when its own
  history shows it, with the same verdicts as everything else.
- Reviews, approvals, checks before merge and disclosed agent help
  aren't in git history and remain untested; they need data from the
  git host, which a live Cowboy install has.

## rerun

    python3 compare_practices.py sorted.jsonl replay pytorch_pytorch.collect.log ray-project_ray.collect.log microsoft_playwright.collect.log

Aggregates: `data/fresh-practices.json`,
`data/fresh-practices-without-pytorch.json`,
`data/spent-practices-a.json`, `data/spent-practices-b.json`.
