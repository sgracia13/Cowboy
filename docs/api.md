# events and the API (for orchestrators and other tools)

Cowboy is the quality-control layer. Orchestrators decide who builds what;
Cowboy tells them how it turned out. Two ways to listen:

- **events**: Cowboy POSTs each thing that happens to your URL, as it happens.
- **the read API**: ask Cowboy about pull requests, rules and per-agent
  scorecards whenever you want.

Both are version 1. Within a version, fields are only ever added, never
renamed or removed: ignore fields you don't know. A breaking change gets a
new version number (`"v": 2`, `/api/v2/`).

Both only describe. Nothing here merges, closes, approves or reviews.

## events

### turn them on

| env | what |
|---|---|
| `EVENT_HOOKS` | one or more URLs, comma-separated (http or https) |
| `EVENT_HOOK_SECRET` | signs every event; strongly recommended |
| `EVENT_HOOK_TYPES` | optional: only these types, e.g. `pr.sorted,outcome.found` |

Then check your receiver can hear Cowboy:

```sh
EVENT_HOOKS=https://orchestrator.acme.dev/cowboy EVENT_HOOK_SECRET=... cowboy hooks test
# test event signed: delivered to 1 of 1 receiver(s)
```

`EVENT_HOOKS` works in `cowboy demo` too, so you can build a receiver
against the demo's pull requests before pointing it at real ones.

### the types

| type | when | `data` |
|---|---|---|
| `pr.sorted` | a pull request is sorted or re-sorted (opened, new commits, a rule change) | `pen`, `agent`, `rules` (careful rules that fired), `head` |
| `pr.moved` | a person moved it to another pen | `from`, `to`; `actor` is who |
| `pr.merged` | it merged | `pen`, `agent` |
| `pr.closed` | it closed without merging | `pen`, `agent` |
| `pr.not_agent` | a person said a pull request marked agent:likely is human work | (none); `actor` is who |
| `outcome.found` | trouble after merge: a revert or broken main (counted), or a likely fix or incident link (a suggestion until a person confirms it) | `id`, `kind`, `status`, `severity`, `evidence` |
| `outcome.confirmed` | a person confirmed or rejected a suggested outcome | `confirmed` (`true`/`false`), `id`, `kind`, `evidence` |
| `incident.received` | an incident arrived from a monitoring tool | `id`, `title`, `severity`, `release`; `actor` is the source |
| `test` | `cowboy hooks test` | `message` |

Sorting past pull requests to rate history (`cowboy backfill`) isn't news
and sends nothing.

### what one looks like

```http
POST /cowboy HTTP/1.1
Content-Type: application/json
User-Agent: cowboy-hooks
X-Cowboy-Event: pr.sorted
X-Cowboy-Delivery: 5b0e2c1f9a7d3e8a1c4b6f20
X-Cowboy-Signature: sha256=8f3a…
```

```json
{
  "v": 1,
  "id": "5b0e2c1f9a7d3e8a1c4b6f20",
  "type": "pr.sorted",
  "at": "2026-10-21T14:03:11Z",
  "repo": "acme/app",
  "number": 507,
  "data": { "pen": "careful", "agent": "codex", "rules": "payments", "head": "9c1e…" },
  "pr": {
    "title": "Retry failed charges",
    "url": "https://github.com/acme/app/pull/507",
    "state": "open",
    "pen": "careful",
    "rules_pen": "careful",
    "agent": "codex",
    "head_sha": "9c1e…",
    "careful_reasons": ["touches pay/ (payments)"],
    "shortcuts": [{ "kind": "skips", "file": "pay/charge_test.go", "detail": "adds a skip marker: t.Skip(\"flaky\")" }],
    "lines": 84,
    "files": 3
  }
}
```

`pr` is the pull request as Cowboy sees it at that moment. `pen` is the pen in
force (a person's move wins); `rules_pen` is what the rules said;
`moved_by_person` is `true` when they differ because someone moved it. An
empty `agent` means a person wrote it.

### check the signature

The signature is HMAC-SHA256 of the exact body bytes with your secret, hex,
prefixed `sha256=`. Check it before trusting anything in the body:

```python
import hmac, hashlib

def from_cowboy(body: bytes, header: str, secret: bytes) -> bool:
    want = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, header or "")
```

```js
import crypto from "node:crypto";
const fromCowboy = (body, header, secret) => {
  const want = "sha256=" + crypto.createHmac("sha256", secret).update(body).digest("hex");
  return header && want.length === header.length && crypto.timingSafeEqual(Buffer.from(want), Buffer.from(header));
};
```

In Go: `hooks.Verify(secret, body, header)` from `internal/hooks`.

### delivery

- **in order**, one at a time, to each URL.
- **answer 2xx** to accept. Answer quickly: do the work after you reply.
  Cowboy waits 10 seconds at most.
- **retries**: on a network error, 429 or 5xx, Cowboy tries again after 1s,
  4s, 15s and 1m, then gives up on that event. Any other 4xx is taken as
  your answer and not retried.
- **at least once**: a retry can repeat an event you already got. Use `id`
  (also in `X-Cowboy-Delivery`) to drop repeats.
- **never slows sorting**: events wait in a queue of 1000. If your receiver
  is down long enough to fill it, newer events are dropped and logged. On a
  clean shutdown Cowboy delivers what's queued (up to 20 seconds).
- **missed some?** Everything is also in the read API below and in the audit
  record (`cowboy audit export`), so a receiver can catch up.

## the read API

### access

Set `API_TOKEN` and send it as a bearer token:

```sh
curl -H "Authorization: Bearer $API_TOKEN" https://cowboy.acme.dev/api/v1/prs
```

The token reads `/api/...` and nothing else: it doesn't open the dashboard
and can't move, confirm or change anything. A signed-in browser session can
read the API too. With no login at all (`cowboy demo`, or a local address),
no token is needed.

Every response is JSON with `"v": 1`. Errors are `{"v": 1, "error": "..."}`
with a 400 or 404.

### `GET /api/v1/prs`

Pull requests Cowboy has sorted, by repo, then newest number first.

| query | what |
|---|---|
| `repo` | `owner/repo` (all repos if empty) |
| `state` | `open` (default), `merged`, `closed` or `all` |
| `pen` | `easy`, `normal` or `careful` (the pen in force) |
| `agent` | an agent name such as `codex`, or `person` for human work |

```json
{ "v": 1, "prs": [ { "repo": "acme/app", "number": 507, "decided_at": "…", "title": "…", "pen": "careful", "…": "the same fields as pr in an event" } ] }
```

### `GET /api/v1/pr?repo=acme/app&n=507`

One pull request: `pr` (as above), `reasons` (every rule that fired, with
its effect) and `outcomes` after merge. Each outcome has `kind`, `status`,
`counts` (true when it counts as trouble: found automatically, or confirmed
by a person), `at`, `evidence` and `link`.

### `GET /api/v1/rules?repo=acme/app`

The rules in force for a repo (its `cowboy.json`, or the defaults).

### `GET /api/v1/scorecards`

How each agent's merged pull requests turned out, and people's for
comparison.

| query | what |
|---|---|
| `repo` | one repo (all if empty) |
| `days` | only pull requests merged in the last N days (plus the trouble window) |

```json
{
  "v": 1,
  "window_days": 30,
  "note": "Rated pull requests only …",
  "agents": [
    { "agent": "person",      "rate": { "n": 2359, "bad": 34, "p": 0.014, "low": 0.010, "high": 0.020 } },
    { "agent": "claude-code", "rate": { "n": 116,  "bad": 0,  "p": 0,     "low": 0,     "high": 0.032 } }
  ],
  "by_area": [ "…the same, split by area of the code (only without days)…" ]
}
```

Read it carefully:

- **rated only.** A pull request counts once its trouble window
  (`OUTCOME_WINDOW_DAYS`, default 30) has passed since merge.
- **trouble** = reverted, broke main, or a fix or incident a person
  confirmed. Unconfirmed suggestions don't count.
- **compare ranges, not rates.** `low`–`high` is a 95% range. 0 of 116 is
  not "never fails"; it's "probably under 3.2%". Two agents whose ranges
  overlap haven't been shown to differ.
- **agents get different work.** An agent given only small changes will look
  safer than one given hard ones. `by_area` helps; it doesn't fix it.

Without after-merge tracking (`OUTCOMES=off`) this returns 404.

## what an orchestrator might do with this

- On `pr.sorted` with `pen: careful`, put the pull request in front of a
  senior reviewer, or hold the next task for that agent until it's reviewed.
- On `pr.sorted` with `shortcuts`, send the agent back to fix them before a
  person spends time on it.
- On `outcome.found`, tell the agent that wrote it, and the person who
  merged it.
- Before handing out work, read `/api/v1/scorecards` and prefer the agent
  whose pull requests in that area hold up, once the ranges are narrow
  enough to say so.

Cowboy won't do any of these itself: it never merges, closes or reviews.
