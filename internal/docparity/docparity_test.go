package docparity

import (
	"go/ast"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestExportedFuncsFindsFunctionsAndMethods(t *testing.T) {
	got := ExportedFuncs(t, ".")
	for _, want := range []string{"ExportedFuncs", "Read", "RequireMentions"} {
		if !slices.Contains(got, want) {
			t.Errorf("ExportedFuncs missing %q in %v", want, got)
		}
	}
	if slices.Contains(got, "receiverName") {
		t.Error("unexported function listed")
	}
}

func TestReadFindsRepositoryFiles(t *testing.T) {
	if doc := Read(t, "docs/sanitization.md"); len(doc) == 0 {
		t.Fatal("empty document")
	}
}

func TestExportedFuncs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), `package p

func Alpha() {}
func beta() {}

type T struct{}

func (t T) Value() {}
func (t *T) Pointer() {}

type inner struct{}

func (i inner) Hidden() {}

type G[V any] struct{}

func (g G[int]) Generic() {}
func (g *G[V]) GenericPtr() {}
`)
	writeFile(t, filepath.Join(dir, "a_test.go"), `package p

func TestOnly() {}
`)

	got := ExportedFuncs(t, dir)
	want := []string{"Alpha", "G.Generic", "G.GenericPtr", "T.Pointer", "T.Value"}
	if !slices.Equal(got, want) {
		t.Fatalf("ExportedFuncs = %v, want %v", got, want)
	}
}

func TestExportedFuncsFailsOnMissingDir(t *testing.T) {
	if !expectFailure(t, func(tb testing.TB) {
		ExportedFuncs(tb, filepath.Join(t.TempDir(), "absent"))
	}) {
		t.Fatal("ExportedFuncs must fail on a directory that cannot be read")
	}
}

func TestReceiverName(t *testing.T) {
	for _, tc := range []struct {
		name string
		recv ast.Expr
		want string
	}{
		{"nil", nil, ""},
		{"ident", ast.NewIdent("T"), "T"},
		{"star", &ast.StarExpr{X: ast.NewIdent("T")}, "T"},
		{"index", &ast.IndexExpr{X: ast.NewIdent("G"), Index: ast.NewIdent("int")}, "G"},
		{"star of index", &ast.StarExpr{X: &ast.IndexExpr{X: ast.NewIdent("G")}}, "G"},
		{"selector", &ast.SelectorExpr{X: ast.NewIdent("pkg"), Sel: ast.NewIdent("T")}, ""},
	} {
		if got := receiverName(tc.recv); got != tc.want {
			t.Errorf("receiverName(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRootWalksUpToGoMod(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/temp\n")
	nested := filepath.Join(root, "internal", "p", "sub")
	writeFile(t, filepath.Join(nested, "p.go"), "package sub\n")
	t.Chdir(nested)
	if got := Root(t); got != root {
		t.Fatalf("Root() = %q, want %q", got, root)
	}
}

func TestRead(t *testing.T) {
	inModule(t, map[string]string{
		"go.mod":      "module example.com/temp\n",
		"docs/api.md": "the document\n",
	})
	if got := Read(t, "docs/api.md"); got != "the document\n" {
		t.Fatalf("Read = %q, want %q", got, "the document\n")
	}
	if !expectFailure(t, func(tb testing.TB) {
		Read(tb, "docs/absent.md")
	}) {
		t.Fatal("Read must fail for a file that does not exist")
	}
}

func TestRequireMentions(t *testing.T) {
	inModule(t, map[string]string{
		"go.mod":      "module example.com/temp\n",
		"docs/api.md": "Alpha runs. Calls Method and T.Full.\n",
	})

	t.Run("present", func(t *testing.T) {
		RequireMentions(t, "docs/api.md", []string{"Alpha", "T.Method", "T.Full"})
	})
	t.Run("missing", func(t *testing.T) {
		if !expectFailure(t, func(tb testing.TB) {
			RequireMentions(tb, "docs/api.md", []string{"Absent", "Type.Missing"})
		}) {
			t.Fatal("RequireMentions must fail for names the document omits")
		}
	})
}

func TestReadDir(t *testing.T) {
	inModule(t, map[string]string{
		"go.mod":          "module example.com/temp\n",
		"docs/a.md":       "alpha",
		"docs/b.md":       "bravo",
		"docs/readme.txt": "text",
		"docs/skip/c.md":  "nested",
	})
	const want = "alpha\n\nbravo\n\n"
	if got := ReadDir(t, "docs"); got != want {
		t.Fatalf("ReadDir = %q, want %q", got, want)
	}
	if !expectFailure(t, func(tb testing.TB) {
		ReadDir(tb, "absent")
	}) {
		t.Fatal("ReadDir must fail for a missing directory")
	}
}

func TestFlagNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "flag"

var names = []string{"computed"}

func register() {
	flag.String("alpha", "", "")
	flag.String("alpha", "", "")
	flag.Bool("Mixed-Case", false, "")
	flag.Int("count", 0, "")
	flag.Int64("sixtyfour", 0, "")
	flag.Uint("unsigned", 0, "")
	flag.Float64("ratio", 0, "")
	flag.Duration("wait", 0, "")
	var v string
	flag.StringVar(&v, "label", "", "")
	flag.Var(nil, "value", "")
	flag.Var(nil, names[0], "")
	flag.Func("function", "", func(string) error { return nil })
}
`)
	writeFile(t, filepath.Join(dir, "main_test.go"), `package main

import "flag"

func init() { flag.String("testonly", "", "") }
`)

	got := FlagNames(t, dir)
	want := []string{"alpha", "count", "label", "ratio", "sixtyfour", "unsigned", "value", "wait"}
	if !slices.Equal(got, want) {
		t.Fatalf("FlagNames = %v, want %v", got, want)
	}
}

func TestFlagNamesFailsOnMissingDir(t *testing.T) {
	if !expectFailure(t, func(tb testing.TB) {
		FlagNames(tb, filepath.Join(t.TempDir(), "absent"))
	}) {
		t.Fatal("FlagNames must fail on a directory that cannot be read")
	}
}

// writeFile creates path with its parent directories and data.
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// inModule writes files into a fresh module tree and makes it the working
// directory for the rest of the test, so Root finds that tree's go.mod.
func inModule(t *testing.T, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	for name, data := range files {
		writeFile(t, filepath.Join(root, name), data)
	}
	t.Chdir(root)
}

// stopped is the panic failRecorder raises to emulate Fatalf's abort.
type stopped struct{}

// expectFailure runs call against a TB whose failure methods record the
// failure instead of ending the test, and reports whether one was recorded.
func expectFailure(t *testing.T, call func(tb testing.TB)) (failed bool) {
	rec := &failRecorder{TB: t}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stopped); !ok {
				panic(r)
			}
		}
		failed = rec.failed
	}()
	call(rec)
	return rec.failed
}

// failRecorder wraps a real TB and records Error and Fatal calls so tests can
// assert that a helper reports a failure. Only methods that mark a test failed
// are overridden; everything else, including the unexported TB method, is
// forwarded, which is why external types cannot implement TB directly.
type failRecorder struct {
	testing.TB
	failed bool
}

func (r *failRecorder) Errorf(string, ...any) { r.failed = true }
func (r *failRecorder) Error(...any)          { r.failed = true }
func (r *failRecorder) Fatalf(string, ...any) { r.failed = true; panic(stopped{}) }
func (r *failRecorder) Fatal(...any)          { r.failed = true; panic(stopped{}) }
