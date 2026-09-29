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
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
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
