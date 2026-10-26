package mergefix

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Duplicate imports are the commonest damage "accept both" does: each side
// added an import, or both added the same one, and keeping both sides lists it
// twice. The checks below read the import statements of one file by a fixed
// pattern per language and report the bindings or statements that occur more
// than once. They read no other part of the file, and what they return is an
// import's own identifier, never a line of the file.

// DuplicateImports reports the imports that occur twice in content, by the
// language its path names: Go, TypeScript and JavaScript, Python, Rust, Java
// and Kotlin, C#. Any other file has none. The result is sorted.
func DuplicateImports(path string, content []byte) []string {
	lines := SplitLines(content)
	var keys []string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		keys = goImports(lines)
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		keys = jsImports(lines)
	case ".py", ".pyi":
		keys = pyImports(lines)
	case ".rs":
		keys = rustImports(lines)
	case ".java", ".kt", ".kts":
		keys = jvmImports(lines)
	case ".cs":
		keys = csImports(lines)
	default:
		return nil
	}
	return duplicates(keys)
}

// NewDuplicateImports are the duplicates in resolved that neither side had:
// a duplicate the repository already carried is not the merge's doing.
func NewDuplicateImports(path string, resolved, ours, theirs []byte) []string {
	had := map[string]bool{}
	for _, k := range DuplicateImports(path, ours) {
		had[k] = true
	}
	for _, k := range DuplicateImports(path, theirs) {
		had[k] = true
	}
	var out []string
	for _, k := range DuplicateImports(path, resolved) {
		if !had[k] {
			out = append(out, k)
		}
	}
	return out
}

func duplicates(keys []string) []string {
	seen := map[string]int{}
	for _, k := range keys {
		if k != "" {
			seen[k]++
		}
	}
	var out []string
	for k, n := range seen {
		if n > 1 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// stripLineComment drops a trailing // comment outside a string. Import lines
// hold only identifiers and one quoted path, so a quote count is enough.
func stripLineComment(s, marker string) string {
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			inQuote = c
		case strings.HasPrefix(s[i:], marker):
			return s[:i]
		}
	}
	return s
}

var goSpecRE = regexp.MustCompile("^(?:([A-Za-z_][A-Za-z0-9_]*|\\.|_)\\s+)?(\"[^\"]*\"|`[^`]*`)$")

// goImports keys each import spec by its alias and path, and each explicit
// alias by its name: the same path twice, or one alias bound to two paths.
func goImports(lines []string) []string {
	var keys []string
	add := func(spec string) {
		spec = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stripLineComment(spec, "//")), ";"))
		m := goSpecRE.FindStringSubmatch(spec)
		if m == nil {
			return
		}
		path := strings.Trim(m[2], "\"`")
		keys = append(keys, "import "+strings.TrimSpace(m[1]+" "+path))
		if m[1] != "" && m[1] != "_" && m[1] != "." {
			keys = append(keys, "name "+m[1])
		}
	}
	inBlock := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if inBlock {
			if strings.HasPrefix(t, ")") {
				inBlock = false
				continue
			}
			add(t)
			continue
		}
		if !strings.HasPrefix(l, "import") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(l, "import"))
		if strings.HasPrefix(rest, "(") {
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "("))
			if strings.HasSuffix(rest, ")") {
				for _, s := range strings.Split(strings.TrimSuffix(rest, ")"), ";") {
					add(s)
				}
				continue
			}
			inBlock = true
			if rest != "" {
				add(rest)
			}
			continue
		}
		add(rest)
	}
	return keys
}

var (
	jsFromRE    = regexp.MustCompile(`^import\s+(?:type\s+)?(.*?)\s*from\s*(['"][^'"]+['"])\s*;?\s*$`)
	jsBareRE    = regexp.MustCompile(`^import\s*(['"][^'"]+['"])\s*;?\s*$`)
	jsRequireRE = regexp.MustCompile(`^import\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*require\(`)
	jsIdentRE   = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
)

// jsImports keys every binding an import statement introduces (default,
// namespace, each named import under its local name) and every side-effect
// import by its module. A statement may span lines up to its from clause.
func jsImports(lines []string) []string {
	var keys []string
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, "import ") && !strings.HasPrefix(l, "import{") && !strings.HasPrefix(l, "import'") && !strings.HasPrefix(l, `import"`) {
			continue
		}
		stmt := stripLineComment(l, "//")
		for j := i + 1; j < len(lines) && j < i+200 && !jsDone(stmt); j++ {
			stmt += " " + stripLineComment(lines[j], "//")
			i = j
		}
		stmt = strings.Join(strings.Fields(stmt), " ")
		if m := jsBareRE.FindStringSubmatch(stmt); m != nil {
			keys = append(keys, "module "+strings.Trim(m[1], `'"`))
			continue
		}
		if m := jsRequireRE.FindStringSubmatch(stmt); m != nil {
			keys = append(keys, "name "+m[1])
			continue
		}
		m := jsFromRE.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		clause := m[1]
		named := ""
		if a, b, ok := strings.Cut(clause, "{"); ok {
			named, _, _ = strings.Cut(b, "}")
			clause = a
		}
		for _, part := range strings.Split(clause, ",") {
			part = strings.TrimSpace(part)
			if ns, ok := strings.CutPrefix(part, "* as "); ok {
				part = strings.TrimSpace(ns)
			}
			if jsIdentRE.MatchString(part) {
				keys = append(keys, "name "+part)
			}
		}
		for _, part := range strings.Split(named, ",") {
			part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "type "))
			if _, local, ok := strings.Cut(part, " as "); ok {
				part = strings.TrimSpace(local)
			}
			if jsIdentRE.MatchString(part) {
				keys = append(keys, "name "+part)
			}
		}
	}
	return keys
}

// jsDone reports whether an import statement is complete: it has its module.
func jsDone(stmt string) bool {
	s := strings.Join(strings.Fields(stmt), " ")
	return jsFromRE.MatchString(s) || jsBareRE.MatchString(s) || jsRequireRE.MatchString(s) || strings.HasSuffix(strings.TrimSpace(s), ";")
}

var pyNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// pyImports keys the module-level imports: an `import a.b as c` by its module
// and alias, and every name a `from x import ...` binds by that name. Indented
// imports (inside a try, a function, a conditional) are left alone, because a
// fallback import there binds the same name on purpose.
func pyImports(lines []string) []string {
	var keys []string
	for i := 0; i < len(lines); i++ {
		l := stripLineComment(lines[i], "#")
		switch {
		case strings.HasPrefix(l, "import "):
			for _, part := range strings.Split(strings.TrimPrefix(l, "import "), ",") {
				f := strings.Fields(part)
				switch {
				case len(f) == 1 && pyNameRE.MatchString(f[0]):
					keys = append(keys, "import "+f[0])
				case len(f) == 3 && f[1] == "as" && pyNameRE.MatchString(f[0]):
					keys = append(keys, "import "+f[0]+" as "+f[2], "name "+f[2])
				}
			}
		case strings.HasPrefix(l, "from "):
			stmt := l
			if strings.Contains(stmt, "(") {
				for j := i + 1; j < len(lines) && j < i+200 && !strings.Contains(stmt, ")"); j++ {
					stmt += " " + stripLineComment(lines[j], "#")
					i = j
				}
			}
			for strings.HasSuffix(strings.TrimSpace(stmt), "\\") && i+1 < len(lines) {
				stmt = strings.TrimSuffix(strings.TrimSpace(stmt), "\\") + " " + stripLineComment(lines[i+1], "#")
				i++
			}
			_, names, ok := strings.Cut(stmt, " import ")
			if !ok {
				continue
			}
			names = strings.NewReplacer("(", " ", ")", " ").Replace(names)
			for _, part := range strings.Split(names, ",") {
				f := strings.Fields(part)
				switch {
				case len(f) == 1 && f[0] != "*" && pyNameRE.MatchString(f[0]):
					keys = append(keys, "name "+f[0])
				case len(f) == 3 && f[1] == "as":
					keys = append(keys, "name "+f[2])
				}
			}
		}
	}
	return keys
}

var rustPrefixRE = regexp.MustCompile(`^(?:pub(?:\([a-z: ]+\))?\s+)?use\s+`)

// rustImports keys every name a module-level use declaration binds, after
// flattening its braces: the last segment of each path, or its alias.
func rustImports(lines []string) []string {
	var keys []string
	for i := 0; i < len(lines); i++ {
		l := stripLineComment(lines[i], "//")
		loc := rustPrefixRE.FindStringIndex(l)
		if loc == nil || loc[0] != 0 {
			continue
		}
		stmt := l[loc[1]:]
		for j := i + 1; j < len(lines) && j < i+200 && !strings.Contains(stmt, ";"); j++ {
			stmt += " " + stripLineComment(lines[j], "//")
			i = j
		}
		stmt, _, _ = strings.Cut(stmt, ";")
		for _, b := range rustBindings(rustTight(stmt)) {
			keys = append(keys, "name "+b)
		}
	}
	return keys
}

// rustTight collapses a use tree's spacing: single spaces, and none around
// the path and brace punctuation, so "a :: { b , c as d }" is "a::{b,c as d}".
func rustTight(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for _, p := range []string{"::", "{", "}", ","} {
		s = strings.ReplaceAll(s, " "+p, p)
		s = strings.ReplaceAll(s, p+" ", p)
	}
	return s
}

// rustBindings flattens a tight use tree into the names it binds.
func rustBindings(tree string) []string {
	var out []string
	var walk func(prefix, t string)
	walk = func(prefix, t string) {
		if open := strings.IndexByte(t, '{'); open >= 0 && strings.HasSuffix(t, "}") {
			head := strings.TrimSuffix(t[:open], "::")
			inner := t[open+1 : len(t)-1]
			for _, part := range splitTop(inner) {
				walk(joinPath(prefix, head), part)
			}
			return
		}
		if t == "" || t == "*" {
			return
		}
		if t == "self" {
			if seg := lastSeg(prefix); seg != "" {
				out = append(out, seg)
			}
			return
		}
		if p, alias, ok := strings.Cut(t, " as "); ok && p != "" && isRustIdent(alias) {
			if alias != "_" {
				out = append(out, alias)
			}
			return
		}
		if seg := lastSeg(t); seg != "" {
			out = append(out, seg)
		}
	}
	walk("", tree)
	return out
}

func isRustIdent(s string) bool {
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return s != ""
}

func joinPath(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "::" + b
}

func lastSeg(p string) string {
	if i := strings.LastIndex(p, "::"); i >= 0 {
		return p[i+2:]
	}
	return p
}

// splitTop splits a brace list at its top-level commas.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// jvmImports keys each import statement of a Java or Kotlin file as written.
func jvmImports(lines []string) []string {
	var keys []string
	for _, l := range lines {
		t := strings.TrimSpace(stripLineComment(l, "//"))
		if !strings.HasPrefix(t, "import ") {
			continue
		}
		keys = append(keys, strings.Join(strings.Fields(strings.TrimSuffix(t, ";")), " "))
	}
	return keys
}

// csImports keys each using directive of a C# file as written. A using
// statement (`using (`, `using var`) is not a directive.
func csImports(lines []string) []string {
	var keys []string
	for _, l := range lines {
		t := strings.TrimSpace(stripLineComment(l, "//"))
		d, ok := strings.CutPrefix(t, "global ")
		if !ok {
			d = t
		}
		if !strings.HasPrefix(d, "using ") || !strings.HasSuffix(d, ";") || strings.Contains(d, "(") || strings.HasPrefix(d, "using var ") {
			continue
		}
		keys = append(keys, strings.Join(strings.Fields(strings.TrimSuffix(t, ";")), " "))
	}
	return keys
}
