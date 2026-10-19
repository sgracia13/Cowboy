#!/usr/bin/env python3
"""Collect real PRs from a squash-merging public repo for the stripped-label test.

Works from a bare, blob-less clone (git clone --bare --filter=blob:none):
  - each first-parent commit "... (#N)" on the default branch is one merged PR
  - refs/pull/N/head gives the PR's own commits (real commit times)
  - the squash commit's diff gives the PR's net change

Ground truth: a PR is "labelled agent" when its squash message or any of its
commits carries an agent trailer or signature. Those markers are kept in the
record (label/how) and removed from the text, so the scorer never sees them.

usage: collect.py REPO_DIR OWNER/NAME OUT.jsonl
"""
import json, re, subprocess, sys
from collections import defaultdict

REPO, NAME, OUT = sys.argv[1], sys.argv[2], sys.argv[3]
PERIODS = {"2022": ("2022-01-01", "2023-01-01"), "2026": ("2025-11-01", "2027-01-01")}
MAX_AUTHORS, MAX_PRS, MIN_PRS = 20, 120, 20

AGENTS = r"(claude|anthropic|copilot|codex|openai|cursor|devin|jules|gemini|aider|codegen|sweep|amazon q|windsurf|opencode)"
re_marker = re.compile(r"^.*(co-authored-by:.*" + AGENTS + r"|generated (with|by) \[?(claude|codex|cursor|copilot|gemini)|🤖 generated).*$", re.I | re.M)
re_bot = re.compile(r"\[bot\]|(^|[^a-z])bot($|[^a-z])|claude|copilot|codex|cursor-agent|devin|renovate|dependabot|github-actions|sentry-autofix|getsantry|seer", re.I)
re_pr = re.compile(r"\(#(\d+)\)\s*$")
SKIP = re.compile(r"(\.lock$|lock\.(json|yaml)$|pnpm-lock|yarn\.lock|\.snap$|\.min\.(js|css)$|(^|/)(dist|vendor|generated|__snapshots__)/|\.svg$|\.png$|\.jpe?g$|\.gif$|\.ico$|\.woff2?$|\.pb\.go$|_gen\.go$|\.json\.golden$)")


def git(*args, inp=None):
    return subprocess.run(["git", "-C", REPO, *args], input=inp, capture_output=True, text=True, errors="replace").stdout


def fetch_oids(oids):
    oids = sorted(set(o for o in oids if o and set(o) != {"0"}))
    for i in range(0, len(oids), 2000):
        subprocess.run(["git", "-C", REPO, "-c", "fetch.negotiationAlgorithm=noop", "fetch", "-q", "origin", "--no-tags",
                        "--no-write-fetch-head", "--recurse-submodules=no", "--filter=blob:none", "--stdin"],
                       input="\n".join(oids[i:i + 2000]) + "\n", text=True, capture_output=True)


def strip(text):
    return re_marker.sub("", text).strip()


def agent_of(text):
    m = re_marker.search(text)
    if not m:
        return ""
    a = re.search(AGENTS, m.group(0), re.I)
    return (a.group(1).lower() if a else "agent")


# 1. merged PRs per period
prs = []
for period, (since, until) in PERIODS.items():
    raw = git("log", "--first-parent", f"--since={since}", f"--until={until}", "--format=%H%x1f%P%x1f%an%x1f%ae%x1f%cI%x1f%B%x1e", "HEAD")
    for rec in raw.split("\x1e"):
        f = rec.strip("\n").split("\x1f")
        if len(f) < 6:
            continue
        sha, parents, an, ae, merged, msg = f
        subj = msg.split("\n", 1)[0]
        m = re_pr.search(subj)
        if not m or len(parents.split()) != 1 or re_bot.search(an) or re_bot.search(ae):
            continue
        prs.append(dict(period=period, sha=sha, parent=parents, author=an, email=ae, merged=merged,
                        number=int(m.group(1)), title=re_pr.sub("", subj).strip(), msg=msg))

# 2. pick authors: most active per period
by = defaultdict(list)
for p in prs:
    by[(p["period"], p["author"])].append(p)
keep = []
for period in PERIODS:
    cands = [(k, v) for k, v in by.items() if k[0] == period and len(v) >= MIN_PRS]
    if period == "2026":  # authors who sometimes label agent work, so both kinds exist
        cands = [(k, v) for k, v in cands if sum(1 for p in v if re_marker.search(p["msg"])) >= 3] or cands
    cands.sort(key=lambda kv: -len(kv[1]))
    for k, v in cands[:MAX_AUTHORS]:
        v.sort(key=lambda p: p["merged"])
        keep += v[-MAX_PRS:]
print(f"{NAME}: {len(keep)} PRs from {len(set((p['period'], p['author']) for p in keep))} author-periods", file=sys.stderr)

# 3. PR head commits
for i in range(0, len(keep), 150):
    specs = [f"refs/pull/{p['number']}/head:refs/pr/{p['number']}" for p in keep[i:i + 150]]
    subprocess.run(["git", "-C", REPO, "fetch", "-q", "--filter=blob:none", "origin", *specs], capture_output=True)
    print(f"  fetched heads {i + len(specs)}/{len(keep)}", file=sys.stderr)

# 4. blobs for the squash diffs
need, raws = [], {}
for p in keep:
    r = git("diff-tree", "-r", "--no-commit-id", "--no-renames", p["parent"], p["sha"])
    files = []
    for line in r.splitlines():
        meta, _, path = line.partition("\t")
        parts = meta.split()
        if len(parts) < 5 or SKIP.search(path):
            continue
        files.append(path)
        need += [parts[2], parts[3]]
    raws[p["sha"]] = files
fetch_oids(need)
print(f"  fetched {len(set(need))} blobs", file=sys.stderr)

# 5. records
n_agent = 0
with open(OUT, "w") as out:
    for p in keep:
        files = raws[p["sha"]][:60]
        fc = []
        if files:
            d = git("diff", "-U0", "--no-color", "--no-renames", p["parent"], p["sha"], "--", *files)
            cur = None
            for line in d.splitlines():
                if line.startswith("diff --git"):
                    cur = None
                elif line.startswith("+++ "):
                    path = line[4:]
                    path = path[2:] if path.startswith("b/") else None
                    if path:
                        cur = dict(path=path, added=[], removed=[])
                        fc.append(cur)
                elif line.startswith("--- ") or line.startswith("@@"):
                    continue
                elif cur is not None and len(cur["added"]) + len(cur["removed"]) < 2000:
                    if line.startswith("+"):
                        cur["added"].append(line[1:])
                    elif line.startswith("-"):
                        cur["removed"].append(line[1:])
            # deleted files have no +++ b/ path; count their removals under the old path
        commits = []
        if git("rev-parse", "-q", "--verify", f"refs/pr/{p['number']}").strip():
            cr = git("log", "--format=%H%x1f%an%x1f%aI%x1f%cI%x1f%B%x1e", "-n", "100", f"refs/pr/{p['number']}", f"^{p['parent']}")
            for rec in cr.split("\x1e"):
                f = rec.strip("\n").split("\x1f")
                if len(f) == 5:
                    commits.append(dict(sha=f[0], author=f[1], author_date=f[2], committer_date=f[3], message=f[4]))
        all_text = p["msg"] + "\n" + "\n".join(c["message"] for c in commits)
        label = agent_of(all_text)
        n_agent += bool(label)
        body = p["msg"].split("\n", 1)[1] if "\n" in p["msg"] else ""
        # grafana-style squash bodies are lists of commit subjects, not PR descriptions
        if re.match(r"^\s*\*\s", body.strip() + " ") and "\n* " in "\n" + body:
            body_kind = "commit-list"
            body = ""
        else:
            body_kind = "pr-description" if body.strip() else "none"
        for c in commits:
            c["message"] = strip(c["message"])
        out.write(json.dumps(dict(repo=NAME, number=p["number"], period=p["period"], author=p["author"], merged=p["merged"],
                                  title=p["title"], body=strip(body), body_kind=body_kind, label=label,
                                  commits=commits, files=fc)) + "\n")
print(f"  wrote {len(keep)} records, {n_agent} labelled agent", file=sys.stderr)
