#!/usr/bin/env python3
"""Write only the source files (what import graphs read) of one commit into a
folder, fetching their contents in bulk from a blob-less clone.

usage: checkout_src.py REPO_DIR COMMIT OUT_DIR
"""
import os, subprocess, sys

REPO, COMMIT, OUT = sys.argv[1:4]
EXT = (".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".go", ".java")
SKIP = ("node_modules/", "vendor/", "dist/", "build/", "fixtures/", ".yarn/")

ls = subprocess.run(["git", "-C", REPO, "ls-tree", "-r", COMMIT], capture_output=True, text=True).stdout
items = []
for line in ls.splitlines():
    meta, _, path = line.partition("\t")
    parts = meta.split()
    if len(parts) == 3 and parts[1] == "blob" and path.endswith(EXT) and not any(s in path for s in SKIP) and not path.endswith(".min.js"):
        items.append((parts[2], path))
oids = sorted(set(o for o, _ in items))
for i in range(0, len(oids), 5000):
    subprocess.run(["git", "-C", REPO, "-c", "fetch.negotiationAlgorithm=noop", "fetch", "-q", "origin", "--no-tags", "--no-write-fetch-head",
                    "--filter=blob:none", "--stdin"], input="\n".join(oids[i:i + 5000]) + "\n", text=True, capture_output=True)
    print(f"  {REPO}: fetched {min(i + 5000, len(oids))}/{len(oids)}", file=sys.stderr)

p = subprocess.Popen(["git", "-C", REPO, "cat-file", "--batch"], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
for oid, path in items:
    p.stdin.write((oid + "\n").encode())
    p.stdin.flush()
    header = p.stdout.readline().split()
    if len(header) < 3 or header[1] == b"missing":
        continue
    data = p.stdout.read(int(header[2]))
    p.stdout.read(1)
    dst = os.path.join(OUT, path)
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    with open(dst, "wb") as f:
        f.write(data)
p.stdin.close()
for extra in ("go.mod", "tsconfig.json", "package.json"):
    b = subprocess.run(["git", "-C", REPO, "show", f"{COMMIT}:{extra}"], capture_output=True).stdout
    if b:
        with open(os.path.join(OUT, extra), "wb") as f:
            f.write(b)
print(f"{REPO}: wrote {len(items)} source files to {OUT}")
