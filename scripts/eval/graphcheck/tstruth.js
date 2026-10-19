// Ground truth for JS/TS: the TypeScript compiler's own import reading
// (preProcessFile) and module resolution (resolveModuleName with each file's
// nearest tsconfig.json). Usage: node tstruth.js REPO > out.jsonl
const ts = require('typescript');
const fs = require('fs'), path = require('path');
const root = path.resolve(process.argv[2]);
const skip = new Set(['.git','node_modules','vendor','dist','build','.venv','venv','__pycache__','target','.next','coverage','bower_components']);
const exts = ['.js','.jsx','.ts','.tsx','.mjs','.cjs','.mts','.cts'];
const files = [];
(function walk(d) {
  for (const e of fs.readdirSync(d, {withFileTypes: true})) {
    const p = path.join(d, e.name);
    if (e.isDirectory()) { if (!skip.has(e.name)) walk(p); }
    else if (e.isFile() && exts.includes(path.extname(e.name)) && fs.statSync(p).size <= 2 << 20) files.push(p);
  }
})(root);
const cfgCache = new Map(), resCache = new Map();
function optionsFor(file) {
  const cfg = ts.findConfigFile(path.dirname(file), ts.sys.fileExists, 'tsconfig.json') || ts.findConfigFile(path.dirname(file), ts.sys.fileExists, 'jsconfig.json');
  const key = cfg && cfg.startsWith(root) ? cfg : '';
  if (!cfgCache.has(key)) {
    let opts = {moduleResolution: ts.ModuleResolutionKind.Node10};
    if (key) {
      const read = ts.readConfigFile(key, ts.sys.readFile);
      const parsed = ts.parseJsonConfigFileContent(read.config || {}, ts.sys, path.dirname(key), undefined, key);
      opts = parsed.options;
    }
    opts = {...opts, allowJs: true, resolveJsonModule: false, noEmit: true};
    if (!opts.moduleResolution) opts.moduleResolution = ts.ModuleResolutionKind.Node10;
    cfgCache.set(key, opts);
    resCache.set(key, ts.createModuleResolutionCache(root, x => x, opts));
  }
  return [cfgCache.get(key), resCache.get(key)];
}
let n = 0;
for (const f of files) {
  const text = fs.readFileSync(f, 'utf8');
  const info = ts.preProcessFile(text, true, true);
  const [opts, cache] = optionsFor(f);
  const rel = path.relative(root, f).split(path.sep).join('/');
  for (const imp of info.importedFiles) {
    let target = '';
    try {
      const r = ts.resolveModuleName(imp.fileName, f, opts, ts.sys, cache).resolvedModule;
      if (r && r.resolvedFileName.startsWith(root + path.sep) && !r.resolvedFileName.includes('/node_modules/') && !r.resolvedFileName.endsWith('.json'))
        target = path.relative(root, r.resolvedFileName).split(path.sep).join('/');
    } catch (e) {}
    process.stdout.write(JSON.stringify({file: rel, spec: imp.fileName, target}) + '\n');
    n++;
  }
}
console.error(`${files.length} files, ${n} imports`);
