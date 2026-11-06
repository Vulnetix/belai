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
//
// Kind, EndLine and Container are for the symbol index (internal/symindex).
// Locate's ranking and previews read Name and Line only, and send exactly
// what they sent before these fields existed.
type Decl struct {
	Name string
	Line int
	// Kind is one of the Kind* words below, or empty when the scanner could
	// not tell.
	Kind string
	// EndLine is the last line of the declaration, or 0 when it is not known
	// (the scanner does not parse a body).
	EndLine int
	// Container is the receiver type of a Go method, empty otherwise.
	Container string
}

// Declaration kinds. The set is closed: the scanner maps a keyword onto one of
// these, so a kind is never text from the file.
const (
	KindFunc      = "func"
	KindMethod    = "method"
	KindType      = "type"
	KindStruct    = "struct"
	KindInterface = "interface"
	KindClass     = "class"
	KindEnum      = "enum"
	KindTrait     = "trait"
	KindImpl      = "impl"
	KindConst     = "const"
	KindVar       = "var"
	KindModule    = "module"
	KindObject    = "object"
)

// Kinds lists every declaration kind, for validation and docs.
var Kinds = []string{
	KindFunc, KindMethod, KindType, KindStruct, KindInterface, KindClass, KindEnum,
	KindTrait, KindImpl, KindConst, KindVar, KindModule, KindObject,
}

// ValidKind reports whether k is one of [Kinds].
func ValidKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// keywordKinds maps the keyword declRe matched onto a kind.
var keywordKinds = map[string]string{
	"func": KindFunc, "function": KindFunc, "def": KindFunc, "fn": KindFunc,
	"class": KindClass, "struct": KindStruct, "interface": KindInterface, "trait": KindTrait,
	"type": KindType, "enum": KindEnum, "impl": KindImpl, "const": KindConst,
	"let": KindVar, "var": KindVar, "module": KindModule, "object": KindObject,
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
var declRe = regexp.MustCompile(`^\s*(?:export\s+|pub(?:\([a-z]+\))?\s+|public\s+|private\s+|protected\s+|static\s+|async\s+|abstract\s+|final\s+|default\s+)*(func|function|def|fn|class|struct|interface|trait|type|enum|impl|const|let|var|module|object)\s+\(?\s*(?:\w+\s+\*?\w+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)

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
	add := func(id *ast.Ident, kind, container string, pos, end token.Pos) {
		if id != nil && id.Name != "_" && len(out) < maxDecls {
			out = append(out, Decl{
				Name: id.Name, Line: fset.Position(pos).Line, Kind: kind,
				EndLine: fset.Position(end).Line, Container: container,
			})
		}
	}
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil && len(d.Recv.List) > 0 {
				add(d.Name, KindMethod, receiverName(d.Recv.List[0].Type), d.Pos(), d.End())
			} else {
				add(d.Name, KindFunc, "", d.Pos(), d.End())
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					kind := KindType
					switch s.Type.(type) {
					case *ast.StructType:
						kind = KindStruct
					case *ast.InterfaceType:
						kind = KindInterface
					}
					add(s.Name, kind, "", s.Pos(), s.End())
				case *ast.ValueSpec:
					kind := KindVar
					if d.Tok == token.CONST {
						kind = KindConst
					}
					for _, id := range s.Names {
						add(id, kind, "", s.Pos(), s.End())
					}
				}
			}
		}
	}
	return out
}

// receiverName is the type name of a method receiver: the identifier under any
// pointer and type-parameter list, or empty for a shape it does not expect.
func receiverName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
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
			out = append(out, Decl{Name: m[2], Line: i + 1, Kind: keywordKinds[m[1]]})
		}
	}
	return out
}

// ParseDecls reads the declared names of a file called name whose bytes are
// src, as Declarations does for a path: a Go file is parsed, any other text is
// scanned line by line. It reads no file and returns nil for text that looks
// binary.
func ParseDecls(name string, src []byte) []Decl {
	head := src
	if len(head) > sniffBytes {
		head = head[:sniffBytes]
	}
	if !IsText(head) {
		return nil
	}
	if strings.EqualFold(filepath.Ext(name), ".go") {
		if ds := goDecls(name, src); ds != nil {
			return ds
		}
	}
	return scanDecls(src)
}
