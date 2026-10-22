import subprocess, re, sys, json, collections, glob, os
REV = {'greptile':r'greptile-apps\[bot\]','coderabbit':r'coderabbitai','copilot-review':r'copilot-pull-request-reviewer|Copilot <\d*\+?Copilot@users','gemini':r'gemini-code-assist','cursor-bugbot':r'cursor\[bot\]','sourcery':r'sourcery-ai','ellipsis':r'ellipsis-dev'}
AGENT = {'claude':r'Claude','cursor-agent':r'cursoragent|Cursor Agent','devin':r'devin-ai','codex':r'Codex|codex','copilot-agent':r'copilot-swe-agent'}
SEP='\x1e'
def norm(x): return re.sub(r'[^a-z0-9]+',' ',x.lower()).strip()
def scan(g, since):
    out = subprocess.run(['git','-C',g,'log','--first-parent','--since='+since,'--format=%H%x1f%ct%x1f%s%x1f%b'+SEP,'HEAD'],capture_output=True,text=True).stdout
    prs, reverts, bysha, bytitle = {}, [], {}, {}
    for rec in out.split(SEP):
        rec = rec.strip('\n')
        if not rec: continue
        h, ct, s, b = (rec.split('\x1f')+['','',''])[:4]
        m = re.search(r'\(#(\d+)\)\s*$', s)
        n = int(m.group(1)) if m else None
        rv = re.match(r'^(?:[a-z]+(?:\([^)]*\))?!?:\s*)?revert\b[:\s]*"?(.*?)"?\s*(?:\(#\d+\))?\s*$', s, re.I)
        if rv and not re.search(r'revert(ing)? (share|a workflow)', s, re.I):
            reverts.append((rv.group(1), b, int(ct), n))
            continue
        if n is None: continue
        trail = '\n'.join(l for l in b.splitlines() if l.lower().startswith('co-authored-by'))
        bysha[h] = n
        bytitle[norm(re.sub(r'\s*\(#\d+\)\s*$','',s))] = n
        prs[n] = dict(t=int(ct), rev=[k for k,p in REV.items() if re.search(p, trail)], agent=[k for k,p in AGENT.items() if re.search(p, trail)])
    reverted = {}
    for title, body, t, rn in reverts:
        n = None
        m = re.search(r'This reverts commit ([0-9a-f]{40})', body)
        if m and m.group(1) in bysha: n = bysha[m.group(1)]
        if n is None:
            m = re.search(r'#(\d+)', title) or re.search(r'reverts? .{0,40}?#(\d+)', body, re.I)
            if m: n = int(m.group(1))
        if n is None: n = bytitle.get(norm(re.sub(r'\s*\(#\d+\)\s*$','',title)))
        if n in prs and 0 <= t - prs[n]['t'] <= 30*86400: reverted[n] = dict(prs[n], revert_pr=rn)
    return prs, reverted, len(reverts)
since = sys.argv[1]
import hashlib
if len(sys.argv) > 3:
    items = []
    for g in sorted(glob.glob(sys.argv[2]+'/*.git')):
        repo = open(g+'/config').read().split('url = https://github.com/')[1].split('.git')[0].strip()
        prs, rev, _ = scan(g, since)
        for n, p in rev.items():
            items.append(dict(repo=repo, n=n, group='reverted', revert_pr=p['revert_pr'], merged=p['t']))
        # control: same repo, not reverted, merged in the same months, deterministic pick, 1 per reverted PR
        months = collections.Counter(p['t']//(30*86400) for p in rev.values())
        pool = sorted((hashlib.sha1(f"{repo}#{n}".encode()).hexdigest(), n) for n, p in prs.items() if n not in rev)
        for _, n in pool:
            m = prs[n]['t']//(30*86400)
            if months[m] > 0:
                months[m] -= 1
                items.append(dict(repo=repo, n=n, group='control', merged=prs[n]['t']))
    json.dump(items, open(sys.argv[3],'w'), indent=0)
    print(len(items), 'items'); sys.exit()
for g in sorted(glob.glob(sys.argv[2]+'/*.git')):
    prs, rev, nrev = scan(g, since)
    days = 365
    sug = collections.Counter(k for p in prs.values() for k in p['rev'])
    ag = sum(1 for p in prs.values() if p['agent'])
    agrev = sum(1 for p in rev.values() if p['agent'])
    print(json.dumps(dict(repo=os.path.basename(g)[:-4], merged_prs=len(prs), revert_commits=nrev, linked_to_pr=len(rev), revert_rate=round(len(rev)/max(1,len(prs))*100,2), agent_coauthored=ag, agent_reverted=agrev, prs_with_accepted_reviewer_suggestion=dict(sug))))
