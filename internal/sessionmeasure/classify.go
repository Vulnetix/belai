// Package sessionmeasure records what a session did in numbers the Software
// Factory can add up: the checks it ran (builds, tests, lint, gates), the
// commits it made, what each model call cost, and a rollup of all of it that
// rides the session-finish call. Every record is harness-measured from a
// process exit code, a git object or a provider's usage block, never from
// model text. docs/session-measures.md has the wire shapes.
package sessionmeasure

import (
	"path"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/shellsafe"
)

// Kind is what a check run checks.
type Kind string

const (
	KindBuild Kind = "build"
	KindTest  Kind = "test"
	KindLint  Kind = "lint"
	// KindGate is a card's acceptance gate, run by the fleet worker's
	// verification. The classifier never returns it: a caller that knows the
	// command was a gate says so.
	KindGate Kind = "gate"
)

// Class is what a command is: the kind of check, the program that ran it and
// the subcommand, target, script or goal that picked the check.
type Class struct {
	Kind       Kind
	Program    string
	Subcommand string
}

var subcommandRE = regexp.MustCompile(`^[a-z][a-z0-9:_.-]{0,31}$`)

// subcommand returns s when it has the shape of a subcommand, target, script
// or goal name, and "" otherwise, so a path or a free-form word is never
// recorded as one.
func subcommand(s string) string {
	s = strings.ToLower(s)
	if subcommandRE.MatchString(s) {
		return s
	}
	return ""
}

// Classify reports what one command is, given its argv. Only the programs go,
// cargo, npm, yarn, pnpm, make, pytest and mvn are known, and only the
// subcommands that build, test or lint. A command it does not know is not a
// check.
func Classify(argv []string) (Class, bool) {
	if len(argv) == 0 {
		return Class{}, false
	}
	prog := path.Base(argv[0])
	args := argv[1:]
	switch prog {
	case "go":
		return classifyGo(args)
	case "cargo":
		return classifyCargo(args)
	case "npm", "yarn", "pnpm":
		return classifyNode(prog, args)
	case "make", "gmake":
		return classifyMake(args)
	case "pytest", "py.test":
		return Class{KindTest, "pytest", ""}, true
	case "python", "python3", "python2":
		if len(args) >= 2 && args[0] == "-m" && (args[1] == "pytest" || args[1] == "py.test") {
			return Class{KindTest, "pytest", ""}, true
		}
	case "uv", "poetry", "pipenv", "hatch":
		// `uv run pytest`: the check is what the runner runs.
		if len(args) >= 2 && args[0] == "run" {
			return Classify(args[1:])
		}
	case "mvn", "mvnw", "mvnd":
		return classifyMaven(args)
	}
	return Class{}, false
}

// ClassifyLine reports what a shell line is. The line is parsed as bash, so a
// program named inside a string, a comment or an echo is not a check, and a
// wrapper (env, sudo, sh -c) is looked through. A line that chains several
// checks (go build && go test) is recorded once, as the last one: with && the
// last check ran only because the earlier ones passed, and the exit code the
// caller holds is the line's.
func ClassifyLine(line string) (Class, bool) {
	a := shellsafe.Analyze(line)
	if !a.Parseable || len(a.Commands) == 0 {
		return Class{}, false
	}
	written := a.Commands[:a.Written]
	unwrapped := a.Commands[a.Written:]
	for _, group := range [][]shellsafe.Command{written, unwrapped} {
		for i := len(group) - 1; i >= 0; i-- {
			if group[i].Dynamic {
				continue
			}
			if c, ok := Classify(group[i].Argv); ok {
				return c, true
			}
		}
	}
	return Class{}, false
}

// operands returns the arguments that are not flags. A flag in valueFlags
// takes the next argument as its value; any other flag stands alone, and so
// does one written --flag=value.
func operands(args []string, valueFlags map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i+1:]...)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if valueFlags[a] && i+1 < len(args) {
				i++
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

func classifyGo(args []string) (Class, bool) {
	ops := operands(args, nil)
	if len(ops) == 0 {
		return Class{}, false
	}
	switch ops[0] {
	case "build", "install":
		return Class{KindBuild, "go", ops[0]}, true
	case "test":
		return Class{KindTest, "go", "test"}, true
	case "vet":
		return Class{KindLint, "go", "vet"}, true
	}
	return Class{}, false
}

// alias maps a short subcommand to its full name: pairs are short, full, ...
func alias(s string, pairs ...string) string {
	for i := 0; i+1 < len(pairs); i += 2 {
		if s == pairs[i] {
			return pairs[i+1]
		}
	}
	return s
}

func classifyCargo(args []string) (Class, bool) {
	// `cargo +nightly test`: the toolchain selector is not the subcommand.
	for len(args) > 0 && strings.HasPrefix(args[0], "+") {
		args = args[1:]
	}
	ops := operands(args, map[string]bool{"-p": true, "--package": true, "--manifest-path": true, "--target": true, "-Z": true, "--config": true, "-C": true})
	if len(ops) == 0 {
		return Class{}, false
	}
	switch ops[0] {
	case "build", "b", "check", "c", "install":
		return Class{KindBuild, "cargo", alias(ops[0], "b", "build", "c", "check")}, true
	case "test", "t", "nextest":
		return Class{KindTest, "cargo", alias(ops[0], "t", "test")}, true
	case "clippy":
		return Class{KindLint, "cargo", "clippy"}, true
	case "fmt":
		// Plain `cargo fmt` rewrites files; only the check form checks.
		for _, a := range args {
			if a == "--check" {
				return Class{KindLint, "cargo", "fmt"}, true
			}
		}
	}
	return Class{}, false
}

// nodeValueFlags are the package managers' flags that take a separate value
// and so must not be read as the script.
var nodeValueFlags = map[string]bool{
	"--filter": true, "-F": true, "--prefix": true, "-C": true, "--dir": true, "--cwd": true,
	"--workspace": true, "-w": true, "--cache": true, "--registry": true, "--loglevel": true,
}

func classifyNode(prog string, args []string) (Class, bool) {
	ops := operands(args, nodeValueFlags)
	if len(ops) == 0 {
		return Class{}, false
	}
	script := ops[0]
	switch script {
	case "run", "run-script", "rum", "urn":
		if len(ops) < 2 {
			return Class{}, false
		}
		script = ops[1]
	case "t", "tst":
		script = "test"
	case "workspace":
		// yarn workspace <name> <script>
		if prog == "yarn" && len(ops) >= 3 {
			script = ops[2]
		}
	}
	s := strings.ToLower(script)
	switch {
	case strings.HasPrefix(s, "test"):
		return Class{KindTest, prog, subcommand(script)}, true
	case strings.HasPrefix(s, "build") || strings.HasPrefix(s, "compile"):
		return Class{KindBuild, prog, subcommand(script)}, true
	case strings.HasPrefix(s, "lint"):
		return Class{KindLint, prog, subcommand(script)}, true
	}
	return Class{}, false
}

func classifyMake(args []string) (Class, bool) {
	ops := operands(args, map[string]bool{"-C": true, "-f": true, "-I": true, "-j": true, "-o": true, "-W": true, "-l": true, "--directory": true, "--file": true, "--jobs": true})
	var targets []string
	for _, o := range ops {
		if !strings.Contains(o, "=") { // VAR=value is not a target
			targets = append(targets, o)
		}
	}
	if len(targets) == 0 {
		// The default goal of a Makefile builds.
		return Class{KindBuild, "make", ""}, true
	}
	for _, t := range targets {
		l := strings.ToLower(t)
		switch {
		case l == "check" || strings.HasPrefix(l, "test"):
			return Class{KindTest, "make", subcommand(t)}, true
		case l == "all" || l == "install" || l == "compile" || strings.HasPrefix(l, "build"):
			return Class{KindBuild, "make", subcommand(t)}, true
		case l == "vet" || strings.HasPrefix(l, "lint"):
			return Class{KindLint, "make", subcommand(t)}, true
		}
	}
	return Class{}, false
}

func classifyMaven(args []string) (Class, bool) {
	ops := operands(args, map[string]bool{"-f": true, "--file": true, "-pl": true, "--projects": true, "-P": true, "--activate-profiles": true,
		"-T": true, "--threads": true, "-s": true, "--settings": true, "-rf": true, "--resume-from": true, "-l": true, "--log-file": true})
	for _, g := range ops {
		l := strings.ToLower(g)
		switch {
		case l == "test" || l == "verify" || l == "integration-test" || l == "surefire:test" || l == "failsafe:integration-test":
			return Class{KindTest, "mvn", subcommand(g)}, true
		case l == "compile" || l == "package" || l == "install":
			return Class{KindBuild, "mvn", subcommand(g)}, true
		case strings.HasSuffix(l, ":check") && (strings.HasPrefix(l, "checkstyle") || strings.HasPrefix(l, "spotless") ||
			strings.HasPrefix(l, "pmd") || strings.HasPrefix(l, "spotbugs")):
			return Class{KindLint, "mvn", subcommand(g)}, true
		}
	}
	return Class{}, false
}
