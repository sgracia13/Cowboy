# Do safe-change practices go with fewer reverts? (see CLAIMS-practices.md)
#
# Among merged PRs that change source code, compare the revert rate of PRs
# that follow a practice with those that don't, holding size fixed: a
# Mantel-Haenszel risk ratio over strata of (repo, size band), weighted
# back to each repo's real mix of PRs (every reverted PR plus a sample of
# the rest, as in compare.py).
#
# usage: python3 compare_practices.py SORTED.jsonl REPLAY_DIR COLLECT_LOGS... > results.json
import json, random, re, sys, datetime, collections

sorted_path, replay, logs = sys.argv[1], sys.argv[2], sys.argv[3:]
DAY = datetime.timedelta(days=30)
BANDS = [10, 50, 200, 1000]  # under 10, 10-49, 50-199, 200-999, 1000+
PRACTICES = ["tests_with_code", "no_shortcuts", "links_issue"]
MIN_LINK_SHARE = 0.20

WINDOW = {}
for p in logs:
    for line in open(p):
        m = re.match(r"(\S+/\S+): (\d+) PRs in window, (\d+) later reverted", line)
        if m:
            WINDOW[m.group(1)] = (int(m.group(2)), int(m.group(3)))


def ts(s):
    return datetime.datetime.fromisoformat(s.replace("Z", "+00:00"))


re_fix = re.compile(r"\b(?:fix(?:es|ed)?|close[sd]?|resolve[sd]?)\s+(?:[\w.-]+/[\w.-]+)?#(\d+)", re.I)
re_hash = re.compile(r"(?<![\w/&])#(\d+)\b")
re_issue_url = re.compile(r"/issues/\d+|/-/issues/\d+")
re_jira = re.compile(r"\b[A-Z][A-Z0-9]{1,9}-\d+\b")


def links_issue(msg, own):
    _, _, body = msg.partition("\n")
    if re_fix.search(body) or re_issue_url.search(body) or re_jira.search(body):
        return True
    return any(int(n) != own for n in re_hash.findall(body))


def band(lines):
    for i, b in enumerate(BANDS):
        if lines < b:
            return i
    return len(BANDS)


prs = collections.defaultdict(list)
for l in open(sorted_path):
    r = json.loads(l)
    if (r.get("classes") or {}).get("source", 0) == 0:
        continue
    prs[r["repo"]].append(r)
all_counts = collections.Counter()
for l in open(sorted_path):
    all_counts[json.loads(l)["repo"]] += 1

for repo, rows in prs.items():
    st = json.load(open(f"{replay}/{repo.replace('/', '_')}/outcomes.json"))
    merged = {m["number"]: ts(m["merged_at"]) for m in st["merges"].values()}
    rev = set()
    for o in st["outcomes"]:
        if o["kind"] == "revert" and o["status"] in ("auto", "confirmed") and o["number"] in merged and ts(o["at"]) - merged[o["number"]] <= DAY:
            rev.add(o["number"])
    total, reverted = WINDOW[repo]
    # weights come from the whole sample (source and non-source PRs alike)
    sampled_others = sum(1 for l in open(sorted_path) if json.loads(l)["repo"] == repo and not json.loads(l)["reverted"])
    w = (total - reverted) / sampled_others
    for r in rows:
        r["bad"] = r["number"] in rev
        r["w"] = 1.0 if r["reverted"] else w
        r["band"] = band(r["lines"])
        r["tests_with_code"] = (r.get("classes") or {}).get("test", 0) > 0
        r["no_shortcuts"] = not r.get("shortcuts")
        r["links_issue"] = links_issue(r.get("msg") or "", r["number"])


def mh(rows_by_repo, practice, adjust=True):
    """Mantel-Haenszel risk ratio of practice vs not, over (repo, band)."""
    num = den = 0.0
    for repo, rows in rows_by_repo.items():
        strata = collections.defaultdict(lambda: [0.0, 0.0, 0.0, 0.0])  # bad_p, n_p, bad_n, n_n
        for r in rows:
            k = r["band"] if adjust else 0
            s = strata[k]
            if r[practice]:
                s[0] += r["w"] * r["bad"]
                s[1] += r["w"]
            else:
                s[2] += r["w"] * r["bad"]
                s[3] += r["w"]
        for a, n1, c, n0 in strata.values():
            t = n1 + n0
            if n1 == 0 or n0 == 0:
                continue
            num += a * n0 / t
            den += c * n1 / t
    return num / den if den else float("nan")


def summary(rows):
    W = sum(r["w"] for r in rows)
    out = {"source_prs": len(rows), "weight": W, "reverted_in_sample": sum(r["bad"] for r in rows)}
    for p in PRACTICES:
        yes = [r for r in rows if r[p]]
        no = [r for r in rows if not r[p]]
        def rate(g):
            ww = sum(r["w"] for r in g)
            return sum(r["w"] * r["bad"] for r in g) / ww if ww else 0
        out[p] = {"share": sum(r["w"] for r in yes) / W if W else 0, "rate_with": rate(yes), "rate_without": rate(no),
                  "n_with": len(yes), "reverted_with": sum(r["bad"] for r in yes), "reverted_without": sum(r["bad"] for r in no)}
    return out


out = {"repos": {}, "method": "Mantel-Haenszel risk ratio over (repo, size band), weighted", "bootstrap": 2000}
counted = []
for repo, rows in sorted(prs.items()):
    m = summary(rows)
    m["counted"] = m["reverted_in_sample"] >= 20
    m["links_measurable"] = m["links_issue"]["share"] >= MIN_LINK_SHARE
    m["all_prs_in_sample"] = all_counts[repo]
    out["repos"][repo] = m
    if m["counted"]:
        counted.append(repo)
out["counted"] = counted


def scope(practice):
    rs = counted
    if practice == "links_issue":
        rs = [r for r in counted if out["repos"][r]["links_measurable"]]
    return rs


pooled = {}
for p in PRACTICES:
    rs = scope(p)
    sub = {r: prs[r] for r in rs}
    pooled[p] = {"repos": rs, "rr": mh(sub, p) if rs else None, "rr_unadjusted": mh(sub, p, adjust=False) if rs else None}

rng = random.Random(2026)
boot = collections.defaultdict(list)
for _ in range(2000):
    sample = {}
    for repo in counted:
        strata = collections.defaultdict(list)
        for r in prs[repo]:
            strata[r["reverted"]].append(r)
        s = []
        for group in strata.values():
            s += [group[rng.randrange(len(group))] for _ in group]
        sample[repo] = s
    for p in PRACTICES:
        rs = pooled[p]["repos"]
        if rs:
            v = mh({r: sample[r] for r in rs}, p)
            if v == v:  # not NaN
                boot[p].append(v)


def ci(xs):
    xs = sorted(xs)
    return [xs[int(0.025 * len(xs))], xs[int(0.975 * len(xs)) - 1]]


for p in PRACTICES:
    if boot[p]:
        lo, hi = ci(boot[p])
        pooled[p]["rr_ci"] = [lo, hi]
        pooled[p]["verdict"] = "holds" if hi < 1 else ("did worse" if lo > 1 else "no demonstrated difference")
    else:
        pooled[p]["verdict"] = "not measurable"
out["pooled"] = pooled
json.dump(out, sys.stdout, indent=1)
