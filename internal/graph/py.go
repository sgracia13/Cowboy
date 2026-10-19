package graph

import "strings"

// pyImport is one Python import statement.
type pyImport struct {
	Level  int      // leading dots: 0 = absolute
	Module string   // a.b.c ("" for `from . import x`)
	Names  []string // names after `from … import` (nil for plain `import`)
}

// pyImports finds every import statement, at any indentation (inside
// functions, try blocks and `if TYPE_CHECKING:` too, as Python's own ast
// does). It joins lines inside brackets and after a backslash, and ignores
// strings and comments, so a docstring that says "import x" isn't counted.
func pyImports(src []byte) []pyImport {
	var out []pyImport
	for _, stmt := range pyStatements(src) {
		out = append(out, pyParse(stmt)...)
	}
	return out
}

// pyStatements splits source into logical statements with strings blanked
// out and comments removed.
func pyStatements(src []byte) []string {
	var stmts []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			stmts = append(stmts, t)
		}
		cur.Reset()
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			i-- // let the newline be handled
		case c == '\'' || c == '"':
			// string prefixes (r, b, f, u…) are already in cur as letters; fine
			q := c
			triple := i+2 < len(src) && src[i+1] == q && src[i+2] == q
			if triple {
				i += 3
				for i < len(src) && !(src[i] == q && i+2 < len(src) && src[i+1] == q && src[i+2] == q) {
					if src[i] == '\\' {
						i++
					}
					i++
				}
				i += 2
			} else {
				i++
				for i < len(src) && src[i] != q && src[i] != '\n' {
					if src[i] == '\\' {
						i++
					}
					i++
				}
			}
			cur.WriteString(`""`)
		case c == '\\' && i+1 < len(src) && src[i+1] == '\n':
			i++
			cur.WriteByte(' ')
		case c == '(' || c == '[' || c == '{':
			depth++
			cur.WriteByte(c)
		case c == ')' || c == ']' || c == '}':
			if depth > 0 {
				depth--
			}
			cur.WriteByte(c)
		case c == '\n':
			if depth > 0 {
				cur.WriteByte(' ')
			} else {
				flush()
			}
		case c == ';' && depth == 0:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return stmts
}

func pyParse(stmt string) []pyImport {
	// `try: import x`, `else: from y import z` and the like
	for _, kw := range []string{"try:", "else:", "finally:"} {
		if strings.HasPrefix(stmt, kw) {
			stmt = strings.TrimSpace(stmt[len(kw):])
		}
	}
	if rest, ok := cutWord(stmt, "import"); ok {
		var out []pyImport
		for _, part := range strings.Split(rest, ",") {
			name := strings.Fields(strings.TrimSpace(part))
			if len(name) > 0 && pyDotted(name[0]) {
				out = append(out, pyImport{Module: name[0]})
			}
		}
		return out
	}
	if rest, ok := cutWord(stmt, "from"); ok {
		mod, names, ok := strings.Cut(rest, " import")
		if !ok {
			return nil
		}
		mod = strings.TrimSpace(mod)
		level := len(mod) - len(strings.TrimLeft(mod, "."))
		mod = strings.TrimSpace(mod[level:])
		if mod != "" && !pyDotted(mod) {
			return nil
		}
		names = strings.Trim(strings.TrimSpace(names), "()")
		imp := pyImport{Level: level, Module: mod}
		for _, part := range strings.Split(names, ",") {
			f := strings.Fields(strings.TrimSpace(part))
			if len(f) > 0 && f[0] != "*" && pyDotted(f[0]) {
				imp.Names = append(imp.Names, f[0])
			}
		}
		return []pyImport{imp}
	}
	return nil
}

func cutWord(s, w string) (string, bool) {
	if !strings.HasPrefix(s, w) || len(s) == len(w) {
		return "", false
	}
	if c := s[len(w)]; c != ' ' && c != '\t' && c != '.' {
		return "", false
	}
	return s[len(w):], true
}

func pyDotted(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" || !isIdentStart(part[0]) {
			return false
		}
		for i := 0; i < len(part); i++ {
			if !isIdentPart(part[i]) || part[i] == '$' {
				return false
			}
		}
	}
	return true
}
