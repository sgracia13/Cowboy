# Compare Cowboy's careful pen with simple alternatives for the same number
# of REVIEWED LINES (see CLAIMS-lines.md). Data loading and weighting are
# the same as compare.py.
#
# The sample isn't random: collect_sort.py keeps every PR that was later
# reverted plus a random sample of the rest. So every PR carries a weight:
# 1 for a reverted PR, and (PRs in window - reverted) / (non-reverted PRs
# sampled) for the rest. All rates below are weighted back to the repo's
# real mix of PRs. "Flag the same number" becomes "flag the same share of
# the repo's PRs".
#
# usage: python3 compare_lines.py SORTED.jsonl REPLAY_DIR COLLECT_LOGS... > results.json
import json, random, re, sys, datetime, collections

sorted_path, replay, logs = sys.argv[1], sys.argv[2], sys.argv[3:]
DAY = datetime.timedelta(days=30)
WINDOW = {}  # repo -> (PRs in window, reverted)
for p in logs:
    for line in open(p):
        m = re.match(r"(\S+/\S+): (\d+) PRs in window, (\d+) later reverted", line)
        if m:
            WINDOW[m.group(1)] = (int(m.group(2)), int(m.group(3)))


def ts(s):
    return datetime.datetime.fromisoformat(s.replace("Z", "+00:00"))


prs = collections.defaultdict(list)
for l in open(sorted_path):
    r = json.loads(l)
    prs[r["repo"]].append(r)
for repo, rows in prs.items():
    st = json.load(open(f"{replay}/{repo.replace('/', '_')}/outcomes.json"))
    merged = {m["number"]: ts(m["merged_at"]) for m in st["merges"].values()}
    rev = set()
    for o in st["outcomes"]:
        if o["kind"] == "revert" and o["status"] in ("auto", "confirmed") and o["number"] in merged and ts(o["at"]) - merged[o["number"]] <= DAY:
            rev.add(o["number"])
    total, reverted = WINDOW[repo]
    others = sum(not r["reverted"] for r in rows)
    w = (total - reverted) / others
    for r in rows:
        r["bad"] = r["number"] in rev
        r["w"] = 1.0 if r["reverted"] else w



def take_lines(ordered, budget):
    """PRs from the front of the order until their weighted lines reach budget."""
    out, acc = [], 0.0
    for r in ordered:
        if acc >= budget:
            break
        out.append(r)
        acc += r["w"] * r["lines"]
    return out


def measure(rows):
    W = sum(r["w"] for r in rows)
    L = sum(r["w"] * r["lines"] for r in rows)
    bad = sum(r["w"] for r in rows if r["bad"])
    car = [r for r in rows if r["pen"] == "careful"]
    B = sum(r["w"] * r["lines"] for r in car)

    def caught(group):
        return sum(r["w"] for r in group if r["bad"]) / bad if bad else 0.0

    by_lines = sorted(rows, key=lambda r: (-r["lines"], r["number"]))
    by_files = sorted(rows, key=lambda r: (-r["files"], -r["lines"], r["number"]))
    small = sorted(rows, key=lambda r: (r["lines"], r["number"]))
    return dict(prs=len(rows), weight=W, lines=L, reverted=bad, budget=B,
                careful_share=sum(r["w"] for r in car) / W, careful_line_share=B / L if L else 0,
                caught_cowboy=caught(car), caught_size=caught(take_lines(by_lines, B)),
                caught_spread=caught(take_lines(by_files, B)), caught_smallest=caught(take_lines(small, B)),
                caught_chance=B / L if L else 0,
                reverted_in_sample=sum(r["bad"] for r in rows))


KEYS = ("cowboy", "size", "spread", "smallest", "chance")


def pooled(by_repo):
    tot = collections.Counter()
    for m in by_repo:
        b = m["reverted"]
        tot["bad"] += b
        tot["B"] += m["budget"]
        tot["L"] += m["lines"]
        for k in KEYS:
            tot[k] += m["caught_" + k] * b
    c = {k: tot[k] / tot["bad"] for k in KEYS}
    # trouble per line in careful / trouble per line overall
    lift = (tot["cowboy"] / tot["B"]) / (tot["bad"] / tot["L"])
    return dict(caught=c, d_size=c["cowboy"] - c["size"], d_spread=c["cowboy"] - c["spread"],
                d_smallest=c["cowboy"] - c["smallest"], lift_per_line=lift)


out = {"repos": {}, "method": "weighted, same reviewed lines", "bootstrap": 2000}
counted = []
for repo, rows in sorted(prs.items()):
    m = measure(rows)
    m["counted"] = m["reverted_in_sample"] >= 20
    out["repos"][repo] = m
    if m["counted"]:
        counted.append(repo)
out["counted"] = counted
out["pooled"] = pooled([out["repos"][r] for r in counted])

rng = random.Random(2026)
boot = collections.defaultdict(list)
for _ in range(2000):
    ms = []
    for repo in counted:
        strata = collections.defaultdict(list)
        for r in prs[repo]:
            strata[r["reverted"]].append(r)
        sample = []
        for group in strata.values():
            sample += [group[rng.randrange(len(group))] for _ in group]
        ms.append(measure(sample))
    p = pooled(ms)
    for k in ("d_size", "d_spread", "d_smallest", "lift_per_line"):
        boot[k].append(p[k])


def ci(xs):
    xs = sorted(xs)
    return [xs[int(0.025 * len(xs))], xs[int(0.975 * len(xs)) - 1]]


for k in ("d_size", "d_spread", "d_smallest", "lift_per_line"):
    out["pooled"][k + "_ci"] = ci(boot[k])
p = out["pooled"]
p["verdict"] = {
    "1 careful beats largest-first": "pass" if p["d_size_ci"][0] > 0 else "not shown",
    "2 careful beats most-files-first": "pass" if p["d_spread_ci"][0] > 0 else "not shown",
    "3 careful beats chance": "pass" if p["lift_per_line_ci"][0] > 1 else "not shown",
}
json.dump(out, sys.stdout, indent=1)
