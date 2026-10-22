package rolemanager

import (
	"path/filepath"
	"strconv"
	"strings"
)

// A hand-written comment scanner for the common languages. Chroma's lexers
// run regular expressions at about 1 MB/s, which cost more than the model
// call they save on a large file; this scanner is a single pass over the
// bytes. It knows only comments, strings and the few raw-string forms of
// each language, and it fails closed: anything it cannot be sure of (an
// unterminated string, an unterminated block comment) sends the file back to
// chroma, so a scanning mistake can only include more text, never hide a
// comment.

// fastSyntax is how one language family writes comments and strings.
type fastSyntax struct {
	line        []string // line comment openers
	block       bool     // /* */ comments
	nestedBlock bool     // block comments nest (Swift, Kotlin, Scala, Dart)
	quotes      string   // single-line string delimiters
	raw         string   // multi-line raw delimiters (` for Go and JS)
	triple      bool     // """ and ''' strings (Python, Kotlin, Swift, ...)
	cppRaw      bool     // C++ R"delim( ... )delim"
	preproc     bool     // C preprocessor lines (#error and the like carry prose)
}

var (
	cFamily      = fastSyntax{line: []string{"//"}, block: true, quotes: `"'`, preproc: true}
	goSyntax     = fastSyntax{line: []string{"//"}, block: true, quotes: `"'`, raw: "`"}
	jsSyntax     = fastSyntax{line: []string{"//"}, block: true, quotes: `"'`, raw: "`"}
	cppSyntax    = fastSyntax{line: []string{"//"}, block: true, quotes: `"'`, cppRaw: true, preproc: true}
	jvmTriple    = fastSyntax{line: []string{"//"}, block: true, quotes: `"'`, triple: true}
	nestTriple   = fastSyntax{line: []string{"//"}, block: true, nestedBlock: true, quotes: `"'`, triple: true}
	pySyntax     = fastSyntax{line: []string{"#"}, quotes: `"'`, triple: true}
	fastSyntaxes = map[string]fastSyntax{
		".go": goSyntax,
		".js": jsSyntax, ".jsx": jsSyntax, ".mjs": jsSyntax, ".cjs": jsSyntax,
		".ts": jsSyntax, ".tsx": jsSyntax, ".mts": jsSyntax, ".cts": jsSyntax,
		".c": cFamily, ".h": cFamily, ".m": cFamily,
		".cc": cppSyntax, ".cpp": cppSyntax, ".cxx": cppSyntax, ".hpp": cppSyntax, ".hh": cppSyntax, ".hxx": cppSyntax, ".mm": cppSyntax,
		".java": jvmTriple, ".cs": jvmTriple,
		".kt": nestTriple, ".kts": nestTriple, ".scala": nestTriple, ".swift": nestTriple, ".dart": nestTriple,
		".py": pySyntax, ".pyi": pySyntax,
	}
)

// fastComments extracts comments the way ExtractComments does, for the
// languages fastSyntaxes covers. ok is false when the language is not covered
// or the scan was not sure, and the caller uses chroma instead.
func fastComments(path, src string) (string, bool) {
	sx, covered := fastSyntaxes[strings.ToLower(filepath.Ext(path))]
	if !covered {
		return "", false
	}
	var out strings.Builder
	line := 1
	lastEnd := -1
	emit := func(startLine int, text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if startLine != lastEnd {
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(strconv.Itoa(startLine))
			out.WriteString(": ")
		} else {
			out.WriteByte(' ')
		}
		out.WriteString(text)
		lastEnd = startLine + strings.Count(text, "\n")
	}
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		if c == '\n' {
			line++
			i++
			continue
		}
		// Line comments, C preprocessor lines (a directive starts its line,
		// after any indentation) and a script's #! line.
		if lc := lineOpener(src[i:], sx.line); lc != "" || (sx.preproc && c == '#' && lineStart(src, i)) || (i == 0 && strings.HasPrefix(src, "#!")) {
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				end = n - i
			}
			emit(line, src[i:i+end])
			i += end
			continue
		}
		// Block comments.
		if sx.block && strings.HasPrefix(src[i:], "/*") {
			end, ok := blockEnd(src, i, sx.nestedBlock)
			if !ok {
				return "", false
			}
			emit(line, src[i:end])
			line += strings.Count(src[i:end], "\n")
			i = end
			continue
		}
		// Triple-quoted strings: prose, always kept.
		if sx.triple && (strings.HasPrefix(src[i:], `"""`) || (strings.HasPrefix(src[i:], "'''") && strings.ContainsRune(sx.quotes, '\''))) {
			delim := src[i : i+3]
			end := strings.Index(src[i+3:], delim)
			if end < 0 {
				return "", false
			}
			end = i + 3 + end + 3
			emit(line, src[i:end])
			line += strings.Count(src[i:end], "\n")
			i = end
			continue
		}
		// C++ raw strings: R"delim( ... )delim", kept when they span lines.
		if sx.cppRaw && c == 'R' && i+1 < n && src[i+1] == '"' && (i == 0 || !isIdent(src[i-1])) {
			open := strings.IndexByte(src[i+2:], '(')
			if open < 0 || open > 16 {
				return "", false
			}
			delim := ")" + src[i+2:i+2+open] + `"`
			end := strings.Index(src[i+2+open:], delim)
			if end < 0 {
				return "", false
			}
			end = i + 2 + open + end + len(delim)
			if strings.Contains(src[i:end], "\n") {
				emit(line, src[i:end])
			}
			line += strings.Count(src[i:end], "\n")
			i = end
			continue
		}
		// Raw multi-line strings (Go and JS backticks): kept when they span
		// lines.
		if sx.raw != "" && c == sx.raw[0] {
			end, ok := rawEnd(src, i, c, sx.raw == "`" && isJS(path))
			if !ok {
				return "", false
			}
			if strings.Contains(src[i:end], "\n") {
				emit(line, src[i:end])
			}
			line += strings.Count(src[i:end], "\n")
			i = end
			continue
		}
		// Single-line strings. One that reaches the end of its line open is a
		// state these languages do not allow, so the scan was wrong (a regex
		// literal, a lifetime, a dialect): fail back to chroma.
		if strings.IndexByte(sx.quotes, c) >= 0 {
			end, ok := quotedEnd(src, i, c)
			if !ok {
				return "", false
			}
			i = end
			continue
		}
		i++
	}
	return out.String(), true
}

func lineOpener(s string, openers []string) string {
	for _, o := range openers {
		if strings.HasPrefix(s, o) {
			return o
		}
	}
	return ""
}

// blockEnd returns the index just past the block comment opening at i.
func blockEnd(src string, i int, nested bool) (int, bool) {
	depth := 0
	for j := i; j+1 < len(src); j++ {
		switch {
		case src[j] == '/' && src[j+1] == '*':
			if depth == 0 || nested {
				depth++
			}
			j++
		case src[j] == '*' && src[j+1] == '/':
			depth--
			j++
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return 0, false
}

// quotedEnd returns the index just past the single-line string opening at i.
func quotedEnd(src string, i int, q byte) (int, bool) {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case q:
			return j + 1, true
		case '\n':
			return 0, false
		}
	}
	return 0, false
}

// rawEnd returns the index just past the backtick string opening at i. A JS
// template's ${ } may hold nested templates, which are skipped in step.
func rawEnd(src string, i int, q byte, js bool) (int, bool) {
	for j := i + 1; j < len(src); j++ {
		switch {
		case js && src[j] == '\\':
			j++
		case src[j] == q:
			return j + 1, true
		case js && src[j] == '$' && j+1 < len(src) && src[j+1] == '{':
			// Skip the expression to its closing brace, minding nested
			// templates and strings.
			depth := 0
			for k := j + 1; k < len(src); k++ {
				switch src[k] {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						j = k
						goto next
					}
				case '`':
					end, ok := rawEnd(src, k, '`', true)
					if !ok {
						return 0, false
					}
					k = end - 1
				case '"', '\'':
					end, ok := quotedEnd(src, k, src[k])
					if !ok {
						return 0, false
					}
					k = end - 1
				}
			}
			return 0, false
		next:
		}
	}
	return 0, false
}

func isJS(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
		return true
	}
	return false
}

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// lineStart reports whether only spaces or tabs come before i on its line.
func lineStart(src string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch src[j] {
		case '\n':
			return true
		case ' ', '\t':
		default:
			return false
		}
	}
	return true
}
