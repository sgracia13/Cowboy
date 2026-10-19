# Ground truth for Python imports: Python's own ast. Usage: python3 pytruth.py REPO > out.jsonl
import ast, json, os, sys, warnings
warnings.filterwarnings("ignore")
root = os.path.abspath(sys.argv[1])
skip = {'.git','node_modules','vendor','dist','build','.venv','venv','__pycache__','target','.next','coverage','bower_components'}
nfiles = bad = 0
for d, dirs, files in os.walk(root):
    dirs[:] = [x for x in dirs if x not in skip]
    for f in files:
        if not f.endswith('.py'): continue
        p = os.path.join(d, f)
        if os.path.getsize(p) > 2 << 20: continue
        rel = os.path.relpath(p, root).replace(os.sep, '/')
        try:
            tree = ast.parse(open(p, 'rb').read())
        except Exception:
            bad += 1; continue
        nfiles += 1
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                for a in node.names:
                    print(json.dumps({"file": rel, "spec": a.name + ":"}))
            elif isinstance(node, ast.ImportFrom):
                names = ",".join(a.name for a in node.names if a.name != "*")
                print(json.dumps({"file": rel, "spec": "." * node.level + (node.module or "") + ":" + names}))
print(f"{nfiles} files parsed, {bad} with syntax errors skipped", file=sys.stderr)
