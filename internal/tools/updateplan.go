package tools

import (
	"context"
	"fmt"

	"github.com/vulnetix/signet/internal/todos"
)

// UpdatePlan is the trained checklist-progress tool (Codex's update_plan). It
// is the primary way a model reports plan progress; the [DONE:n] marker
// convention remains as a fallback parser for models that emit markers
// instead of calling the tool.
type UpdatePlan struct{}

// Definition describes the tool to the model. It deliberately documents the
// divergence from Codex: signet accepts update_plan inside plan mode too,
// because its plan pass loop already tracks a planning checklist.
func (UpdatePlan) Definition() Definition {
	return Definition{
		Name: "update_plan",
		Description: "Report plan progress against the checklist. Each step carries a " +
			"status: pending, in_progress, or completed. Unlike Codex this tool is also " +
			"available in plan mode, where it drives the planning checklist rather than " +
			"an execution checklist.",
		Properties: map[string]Property{
			"explanation": {Type: "string", Description: "Optional note about this update."},
			"plan": {
				Type:        "array",
				Description: "The steps and their statuses.",
				Items: &Property{
					Type: "object",
					Properties: map[string]Property{
						"step":   {Type: "string", Description: "The step text."},
						"status": {Type: "string", Description: "pending | in_progress | completed"},
					},
					Required: []string{"step", "status"},
				},
			},
		},
		Required: []string{"plan"},
	}
}

// Kind returns the dedicated update_plan kind: read-only (it never mutates the
// workspace) and sanitise-only (its result is a terse harness-composed
// summary, not arbitrary content).
func (UpdatePlan) Kind() Kind { return KindUpdatePlan }

// Subject has no permission subject.
func (UpdatePlan) Subject(args map[string]any) string { return "" }

// Mutates reports false: this tool performs no workspace I/O.
func (UpdatePlan) Mutates() bool { return false }

// Execute validates the Codex schema and returns a shaped, harness-composed
// progress summary. The full checklist is carried in Meta so the agent loop
// can adopt it into the shared todo list.
func (UpdatePlan) Execute(ctx context.Context, args map[string]any) (Result, error) {
	raw, _ := args["plan"].([]any)
	if len(raw) == 0 {
		return Result{}, fmt.Errorf("plan must be a non-empty array of steps")
	}
	var items []todos.Item
	done := 0
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return Result{}, fmt.Errorf("plan[%d] must be an object", i)
		}
		step, _ := m["step"].(string)
		status, _ := m["status"].(string)
		if step == "" {
			return Result{}, fmt.Errorf("plan[%d].step is required", i)
		}
		st, err := mapStatus(status)
		if err != nil {
			return Result{}, fmt.Errorf("plan[%d]: %v", i, err)
		}
		items = append(items, todos.Item{N: i + 1, Text: step, Status: st})
		if st == todos.StatusDone {
			done++
		}
	}
	list := todos.List{Items: items}
	return Result{
		Kind:    KindUpdatePlan,
		Content: fmt.Sprintf("plan updated: %d/%d", done, len(items)),
		Meta:    map[string]any{"todos": list},
	}, nil
}

// mapStatus maps the Codex status vocabulary onto the todos statuses.
func mapStatus(s string) (todos.Status, error) {
	switch s {
	case "pending":
		return todos.StatusPending, nil
	case "in_progress":
		return todos.StatusActive, nil
	case "completed":
		return todos.StatusDone, nil
	}
	return "", fmt.Errorf("status %q must be pending, in_progress, or completed", s)
}

// Ensure UpdatePlan implements the expected interfaces.
var (
	_ Tool    = UpdatePlan{}
	_ Mutator = UpdatePlan{}
)
