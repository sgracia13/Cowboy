import json, re, sys, os
from collections import Counter, defaultdict
repo = sys.argv[1]
def rows(p):
    with open(p) as f:
        for l in f:
            yield json.loads(l)
JS_EXT = ('.js','.jsx','.ts','.tsx','.mjs','.cjs','.mts','.cts')
out = {}
# ---------- JS/TS extraction
truth = defaultdict(Counter); truth_target = {}
for r in rows(f'{repo}.ts.jsonl'):
    truth[r['file']][r['spec']] += 1
    if r['target']: truth_target[(r['file'], r['spec'])] = r['target']
ours = defaultdict(Counter); ours_target = {}
pyours = defaultdict(Counter); py_res = Counter()
for r in rows(f'{repo}.cowboy.jsonl'):
    if r['lang'] == 'js':
        ours[r['file']][r['spec']] += 1
        if r.get('targets'): ours_target[(r['file'], r['spec'])] = r['targets'][0]
    elif r['lang'] == 'py':
        pyours[r['file']][r['spec']] += 1
        py_res['resolved' if r.get('targets') else 'unresolved'] += 1
old_re = re.compile(r'''(?:import\s[^'"]*from\s*|import\s*\(\s*|require\(\s*|import\s+)['"]([^'"]+)['"]''')
def old_specs(rel):
    c = Counter()
    try:
        for line in open(os.path.join(repo, rel), encoding='utf-8', errors='replace'):
            for m in old_re.findall(line): c[m] += 1
    except Exception: pass
    return c
T = sum(sum(c.values()) for c in truth.values())
def score(get):
    tp = ours_n = 0
    for f in set(truth) | set(ours):
        o = get(f); t = truth.get(f, Counter())
        tp += sum((o & t).values()); ours_n += sum(o.values())
    return tp / T, tp / max(ours_n, 1)
out['js_imports_truth'] = T
out['js_new_recall'], out['js_new_precision'] = score(lambda f: ours.get(f, Counter()))
out['js_old_recall'], out['js_old_precision'] = score(old_specs)
# ---------- JS/TS resolution, where TypeScript resolved the import inside the repo
agree = dis = miss = 0; dis_ex = []
for k, t in truth_target.items():
    o = ours_target.get(k)
    if o == t: agree += 1
    elif o is None: miss += 1; dis_ex.append(('missed',) + k + (t,))
    else: dis += 1; dis_ex.append(('different',) + k + (t, o))
out['js_ts_resolved'] = len(truth_target)
out['js_resolution_same'] = agree / max(len(truth_target), 1)
out['js_resolution_missed'] = miss / max(len(truth_target), 1)
out['js_resolution_different'] = dis / max(len(truth_target), 1)
truth_keys = {(f, s) for f in truth for s in truth[f]}
only_ours = [k for k in ours_target if k not in truth_target and k in truth_keys]
out['js_only_cowboy_resolved'] = len(only_ours)
# ---------- fan-in at the thresholds the rules use (JS/TS files)
tf = defaultdict(set)
for (f, s), t in truth_target.items():
    if t != f: tf[t].add(f)
def load_fanin(p):
    return {r['file']: r['fanin'] for r in rows(p)}
newf, oldf = load_fanin(f'{repo}.new.fanin.jsonl'), load_fanin(f'{repo}.old.fanin.jsonl')
files = [f for f in set(tf) | {f for f in newf if f.endswith(JS_EXT)} | {f for f in oldf if f.endswith(JS_EXT)} if f.endswith(JS_EXT) and not f.endswith('.d.ts')]
def cls(n): return 'careful' if n >= 20 else ('easy' if n <= 3 else 'mid')
for name, m in (('new', newf), ('old', oldf)):
    same = sum(cls(m.get(f, 0)) == cls(len(tf.get(f, ()))) for f in files)
    tc = [f for f in files if len(tf.get(f, ())) >= 20]
    caught = sum(m.get(f, 0) >= 20 for f in tc)
    flagged = [f for f in files if m.get(f, 0) >= 20]
    right = sum(len(tf.get(f, ())) >= 20 for f in flagged)
    out[f'fanin_{name}_same_band'] = same / max(len(files), 1)
    out[f'fanin_{name}_catches_20plus'] = caught / max(len(tc), 1)
    out[f'fanin_{name}_20plus_confirmed'] = right / max(len(flagged), 1)
out['fanin_files'] = len(files); out['fanin_truth_20plus'] = sum(len(tf.get(f, ())) >= 20 for f in files)
# ---------- Python extraction vs ast
pt = defaultdict(Counter)
for r in rows(f'{repo}.py.jsonl'): pt[r['file']][r['spec']] += 1
PT = sum(sum(c.values()) for c in pt.values())
if PT:
    tp = on = 0
    for f in set(pt) | set(pyours):
        o = pyours.get(f, Counter()); t = pt.get(f, Counter())
        if f not in pt: continue  # file ast couldn't parse
        tp += sum((o & t).values()); on += sum(o.values())
    out['py_imports_truth'] = PT; out['py_new_recall'] = tp / PT; out['py_new_precision'] = tp / max(on, 1)
    # old regexes, compared at module level (they never read names)
    pf = re.compile(r'^\s*from\s+([.\w]+)\s+import\s'); pi = re.compile(r'^\s*import\s+([\w.]+)')
    tp = on = tm = 0
    for f, t in pt.items():
        tmod = Counter(s.split(':')[0] for s in t.elements()); tm += sum(tmod.values())
        o = Counter()
        for line in open(os.path.join(repo, f), encoding='utf-8', errors='replace'):
            m = pf.match(line) or pi.match(line)
            if m: o[m.group(1)] += 1
        tp += sum((o & tmod).values()); on += sum(o.values())
    out['py_old_recall_module'] = tp / tm; out['py_old_precision_module'] = tp / max(on, 1)
    out['py_resolved_share'] = py_res['resolved'] / max(sum(py_res.values()), 1)
json.dump(out, open(f'{repo}.compare.json', 'w'), indent=1)
with open(f'{repo}.disagree.txt', 'w') as fh:
    for e in dis_ex[:3000]: fh.write('\t'.join(e) + '\n')
for k, v in out.items(): print(f'{k:32s} {v:.3f}' if isinstance(v, float) else f'{k:32s} {v}')
