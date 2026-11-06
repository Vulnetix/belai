package lintdetect

import (
	"path/filepath"
	"strings"
)

// fileForm is how one linter checks a single edited file.
type fileForm struct {
	exts []string
	// head is the fixed argv before the operand.
	head []string
	// pkgDir makes the operand the file's directory ("./dir") instead of the
	// file, for a tool that analyses whole packages.
	pkgDir bool
	// dashDash puts "--" before the operand, so a name can never be a flag.
	dashDash bool
}

// fileForms is the per-file form of each linter that can check one file or one
// package directory. A linter absent from it (rustfmt, clippy and ESLint's
// `lint` script) is package-wide and never runs on one edited file.
//
// ruff and rubocop are asked for their one-row-per-finding text forms so the
// per-edit loop can read a location and a message from each line.
var fileForms = map[string]fileForm{
	"lint-ruff":     {exts: []string{".py", ".pyi"}, head: []string{"ruff", "check", "--output-format=concise"}, dashDash: true},
	"lint-rubocop":  {exts: []string{".rb", ".rake", ".gemspec"}, head: []string{"rubocop", "--format", "emacs"}, dashDash: true},
	"lint-golangci": {exts: []string{".go"}, head: []string{"golangci-lint", "run"}, pkgDir: true},
}

// PlainRelPath reports whether rel is a slash-separated path inside the
// repository made only of letters, digits and . _ - / @ +, with no empty, "."
// or ".." component, no leading "-" or "/", and at most 240 bytes. Such a name
// needs no quoting anywhere, which is what lets a permission rule written for
// the argv match it exactly.
func PlainRelPath(rel string) bool {
	if rel == "" || len(rel) > 240 || rel[0] == '-' || rel[0] == '/' {
		return false
	}
	for _, r := range rel {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '/', r == '@', r == '+':
		default:
			return false
		}
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// FileArgv is the fixed argv that lints the one file rel (a slash-separated
// path relative to the repository root) with l, and whether l has such a form.
// It is false for a package-wide linter, for an extension the tool does not
// own, and for a path that is not plain (see PlainRelPath). The argv is the
// table's, never a model's: only the operand varies, and it is the harness's
// own validated path. golangci-lint analyses packages, so its operand is the
// file's directory anchored with "./".
func FileArgv(l Linter, rel string) ([]string, bool) {
	form, ok := fileForms[l.Name]
	if !ok || !PlainRelPath(rel) || !hasExt(rel, form.exts) {
		return nil, false
	}
	argv := append([]string(nil), form.head...)
	if form.dashDash {
		argv = append(argv, "--")
	}
	if form.pkgDir {
		operand := "./"
		if i := strings.LastIndexByte(rel, '/'); i >= 0 {
			operand = "./" + rel[:i]
		}
		return append(argv, operand), true
	}
	return append(argv, rel), true
}

func hasExt(rel string, exts []string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	for _, e := range exts {
		if ext == e {
			return true
		}
	}
	return false
}
