#!/usr/bin/env python3
"""Collect PRs for the sorting test: every 2026 PR that was later reverted, a
random sample of the rest, their diffs, and what happened after merge.

usage: collect_sort.py REPO_DIR OWNER/NAME OUT.jsonl CHECKOUT_DIR [SAMPLE=2500]
"""
import json, random, re, subprocess, sys
from datetime import datetime, timedelta

REPO, NAME, OUT, CHECKOUT = sys.argv[1:5]
SAMPLE = int(sys.argv[5]) if len(sys.argv) > 5 else 2500
SINCE, UNTIL = "2025-11-01", "2026-09-15"  # leave a month after the last PR for reverts and fixes to show up
re_pr = re.compile(r"\(#(\d+)\)\s*$")
re_reverts_sha = re.compile(r"This reverts commit ([0-9a-f]{7,40})")
re_revert_subj = re.compile(r'^Revert "(.*)"', re.I)
re_ref = re.compile(r"#(\d+)")
AGENTS = r"(claude|anthropic|copilot|codex|openai|cursor|devin|jules|gemini|aider)"
re_marker = re.compile(r"(co-authored-by:.*" + AGENTS + r"|generated (with|by) \[?(claude|codex|cursor|copilot)|made with \[?cursor|claude-session:)", re.I)
re_bot = re.compile(r"\[bot\]|renovate|dependabot|github-actions|getsantry|sentry-autofix", re.I)
HEAVY = re.compile(r"(\.lock$|lock\.(json|yaml)$|pnpm-lock|yarn\.lock|go\.sum$|\.snap$|\.min\.(js|css)$|\.svg$|\.png$|\.jpe?g$|\.gif$|\.ico$|\.woff2?$|\.ttf$|\.pdf$|\.zip$)")


def norm(t):
    t = re.sub(r"\(#\d+\)|\(no-changelog\)|\(PR #?\d+\)", "", t)
    t = re.sub(r"^\s*[a-z]+(\([^)]*\))?!?:\s*", "", t.strip(), flags=re.I)
    return re.sub(r"[^a-z0-9]+", " ", t.lower()).strip()


def git(*a, inp=None):
    return subprocess.run(["git", "-C", REPO, *a], input=inp, capture_output=True, text=True, errors="replace").stdout


def fetch_oids(oids):
    oids = sorted(set(o for o in oids if o and set(o) != {"0"}))
    for i in range(0, len(oids), 2000):
        subprocess.run(["git", "-C", REPO, "-c", "fetch.negotiationAlgorithm=noop", "fetch", "-q", "origin", "--no-tags",
                        "--no-write-fetch-head", "--recurse-submodules=no", "--filter=blob:none", "--stdin"],
                       input="\n".join(oids[i:i + 2000]) + "\n", text=True, capture_output=True)


# every first-parent commit from SINCE to now (later commits are where reverts and fixes appear)
raw = git("log", "--first-parent", f"--since={SINCE}", "--format=%H%x1f%P%x1f%an%x1f%cI%x1f%B%x1e", "HEAD")
commits = []
for rec in raw.split("\x1e"):
    f = rec.strip("\n").split("\x1f")
    if len(f) == 5:
        commits.append(dict(sha=f[0], parent=f[1].split()[0] if f[1] else "", author=f[2], at=datetime.fromisoformat(f[3]), msg=f[4]))
commits.reverse()
by_sha = {c["sha"]: c for c in commits}
by_num = {}
for c in commits:
    m = re_pr.search(c["msg"].split("\n", 1)[0])
    if m:
        c["number"] = int(m.group(1))
        by_num[c["number"]] = c

# reverts: "This reverts commit <sha>" or Revert "title (#N)"
reverted = {}
for c in commits:
    subj = c["msg"].split("\n", 1)[0]
    if not re_revert_subj.match(subj) and "This reverts commit" not in c["msg"]:
        continue
    target = None
    for s in re_reverts_sha.findall(c["msg"]):
        t = next((x for k, x in by_sha.items() if k.startswith(s)), None)
        if t is not None and "number" in t:
            target = t
            break
    if target is None:
        m = re_revert_subj.match(subj)
        if m:
            n = re_pr.search(m.group(1))
            if n and int(n.group(1)) in by_num:
                target = by_num[int(n.group(1))]
    if target is None:  # free-form: 'Revert - Title', "Revert 'Title' (PR 123)", 'revert: Title'
        pm = re.search(r"\(PR #?(\d+)\)", subj)
        if pm and int(pm.group(1)) in by_num:
            target = by_num[int(pm.group(1))]
        else:
            key = norm(re.sub(r"^.*?\brevert(ed|ing)?\b[\s:\-\"']*", "", subj, flags=re.I))
            if len(key) > 12:
                for t in reversed(commits[:commits.index(c)]):
                    if "number" in t and norm(t["msg"].split("\n", 1)[0]) == key:
                        target = t
                        break
    if target is not None and target is not c and target["sha"] not in reverted:
        reverted[target["sha"]] = (c["at"] - target["at"]).total_seconds() / 86400

# follow-up fixes: a later "fix" commit within 14 days that names the PR number
followups = {}
for c in commits:
    subj = c["msg"].split("\n", 1)[0].lower()
    if not (subj.startswith("fix") or "regression" in subj or "hotfix" in subj) or re_revert_subj.match(subj):
        continue
    for n in set(int(x) for x in re_ref.findall(c["msg"])):
        t = by_num.get(n)
        if t and t is not c and timedelta(0) < c["at"] - t["at"] < timedelta(days=14) and n != c.get("number"):
            followups.setdefault(t["sha"], []).append(c.get("number"))

since, until = datetime.fromisoformat(SINCE + "T00:00:00+00:00"), datetime.fromisoformat(UNTIL + "T00:00:00+00:00")
window = [c for c in commits if "number" in c and since <= c["at"] < until and not re_revert_subj.match(c["msg"])]
rev = [c for c in window if c["sha"] in reverted]
rest = [c for c in window if c["sha"] not in reverted]
random.seed(7)
sample = random.sample(rest, min(SAMPLE, len(rest)))
pick = rev + sample
print(f"{NAME}: {len(window)} PRs in window, {len(rev)} later reverted, {sum(1 for c in window if c['sha'] in followups)} with follow-up fixes; sorting {len(pick)}", file=sys.stderr)

# blobs for the diffs
need, lists = [], {}
for c in pick:
    files = []
    for line in git("diff-tree", "-r", "--no-commit-id", "--no-renames", c["parent"], c["sha"]).splitlines():
        meta, _, path = line.partition("\t")
        parts = meta.split()
        if len(parts) < 5:
            continue
        files.append((path, parts[4], HEAVY.search(path) is not None))
        if not HEAVY.search(path):
            need += [parts[2], parts[3]]
    lists[c["sha"]] = files
fetch_oids(need)
print(f"  fetched {len(set(need))} blobs", file=sys.stderr)

with open(OUT, "w") as out:
    for c in pick:
        files = lists[c["sha"]]
        light = [p for p, _, heavy in files if not heavy][:300]
        patch = git("diff", "--no-color", "--no-renames", c["parent"], c["sha"], "--", *light) if light else ""
        # heavy files (lock files, snapshots, images) count for file class but their contents aren't fetched
        for p, st, heavy in files:
            if heavy:
                patch += f"diff --git a/{p} b/{p}\n--- a/{p}\n+++ b/{p}\n@@ -1 +1 @@\n-x\n+y\n"
        title = re_pr.sub("", c["msg"].split("\n", 1)[0]).strip()
        out.write(json.dumps(dict(repo=NAME, number=c["number"], author=c["author"], merged=c["at"].isoformat(), title=title,
                                  msg=c["msg"], bot=bool(re_bot.search(c["author"])), agent_label=bool(re_marker.search(c["msg"])),
                                  reverted=c["sha"] in reverted, reverted_after_days=reverted.get(c["sha"]),
                                  followups=len(followups.get(c["sha"], [])), in_sample=c in sample, files=len(files), patch=patch)) + "\n")

# for blast radius, write the source files of a mid-window commit with checkout_src.py
mid = window[len(window) // 2]["sha"] if window else "HEAD"
print(f"  wrote {OUT}; for blast radius run: checkout_src.py {REPO} {mid} {CHECKOUT}", file=sys.stderr)
