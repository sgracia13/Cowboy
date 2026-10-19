# blast radius accuracy check

Checks Cowboy's import graph against the reference tools: the TypeScript
compiler (`ts.preProcessFile` to read imports, `ts.resolveModuleName` with
each file's nearest tsconfig to resolve them) and Python's own `ast`.

## results (October 19, 2026)

Repos, shallow clones of the default branch: n8n-io/n8n `06ba85cbb9bf`,
grafana/grafana `d9178ed1a764`, getsentry/sentry `50bbbad27693`.
"old" is v0.2.0 (line patterns), "new" is v0.3.

| | n8n | grafana | sentry |
|---|---|---|---|
| JS/TS imports (TypeScript's count) | 112,472 | 60,463 | 77,384 |
| found, old | 88.3% | 93.5% | 93.3% |
| found, new | 100.0% | 100.0% | 100.0% |
| wrongly found, new | 0.1% | 0.0% | 0.0% |
| resolved to the same file as TypeScript, new | 100.0% | 99.9% | 100.0% |
| files with 20+ importers (TypeScript) | 409 | 292 | 446 |
| caught at 20+, old | 49.1% | 59.6% | 0.9% |
| caught at 20+, new | 100% | 100% | 100% |
| same fan-in band (≤3, 4–19, 20+), old | 89.9% | 90.8% | 72.2% |
| same fan-in band, new | 99.2% | 100.0% | 100.0% |
| Python imports (`ast`'s count) | 306 | 8 | 76,863 |
| found, new | 100.0% | 100.0% | 100.0% |

- Python resolution has no reference tool. In sentry, 99.99% of imports of
  its own packages (48,921 of 48,926) resolve to a file; the 5 that don't are
  test fixtures importing modules that don't exist.
- n8n: 23,753 imports point at workspace packages whose package.json names
  build output (`dist/`) that isn't in the repo, so TypeScript can't resolve
  them without a build. Cowboy maps them back to `src/` (the packages'
  `rootDir`); checked by hand on a sample. These explain n8n's lower "same
  band" and the 9.7% of its 20+ files TypeScript can't confirm.
- Graphs built from a `git archive` .tar.gz were identical to graphs built
  from the folder, on all three repos. 6 runs per repo gave identical output.
- Go is read with Go's own parser (`go/parser`), so it isn't checked here.
  Java and Kotlin aren't checked yet.

## run it

```sh
git clone --depth 1 https://github.com/grafana/grafana
npm install typescript@5.6.3
python3 linkws.py grafana                 # link workspace packages, like an install
go run ../../../cmd/cowboy-graphlab imports grafana > grafana.cowboy.jsonl
go run ../../../cmd/cowboy-graphlab fanin grafana > grafana.new.fanin.jsonl
node tstruth.js grafana > grafana.ts.jsonl
python3 pytruth.py grafana > grafana.py.jsonl
# for the old/new comparison, build v0.2.0's graph into grafana.old.fanin.jsonl
python3 compare.py grafana
```
