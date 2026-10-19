package graph

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// goImports uses Go's own parser, so it's exact.
func goImports(rel string, src []byte) []string {
	f, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.ImportsOnly)
	if err != nil && f == nil {
		return nil
	}
	var out []string
	for _, im := range f.Imports {
		if p, err := strconv.Unquote(im.Path.Value); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// javaFile is what Cowboy reads from a Java or Kotlin file.
type javaFile struct {
	Package string
	Imports []string // a.b.C, or a.b.* for a whole package
}

// javaImports reads the package and import lines, skipping comments and
// strings. Java has no imports inside methods, so the top of the file is
// enough; it stops at the first class body.
func javaImports(src []byte) javaFile {
	var jf javaFile
	var cur strings.Builder
	stmt := func() {
		t := strings.Join(strings.Fields(cur.String()), " ")
		cur.Reset()
		if rest, ok := strings.CutPrefix(t, "package "); ok {
			jf.Package = strings.ReplaceAll(rest, " ", "")
		} else if rest, ok := strings.CutPrefix(t, "import "); ok {
			rest = strings.TrimPrefix(rest, "static ")
			if i := strings.Index(rest, " as "); i >= 0 { // kotlin alias
				rest = rest[:i]
			}
			rest = strings.ReplaceAll(rest, " ", "")
			if rest != "" {
				jf.Imports = append(jf.Imports, rest)
			}
		}
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			stmt() // kotlin has no semicolons: a line ends a statement
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
		case c == '"':
			i++
			for i < len(src) && src[i] != '"' && src[i] != '\n' {
				if src[i] == '\\' {
					i++
				}
				i++
			}
		case c == ';' || c == '\n':
			stmt()
		case c == '{' || c == '@':
			// the class body or its annotations: no more imports
			stmt()
			return jf
		default:
			cur.WriteByte(c)
		}
	}
	stmt()
	return jf
}
