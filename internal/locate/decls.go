package locate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Decl is a declared name and the line it starts on.
type Decl struct {
	Name string
	Line int
}

const (
	// maxDeclBytes is how much of a file is read to find declarations.
	maxDeclBytes = 256 << 10
	// maxDecls bounds the declarations kept per file.
	maxDecls = 200
)

// declRe finds a declaration keyword and the name after it in languages other
// than Go. It is a scanner, not a parser: it can miss a declaration or name a
// commented-out one, which only affects ranking.
var declRe = regexp.MustCompile(`^\s*(?:export\s+|pub(?:\([a-z]+\))?\s+|public\s+|private\s+|protected\s+|static\s+|async\s+|abstract\s+|final\s+|default\s+)*(?:func|function|def|fn|class|struct|interface|trait|type|enum|impl|const|let|var|module|object)\s+\(?\s*(?:\w+\s+\*?\w+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)

// Declarations reads the declared names of the file at abs. A Go file is
// parsed; any other text file is scanned line by line. The second result says
// whether the file is readable text; a binary or unreadable file has no
// declarations and is not a candidate.
func Declarations(abs string) ([]Decl, bool) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	buf := make([]byte, maxDeclBytes)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if !IsText(buf) {
		return nil, false
	}
	if strings.EqualFold(filepath.Ext(abs), ".go") {
		if ds := goDecls(abs, buf); ds != nil {
			return ds, true
		}
	}
	return scanDecls(buf), true
}

func goDecls(name string, src []byte) []Decl {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if file == nil {
		return nil
	}
	_ = err // a partial tree still names what it could read
	var out []Decl
	add := func(id *ast.Ident, pos token.Pos) {
		if id != nil && id.Name != "_" && len(out) < maxDecls {
			out = append(out, Decl{Name: id.Name, Line: fset.Position(pos).Line})
		}
	}
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			add(d.Name, d.Pos())
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					add(s.Name, s.Pos())
				case *ast.ValueSpec:
					for _, id := range s.Names {
						add(id, s.Pos())
					}
				}
			}
		}
	}
	return out
}

func scanDecls(src []byte) []Decl {
	var out []Decl
	for i, line := range strings.Split(string(src), "\n") {
		if len(out) >= maxDecls {
			break
		}
		if len(line) > 400 {
			line = line[:400]
		}
		if m := declRe.FindStringSubmatch(line); m != nil {
			out = append(out, Decl{Name: m[1], Line: i + 1})
		}
	}
	return out
}
