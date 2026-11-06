package lintdetect

import (
	"reflect"
	"strings"
	"testing"
)

func linter(name string) Linter {
	for _, r := range table {
		if r.name == name {
			return Linter{Name: r.name, Tool: r.tool, Command: r.command, FindingsExit: r.findingsExit}
		}
	}
	return Linter{Name: name}
}

func TestFileArgvForms(t *testing.T) {
	cases := []struct {
		name, rel string
		want      []string
	}{
		{"lint-ruff", "app/main.py", []string{"ruff", "check", "--output-format=concise", "--", "app/main.py"}},
		{"lint-ruff", "stubs/x.pyi", []string{"ruff", "check", "--output-format=concise", "--", "stubs/x.pyi"}},
		{"lint-rubocop", "lib/a.rb", []string{"rubocop", "--format", "emacs", "--", "lib/a.rb"}},
		{"lint-golangci", "internal/x/y.go", []string{"golangci-lint", "run", "./internal/x"}},
		{"lint-golangci", "main.go", []string{"golangci-lint", "run", "./"}},
	}
	for _, c := range cases {
		got, ok := FileArgv(linter(c.name), c.rel)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("FileArgv(%s, %s) = %v, %v; want %v", c.name, c.rel, got, ok, c.want)
		}
	}
}

func TestFileArgvPackageWideLintersHaveNoForm(t *testing.T) {
	for _, n := range []string{"lint-rustfmt", "lint-clippy", "lint-eslint", "lint-unknown"} {
		if argv, ok := FileArgv(linter(n), "src/lib.rs"); ok {
			t.Errorf("%s ran on one file: %v", n, argv)
		}
	}
}

func TestFileArgvRefusesWrongExtensionAndUnsafePaths(t *testing.T) {
	ruff := linter("lint-ruff")
	for _, rel := range []string{
		"main.go", "README", "", "/etc/x.py", "../x.py", "a/../x.py", "./x.py", "a//b.py",
		"-rf.py", "--config=evil.py", "has space.py", "semi;colon.py", "new\nline.py", "uni‮code.py",
		strings.Repeat("a/", 130) + "x.py",
	} {
		if argv, ok := FileArgv(ruff, rel); ok {
			t.Errorf("FileArgv accepted %q: %v", rel, argv)
		}
	}
	// Even a name with a leading dash is refused for the directory form.
	if argv, ok := FileArgv(linter("lint-golangci"), "-x/y.go"); ok {
		t.Errorf("golangci accepted a flag-shaped directory: %v", argv)
	}
}

func TestFileArgvDoesNotAliasTheTable(t *testing.T) {
	a, _ := FileArgv(linter("lint-ruff"), "a.py")
	a[0] = "evil"
	b, _ := FileArgv(linter("lint-ruff"), "a.py")
	if b[0] != "ruff" {
		t.Fatalf("table mutated: %v", b)
	}
}
