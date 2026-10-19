#!/usr/bin/env python3
"""How much could the current signals do if the weights were learned from data?

Trains on the matrices cowboy-eval exports (-matrix), leaving one repo out at a
time, so every number is measured on a repo the model never saw.
Positives: agent PRs with labels stripped. Negatives: unlabelled 2026 PRs from
the same people (some of these are hidden agent work, so results are, if
anything, pessimistic). 2022 PRs are reported separately as clean humans.

usage: fit.py results_with_matrix.jsonl
"""
import json, math, sys
import numpy as np
from sklearn.ensemble import HistGradientBoostingClassifier
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import roc_auc_score
from sklearn.impute import SimpleImputer
from sklearn.pipeline import make_pipeline
from sklearn.preprocessing import StandardScaler
from sklearn.inspection import permutation_importance

SKIP = {"DisclosedAgentShare", "UsualAgent", "CorrectionsAgainst", "PhraseListVersion", "Agent", "How"}


def flatten(prefix, o, out):
    if isinstance(o, dict):
        if "known" in o and "value" in o:
            if not o["known"]:
                return
            v = o["value"]
            if isinstance(v, bool):
                out[prefix] = float(v)
            elif isinstance(v, (int, float)):
                out[prefix] = float(v)
            elif isinstance(v, dict):
                for k, x in v.items():
                    if isinstance(x, (int, float)) and not isinstance(x, bool):
                        out[prefix + "." + k] = float(x)
            elif isinstance(v, list):
                out[prefix + ".n"] = float(len(v))
            return
        for k, x in o.items():
            if k in SKIP:
                continue
            flatten(prefix + "." + k if prefix else k, x, out)


def feats(r):
    out = {}
    m = r["matrix"]
    for mod in ("Time", "Text", "Code"):
        key = {"Time": "time", "Text": "text", "Code": "code"}[mod]
        flatten(key, m.get(key) or m.get(mod) or {}, out)
    b = r.get("baseline") or {}
    out["base.sample_prs"] = float(b.get("sample_prs") or 0)
    # the author's own normal, and how far this PR sits from it
    for k, st in b.items():
        if isinstance(st, dict) and "median" in st and st.get("n"):
            out[f"base.{k}.median"] = st["median"]
            out[f"base.{k}.p90"] = st["p90"]
            for mod in ("time", "text", "code"):
                cur = out.get(f"{mod}.{k}", out.get(f"{mod}.{k}.median"))
                if cur is not None:
                    spread = max(st.get("mad") or 0, 1e-6)
                    out[f"{mod}.{k}.vs_own"] = (cur - st["median"]) / spread
    return out


rows = [json.loads(l) for l in open(sys.argv[1])]
for r in rows:
    r["f"] = feats(r)
names = sorted({k for r in rows for k in r["f"]})
names = [n for n in names if sum(1 for r in rows if n in r["f"]) > 0.05 * len(rows)]
X = np.array([[r["f"].get(n, np.nan) for n in names] for r in rows])
truth = np.array([r["truth"] for r in rows])
repo = np.array([r["repo"] for r in rows])
print(f"{len(rows)} PRs, {len(names)} features")

models = {
    "simple (logistic)": lambda: make_pipeline(SimpleImputer(strategy="median", add_indicator=True), StandardScaler(), LogisticRegression(max_iter=2000, C=0.5)),
    "flexible (boosted trees)": lambda: HistGradientBoostingClassifier(max_iter=300, learning_rate=0.05, max_leaf_nodes=15, l2_regularization=1.0),
}
for mname, mk in models.items():
    print(f"\n### {mname}, trained on two repos, tested on the third")
    allp = np.zeros(len(rows))
    for held in sorted(set(repo)):
        tr = (repo != held) & np.isin(truth, ["agent", "unlabelled"])
        te = repo == held
        clf = mk().fit(X[tr], (truth[tr] == "agent").astype(int))
        p = clf.predict_proba(X[te])[:, 1]
        allp[te] = p
        a, u, h = p[truth[te] == "agent"], p[truth[te] == "unlabelled"], p[truth[te] == "human-2022"]
        # pick the line so only 1% of clean 2022 humans would be flagged
        line = np.quantile(h, 0.99)
        line2 = np.quantile(u, 0.95)
        print(f"  {held:<18} AUC vs unlabelled {roc_auc_score(np.r_[np.ones(len(a)), np.zeros(len(u))], np.r_[a, u]):.3f}"
              f"  vs 2022 humans {roc_auc_score(np.r_[np.ones(len(a)), np.zeros(len(h))], np.r_[a, h]):.3f}"
              f" | at 1% wrong flags on 2022 humans: catches {100 * (a > line).mean():4.1f}%"
              f" | at 5% of unlabelled flagged: catches {100 * (a > line2).mean():4.1f}%")
    a, u, h = allp[truth == "agent"], allp[truth == "unlabelled"], allp[truth == "human-2022"]
    print(f"  overall AUC vs unlabelled {roc_auc_score(np.r_[np.ones(len(a)), np.zeros(len(u))], np.r_[a, u]):.3f}"
          f" vs 2022 humans {roc_auc_score(np.r_[np.ones(len(a)), np.zeros(len(h))], np.r_[a, h]):.3f}")

# which signals carry the weight
tr = np.isin(truth, ["agent", "unlabelled"])
clf = models["flexible (boosted trees)"]().fit(X[tr], (truth[tr] == "agent").astype(int))
pi = permutation_importance(clf, X[tr], (truth[tr] == "agent").astype(int), scoring="roc_auc", n_repeats=3, random_state=0)
print("\nsignals that matter most (drop in AUC when scrambled):")
for i in np.argsort(-pi.importances_mean)[:15]:
    print(f"  {pi.importances_mean[i]:.3f}  {names[i]}")
