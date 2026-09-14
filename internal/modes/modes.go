// Package modes defines the harness interaction modes: agent (default),
// plan (read-only planning), and goal (goal-directed work).
package modes

import "fmt"

// Mode is a harness interaction mode.
type Mode string

const (
	ModeAgent Mode = "agent"
	ModePlan  Mode = "plan"
	ModeGoal  Mode = "goal"
)

// Default returns the default interactive mode (agent).
func Default() Mode { return ModeAgent }

// Parse maps a string to a Mode, rejecting unknown values.
func Parse(s string) (Mode, error) {
	switch Mode(s) {
	case ModeAgent, ModePlan, ModeGoal:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("unknown mode %q", s)
	}
}
