// Package vulnetixcli detects and drives the Vulnetix CLI. When the CLI is on
// PATH the harness unlocks its agent hooks, skills, and /code-review.
package vulnetixcli

import (
	"fmt"
	"os/exec"
	"strings"
)

// CLI is a detected vulnetix binary.
type CLI struct {
	Path string
	// Dir is the working directory for Run (defaults to the current directory).
	Dir string
}

// Detect looks for the vulnetix binary on PATH.
func Detect() (*CLI, error) {
	p, err := exec.LookPath("vulnetix")
	if err != nil {
		return nil, fmt.Errorf("vulnetix not found on PATH: %w", err)
	}
	return &CLI{Path: p}, nil
}

// Run executes the vulnetix binary with the given arguments and returns its
// combined output. Output is trusted internal content (Vulnetix CLI output).
func (c *CLI) Run(args ...string) (string, error) {
	cmd := exec.Command(c.Path, args...)
	if c.Dir != "" {
		cmd.Dir = c.Dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("vulnetix %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
