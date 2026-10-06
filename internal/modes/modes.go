// Package modes defines the harness interaction modes: agent (default),
// plan (read-only planning), goal (goal-directed work) and code (agent work
// that batches tool calls in a script).
package modes

import "fmt"

// Mode is a harness interaction mode.
type Mode string

const (
	ModeAgent Mode = "agent"
	ModePlan  Mode = "plan"
	ModeGoal  Mode = "goal"
	// ModeCode is agent mode with the code-mode surface: the native coding
	// tools plus Code, a script that calls tools as functions, with MCP
	// reachable only from inside a script (docs/code-mode.md). It is chosen
	// explicitly and never picked by intent detection.
	ModeCode Mode = "code"
)

// Default returns the default interactive mode (agent).
func Default() Mode { return ModeAgent }

// Parse maps a string to a Mode, rejecting unknown values.
func Parse(s string) (Mode, error) {
	switch Mode(s) {
	case ModeAgent, ModePlan, ModeGoal, ModeCode:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("unknown mode %q", s)
	}
}
