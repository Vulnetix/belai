// Package lintdetect detects a repository's linters from a fixed marker-file
// table. It holds harness-computed facts only: marker-file presence, lock-file
// presence and package.json script names. It never reads a file body, a rule
// file or a script, which is what lets the detected commands reach the model
// in the repository map without violating the untrusted-content invariant.
//
// A linter is a rule file the repository checked in, not a guess: the table
// names the file that configures the tool, and a tool with no such file is not
// detected. The argv is fixed by the table and never comes from a model.
package lintdetect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Linter is one detected linter.
type Linter struct {
	// Name is a stable identifier ("lint-golangci", "lint-ruff"). A runnable
	// gate may name it, as it names a test suite.
	Name string
	// Tool is the program the argv runs ("golangci-lint", "ruff", "eslint").
	Tool string
	// Ecosystem is the owning toolchain ("go", "python", "rust", "ruby",
	// "node").
	Ecosystem string
	// Command is the full, fixed argv.
	Command []string
	// Marker is the file whose presence made the match ("ruff.toml").
	Marker string
	// FindingsExit is the exit code this tool uses for "I found something", so
	// a different failing code (a config error) is not read as drift. Zero
	// means the tool signals findings in its output alone.
	FindingsExit int
}

// NamePrefix starts the name of every linter, and so of every lint gate suite.
const NamePrefix = "lint-"

// IsLintName reports whether a suite name belongs to a linter.
func IsLintName(name string) bool { return strings.HasPrefix(name, NamePrefix) }

type rule struct {
	name, tool, ecosystem string
	command               []string
	markers               []string
	findingsExit          int
}

// table is the whole detection rule. Order is the order linters are listed in.
var table = []rule{
	{"lint-golangci", "golangci-lint", "go", []string{"golangci-lint", "run"}, []string{".golangci.yml", ".golangci.yaml"}, 1},
	{"lint-ruff", "ruff", "python", []string{"ruff", "check"}, []string{"ruff.toml", ".ruff.toml"}, 1},
	{"lint-rustfmt", "rustfmt", "rust", []string{"cargo", "fmt", "--check"}, []string{"rustfmt.toml", ".rustfmt.toml"}, 1},
	{"lint-clippy", "clippy", "rust", []string{"cargo", "clippy"}, []string{"clippy.toml", ".clippy.toml"}, 0},
	{"lint-rubocop", "rubocop", "ruby", []string{"rubocop"}, []string{".rubocop.yml"}, 1},
}

// eslintMarkers are the ESLint config files: the flat config and the legacy
// rc files. An ESLint config alone is not enough; see Detect.
var eslintMarkers = []string{
	"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs",
	"eslint.config.ts", "eslint.config.mts", "eslint.config.cts",
	".eslintrc", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.json", ".eslintrc.yml", ".eslintrc.yaml",
}

// Detect returns the linters for root, in table order. A linter is listed only
// when its rule file is present. ESLint is listed only when an ESLint config
// is present and package.json declares a `lint` script, because the script is
// the repository's own way to run it with the right paths and plugins; the
// argv is the package manager's run of that script. Only file names and
// script names are read.
func Detect(root string) []Linter {
	var out []Linter
	for _, r := range table {
		for _, m := range r.markers {
			if exists(root, m) {
				out = append(out, Linter{Name: r.name, Tool: r.tool, Ecosystem: r.ecosystem, Command: append([]string(nil), r.command...), Marker: m, FindingsExit: r.findingsExit})
				break
			}
		}
	}
	if cmd := PackageLintCommand(root); len(cmd) > 0 {
		for _, m := range eslintMarkers {
			if exists(root, m) {
				out = append(out, Linter{Name: "lint-eslint", Tool: "eslint", Ecosystem: "node", Command: cmd, Marker: m, FindingsExit: 1})
				break
			}
		}
	}
	return out
}

// PackageLintCommand is the package manager's run of package.json's `lint`
// script, or nil when there is no package.json or it declares no such script.
// The manager is named by its lock file (yarn.lock, pnpm-lock.yaml), else npm.
// Only the script's name is read, never its body.
func PackageLintCommand(root string) []string {
	if !hasScriptName(filepath.Join(root, "package.json"), "lint") {
		return nil
	}
	switch {
	case exists(root, "pnpm-lock.yaml"):
		return []string{"pnpm", "run", "lint"}
	case exists(root, "yarn.lock"):
		return []string{"yarn", "run", "lint"}
	}
	return []string{"npm", "run", "lint"}
}

// CommandStrings returns the space-joined command of each linter, in order.
func CommandStrings(ls []Linter) []string {
	var out []string
	for _, l := range ls {
		if len(l.Command) > 0 {
			out = append(out, strings.Join(l.Command, " "))
		}
	}
	return out
}

// hasScriptName reports whether package.json declares the named script key. It
// reads only script names (keys), never the script bodies (values).
func hasScriptName(path, name string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	_, ok := pkg.Scripts[name]
	return ok
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}
