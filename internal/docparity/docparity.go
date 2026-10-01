// Package docparity keeps documentation in step with code. Tests in other
// packages use it to assert that every exported function of a package is
// named in the page that documents it, and that named settings, formats and
// constants appear in the docs and the site. It is test support: nothing in
// the program imports it.
package docparity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ExportedFuncs lists the exported top-level functions and methods of the Go
// package in dir, excluding test files. Methods are reported as "Type.Name".
func ExportedFuncs(t testing.TB, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	var out []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || !fn.Name.IsExported() {
					continue
				}
				name := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					recv := receiverName(fn.Recv.List[0].Type)
					if recv == "" || !ast.IsExported(recv) {
						continue
					}
					name = recv + "." + name
				}
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func receiverName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return receiverName(x.X)
	}
	return ""
}

// Read returns the contents of a repository file, given a path relative to the
// repository root. Tests run in their package directory, so the root is found
// by walking up to go.mod.
func Read(t testing.TB, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(Root(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// Root returns the repository root: the nearest directory above the test's own
// that holds go.mod.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// RequireMentions fails the test for each name that does not appear in the
// document at rel. A method "Type.Name" is satisfied by "Name" or the
// qualified form, since prose names methods both ways.
func RequireMentions(t testing.TB, rel string, names []string) {
	t.Helper()
	doc := Read(t, rel)
	for _, n := range names {
		short := n
		if i := strings.LastIndex(n, "."); i >= 0 {
			short = n[i+1:]
		}
		if !strings.Contains(doc, n) && !strings.Contains(doc, short) {
			t.Errorf("%s does not mention %q", rel, n)
		}
	}
}

// ReadDir returns every Markdown file directly under the repository directory
// rel, joined with a blank line between them. It is for checks that a name
// appears somewhere in the documentation rather than in one named page.
func ReadDir(t testing.TB, rel string) string {
	t.Helper()
	dir := filepath.Join(Root(t), rel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", rel, err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", rel, e.Name(), err)
		}
		b.Write(data)
		b.WriteString("\n\n")
	}
	return b.String()
}

// FlagNames lists the command-line flags the Go package in dir defines with the
// standard flag package: every call of String, Bool, Int, Int64, Uint, Float64
// or Duration (name first) and of the matching Var forms (name second) whose
// name is a string literal. Test files are skipped. The result is sorted and
// has no repeats, so a flag a subcommand and the main command both define
// appears once.
func FlagNames(t testing.TB, dir string) []string {
	t.Helper()
	first := map[string]bool{"String": true, "Bool": true, "Int": true, "Int64": true, "Uint": true, "Float64": true, "Duration": true}
	second := map[string]bool{"StringVar": true, "BoolVar": true, "IntVar": true, "Int64Var": true, "UintVar": true, "Float64Var": true, "DurationVar": true}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				idx := -1
				switch {
				case first[sel.Sel.Name]:
					idx = 0
				case second[sel.Sel.Name]:
					idx = 1
				}
				if idx < 0 || len(call.Args) <= idx {
					return true
				}
				lit, ok := call.Args[idx].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				name := strings.Trim(lit.Value, "\"`")
				if name != "" && name[0] >= 'a' && name[0] <= 'z' {
					seen[name] = true
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
