# Link every workspace package into node_modules, as `pnpm install` would, so
# the TypeScript reference can resolve workspace imports too.
import json, os, sys
root = os.path.abspath(sys.argv[1]); pkgs = {}; aliases = {}
for d, dirs, files in os.walk(root):
    dirs[:] = [x for x in dirs if x not in ('node_modules', '.git', 'dist', 'build')]
    if 'package.json' in files:
        try: p = json.load(open(os.path.join(d, 'package.json')))
        except Exception: continue
        if p.get('name'): pkgs[p['name']] = d
        for deps in (p.get('dependencies') or {}, p.get('devDependencies') or {}):
            for a, v in deps.items():
                for pre in ('workspace:', 'npm:'):
                    if isinstance(v, str) and v.startswith(pre):
                        rest = v[len(pre):]; i = rest.rfind('@')
                        if i > 0 and rest[:i] != a: aliases[a] = rest[:i]
links = dict(pkgs)
for a, t in aliases.items():
    if t in pkgs and a not in links: links[a] = pkgs[t]
nm = os.path.join(root, 'node_modules'); n = 0
for name, d in links.items():
    dst = os.path.join(nm, name)
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    if not os.path.lexists(dst): os.symlink(d, dst); n += 1
print(f'{len(pkgs)} workspace packages, {len(aliases)} aliases, {n} links', file=sys.stderr)
