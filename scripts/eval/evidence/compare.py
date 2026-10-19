# Compare Cowboy's pens with simple alternatives (see CLAIMS.md).
#
# The sample isn't random: collect_sort.py keeps every PR that was later
# reverted plus a random sample of the rest. So every PR carries a weight:
# 1 for a reverted PR, and (PRs in window - reverted) / (non-reverted PRs
# sampled) for the rest. All rates below are weighted back to the repo's
# real mix of PRs. "Flag the same number" becomes "flag the same share of
# the repo's PRs".
#
# usage: python3 compare.py SORTED.jsonl REPLAY_DIR COLLECT_LOGS... > results.json
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


def top_share(ordered, share, wsum):
    """PRs from the front of the order until they make up `share` of the weight."""
    out, acc = [], 0.0
    for r in ordered:
        if acc >= share * wsum:
            break
        out.append(r)
        acc += r["w"]
    return out


def measure(rows):
    W = sum(r["w"] for r in rows)
    bad = sum(r["w"] for r in rows if r["bad"])
    car = [r for r in rows if r["pen"] == "careful"]
    easy = [r for r in rows if r["pen"] == "easy"]
    s = sum(r["w"] for r in car) / W
    se = sum(r["w"] for r in easy) / W
    by_lines = sorted(rows, key=lambda r: (-r["lines"], r["number"]))
    by_files = sorted(rows, key=lambda r: (-r["files"], -r["lines"], r["number"]))
    small = sorted(rows, key=lambda r: (r["lines"], r["number"]))
    def caught(group):
        return sum(r["w"] for r in group if r["bad"])
    def rate(group):
        g = sum(r["w"] for r in group)
        return caught(group) / g if g else 0.0
    smallest = top_share(small, se, W)
    pens = {p: rate([r for r in rows if r["pen"] == p]) for p in ("easy", "normal", "careful")}
    return dict(prs=len(rows), weight=W, reverted=bad, careful_share=s, easy_share=se, base_rate=bad / W,
                recall_cowboy=caught(car) / bad if bad else 0, recall_size=caught(top_share(by_lines, s, W)) / bad if bad else 0,
                recall_spread=caught(top_share(by_files, s, W)) / bad if bad else 0, recall_chance=s,
                lift=rate(car) / (bad / W) if bad else 0, rate_easy=rate(easy), rate_smallest=rate(smallest), pens=pens,
                reverted_in_sample=sum(r["bad"] for r in rows))


def pooled(by_repo):
    # combine counted repos by weight: reverted PRs caught over all reverted PRs
    tot = collections.Counter()
    for m in by_repo:
        b = m["reverted"]
        tot["bad"] += b
        tot["w"] += m["weight"]
        for k in ("cowboy", "size", "spread", "chance"):
            tot[k] += m["recall_" + k] * b
        tot["car_w"] += m["careful_share"] * m["weight"]
        tot["car_bad"] += m["recall_cowboy"] * b
        tot["easy_w"] += m["easy_share"] * m["weight"]
        tot["easy_bad"] += m["rate_easy"] * m["easy_share"] * m["weight"]
        tot["small_bad"] += m["rate_smallest"] * m["easy_share"] * m["weight"]
    r = {k: tot[k] / tot["bad"] for k in ("cowboy", "size", "spread", "chance")}
    return dict(recall=r, d_size=r["cowboy"] - r["size"], d_spread=r["cowboy"] - r["spread"],
                lift=(tot["car_bad"] / tot["car_w"]) / (tot["bad"] / tot["w"]),
                d_easy=(tot["easy_bad"] - tot["small_bad"]) / tot["easy_w"])


out = {"repos": {}, "method": "weighted", "bootstrap": 2000}
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
        rows = prs[repo]
        strata = collections.defaultdict(list)
        for r in rows:
            strata[r["reverted"]].append(r)
        sample = []
        for group in strata.values():
            sample += [group[rng.randrange(len(group))] for _ in group]
        ms.append(measure(sample))
    p = pooled(ms)
    for k in ("d_size", "d_spread", "lift", "d_easy"):
        boot[k].append(p[k])


def ci(xs):
    xs = sorted(xs)
    return [xs[int(0.025 * len(xs))], xs[int(0.975 * len(xs)) - 1]]


for k in ("d_size", "d_spread", "lift", "d_easy"):
    out["pooled"][k + "_ci"] = ci(boot[k])
json.dump(out, sys.stdout, indent=1)
