// Package builddetect names a repository's build checks from a fixed
// marker-file table, the way internal/testdetect names its test suites and
// internal/lintdetect its linters. It reads file names, and package.json
// script names, never a file body or a script, and every argv is fixed by the
// table: no model chooses a command. The merge-conflict loop runs these on a
// resolved merge so a language that compiles (Go, Rust, C#, Java and Kotlin,
// TypeScript) is built before the resolution is pushed.
//
// A check whose toolchain needs installed dependencies (TypeScript's tsc, a
// package.json build script) is listed only when node_modules is already in
// the directory: the harness never installs anything, and a check that cannot
// pass in a fresh checkout would fail every attempt for no fault of the merge.
package builddetect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// NamePrefix starts every build check's name.
const NamePrefix = "build-"

// Build is one detected build check.
type Build struct {
	// Name is a stable identifier ("build-go", "build-cargo").
	Name string
	// Ecosystem is the owning toolchain.
	Ecosystem string
	// Command is the fixed argv. A wrapper script in the directory (gradlew,
	// mvnw) or an installed binary (tsc) is named by its absolute path.
	Command []string
	// Marker is the file whose presence made the match.
	Marker string
}

// Display is the command as a note names it: a path inside root is written
// relative to it, so a note never carries a machine's directory layout.
func (b Build) Display(root string) string {
	parts := make([]string, 0, len(b.Command))
	prefix := filepath.Clean(root) + string(filepath.Separator)
	for _, a := range b.Command {
		if rel, ok := strings.CutPrefix(a, prefix); ok {
			a = "./" + filepath.ToSlash(rel)
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Detect returns the build checks for root, in table order.
func Detect(root string) []Build {
	var out []Build
	add := func(name, eco, marker string, argv ...string) {
		out = append(out, Build{Name: name, Ecosystem: eco, Command: argv, Marker: marker})
	}
	if isFile(root, "go.mod") {
		add("build-go", "go", "go.mod", "go", "build", "./...")
		add("build-go-vet", "go", "go.mod", "go", "vet", "./...")
	}
	if isFile(root, "Cargo.toml") {
		add("build-cargo", "rust", "Cargo.toml", "cargo", "build")
	}
	if m := dotnetMarker(root); m != "" {
		add("build-dotnet", "dotnet", m, "dotnet", "build")
	}
	if isFile(root, "gradlew") && (isFile(root, "build.gradle") || isFile(root, "build.gradle.kts") || isFile(root, "settings.gradle") || isFile(root, "settings.gradle.kts")) {
		add("build-gradle", "jvm", "gradlew", filepath.Join(root, "gradlew"), "--no-daemon", "-q", "classes")
	}
	if isFile(root, "mvnw") && isFile(root, "pom.xml") {
		add("build-maven", "jvm", "mvnw", filepath.Join(root, "mvnw"), "-q", "-B", "compile")
	}
	if isFile(root, "tsconfig.json") && isFile(root, filepath.Join("node_modules", "typescript", "package.json")) && isFile(root, filepath.Join("node_modules", ".bin", "tsc")) {
		add("build-tsc", "node", "tsconfig.json", filepath.Join(root, "node_modules", ".bin", "tsc"), "--noEmit")
	}
	if hasScriptName(filepath.Join(root, "package.json"), "build") && isDir(root, "node_modules") {
		switch {
		case isFile(root, "pnpm-lock.yaml"):
			add("build-node", "node", "package.json", "pnpm", "run", "build")
		case isFile(root, "yarn.lock"):
			add("build-node", "node", "package.json", "yarn", "run", "build")
		default:
			add("build-node", "node", "package.json", "npm", "run", "build")
		}
	}
	return out
}

// dotnetMarker is the solution or project file at root, if there is one.
func dotnetMarker(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, ext := range []string{".sln", ".slnx", ".csproj", ".fsproj"} {
		for _, e := range entries {
			if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ext) {
				return e.Name()
			}
		}
	}
	return ""
}

// hasScriptName reports whether package.json declares the named script. Only
// the keys are read, never a script body.
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

// isFile reports a regular file, never following a symlink at the end of the
// path: a link planted in a merge cannot name a binary outside the tree.
func isFile(root, name string) bool {
	fi, err := os.Lstat(filepath.Join(root, name))
	return err == nil && fi.Mode().IsRegular()
}

func isDir(root, name string) bool {
	fi, err := os.Lstat(filepath.Join(root, name))
	return err == nil && fi.IsDir()
}
