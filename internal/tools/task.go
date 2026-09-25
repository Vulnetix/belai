package tools

import (
	"context"
	"errors"
	"fmt"
)

// Task runs one read-only subagent investigation. The harness-level runner is
// injected by the agent layer because only the agent has the active session,
// tool surface, and agent pool needed to execute a subagent. Task is always
// registered in the default registry so agent profiles can name it; a nil
// runner fails closed if a model ever invokes it outside a fan-out turn.
type Task struct {
	// Runner executes the subagent. It is set by the agent layer when the tool
	// is advertised for a fan-out turn.
	Runner func(ctx context.Context, description, prompt string) (Result, error)
}

// Definition matches the trained name and shape.
func (Task) Definition() Definition {
	return Definition{
		Name:        "Task",
		Description: "Run one independent read-only subagent investigation and return its report.",
		Properties: map[string]Property{
			"description": {Type: "string", Description: "Short label for this investigation."},
			"prompt":      {Type: "string", Description: "The read-only question the subagent should answer."},
		},
		Required: []string{"description", "prompt"},
	}
}

// Kind returns KindSubagent: subagent reports are model-written arbitrary
// text, so they classify before promotion.
func (Task) Kind() Kind { return KindSubagent }

// Subject returns the investigation description for permission logging.
func (Task) Subject(args map[string]any) string {
	s, _ := args["description"].(string)
	if s == "" {
		s, _ = args["prompt"].(string)
	}
	return s
}

// Execute runs the injected runner, or fails closed when none is set.
func (t Task) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if t.Runner == nil {
		return Result{}, errors.New("Task is not available on this turn")
	}
	description, _ := args["description"].(string)
	prompt, _ := args["prompt"].(string)
	if description == "" || prompt == "" {
		return Result{}, fmt.Errorf("Task requires description and prompt")
	}
	return t.Runner(ctx, description, prompt)
}
