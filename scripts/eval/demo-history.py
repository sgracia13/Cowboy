#!/usr/bin/env python3
"""Build the replayed history the demo's outcomes page shows.

usage: demo-history.py REPLAY_REPO_DIR SORTED.jsonl COLLECT_LOG HEAD_TIME OUT.json.gz

REPLAY_REPO_DIR is one repo's folder from cowboy-replay (cowboy.json and
outcomes.json); SORTED.jsonl is the cowboy-sortlab output it was made from;
COLLECT_LOG has the collector's "N PRs in window, R later reverted" line;
HEAD_TIME is the end of the history (the latest commit on the default branch).

The collector keeps every reverted PR plus a random sample of the rest. To
keep the demo's rates honest without weights, reverted PRs are thinned to the
same sampling rate (a fixed hash, so the result doesn't change between runs).
Author names, PR bodies (co-author lines), approvers and line fingerprints are
removed, and the result is checked for email addresses.
"""
import gzip, hashlib, json, re, sys

repo_dir, sorted_path, log_path, head, out = sys.argv[1:6]
store = json.load(open(repo_dir + "/cowboy.json"))
oc = json.load(open(repo_dir + "/outcomes.json"))
repo = next(iter(store["decisions"].values()))["repo"]
rows = {}
for l in open(sorted_path):
    r = json.loads(l)
    if r["repo"] == repo:
        rows[r["number"]] = r
m = re.search(re.escape(repo) + r": (\d+) PRs in window, (\d+) later reverted.*sorting (\d+)", open(log_path).read())
total, reverted, sorting = map(int, m.groups())
keep_p = (sorting - reverted) / (total - reverted)


def keep(n):
    if not rows[n]["reverted"]:
        return True
    h = int(hashlib.sha256(f"cowboy-demo-{n}".encode()).hexdigest()[:8], 16) / 0xFFFFFFFF
    return h < keep_p


decs, dropped = [], set()
for d in store["decisions"].values():
    if not keep(d["number"]):
        dropped.add(d["number"])
        continue
    d.update(body="", author="", comment="", backfilled=True)
    decs.append(d)
merges = {k: v for k, v in oc["merges"].items() if v["number"] not in dropped}
for v in merges.values():
    v["author"] = ""
    v["reviews_known"] = False
    for k in ("approvers", "lines", "removed"):
        v.pop(k, None)
outs = [o for o in oc["outcomes"] if o["number"] not in dropped]
for o in outs:
    o["by"] = ""
blob = json.dumps([decs, merges, outs])
emails = re.findall(r"[\w.+-]+@[\w-]+\.(?:com|org|net|io|dev|co|me|app|ai|sh)\b", blob)
assert not emails, emails[:5]
assert "co-authored-by" not in blob.lower()
note = (f"this page is a replay of real {repo} history: pull requests merged november 2025 to september 2026, "
        "sorted by today's rules and followed after merge the way cowboy would have. "
        f"it's a random sample of about {round(keep_p * 100)}% of them, and author names are removed. "
        "the corral shows the made-up sample repo instead.")
with gzip.open(out, "wt") as f:
    json.dump({"repo": repo, "now": head, "note": note, "decisions": decs,
               "outcomes": {"merges": merges, "outcomes": outs, "incidents": [], "scans": {}}}, f, separators=(",", ":"))
print(f"{len(decs)} PRs, {len(outs)} outcomes, {len(dropped)} reverted PRs thinned out -> {out}")
