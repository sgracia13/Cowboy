# Score the walk-forward test of fitted rules (see CLAIMS-fit.md).
#
# usage: python3 compare_fit.py FITLAB.prs.jsonl > results.json
import collections, json, random, sys

rows = [json.loads(l) for l in open(sys.argv[1])]
groups = collections.defaultdict(list)  # (repo, month) -> rows
for r in rows:
    groups[(r["repo"], r["month"])].append(r)


def measure(gs):
    tot = collections.Counter()
    per_repo = collections.defaultdict(collections.Counter)
    for (repo, month), rs in gs.items():
        W = sum(r["w"] for r in rs)
        fitted = [r for r in rs if r["fitted"]]
        share = sum(r["w"] for r in fitted) / W if W else 0
        big, acc = [], 0.0
        for r in sorted(rs, key=lambda r: (-r["lines"], r["number"])):
            if acc >= share * W:
                break
            big.append(r)
            acc += r["w"]
        bad = lambda g: sum(r["w"] for r in g if r["bad"])
        wt = lambda g: sum(r["w"] for r in g)
        default = [r for r in rs if r["default"]]
        for k, v in (("W", W), ("bad", bad(rs)), ("fit_w", wt(fitted)), ("fit_bad", bad(fitted)), ("big_bad", bad(big)),
                     ("def_w", wt(default)), ("def_bad", bad(default))):
            tot[k] += v
            per_repo[repo][k] += v
    def summary(t):
        base = t["bad"] / t["W"] if t["W"] else 0
        lift = lambda b, w: (b / w) / base if w and base else 0
        return dict(prs_weighted=t["W"], reverted=t["bad"], base_rate=base,
                    fitted_share=t["fit_w"] / t["W"] if t["W"] else 0, default_share=t["def_w"] / t["W"] if t["W"] else 0,
                    recall_fitted=t["fit_bad"] / t["bad"] if t["bad"] else 0, recall_biggest=t["big_bad"] / t["bad"] if t["bad"] else 0,
                    recall_default=t["def_bad"] / t["bad"] if t["bad"] else 0,
                    d_biggest=(t["fit_bad"] - t["big_bad"]) / t["bad"] if t["bad"] else 0,
                    lift_fitted=lift(t["fit_bad"], t["fit_w"]), lift_default=lift(t["def_bad"], t["def_w"]),
                    d_lift=lift(t["fit_bad"], t["fit_w"]) - lift(t["def_bad"], t["def_w"]))
    return summary(tot), {k: summary(v) for k, v in per_repo.items()}


pooled, repos = measure(groups)
out = {"pooled": pooled, "repos": repos, "bootstrap": 2000}
out["months_fitted"] = collections.Counter()
for (repo, month), rs in groups.items():
    out["months_fitted"][repo + (" fitted" if rs[0]["used_fit"] else " defaults")] += 1

rng = random.Random(2026)
boot = collections.defaultdict(list)
for _ in range(2000):
    gs = {}
    for k, rs in groups.items():
        strata = collections.defaultdict(list)
        for r in rs:
            strata[r["bad"]].append(r)
        sample = []
        for g in strata.values():
            sample += [g[rng.randrange(len(g))] for _ in g]
        gs[k] = sample
    p, _ = measure(gs)
    for k in ("d_biggest", "lift_fitted", "d_lift"):
        boot[k].append(p[k])


def ci(xs):
    xs = sorted(xs)
    return [xs[int(0.025 * len(xs))], xs[int(0.975 * len(xs)) - 1]]


for k in ("d_biggest", "lift_fitted", "d_lift"):
    out["pooled"][k + "_ci"] = ci(boot[k])
p = out["pooled"]
out["claims"] = {
    "1 fitted beats biggest-first": p["d_biggest_ci"][0] > 0,
    "2 fitted beats chance": p["lift_fitted_ci"][0] > 1,
    "3 fitted beats the defaults": p["d_lift_ci"][0] > 0,
    "4 fitted pen under 20% in every repo": all(v["fitted_share"] < 0.20 for v in repos.values()),
}
json.dump(out, sys.stdout, indent=1)
