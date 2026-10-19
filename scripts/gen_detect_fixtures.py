"""Generates the sample PRs and author histories for the undisclosed-agent
check. Deterministic (fixed seed), so the fixtures never drift.

  #540  jdoe   masked agent PR on a codex/ branch       -> should be caught
  #542  priya  masked agent PR, ordinary branch name    -> should be caught
  #541  sam    fast human codemod (sam does these)      -> should NOT be caught
  #520  alice  small human change                       -> should NOT be caught
"""
import json, random, os, datetime as dt
R = random.Random(42)
ROOT = os.path.join(os.path.dirname(__file__), "..", "testdata")
Z = lambda d: d.strftime("%Y-%m-%dT%H:%M:%SZ")
base_day = dt.datetime(2026, 10, 16)

def write(path, obj):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        if isinstance(obj, str): f.write(obj)
        else: json.dump(obj, f, indent=2)

# ------------------------------------------------------------------ histories
def lognorm(median, spread):
    import math
    return median * math.exp(R.gauss(0, spread))

def history(author, n, hours, lpm, gap, b2p, typo, multi, conv, signed, template=0.0, codemod=0.0, preagent=0):
    out = []
    for i in range(n + preagent):
        pre = i >= n
        when = (dt.datetime(2022, 3, 1) + dt.timedelta(days=i * 3)) if pre else base_day - dt.timedelta(days=R.uniform(2, 85))
        commits = R.choice([1, 1, 2, 2, 3, 4, 5])
        is_mod = R.random() < codemod
        h = R.choice(hours)
        p = {
            "repo": "acme/app", "number": 100 + i, "at": Z(when.replace(hour=h)),
            "lines_per_minute": round(lognorm(180, 0.35) if is_mod else lognorm(lpm, 0.6), 2),
            "commits": commits,
            "hours": [min(23, max(0, h + R.choice([0, 0, 1, -1]))) for _ in range(commits)],
            "typo_rate_per_1k_words": round(max(0, R.gauss(typo, typo * 0.35)), 2),
            "multiline_commit_ratio": round(min(1, max(0, R.gauss(multi, 0.08))), 2) if R.random() < 0.85 else 0,
            "conventional_commit_ratio": conv,
            "comment_to_code_ratio": round(abs(R.gauss(0.08, 0.05)), 2),
            "bullet_density": round(abs(R.gauss(0.1, 0.08)), 2),
            "template_sections": R.random() < template,
            "signed": R.random() < signed,
            "pre_agent": pre,
        }
        if commits > 1:
            p["inter_commit_gap_sec"] = round(lognorm(gap, 0.8))
        p["branch_to_pr_sec"] = round(lognorm(60 if is_mod else b2p, 0.7))
        out.append(p)
    return out

H = os.path.join(ROOT, "history")
write(os.path.join(H, "jdoe.json"), history("jdoe", 40, list(range(13, 22)), 4, 2400, 6 * 3600, 6.5, 0.04, 0.6, 0.92, preagent=20))
write(os.path.join(H, "priya.json"), history("priya", 35, list(range(8, 17)), 3.5, 3000, 5 * 3600, 4.0, 0.02, 0.3, 0.0))
write(os.path.join(H, "sam.json"), history("sam", 50, list(range(14, 23)), 5, 1500, 2 * 3600, 9.0, 0.0, 0.8, 0.5, codemod=0.2))
write(os.path.join(H, "alice.json"), history("alice", 30, list(range(12, 20)), 3, 2000, 4 * 3600, 5.0, 0.1, 0.5, 0.7))

# ------------------------------------------------------------------- PR #540
P = os.path.join(ROOT, "prs")
t = dt.datetime(2026, 10, 18, 3, 12, 0)
body540 = """## Summary
This PR introduces a retry budget for the job runner so failed jobs back off instead of hammering the queue.

## Changes
- Added a `RetryBudget` class that tracks attempts per job.
- Updated `j0` to consult the budget before retrying.
- Added comprehensive unit tests covering edge cases.

## Testing
- Ran the full test suite locally and all tests pass."""
diff540 = """diff --git a/src/jobs/retryBudget.ts b/src/jobs/retryBudget.ts
new file mode 100644
--- /dev/null
+++ b/src/jobs/retryBudget.ts
@@ -0,0 +1,44 @@
+// RetryBudget tracks how many times each job has been retried.
+export class RetryBudget {
+  // Map of job id to the number of attempts made so far
+  private attempts: Map<string, number> = new Map();
+
+  constructor(private readonly maxAttempts: number = 3) {
+    // Validate the max attempts value
+    if (maxAttempts === undefined || maxAttempts === null) {
+      throw new Error("maxAttempts is required");
+    }
+    if (maxAttempts <= 0) {
+      throw new Error("maxAttempts must be positive");
+    }
+  }
+
+  // Check whether the job can be retried
+  canRetry(jobId: string): boolean {
+    if (!jobId) {
+      return false;
+    }
+    const used = this.attempts.get(jobId) ?? 0;
+    return used < this.maxAttempts;
+  }
+
+  // Record a retry attempt for the job
+  record(jobId: string): void {
+    if (!jobId) {
+      throw new Error("jobId is required");
+    }
+    const used = this.attempts.get(jobId) ?? 0;
+    this.attempts.set(jobId, used + 1);
+  }
+
+  // Reset the attempts for the job
+  reset(jobId: string): void {
+    if (!jobId) {
+      return;
+    }
+    this.attempts.delete(jobId);
+  }
+}
+
+export const defaultBudget = new RetryBudget();
diff --git a/src/jobs/j0.ts b/src/jobs/j0.ts
--- a/src/jobs/j0.ts
+++ b/src/jobs/j0.ts
@@ -1,2 +1,14 @@
-import { send } from "../webhooks/sender";
-export const j0 = () => send("/x", "0");
+import { send } from '../webhooks/sender';
+import { defaultBudget } from './retryBudget';
+
+// Run job 0, retrying while the budget allows
+export const j0 = async () => {
+  try {
+    return await send('/x', '0');
+  } catch (err) {
+    if (defaultBudget.canRetry('j0')) {
+      defaultBudget.record('j0');
+      return j0();
+    }
+    throw err;
+  }
+};
diff --git a/src/jobs/retryBudget.test.ts b/src/jobs/retryBudget.test.ts
new file mode 100644
--- /dev/null
+++ b/src/jobs/retryBudget.test.ts
@@ -0,0 +1,18 @@
+import { RetryBudget } from "./retryBudget";
+
+describe("RetryBudget", () => {
+  it("allows retries up to the limit", () => {
+    const b = new RetryBudget(2);
+    expect(b.canRetry("a")).toBe(true);
+    b.record("a");
+    b.record("a");
+    expect(b.canRetry("a")).toBe(false);
+  });
+
+  it("handles empty job ids", () => {
+    const b = new RetryBudget();
+    expect(b.canRetry("")).toBe(false);
+  });
+
+  it("rejects zero", () => { expect(() => new RetryBudget(0)).toThrow(); });
+});
"""
msgs540 = [
    "feat(jobs): add RetryBudget class\n\nIntroduce RetryBudget to cap the number of retries per job.",
    "feat(jobs): use retry budget in j0\n\nUpdate j0 to consult the budget before retrying a failed send.",
    "test(jobs): add RetryBudget tests\n\nCover the retry limit, empty job ids and invalid limits.",
    "refactor(jobs): tidy imports\n\nNormalize quote style in the job runner.",
]
def commits(start, gaps, msgs, lines, verified):
    out, cur = [], start
    for i, m in enumerate(msgs):
        if i: cur = cur + dt.timedelta(seconds=gaps[i - 1])
        out.append({"sha": f"{R.getrandbits(28):07x}", "message": m, "author_date": Z(cur), "committer_date": Z(cur),
                    "verified": verified, "lines_added": lines[i][0], "lines_removed": lines[i][1]})
    return out
c540 = commits(t, [12, 9, 15], msgs540, [(44, 0), (13, 2), (18, 0), (1, 0)], False)
last = dt.datetime.strptime(c540[-1]["committer_date"], "%Y-%m-%dT%H:%M:%SZ")
write(os.path.join(P, "540", "meta.json"), {"repo": "acme/app", "number": 540, "title": "add retry budget to job runner", "author": "jdoe",
      "body": body540, "commits": msgs540, "checks_ok": True})
write(os.path.join(P, "540", "pr.diff"), diff540)
write(os.path.join(P, "540", "snapshot.json"), {"branch": "codex/retry-budget", "opened_at": Z(last + dt.timedelta(seconds=10)),
      "first_push_at": Z(last + dt.timedelta(seconds=6)), "author_created": "2020-01-15T10:00:00Z", "org_member": True,
      "force_pushes_known": True, "force_pushes": [], "commits": c540, "reviews": []})
write(os.path.join(P, "540", "expect.txt"), "skip\n")

# ------------------------------------------------------------------- PR #542
t = dt.datetime(2026, 10, 18, 14, 5, 0)
body542 = """## Summary
This PR introduces paginated CSV export for the audit log, ensuring that large exports no longer time out.

## Key changes
- Added `exportAudit` with page-by-page streaming for better readability and maintainability.
- Updated `listAudit` to accept `page` and `size` arguments.
- Added tests to cover edge cases such as empty pages.

## Testing
- All existing and new tests pass locally."""
diff542 = """diff --git a/src/api/audit.ts b/src/api/audit.ts
--- a/src/api/audit.ts
+++ b/src/api/audit.ts
@@ -1,1 +1,12 @@
-export function listAudit() { return []; }
+// Return one page of audit rows
+export function listAudit(page: number = 0, size: number = 50): string[] {
+  // Validate the page number
+  if (page === undefined || page === null || page < 0) {
+    throw new Error("page must be zero or more");
+  }
+  // Validate the page size
+  if (size === undefined || size === null || size <= 0) {
+    throw new Error("size must be positive");
+  }
+  return [];
+}
diff --git a/src/api/export.ts b/src/api/export.ts
new file mode 100644
--- /dev/null
+++ b/src/api/export.ts
@@ -0,0 +1,26 @@
+import { listAudit } from "./audit";
+
+// Export the audit log as CSV, one page at a time
+export async function exportAudit(size: number = 50): Promise<string> {
+  // Validate the size argument
+  if (size === undefined || size === null || size <= 0) {
+    throw new Error("size must be positive");
+  }
+  const lines: string[] = [];
+  let page = 0;
+  // Loop over every page until an empty page is returned
+  while (true) {
+    const rows = listAudit(page, size);
+    // Stop when the page is empty
+    if (!rows || rows.length === 0) {
+      break;
+    }
+    // Add each row to the output
+    for (const row of rows) {
+      lines.push(row ?? "");
+    }
+    page = page + 1;
+  }
+  // Join the lines with newlines
+  return lines.join("\\n");
+}
diff --git a/src/api/export.test.ts b/src/api/export.test.ts
new file mode 100644
--- /dev/null
+++ b/src/api/export.test.ts
@@ -0,0 +1,10 @@
+import { exportAudit } from "./export";
+
+describe("exportAudit", () => {
+  it("returns an empty string for an empty log", async () => {
+    expect(await exportAudit()).toBe("");
+  });
+  it("rejects a zero size", async () => {
+    await expect(exportAudit(0)).rejects.toThrow();
+  });
+});
"""
msgs542 = [
    "feat(audit): paginate listAudit\n\nAdd page and size arguments with validation.",
    "feat(audit): add paginated CSV export\n\nStream the audit log page by page to avoid timeouts.",
    "test(audit): cover export edge cases\n\nAdd tests for empty logs and invalid sizes.",
]
c542 = commits(t, [24, 31], msgs542, [(12, 1), (26, 0), (10, 0)], False)
last = dt.datetime.strptime(c542[-1]["committer_date"], "%Y-%m-%dT%H:%M:%SZ")
write(os.path.join(P, "542", "meta.json"), {"repo": "acme/app", "number": 542, "title": "paginate audit log export", "author": "priya",
      "body": body542, "commits": msgs542, "checks_ok": True})
write(os.path.join(P, "542", "pr.diff"), diff542)
write(os.path.join(P, "542", "snapshot.json"), {"branch": "fix/audit-export", "opened_at": Z(last + dt.timedelta(seconds=9)),
      "first_push_at": Z(last + dt.timedelta(seconds=4)), "author_created": "2019-06-02T08:00:00Z", "org_member": True,
      "force_pushes_known": True, "force_pushes": [], "commits": c542, "reviews": []})
write(os.path.join(P, "542", "expect.txt"), "skip\n")

# ------------------------------------------------------------------- PR #541
t = dt.datetime(2026, 10, 18, 16, 40, 0)
files = [f"src/jobs/j{i}.ts" for i in range(0, 6)]
diff541 = ""
for i, f in enumerate(files):
    diff541 += f"""diff --git a/{f} b/{f}
--- a/{f}
+++ b/{f}
@@ -1,2 +1,2 @@
-import {{ send }} from "../webhooks/sender";
-export const j{i} = () => send("/x", "{i}");
+import {{ dispatch }} from "../webhooks/sender";
+export const j{i} = () => dispatch("/x", "{i}");
"""
for name in ["client", "sender"]:
    diff541 += f"""diff --git a/src/webhooks/{name}.ts b/src/webhooks/{name}.ts
--- a/src/webhooks/{name}.ts
+++ b/src/webhooks/{name}.ts
@@ -1,1 +1,1 @@
-export async function send(url: string, body: string) {{ return fetch(url, {{method: "POST", body}}); }}
+export async function dispatch(url: string, body: string) {{ return fetch(url, {{method: "POST", body}}); }}
"""
for k in range(0, 38):
    f = f"src/features/f{k:02d}/index.ts"
    diff541 += f"""diff --git a/{f} b/{f}
--- a/{f}
+++ b/{f}
@@ -1,1 +1,1 @@
-// uses send
+// uses dispatch
"""
msgs541 = ["rename send -> dispatch (codemod)\n\nran scripts/rename.sh send dispatch, no behaviour change"]
c541 = [{"sha": "5a1c0de", "message": msgs541[0], "author_date": Z(t), "committer_date": Z(t + dt.timedelta(seconds=50)),
         "verified": True, "lines_added": 46, "lines_removed": 46}]
write(os.path.join(P, "541", "meta.json"), {"repo": "acme/app", "number": 541, "title": "rename send to dispatch everywhere", "author": "sam",
      "body": "codemod: send -> dispatch. ran ./scripts/rename.sh, tests green locally. no behaviour change, dont review line by line", "commits": msgs541, "checks_ok": True})
write(os.path.join(P, "541", "pr.diff"), diff541)
write(os.path.join(P, "541", "snapshot.json"), {"branch": "sam/rename-dispatch", "opened_at": Z(t + dt.timedelta(seconds=95)),
      "first_push_at": Z(t + dt.timedelta(seconds=80)), "author_created": "2017-04-11T08:00:00Z", "org_member": True,
      "force_pushes_known": True, "force_pushes": [], "commits": c541, "reviews": []})
write(os.path.join(P, "541", "expect.txt"), "skip\n")

# ------------------------------------------------------------------- PR #520 timing
t = dt.datetime(2026, 10, 18, 15, 2, 0)
write(os.path.join(P, "520", "snapshot.json"), {"branch": "alice/logging", "opened_at": Z(t + dt.timedelta(minutes=34)),
      "first_push_at": Z(t + dt.timedelta(minutes=31)), "author_created": "2018-09-01T08:00:00Z", "org_member": True,
      "force_pushes_known": True, "force_pushes": [], "commits": [{"sha": "0b5e2aa", "message": "chore: logging", "author_date": Z(t),
      "committer_date": Z(t + dt.timedelta(minutes=2)), "verified": True, "lines_added": 1, "lines_removed": 1}], "reviews": []})
print("fixtures written")
