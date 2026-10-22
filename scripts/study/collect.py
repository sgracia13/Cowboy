"""Collect AI reviewer activity on a sample of public pull requests.

For each pull request in sample.json (reverted within 30 days of merge, or a
control merged in the same month and repo), record what bot reviewers said:
review summaries, inline comments with file and line, and top-level comments.
For reverted pull requests, also record the revert pull request's description
(the stated reason), used only for matching and never published.

Human reviewers are counted, never named. Output: results.jsonl, one line per
pull request, written as it goes so a partial run is still usable.
"""
import json, os, sys, time, urllib.request, urllib.error

TOKEN = os.environ.get("GITHUB_TOKEN", "")
OUT = sys.argv[2] if len(sys.argv) > 2 else "results.jsonl"


def get(url):
    while True:
        req = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json", "Authorization": "Bearer " + TOKEN, "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "cowboy-reviewer-study"})
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                body = json.load(r)
                link = r.headers.get("Link", "")
                rem = int(r.headers.get("X-RateLimit-Remaining", "100"))
                if rem < 5:
                    wait = max(5, int(r.headers.get("X-RateLimit-Reset", "0")) - int(time.time()) + 5)
                    print(f"rate limit: sleeping {wait}s", flush=True)
                    time.sleep(wait)
                return body, link
        except urllib.error.HTTPError as e:
            limited = e.code == 429 or e.headers.get("X-RateLimit-Remaining") == "0" or e.headers.get("Retry-After")
            if e.code in (403, 429) and limited:
                reset = e.headers.get("X-RateLimit-Reset")
                wait = max(30, int(reset) - int(time.time()) + 5) if reset else 60
                print(f"{e.code}: sleeping {wait}s", flush=True)
                time.sleep(wait)
                continue
            if e.code in (403, 451):
                print(f"refused {e.code} for {url}: {e.read()[:300]!r}", flush=True)
                raise SystemExit(1)
            if e.code == 404:
                return None, ""
            raise
        except Exception as e:  # network blip
            print("retry:", e, flush=True)
            time.sleep(10)


def pages(url):
    out = []
    while url:
        body, link = get(url + ("&" if "?" in url else "?") + "per_page=100" if "per_page" not in url else url)
        if not body:
            break
        out += body
        url = None
        for part in link.split(","):
            if 'rel="next"' in part:
                url = part.split(";")[0].strip()[1:-1]
    return out


def is_bot(u):
    return bool(u) and (u.get("type") == "Bot" or u.get("login", "").endswith("[bot]") or u.get("login") == "Copilot")


def clip(s, n=2000):
    return (s or "")[:n]


def main():
    items = json.load(open(sys.argv[1]))
    done = set()
    if os.path.exists(OUT):
        for l in open(OUT):
            d = json.loads(l)
            done.add((d["repo"], d["n"]))
    f = open(OUT, "a")
    for i, it in enumerate(items):
        if (it["repo"], it["n"]) in done:
            continue
        base = f"https://api.github.com/repos/{it['repo']}"
        reviews = pages(f"{base}/pulls/{it['n']}/reviews")
        inline = pages(f"{base}/pulls/{it['n']}/comments")
        talk = pages(f"{base}/issues/{it['n']}/comments")
        rec = dict(it)
        rec["bot_reviews"] = [dict(bot=r["user"]["login"], state=r.get("state"), body=clip(r.get("body"))) for r in reviews if is_bot(r.get("user"))]
        rec["bot_inline"] = [dict(bot=c["user"]["login"], path=c.get("path"), line=c.get("line") or c.get("original_line"), start=c.get("start_line"), body=clip(c.get("body"), 1500)) for c in inline if is_bot(c.get("user"))]
        rec["bot_talk"] = [dict(bot=c["user"]["login"], body=clip(c.get("body"), 3000)) for c in talk if is_bot(c.get("user"))]
        rec["human_reviews"] = sum(1 for r in reviews if not is_bot(r.get("user")))
        rec["human_changes_requested"] = sum(1 for r in reviews if not is_bot(r.get("user")) and r.get("state") == "CHANGES_REQUESTED")
        rec["human_inline"] = sum(1 for c in inline if not is_bot(c.get("user")))
        if it.get("revert_pr"):
            rp, _ = get(f"{base}/pulls/{it['revert_pr']}")
            rec["revert_reason"] = clip((rp or {}).get("body"), 3000)
            rec["revert_title"] = (rp or {}).get("title")
        f.write(json.dumps(rec) + "\n")
        f.flush()
        if i % 25 == 0:
            print(f"{i}/{len(items)}", flush=True)


main()
