package graph

// jsImports returns the module specifiers a JavaScript or TypeScript file
// imports: static imports (including ones split over many lines), side-effect
// imports, `export … from`, dynamic `import()`, `require()` and TypeScript's
// `import x = require()`. It skips comments, strings, template literals and
// regular expressions, so an import inside any of those isn't counted.
//
// It's a scanner, not a full parser: it only has to find these few forms,
// which is what the TypeScript compiler's own preProcessFile does too.
func jsImports(src []byte) []string {
	s := &jsScan{src: src}
	var out []string
	for s.i < len(s.src) {
		c := s.src[s.i]
		switch {
		case c == '/' && s.peek(1) == '/':
			s.lineComment()
		case c == '/' && s.peek(1) == '*':
			s.blockComment()
		case c == '\'' || c == '"':
			s.str(c)
			s.prev = 'v'
		case c == '`':
			s.template()
			s.prev = 'v'
		case c == '/':
			if s.regexAllowed() {
				s.regex()
				s.prev = 'v'
			} else {
				s.i++
				s.prev = '/'
			}
		case c == '}' && len(s.tmpl) > 0 && s.tmpl[len(s.tmpl)-1] == s.depth:
			// end of a ${…} inside a template literal: back into the template
			s.tmpl = s.tmpl[:len(s.tmpl)-1]
			s.i++
			s.templateRest()
			s.prev = 'v'
		case isIdentStart(c):
			start := s.i
			for s.i < len(s.src) && isIdentPart(s.src[s.i]) {
				s.i++
			}
			word := string(s.src[start:s.i])
			dotted := s.prev == '.'
			s.prevWord = word
			s.prev = 'w'
			if dotted {
				continue // obj.import, obj.require: not ours
			}
			switch word {
			case "import":
				out = append(out, s.afterImport()...)
			case "export":
				out = append(out, s.afterExport()...)
			case "require":
				if spec, ok := s.callWithString(); ok {
					out = append(out, spec)
				}
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s.i++
		default:
			switch c {
			case '{', '(', '[':
				s.depth++
			case '}', ')', ']':
				s.depth--
			}
			s.prev = c
			s.prevWord = ""
			s.i++
		}
	}
	return out
}

type jsScan struct {
	src      []byte
	i        int
	prev     byte   // last significant thing: a punctuator, 'w' (word) or 'v' (value)
	prevWord string // the word, when prev == 'w'
	depth    int    // bracket depth, to find the end of a ${…}
	tmpl     []int  // depths at which a ${ was opened
}

func (s *jsScan) peek(n int) byte {
	if s.i+n < len(s.src) {
		return s.src[s.i+n]
	}
	return 0
}

func (s *jsScan) lineComment() {
	for s.i < len(s.src) && s.src[s.i] != '\n' {
		s.i++
	}
}

func (s *jsScan) blockComment() {
	s.i += 2
	for s.i < len(s.src) && !(s.src[s.i] == '*' && s.peek(1) == '/') {
		s.i++
	}
	s.i += 2
}

// str reads a quoted string and returns its contents.
func (s *jsScan) str(q byte) string {
	s.i++
	start := s.i
	for s.i < len(s.src) {
		switch s.src[s.i] {
		case '\\':
			s.i += 2
			continue
		case q:
			v := string(s.src[start:s.i])
			s.i++
			return v
		case '\n':
			return string(s.src[start:s.i]) // unterminated; stop at the line
		}
		s.i++
	}
	return string(s.src[start:])
}

func (s *jsScan) template() {
	s.i++
	s.templateRest()
}

// templateRest reads template text until the closing backtick, or until a
// ${, where it hands back to the main loop (code inside ${} can import too).
func (s *jsScan) templateRest() {
	for s.i < len(s.src) {
		switch s.src[s.i] {
		case '\\':
			s.i += 2
			continue
		case '`':
			s.i++
			return
		case '$':
			if s.peek(1) == '{' {
				// remember the depth: the } that brings it back here ends the ${…}
				s.i += 2
				s.tmpl = append(s.tmpl, s.depth)
				s.prev = '{'
				return
			}
		}
		s.i++
	}
}

// regexAllowed: a / starts a regular expression where a value can start
// (after an operator, an opening bracket, or a keyword like return).
func (s *jsScan) regexAllowed() bool {
	switch s.prev {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '<', '>', '~', '^':
		return true
	case 'w':
		switch s.prevWord {
		case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await":
			return true
		}
	}
	return false
}

func (s *jsScan) regex() {
	s.i++
	inClass := false
	for s.i < len(s.src) {
		switch s.src[s.i] {
		case '\\':
			s.i += 2
			continue
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				s.i++
				for s.i < len(s.src) && isIdentPart(s.src[s.i]) { // flags
					s.i++
				}
				return
			}
		case '\n':
			return
		}
		s.i++
	}
}

// skipSpace skips whitespace and comments.
func (s *jsScan) skipSpace() {
	for s.i < len(s.src) {
		c := s.src[s.i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s.i++
		case c == '/' && s.peek(1) == '/':
			s.lineComment()
		case c == '/' && s.peek(1) == '*':
			s.blockComment()
		default:
			return
		}
	}
}

func (s *jsScan) word() string {
	start := s.i
	for s.i < len(s.src) && isIdentPart(s.src[s.i]) {
		s.i++
	}
	return string(s.src[start:s.i])
}

// callWithString reads `( 'spec'` after require or import.
func (s *jsScan) callWithString() (string, bool) {
	save := s.i
	s.skipSpace()
	if s.i >= len(s.src) || s.src[s.i] != '(' {
		s.i = save
		return "", false
	}
	s.i++
	s.skipSpace()
	if s.i < len(s.src) && (s.src[s.i] == '\'' || s.src[s.i] == '"') {
		spec := s.str(s.src[s.i])
		s.prev = 'v'
		s.depth++ // the ( we took; the main loop sees its )
		return spec, true
	}
	if s.i < len(s.src) && s.src[s.i] == '`' {
		// import(`./x`) with no ${}: a plain path
		start := s.i + 1
		j := start
		for j < len(s.src) && s.src[j] != '`' && s.src[j] != '$' && s.src[j] != '\n' {
			j++
		}
		if j < len(s.src) && s.src[j] == '`' {
			s.i = j + 1
			s.prev = 'v'
			s.depth++
			return string(s.src[start:j]), true
		}
	}
	s.i = save
	return "", false
}

// afterImport handles what follows the word import.
func (s *jsScan) afterImport() []string {
	save := s.i
	s.skipSpace()
	if s.i >= len(s.src) {
		return nil
	}
	switch c := s.src[s.i]; {
	case c == '(':
		s.i = save
		if spec, ok := s.callWithString(); ok {
			return []string{spec}
		}
		return nil
	case c == '.': // import.meta
		s.i = save
		return nil
	case c == '\'' || c == '"': // import './side-effect'
		spec := s.str(c)
		s.prev = 'v'
		return []string{spec}
	}
	return s.clauseFrom(true)
}

// afterExport handles `export … from 'x'`; any other export isn't an import.
func (s *jsScan) afterExport() []string {
	save := s.i
	s.skipSpace()
	if s.i >= len(s.src) {
		return nil
	}
	c := s.src[s.i]
	if c == '*' || c == '{' {
		return s.clauseFrom(false)
	}
	if isIdentStart(c) {
		w := s.word()
		if w == "type" {
			s.skipSpace()
			if s.i < len(s.src) && (s.src[s.i] == '{' || s.src[s.i] == '*') {
				return s.clauseFrom(false)
			}
		}
	}
	s.i = save
	return nil
}

// clauseFrom reads an import or export clause up to `from 'spec'`, or
// TypeScript's `import x = require('spec')`. It stops at anything that can't
// be part of a clause, so a stray word never swallows the file.
func (s *jsScan) clauseFrom(isImport bool) []string {
	save := s.i
	braces := 0
	for steps := 0; s.i < len(s.src) && steps < 4000; steps++ {
		s.skipSpace()
		if s.i >= len(s.src) {
			break
		}
		c := s.src[s.i]
		switch {
		case c == '{':
			braces++
			s.i++
		case c == '}':
			braces--
			s.i++
		case c == ',' || c == '*':
			s.i++
		case c == '\'' || c == '"':
			if braces > 0 { // import { "string name" as x }
				s.str(c)
				continue
			}
			s.i = save
			return nil
		case c == '=' && isImport && braces == 0:
			s.i++
			s.skipSpace()
			if w := s.word(); w == "require" {
				if spec, ok := s.callWithString(); ok {
					return []string{spec}
				}
			}
			s.i = save
			return nil
		case isIdentStart(c):
			w := s.word()
			if w == "from" && braces == 0 {
				s.skipSpace()
				if s.i < len(s.src) && (s.src[s.i] == '\'' || s.src[s.i] == '"') {
					spec := s.str(s.src[s.i])
					s.prev = 'v'
					return []string{spec}
				}
				// `import from from 'x'` or a name called from: keep going
			}
		default:
			s.i = save
			return nil
		}
	}
	s.i = save
	return nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }
