// Package testdetect detects a repository's test suites from a fixed
// marker-file table and scopes them to changed paths. It holds
// harness-computed facts only: marker-file presence, package.json script
// names and changed paths. It never reads file bodies or prose, which is what
// lets the detected suites reach the model in the repository map without
// violating the untrusted-content invariant.
package testdetect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Scope says whether a suite's command runs the full tree or only the
// packages that own changed paths.
type Scope string

const (
	// ScopeFull runs the suite's whole command, unscoped.
	ScopeFull Scope = "full"
	// ScopePaths runs only the packages owning changed paths.
	ScopePaths Scope = "paths"
)

// Suite is one detected test suite. Command is the full (unscoped) argv;
// Scope and Dirs carry the contextual scoping derived from changed paths.
type Suite struct {
	// Name is a stable identifier ("go", "pytest", "just-check").
	Name string
	// Ecosystem is the owning toolchain ("go", "node", "python", "rust",
	// "just", "make").
	Ecosystem string
	// Command is the full, unscoped argv, e.g. ["go", "test", "./..."].
	Command []string
	// Scope is ScopeFull or ScopePaths.
	Scope Scope
	// Framework is the test framework when one is known from marker files
	// (go, pytest, cargo, jest/vitest/mocha). Empty when undetermined.
	Framework string
	// TestFilePattern is the conventional test-file glob when one is fixed
	// ("*_test.go"). Empty when the ecosystem has none.
	TestFilePattern string
	// Dirs are the owning package directories when Scope is ScopePaths.
	Dirs []string
}

// ScopedCommand returns the argv to run for the suite: the package-scoped go
// command when Scope is ScopePaths, else the full Command.
func (s Suite) ScopedCommand() []string {
	if s.Scope != ScopePaths || s.Ecosystem != "go" {
		return s.Command
	}
	cmd := []string{"go", "test"}
	for _, d := range s.Dirs {
		if d == "." || d == "" {
			cmd = append(cmd, "./...")
			continue
		}
		cmd = append(cmd, "./"+strings.TrimPrefix(d, "./")+"/...")
	}
	return cmd
}

// CommandStrings returns the full, space-joined command for each suite, in
// order. It is the source repomap.Commands.Test is derived from, so existing
// consumers that read the flat test-command list keep working unchanged.
func CommandStrings(suites []Suite) []string {
	var out []string
	for _, s := range suites {
		if len(s.Command) > 0 {
			out = append(out, strings.Join(s.Command, " "))
		}
	}
	return out
}

// Detect returns the test suites for root, one per entry in testCommands (the
// space-joined commands from repomap.Commands.Test), scoped to changed.
// Detection re-reads only fixed marker files and package.json script names;
// file bodies and prose are never consulted. A command that does not resolve
// to a real suite (for example "npm test" with no test script) is skipped.
func Detect(root string, testCommands []string, changed []string) []Suite {
	var suites []Suite
	seen := map[string]bool{}
	for _, cmd := range testCommands {
		s := classify(root, cmd)
		if s.Name == "" || seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		suites = append(suites, s)
	}
	return Rescope(suites, changed)
}

// Rescope returns a copy of suites with each suite re-scoped to changed. Only
// Go maps changed paths to package directories; every other ecosystem falls
// back to its full command. No mapping, or any uncertainty, means the full
// suite.
func Rescope(suites []Suite, changed []string) []Suite {
	out := make([]Suite, len(suites))
	for i, s := range suites {
		out[i] = s
		out[i].Scope = ScopeFull
		out[i].Dirs = nil
		if s.Ecosystem != "go" {
			continue
		}
		if dirs := goDirs(changed); len(dirs) > 0 {
			out[i].Scope = ScopePaths
			out[i].Dirs = dirs
		}
	}
	return out
}

// classify turns one flat test command into a Suite, enriching it with the
// ecosystem's fixed facts (framework, test-file pattern). It returns a zero
// Suite for a command it cannot or must not describe.
func classify(root, cmd string) Suite {
	argv := strings.Fields(cmd)
	if len(argv) == 0 {
		return Suite{}
	}
	switch argv[0] {
	case "just":
		if len(argv) < 2 {
			return Suite{}
		}
		return Suite{
			Name:      "just-" + argv[1],
			Ecosystem: "just",
			Command:   argv,
			Scope:     ScopeFull,
			Framework: "just",
		}
	case "make":
		if len(argv) < 2 || argv[1] != "test" {
			return Suite{}
		}
		return Suite{Name: "make-test", Ecosystem: "make", Command: argv, Scope: ScopeFull, Framework: "make"}
	case "go":
		return Suite{
			Name:            "go",
			Ecosystem:       "go",
			Command:         argv,
			Scope:           ScopeFull,
			Framework:       "go",
			TestFilePattern: "*_test.go",
		}
	case "cargo":
		return Suite{Name: "cargo", Ecosystem: "rust", Command: argv, Scope: ScopeFull, Framework: "cargo"}
	case "pytest":
		return Suite{Name: "pytest", Ecosystem: "python", Command: argv, Scope: ScopeFull, Framework: "pytest"}
	case "npm", "yarn", "pnpm":
		if len(argv) < 2 || argv[1] != "test" {
			return Suite{}
		}
		if !hasScriptName(filepath.Join(root, "package.json"), "test") {
			return Suite{}
		}
		return Suite{Name: "node", Ecosystem: "node", Command: argv, Scope: ScopeFull, Framework: nodeFramework(root)}
	}
	return Suite{}
}

// goDirs maps changed Go paths to their package directories. A dependency-file
// change (go.mod, go.sum, go.work) is uncertainty: the whole suite must run,
// so it returns nil. Non-Go paths are not owned by the Go suite and are
// ignored.
func goDirs(changed []string) []string {
	var dirs []string
	seen := map[string]bool{}
	for _, p := range changed {
		p = filepath.ToSlash(p)
		if p == "go.mod" || p == "go.sum" || p == "go.work" || strings.HasSuffix(p, "/go.mod") || strings.HasSuffix(p, "/go.sum") || strings.HasSuffix(p, "/go.work") {
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			continue
		}
		d := filepath.ToSlash(filepath.Dir(p))
		if d == "." {
			d = "."
		}
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	return dirs
}

// nodeFramework names the JS test framework from fixed config-file presence
// (jest, then vitest, then mocha). It never reads the files' contents.
func nodeFramework(root string) string {
	for _, name := range []string{
		"jest.config.js", "jest.config.ts", "jest.config.mjs", "jest.config.cjs", "jest.config.json",
	} {
		if exists(root, name) {
			return "jest"
		}
	}
	for _, name := range []string{
		"vitest.config.ts", "vitest.config.js", "vitest.config.mts", "vitest.workspace.ts", "vitest.workspace.js",
	} {
		if exists(root, name) {
			return "vitest"
		}
	}
	for _, name := range []string{
		".mocharc.json", ".mocharc.js", ".mocharc.yml", ".mocharc.yaml", "mocha.opts",
	} {
		if exists(root, name) {
			return "mocha"
		}
	}
	return ""
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

// exists reports whether a marker file is present under root.
func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}
