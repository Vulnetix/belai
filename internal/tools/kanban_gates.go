package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/kanban"
)

// GateSuite is one test suite the harness detected in the repository: the only
// things a runnable acceptance gate may name. Ecosystem decides whether a gate
// may narrow the suite (only a Go suite takes a dir and a test name).
type GateSuite struct {
	Name      string
	Ecosystem string
}

// gateProperty is the schema of one gate in KanbanHandoff's gates argument.
func gateProperty(suites []GateSuite) Property {
	names := make([]string, len(suites))
	for i, s := range suites {
		names[i] = s.Name
	}
	props := map[string]Property{
		"title": {Type: "string", Description: "The observable outcome, as a statement that is true when the task is done (at most 120 characters)."},
		"kind":  {Type: "string", Enum: []string{string(kanban.GateRunnable), string(kanban.GateManual)}, Description: "runnable: a detected test suite decides it. manual: a reviewer decides it, for what no suite can observe."},
		"dir":   {Type: "string", Description: "Optional, Go suites only: the package directory, relative to the repository root, that narrows the suite."},
		"test":  {Type: "string", Description: "Optional, Go suites only: one test function name that narrows the suite."},
	}
	if len(names) > 0 {
		props["suite"] = Property{Type: "string", Enum: names, Description: "For a runnable gate: the detected test suite that runs it."}
	}
	return Property{Type: "object", Properties: props, Required: []string{"title", "kind"}}
}

const gatesHelp = "Acceptance gates: each names an outcome the harness or a reviewer decides after the work. " +
	"A runnable gate references a detected test suite (never a command; the harness runs the suite and reads its exit code), " +
	"and for a Go suite may narrow it to a package dir and one test. Give a manual gate only for what no test can observe. " +
	"At most 8. The harness always adds the checks that the detected suites pass and that nothing regressed."

// parseGates reads and checks the gates argument of a handoff against what
// the harness knows: the detected suites and, for a Go suite, the directory.
// Every problem is an error naming what to fix; a gate is never dropped.
func (c *WorkerClaim) parseGates(args map[string]any) ([]kanban.Gate, error) {
	raw, present := args["gates"]
	var list []any
	switch v := raw.(type) {
	case nil:
	case []any:
		list = v
	default:
		return nil, errors.New("gates must be a list of objects with title, kind and, for a runnable gate, suite")
	}
	if !present || len(list) == 0 {
		if c.GatesRequired {
			return nil, errors.New("this agent must give every handoff at least one acceptance gate: the outcome that proves the task done (see gates)")
		}
		return nil, nil
	}
	var out []kanban.Gate
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("gate %d must be an object with title, kind and, for a runnable gate, suite", i+1)
		}
		g := kanban.Gate{Kind: kanban.GateKind(strings.TrimSpace(str(m["kind"])))}
		g.Title = str(m["title"])
		g.Suite = strings.TrimSpace(str(m["suite"]))
		g.Dir = strings.TrimSpace(str(m["dir"]))
		g.Test = strings.TrimSpace(str(m["test"]))
		if g.Kind == kanban.GateRunnable {
			s, ok := c.gateSuite(g.Suite)
			if !ok {
				return nil, fmt.Errorf("gate %d: suite %q is not a detected test suite (%s)", i+1, g.Suite, c.suiteNames())
			}
			if (g.Dir != "" || g.Test != "") && s.Ecosystem != "go" {
				return nil, fmt.Errorf("gate %d: only a Go suite can be narrowed with dir or test; give the whole %s suite", i+1, s.Name)
			}
			if g.Dir != "" && c.GateRoot != "" && !dirInside(c.GateRoot, g.Dir) {
				return nil, fmt.Errorf("gate %d: dir %q is not a directory of this repository", i+1, g.Dir)
			}
			if g.Test != "" && g.Dir == "" {
				return nil, fmt.Errorf("gate %d: a test name needs the dir of its package", i+1)
			}
		}
		out = append(out, g)
	}
	// The final shape check is the board's own, so the tool and the store can
	// never disagree about what a valid gate is.
	if _, err := kanban.NormGates(out); err != nil {
		return nil, err
	}
	return out, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func (c *WorkerClaim) gateSuite(name string) (GateSuite, bool) {
	for _, s := range c.GateSuites {
		if s.Name == name {
			return s, true
		}
	}
	return GateSuite{}, false
}

func (c *WorkerClaim) suiteNames() string {
	if len(c.GateSuites) == 0 {
		return "none detected in this repository; give a manual gate"
	}
	names := make([]string, len(c.GateSuites))
	for i, s := range c.GateSuites {
		names[i] = s.Name
	}
	slices.Sort(names)
	return "detected: " + strings.Join(names, ", ")
}

// dirInside reports whether dir names an existing directory that stays inside
// root once symlinks are resolved.
func dirInside(root, dir string) bool {
	if filepath.IsAbs(dir) {
		return false
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return false
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	fi, err := os.Stat(real)
	return err == nil && fi.IsDir()
}
