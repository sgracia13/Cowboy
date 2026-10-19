#!/usr/bin/env python3
"""Summarise cowboy-eval results. usage: report.py results.jsonl [more.jsonl ...]"""
import json, sys
from collections import Counter, defaultdict


def load(paths):
    rows = []
    for p in paths:
        with open(p) as f:
            rows += [json.loads(l) for l in f]
    return rows


def auc(pos, neg):
    if not pos or not neg:
        return float("nan")
    allv = sorted([(v, 1) for v in pos] + [(v, 0) for v in neg])
    # rank-sum with ties averaged
    ranks, i = {}, 0
    rs = 0.0
    while i < len(allv):
        j = i
        while j < len(allv) and allv[j][0] == allv[i][0]:
            j += 1
        r = (i + j + 1) / 2
        rs += r * sum(1 for k in range(i, j) if allv[k][1])
        i = j
    n1, n0 = len(pos), len(neg)
    return (rs - n1 * (n1 + 1) / 2) / (n1 * n0)


def pct(a, b):
    return f"{100 * a / b:5.1f}%" if b else "   - "


def table(rows, title):
    print(f"\n## {title}")
    g = defaultdict(list)
    for r in rows:
        g[r["truth"]].append(r)
    print(f"{'group':<22}{'PRs':>7}{'flagged':>9}{'abstain':>9}{'median':>8}{'p90':>6}")
    for k in ("agent", "human-2022", "unlabelled"):
        v = g.get(k, [])
        if not v:
            continue
        s = sorted(x["score"] for x in v)
        print(f"{k:<22}{len(v):>7}{pct(sum(x['flagged'] for x in v), len(v)):>9}{pct(sum(x['abstained'] for x in v), len(v)):>9}"
              f"{s[len(s) // 2]:>8}{s[int(len(s) * .9)]:>6}")
    a, h, u = g.get("agent", []), g.get("human-2022", []), g.get("unlabelled", [])
    print(f"ranking (AUC, 0.5 = coin flip, 1.0 = perfect): agent vs 2022 humans {auc([x['score'] for x in a], [x['score'] for x in h]):.3f}"
          f" · agent vs unlabelled 2026 {auc([x['score'] for x in a], [x['score'] for x in u]):.3f}")
    return a, h, u


def sweep(a, h):
    print("\nif the label line were moved (acts on the low end of the range):")
    print(f"{'line':>6}{'caught':>9}{'humans flagged':>16}")
    for t in (50, 60, 70, 80, 90):
        print(f"{t:>6}{pct(sum(x['low'] >= t for x in a), len(a)):>9}{pct(sum(x['low'] >= t for x in h), len(h)):>16}")


def precision(a, h):
    if not a or not h:
        return
    rec = sum(x["flagged"] for x in a) / len(a)
    fpr = sum(x["flagged"] for x in h) / len(h)
    print(f"\ncatch rate {100 * rec:.1f}%, wrong-flag rate on clean humans {100 * fpr:.2f}%")
    for prev in (0.1, 0.3, 0.5):
        tp, fp = rec * prev, fpr * (1 - prev)
        print(f"  if {int(prev * 100)}% of PRs were hidden agent work, a flag would be right {pct(tp, tp + fp) if tp + fp else '   - '} of the time")


def modules(a, h):
    print("\naverage evidence per module (counted after caps):")
    for name, v in (("agent", a), ("human-2022", h)):
        tot = defaultdict(float)
        for x in v:
            for m in x.get("modules") or []:
                tot[m["module"]] += m["counted"]
        print(f"  {name:<12}" + "  ".join(f"{k} {tot[k] / max(1, len(v)):+.2f}" for k in ("disclosure", "time", "text", "code", "baseline")))


def reasons(v, title, n=8):
    c = Counter()
    for x in v:
        for r in x.get("reasons") or []:
            if r["effect"] > 0:
                c[r["module"] + "." + r["field"]] += 1
    print(f"\nmost common reasons, {title} ({len(v)} PRs):")
    for k, cnt in c.most_common(n):
        print(f"  {cnt:>5}  {k}")


rows = load(sys.argv[1:])
a, h, u = table(rows, "all repos")
sweep(a, h)
precision(a, h)
modules(a, h)
reasons([x for x in h if x["flagged"]], "humans wrongly flagged")
reasons([x for x in a if x["flagged"]], "agent PRs caught")
warm = [x for x in rows if not x["cold"]]
a2, h2, _ = table(warm, "authors with 10+ past PRs (warm baselines)")
precision(a2, h2)
for repo in sorted(set(x["repo"] for x in rows)):
    ra, rh, _ = table([x for x in rows if x["repo"] == repo], repo)
    precision(ra, rh)
bk = defaultdict(list)
for x in a:
    bk[x["body_kind"]].append(x)
print("\nagent PRs caught, by what text Cowboy could see:")
for k, v in bk.items():
    print(f"  {k:<16}{len(v):>6} PRs  caught {pct(sum(x['flagged'] for x in v), len(v))}")
cm = defaultdict(list)
for x in a:
    cm["1 commit" if x["commits"] <= 1 else "2-5 commits" if x["commits"] <= 5 else "6+ commits"].append(x)
print("agent PRs caught, by number of commits:")
for k in sorted(cm):
    v = cm[k]
    print(f"  {k:<16}{len(v):>6} PRs  caught {pct(sum(x['flagged'] for x in v), len(v))}")
