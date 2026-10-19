package graph

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestJSImports(t *testing.T) {
	src := "import a from './a'\n" +
		"import {\n  b1,\n  b2 as bb,\n} from \"./b\";\n" +
		"import type { T } from './types'\n" +
		"import * as ns from '../ns'\n" +
		"import './side-effect.css'\n" +
		"export * from './re-export'\n" +
		"export { x } from './x'\n" +
		"export type { Y } from './y'\n" +
		"export const notAnImport = 1\n" +
		"const lazy = () => import('./lazy')\n" +
		"const c = require('./c')\n" +
		"import legacy = require('./legacy')\n" +
		"// import fake from './commented'\n" +
		"/* import alsoFake from './block'\n */\n" +
		"const s = \"import nope from './in-string'\"\n" +
		"const tpl = `import nope from './in-template' ${await import('./in-substitution')}`\n" +
		"const re = /import x from '.\\/regex'/g\n" +
		"const ratio = a / b / c\n" +
		"obj.require('./method-not-require')\n" +
		"console.log(import.meta.url)\n" +
		"import { z } from './after-everything'\n"
	got := jsImports([]byte(src))
	want := []string{"./a", "./b", "./types", "../ns", "./side-effect.css", "./re-export", "./x", "./y", "./lazy", "./c", "./legacy", "./in-substitution", "./after-everything"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPyImports(t *testing.T) {
	src := `"""Module docs.

import not_this
from also import not_this
"""
import os, sys as system
import a.b.c
from . import sibling
from ..parent import (
    one,
    two as deux,  # comment
)
from pkg.mod \
    import thing
x = "from fake import nope"  # import nope
try: import optional
except ImportError: pass
def f():
    from inner import late
`
	var got []string
	for _, im := range pyImports([]byte(src)) {
		got = append(got, strings.Repeat(".", im.Level)+im.Module+":"+strings.Join(im.Names, ","))
	}
	want := []string{"os:", "sys:", "a.b.c:", ".:sibling", "..parent:one,two", "pkg.mod:thing", "optional:", "inner:late"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestJavaImports(t *testing.T) {
	src := `/* header
import fake.Nope; */
package com.acme.app;

import com.acme.util.Strings;
import static com.acme.util.Math.max;
import com.acme.model.*;
// import com.fake.Gone;
@Service
public class App { String s = "import x.Y;"; }
`
	jf := javaImports([]byte(src))
	if jf.Package != "com.acme.app" || !reflect.DeepEqual(jf.Imports, []string{"com.acme.util.Strings", "com.acme.util.Math.max", "com.acme.model.*"}) {
		t.Errorf("%+v", jf)
	}
}

// build makes a graph from an in-memory repo.
func build(files map[string]string) *Graph {
	b := NewBuilder()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := []byte(files[n])
		if b.Wants(n, int64(len(c))) {
			b.Add(n, int64(len(c)), c)
		} else {
			b.Add(n, int64(len(c)), nil)
		}
	}
	return b.Graph()
}

func fanIn(t *testing.T, g *Graph, file string, want ...string) {
	t.Helper()
	_, got := g.FanIn(file)
	sort.Strings(want)
	if len(want) == 0 {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s imported by %q, want %q", file, got, want)
	}
}

func TestResolveTypeScript(t *testing.T) {
	g := build(map[string]string{
		"tsconfig.base.json": `{
			// comments are allowed
			"compilerOptions": {
				"baseUrl": ".",
				"paths": { "@/*": ["src/*"], "@app/config": ["src/config/index.ts"], },
			},
		}`,
		"tsconfig.json":                `{ "extends": "./tsconfig.base.json" }`,
		"src/lib/http.ts":              `export const get = 1`,
		"src/config/index.ts":          `export default {}`,
		"src/ui/button.tsx":            `import { get } from '@/lib/http'`,
		"src/ui/form.tsx":              "import cfg from '@app/config'\nimport { get } from '../lib/http.js'",
		"src/pages/home.ts":            "import { Button } from '@/ui/button'\nimport { get } from 'src/lib/http'",
		"packages/shared/package.json": `{"name": "@acme/shared", "main": "dist/index.js"}`,
		"packages/shared/src/index.ts": `export * from './dates'`,
		"packages/shared/src/dates.ts": `export const d = 1`,
		"apps/web/main.ts":             "import { d } from '@acme/shared'\nimport { d as d2 } from '@acme/shared/dates'\nimport React from 'react'",
		"package.json":                 `{"name": "root", "dependencies": {"shared": "workspace:@acme/shared@*"}}`,
		"apps/web/alias.ts":            "import { d } from 'shared/dates'",
		"node_modules/react/index.js":  `module.exports = {}`,
	})
	fanIn(t, g, "src/lib/http.ts", "src/ui/button.tsx", "src/ui/form.tsx", "src/pages/home.ts")
	fanIn(t, g, "src/config/index.ts", "src/ui/form.tsx")
	fanIn(t, g, "src/ui/button.tsx", "src/pages/home.ts")
	fanIn(t, g, "packages/shared/src/index.ts", "apps/web/main.ts")
	fanIn(t, g, "packages/shared/src/dates.ts", "apps/web/alias.ts", "apps/web/main.ts", "packages/shared/src/index.ts")
	if g.Has("node_modules/react/index.js") {
		t.Error("node_modules should be skipped")
	}
}

func TestResolvePython(t *testing.T) {
	g := build(map[string]string{
		"src/acme/__init__.py":        "",
		"src/acme/models/__init__.py": "from .user import User",
		"src/acme/models/user.py":     "class User: pass",
		"src/acme/api/__init__.py":    "",
		"src/acme/api/views.py":       "from acme.models import User\nfrom acme.models.user import User as U\nfrom . import helpers",
		"src/acme/api/helpers.py":     "from ..models import user",
		"tests/test_views.py":         "import acme.api.views",
		"scripts/run.py":              "import tool",
		"scripts/tool.py":             "",
	})
	fanIn(t, g, "src/acme/models/user.py", "src/acme/models/__init__.py", "src/acme/api/views.py", "src/acme/api/helpers.py")
	fanIn(t, g, "src/acme/models/__init__.py", "src/acme/api/views.py")
	fanIn(t, g, "src/acme/api/helpers.py", "src/acme/api/views.py")
	fanIn(t, g, "src/acme/api/views.py", "tests/test_views.py")
	fanIn(t, g, "scripts/tool.py", "scripts/run.py")
}

func TestResolveGoAndJava(t *testing.T) {
	g := build(map[string]string{
		"go.mod":                                 "module github.com/acme/app\n\ngo 1.22\n",
		"internal/store/store.go":                "package store",
		"internal/store/more.go":                 "package store\nimport \"github.com/acme/app/internal/log\"",
		"internal/log/log.go":                    "package log",
		"cmd/app/main.go":                        "package main\nimport (\n\t\"fmt\"\n\tst \"github.com/acme/app/internal/store\"\n\t// \"github.com/acme/app/internal/gone\"\n)",
		"src/main/java/com/acme/A.java":          "package com.acme;\nimport com.acme.util.*;\nimport com.acme.model.User.Role;",
		"src/main/java/com/acme/util/S.java":     "package com.acme.util;",
		"src/main/java/com/acme/util/T.java":     "package com.acme.util;",
		"src/main/java/com/acme/model/User.java": "package com.acme.model;",
	})
	fanIn(t, g, "internal/store/store.go", "cmd/app/main.go")
	fanIn(t, g, "internal/log/log.go", "internal/store/more.go")
	fanIn(t, g, "src/main/java/com/acme/util/S.java", "src/main/java/com/acme/A.java")
	fanIn(t, g, "src/main/java/com/acme/model/User.java", "src/main/java/com/acme/A.java")
}

func tarGz(t *testing.T, prefix string, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: prefix + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: prefix + "/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestFromTarGz(t *testing.T) {
	files := map[string]string{
		"src/a.ts":                "import { b } from './b'",
		"src/b.ts":                "export const b = 1",
		"src/c.ts":                "import { b } from './b'",
		"node_modules/x/index.js": "require('../../src/b')",
		"big.min.js":              strings.Repeat("x", MaxFileBytes+1),
	}
	g, err := FromTarGz(bytes.NewReader(tarGz(t, "acme-app-1a2b3c", files)), 0)
	if err != nil {
		t.Fatal(err)
	}
	fanIn(t, g, "src/b.ts", "src/a.ts", "src/c.ts")
	if g.Stats.Source != "archive" || g.Stats.TooBig != 1 || g.Stats.Parsed != 3 {
		t.Errorf("stats %+v", g.Stats)
	}
	noise := make([]byte, 20000)
	rand.Read(noise) // random bytes don't compress
	big := tarGz(t, "p", map[string]string{"a.ts": "export {}", "b.bin": string(noise)})
	if _, err := FromTarGz(bytes.NewReader(big), 1000); !errors.Is(err, ErrTooBig) {
		t.Errorf("a limit of 1000 bytes: err = %v, want ErrTooBig", err)
	}
}

// Node's rule for exports patterns: the longest prefix before * wins. The
// pick must not depend on map order (Cowboy promises the same pen every time).
func TestExportsPatternsAreDeterministic(t *testing.T) {
	files := map[string]string{
		"packages/schema/package.json": `{"name": "@acme/schema", "exports": {
			"./*": "./src/*.ts",
			"./apis/*": "./src/apis/*.ts",
			"./apis/*/examples": "./src/examples/*.ts",
			".": {"@acme/source": "./src/index.ts", "types": "./dist/index.d.ts", "import": "./dist/esm/index.mjs"}
		}}`,
		"packages/schema/src/index.ts":            "",
		"packages/schema/src/examples/v2.ts":      "",
		"packages/schema/src/apis/v2.ts":          "",
		"packages/schema/src/apis/v2/examples.ts": "",
		"app/a.ts": "import x from '@acme/schema/apis/v2/examples'\nimport y from '@acme/schema'",
	}
	for i := 0; i < 50; i++ {
		g := build(files)
		fanIn(t, g, "packages/schema/src/examples/v2.ts", "app/a.ts")
		fanIn(t, g, "packages/schema/src/index.ts", "app/a.ts")
		if t.Failed() {
			t.Fatalf("run %d", i)
		}
	}
}
