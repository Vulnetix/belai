package docsuite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

var pathRef = regexp.MustCompile("`((?:internal|cmd|docs|e2e|site|tools|bench)/[A-Za-z0-9_./*-]*[A-Za-z0-9_*])`")

// declared lists every name a Go package declares at the top level, plus the
// methods and struct fields of its types, so `pkg.Type.Method` style references
// and `pkg.Field` both resolve.
func declared(t *testing.T, dir string) map[string]bool {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	out := map[string]bool{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					out[d.Name.Name] = true
				case *ast.GenDecl:
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.TypeSpec:
							out[s.Name.Name] = true
							if st, ok := s.Type.(*ast.StructType); ok {
								for _, fl := range st.Fields.List {
									for _, n := range fl.Names {
										out[n.Name] = true
									}
								}
							}
						case *ast.ValueSpec:
							for _, n := range s.Names {
								out[n.Name] = true
							}
						}
					}
				}
			}
		}
	}
	return out
}

// TestDocsReferenceOnlyFilesAndSymbolsThatExist keeps the pages from pointing
// at code that has moved: every `internal/…`, `cmd/…` or `docs/…` path in the
// docs, the README and AGENTS.md exists, and a reference of the form
// `internal/pkg.Symbol` or `internal/pkg/Symbol` names a symbol the package
// declares.
func TestDocsReferenceOnlyFilesAndSymbolsThatExist(t *testing.T) {
	root := docparity.Root(t)
	text := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md") + docparity.Read(t, "AGENTS.md")
	seen := map[string]bool{}
	for _, m := range pathRef.FindAllStringSubmatch(text, -1) {
		ref := m[1]
		if seen[ref] || strings.HasSuffix(ref, "...") {
			continue
		}
		seen[ref] = true
		if strings.ContainsAny(ref, "*") {
			if matches, _ := filepath.Glob(filepath.Join(root, ref)); len(matches) == 0 {
				t.Errorf("the docs mention %s, which matches nothing", ref)
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(root, ref)); err == nil {
			continue
		}
		// pkg.Symbol or pkg/Symbol: the part before the last separator must be a
		// package directory and the part after it a name that package declares.
		cut := strings.LastIndexAny(ref, "./")
		if cut < 0 {
			t.Errorf("the docs mention %s, which does not exist", ref)
			continue
		}
		dir, sym := ref[:cut], ref[cut+1:]
		if info, err := os.Stat(filepath.Join(root, dir)); err != nil || !info.IsDir() {
			t.Errorf("the docs mention %s, which does not exist", ref)
			continue
		}
		if !declared(t, filepath.Join(root, dir))[sym] {
			t.Errorf("the docs mention %s, but %s declares no %s", ref, dir, sym)
		}
	}
	if len(seen) < 100 {
		t.Fatalf("only %d references found; the pattern is wrong", len(seen))
	}
}

var symbolRef = regexp.MustCompile("`([a-z][a-z0-9]*)\\.([A-Z][A-Za-z0-9_]*)(?:\\.[A-Za-z0-9_]+)?[`(]")

// TestDocsNameOnlyExportedSymbolsThatExist checks the short form of a code
// reference, `pkg.Name`, where pkg is the final element of one internal
// package's path: the package must declare Name. A name that no internal
// package owns (a Go standard library call, a third-party type) is skipped.
func TestDocsNameOnlyExportedSymbolsThatExist(t *testing.T) {
	root := docparity.Root(t)
	pkgDirs := map[string][]string{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if matches, _ := filepath.Glob(filepath.Join(p, "*.go")); len(matches) > 0 {
			pkgDirs[d.Name()] = append(pkgDirs[d.Name()], p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	decl := map[string]map[string]bool{}
	text := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md") + docparity.Read(t, "AGENTS.md")
	seen := map[string]bool{}
	checked := 0
	for _, m := range symbolRef.FindAllStringSubmatch(text, -1) {
		pkg, sym := m[1], m[2]
		key := pkg + "." + sym
		dirs, ok := pkgDirs[pkg]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		found := false
		for _, d := range dirs {
			if decl[d] == nil {
				decl[d] = declared(t, d)
			}
			if decl[d][sym] {
				found = true
			}
		}
		checked++
		if !found {
			t.Errorf("the docs mention %s, but no internal/%s package declares %s", key, pkg, sym)
		}
	}
	if checked < 40 {
		t.Fatalf("only %d references checked; the pattern is wrong", checked)
	}
}
