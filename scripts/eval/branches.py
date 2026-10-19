#!/usr/bin/env python3
"""Branch-name test on repos that merge with merge commits.

"Merge pull request #N from owner/branch" keeps the branch name in git, so we
can ask: how often do PRs come from agent-named branches (claude/, codex/...),
with and without an agent label, and how often did humans use such names in
2022, before agents were common?

usage: branches.py REPO_DIR [REPO_DIR ...]   (bare clones, --filter=tree:0 is enough)
"""
import re, subprocess, sys, json
from collections import Counter, defaultdict

PREFIXES = ["codex/", "copilot/", "cursor/", "claude/", "devin/", "jules/", "aider/", "openhands/", "sweep/", "amazon-q/"]
AGENTS = r"(claude|anthropic|copilot|codex|openai|cursor|devin|jules|gemini|aider|sweep|amazon q|windsurf|opencode)"
re_marker = re.compile(r"(co-authored-by:.*" + AGENTS + r"|generated (with|by) \[?(claude|codex|cursor|copilot|gemini)|🤖 generated|made with \[?cursor|claude-session:)", re.I)
re_bot = re.compile(r"\[bot\]|swe-agent|copilot|claude|codex|devin|dependabot|renovate|github-actions", re.I)
re_merge = re.compile(r"^Merge pull request #(\d+) from ([^/\s]+)/(\S+)")
PERIODS = {"2022": ("2022-01-01", "2023-01-01"), "2026": ("2025-11-01", "2027-01-01")}


def git(repo, *a):
    return subprocess.run(["git", "-C", repo, *a], capture_output=True, text=True, errors="replace").stdout


def strict(branch):
    b = branch.lower()
    return next((p for p in PREFIXES if b.startswith(p)), "")


def loose(branch):  # an agent name as any path segment or segment prefix: me/claude-fix, dev/copilot/x
    for seg in branch.lower().split("/"):
        for p in PREFIXES:
            if seg == p[:-1] or seg.startswith(p[:-1] + "-") or seg.startswith(p[:-1] + "_"):
                return p[:-1]
    return ""


rows = []
for repo in sys.argv[1:]:
    name = repo.rstrip("/").split("/")[-1].removesuffix(".git").replace("_", "/", 1)
    for period, (since, until) in PERIODS.items():
        raw = git(repo, "log", "--merges", f"--since={since}", f"--until={until}", "--grep=^Merge pull request #",
                  "--format=%H%x1f%P%x1f%s%x1e", "HEAD")
        seen = set()
        for rec in raw.split("\x1e"):
            f = rec.strip().split("\x1f")
            if len(f) < 3:
                continue
            sha, parents, subj = f
            m = re_merge.match(subj)
            ps = parents.split()
            if not m or len(ps) != 2 or m.group(1) in seen:
                continue
            seen.add(m.group(1))
            branch = m.group(3)
            log = git(repo, "log", "--no-merges", "-n", "60", "--format=%an%x1f%ae%x1f%B%x1e", f"{ps[0]}..{ps[1]}")
            commits = [c.strip().split("\x1f") for c in log.split("\x1e") if c.strip()]
            commits = [c for c in commits if len(c) == 3]
            if not commits:
                continue
            authors = Counter(c[0] for c in commits)
            author = authors.most_common(1)[0][0]
            emails = " ".join(c[1] for c in commits)
            bot = bool(re_bot.search(author) or re_bot.search(emails))
            label = bool(any(re_marker.search(c[2]) for c in commits))
            rows.append(dict(repo=name, period=period, number=int(m.group(1)), branch=branch, author=author, bot=bot,
                             label=label, strict=strict(branch), loose=loose(branch), commits=len(commits)))
    print(f"{name}: {sum(1 for r in rows if r['repo'] == name)} PRs", file=sys.stderr)

json.dump(rows, open("branch_rows.json", "w"))


def pct(a, b):
    return f"{100 * a / b:5.1f}%" if b else "   - "


def summary(rs, title):
    print(f"\n## {title}")
    people26 = [r for r in rs if r["period"] == "2026" and not r["bot"]]
    bots26 = [r for r in rs if r["period"] == "2026" and r["bot"]]
    people22 = [r for r in rs if r["period"] == "2022" and not r["bot"]]
    lab = [r for r in people26 if r["label"]]
    unl = [r for r in people26 if not r["label"]]
    print(f"2026, people's accounts: {len(people26)} PRs ({len(lab)} labelled agent, {len(unl)} unlabelled); agent/bot accounts: {len(bots26)}")
    print(f"{'':<40}{'agent prefix':>14}{'name anywhere':>15}")
    for t, v in (("labelled agent PRs (people)", lab), ("unlabelled PRs (people)", unl), ("agent/bot-account PRs", bots26),
                 ("2022 PRs (people, pre-agent)", people22)):
        print(f"{t:<40}{pct(sum(bool(r['strict']) for r in v), len(v)):>14}{pct(sum(bool(r['loose']) for r in v), len(v)):>15}   ({len(v)})")


summary(rows, "all repos")
for repo in sorted(set(r["repo"] for r in rows)):
    summary([r for r in rows if r["repo"] == repo], repo)

print("\nunlabelled PRs from people's accounts on agent-prefixed branches (possible hidden agent work):")
for r in [r for r in rows if r["period"] == "2026" and not r["bot"] and not r["label"] and r["strict"]][:40]:
    print(f"  {r['repo']}#{r['number']}  {r['branch'][:60]}  by {r['author']}")
print("\n2022 PRs whose branch contains an agent name (would be wrong flags):")
for r in [r for r in rows if r["period"] == "2022" and (r["strict"] or r["loose"])][:40]:
    print(f"  {r['repo']}#{r['number']}  {r['branch'][:60]}  by {r['author']}")
