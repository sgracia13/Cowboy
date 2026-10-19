#!/usr/bin/env python3
"""Summarise the sorting test. usage: report_sort.py sorted.jsonl [...] --totals repo=N,...

--totals gives each repo's count of non-reverted PRs in the window, so the
random sample can be weighted back up to the whole repo.
"""
import json, random, sys
from collections import Counter, defaultdict

args = [a for a in sys.argv[1:] if not a.startswith("--totals")]
totals = {}
for a in sys.argv[1:]:
    if a.startswith("--totals"):
        for kv in a.split("=", 1)[1].split(","):
            k, v = kv.rsplit("=", 1)
            totals[k] = int(v)
rows = [json.loads(l) for p in args for l in open(p)]
PENS = ("easy", "normal", "careful")


def pct(a, b):
    return f"{100 * a / b:5.1f}%" if b else "   - "


def spread(v):
    c = Counter(r["pen"] for r in v)
    return "  ".join(f"{p} {pct(c[p], len(v))}" for p in PENS)


def block(rs, title):
    sample = [r for r in rs if r["in_sample"]]
    rev = [r for r in rs if r["reverted"]]
    print(f"\n## {title}")
    print(f"random sample of {len(sample)} PRs:   {spread(sample)}")
    print(f"the {len(rev)} PRs later reverted:  {spread(rev)}")
    # revert rate per pen, weighting the sample back up to the repo
    print(f"{'pen':<9}{'PRs (est.)':>11}{'reverted':>10}{'revert rate':>13}{'follow-up fix rate':>20}")
    rates = {}
    for p in PENS:
        est, nrev = 0.0, 0
        for repo in set(r["repo"] for r in rs):
            s = [r for r in sample if r["repo"] == repo]
            w = totals.get(repo, len(s)) / max(1, len(s))
            est += w * sum(1 for r in s if r["pen"] == p)
            nrev += sum(1 for r in rev if r["repo"] == repo and r["pen"] == p)
        sp = [r for r in sample if r["pen"] == p]
        fu = sum(1 for r in sp if r["followups"] > 0)
        rate = nrev / (est + nrev) if est + nrev else 0
        rates[p] = rate
        print(f"{p:<9}{est:>11.0f}{nrev:>10}{pct(nrev, est + nrev):>13}{pct(fu, len(sp)):>20}")
    if rates.get("easy"):
        print(f"careful PRs were reverted {rates['careful'] / rates['easy']:.1f}x as often as easy ones"
              f" ({rates['careful'] / rates['normal']:.1f}x normal)" if rates.get("normal") else "")
    return sample, rev


sample, rev = block(rows, "all three repos")
for repo in sorted(set(r["repo"] for r in rows)):
    block([r for r in rows if r["repo"] == repo], repo)

print("\n## why PRs went to careful (random sample)")
c = Counter()
car = [r for r in sample if r["pen"] == "careful"]
for r in car:
    for t in set(r["triggers"] or []):
        c[t] += 1
for k, v in c.most_common():
    print(f"  {k:<14}{pct(v, len(car))} of careful PRs ({v})")
only = Counter(tuple(sorted(set(r["triggers"] or []))) for r in car)
print("  most common combinations:", ", ".join(f"{'+'.join(k)} {v}" for k, v in only.most_common(5)))

print("\n## which careful triggers predict a revert")
for t in ("sensitive", "blast_radius", "shortcut", "size"):
    has_s = [r for r in sample if t in (r["triggers"] or [])]
    has_r = [r for r in rev if t in (r["triggers"] or [])]
    print(f"  {t:<14}in {pct(len(has_s), len(sample))} of all PRs, in {pct(len(has_r), len(rev))} of reverted PRs")
for k in ("tests_removed", "skips", "catch_all", "suppressions", "special_case_comments", "ci_loosened"):
    hs = [r for r in sample if any(f["kind"] == k for f in r["shortcuts"] or [])]
    hr = [r for r in rev if any(f["kind"] == k for f in r["shortcuts"] or [])]
    print(f"    shortcut {k:<22}in {pct(len(hs), len(sample))} of all PRs, in {pct(len(hr), len(rev))} of reverted")

print("\n## agent-labelled vs other PRs (random sample, people's accounts)")
ppl = [r for r in sample if not r["bot"]]
for name, v in (("agent-labelled", [r for r in ppl if r["agent_label"]]), ("not labelled", [r for r in ppl if not r["agent_label"]])):
    print(f"  {name:<16}{len(v):>6} PRs   {spread(v)}")
print(f"  agent-labelled share of reverted PRs (people): {pct(sum(1 for r in rev if r['agent_label'] and not r['bot']), sum(1 for r in rev if not r['bot']))}"
      f"  vs {pct(sum(1 for r in ppl if r['agent_label']), len(ppl))} of all PRs")

print("\n## size of PRs per pen (random sample, median lines changed)")
for p in PENS:
    v = sorted(r["lines"] for r in sample if r["pen"] == p)
    if v:
        print(f"  {p:<8} median {v[len(v) // 2]} lines, 90th percentile {v[int(len(v) * .9)]}")
fk = [r for r in sample if r["fan_in_known"]]
print(f"  blast radius measured for {pct(len(fk), len(sample))} of PRs")

random.seed(3)
flagged = [(r, f) for r in sample for f in (r["shortcuts"] or [])]
random.shuffle(flagged)
with open("shortcut_sample.jsonl", "w") as o:
    for r, f in flagged[:60]:
        o.write(json.dumps(dict(repo=r["repo"], number=r["number"], title=r["title"], **f)) + "\n")
print(f"\nwrote {min(60, len(flagged))} random shortcut flags to shortcut_sample.jsonl for hand checking ({len(flagged)} flags in the sample)")
