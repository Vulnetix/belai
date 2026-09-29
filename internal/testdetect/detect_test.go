package testdetect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeTree lays down a set of relative paths under dir as empty files.
func writeTree(t *testing.T, dir string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writePackage writes package.json with the given contents.
func writePackage(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectPerEcosystem(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir,
		"go.mod", "pkg/a/a.go", "pkg/a/a_test.go", "pkg/b/b_test.go",
		"jest.config.js", "Cargo.toml", "pyproject.toml",
		"Makefile",
	)
	writePackage(t, dir, `{"scripts":{"test":"jest"}}`)

	suites := Detect(dir, []string{
		"just check", "go test ./...", "cargo test", "pytest", "npm test", "make test",
	}, nil)

	got := map[string]Suite{}
	for _, s := range suites {
		got[s.Name] = s
	}

	if _, ok := got["just-check"]; !ok {
		t.Errorf("just-check not detected")
	}
	if s := got["go"]; s.Name == "" || s.Framework != "go" || s.TestFilePattern != "*_test.go" || s.Scope != ScopeFull {
		t.Errorf("go suite wrong: %+v", s)
	}
	if s := got["cargo"]; s.Framework != "cargo" || s.Ecosystem != "rust" {
		t.Errorf("cargo suite wrong: %+v", s)
	}
	if s := got["pytest"]; s.Framework != "pytest" || s.Ecosystem != "python" {
		t.Errorf("pytest suite wrong: %+v", s)
	}
	if s := got["node"]; s.Ecosystem != "node" || s.Framework != "jest" {
		t.Errorf("node suite wrong: %+v", s)
	}
	if s := got["make-test"]; s.Ecosystem != "make" {
		t.Errorf("make suite wrong: %+v", s)
	}
}

func TestDetectNodeFrameworkOrder(t *testing.T) {
	cases := []struct {
		name      string
		files     []string
		framework string
	}{
		{"jest", []string{"package.json", "jest.config.js", "vitest.config.ts"}, "jest"},
		{"vitest", []string{"package.json", "vitest.config.ts", ".mocharc.json"}, "vitest"},
		{"mocha", []string{"package.json", ".mocharc.json"}, "mocha"},
		{"none", []string{"package.json"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir, tc.files...)
			writePackage(t, dir, `{"scripts":{"test":"test"}}`)
			suites := Detect(dir, []string{"npm test"}, nil)
			if len(suites) != 1 {
				t.Fatalf("got %d suites, want 1", len(suites))
			}
			if suites[0].Framework != tc.framework {
				t.Errorf("framework = %q, want %q", suites[0].Framework, tc.framework)
			}
		})
	}
}

// TestDetectNoScriptName ensures a package.json without a test script name is
// skipped: only the script *name* is read, never its body.
func TestDetectNoScriptName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"npm run lint"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	suites := Detect(dir, []string{"npm test"}, nil)
	if len(suites) != 0 {
		t.Fatalf("got %d suites, want 0", len(suites))
	}
}

func TestRescopeGo(t *testing.T) {
	suites := []Suite{{Name: "go", Ecosystem: "go", Command: []string{"go", "test", "./..."}, Scope: ScopeFull}}
	got := Rescope(suites, []string{"pkg/a/a.go", "pkg/b/b.go", "README.md"})
	if len(got) != 1 {
		t.Fatalf("got %d suites, want 1", len(got))
	}
	if got[0].Scope != ScopePaths {
		t.Fatalf("scope = %q, want paths", got[0].Scope)
	}
	want := []string{"pkg/a", "pkg/b"}
	if !reflect.DeepEqual(got[0].Dirs, want) {
		t.Errorf("dirs = %v, want %v", got[0].Dirs, want)
	}
	if cmd := got[0].ScopedCommand(); !reflect.DeepEqual(cmd, []string{"go", "test", "./pkg/a/...", "./pkg/b/..."}) {
		t.Errorf("scoped command = %v", cmd)
	}
}

// TestRescopeGoDependencyFiles ensures a go.mod/go.sum change is uncertainty:
// the whole suite runs.
func TestRescopeGoDependencyFiles(t *testing.T) {
	for _, changed := range [][]string{
		{"go.mod"},
		{"go.sum"},
		{"go.work"},
		{"sub/go.mod"},
	} {
		suites := []Suite{{Name: "go", Ecosystem: "go", Command: []string{"go", "test", "./..."}}}
		got := Rescope(suites, changed)
		if got[0].Scope != ScopeFull {
			t.Errorf("changed %v: scope = %q, want full", changed, got[0].Scope)
		}
	}
}

// TestRescopeNoGoFiles ensures a non-Go change falls back to the full suite.
func TestRescopeNoGoFiles(t *testing.T) {
	suites := []Suite{{Name: "go", Ecosystem: "go", Command: []string{"go", "test", "./..."}}}
	got := Rescope(suites, []string{"README.md", "site/index.astro"})
	if got[0].Scope != ScopeFull {
		t.Errorf("scope = %q, want full", got[0].Scope)
	}
}

// TestRescopeNonGoFallsToFull ensures non-Go ecosystems never narrow to paths.
func TestRescopeNonGoFallsToFull(t *testing.T) {
	suites := []Suite{{Name: "pytest", Ecosystem: "python", Command: []string{"pytest"}}}
	got := Rescope(suites, []string{"tests/test_x.py"})
	if got[0].Scope != ScopeFull || len(got[0].Dirs) != 0 {
		t.Errorf("scope = %q dirs = %v, want full/nil", got[0].Scope, got[0].Dirs)
	}
}

func TestScopedCommandRoot(t *testing.T) {
	s := Suite{Ecosystem: "go", Scope: ScopePaths, Command: []string{"go", "test", "./..."}, Dirs: []string{"."}}
	if cmd := s.ScopedCommand(); !reflect.DeepEqual(cmd, []string{"go", "test", "./..."}) {
		t.Errorf("root scoped command = %v", cmd)
	}
}

func TestCommandStrings(t *testing.T) {
	suites := []Suite{
		{Name: "go", Command: []string{"go", "test", "./..."}},
		{Name: "pytest", Command: []string{"pytest"}},
	}
	want := []string{"go test ./...", "pytest"}
	if got := CommandStrings(suites); !reflect.DeepEqual(got, want) {
		t.Errorf("CommandStrings = %v, want %v", got, want)
	}
}
