package tools

import (
	"context"
)

// TodoName is the Todo tool's name.
const TodoName = "Todo"

// Todo is the checklist tool of goal, agent and code mode: the model keeps its
// own todo list current with it, and the harness holds the turn to the list
// (an open item at the end of a turn sends the model back for another pass).
// Plan mode has update_plan instead, so the two never share a surface. Both
// parse through parseChecklist and return the same shaped result.
type Todo struct{}

// NonPlan keeps Todo off the plan-mode surface (Registry.PlanWith).
func (Todo) NonPlan() bool { return true }

// NonPlan is a marker implemented by tools that belong to every mode except
// plan mode. Plan mode drops them from its surface.
type NonPlan interface{ NonPlan() bool }

// Definition describes the tool to the model. It deliberately diverges from the
// trained update_plan name outside plan mode: the checklist is a todo list there
// and "plan" belongs to plan mode alone.
func (Todo) Definition() Definition {
	return Definition{
		Name: TodoName,
		Description: "Keep your todo list current. Send the whole list every time: one entry per piece of " +
			"work, each with a status of pending, in_progress or completed, and at most one in_progress. " +
			"Call it in the same response as the tool calls that do the work, mark an entry completed as " +
			"soon as it is done, and finish every entry before you give the final report. It tracks work, " +
			"it does not replace it. " +
			"(Divergence from the trained update_plan: goal, agent and code mode use Todo for the checklist, " +
			"and update_plan is for plan mode only.)",
		Properties: map[string]Property{
			"todos": {
				Type:        "array",
				Description: "The whole todo list, in order, with each entry's status.",
				Items: &Property{
					Type: "object",
					Properties: map[string]Property{
						"content": {Type: "string", Description: "What the todo is, in one short line."},
						"status":  {Type: "string", Description: "pending | in_progress | completed"},
					},
					Required: []string{"content", "status"},
				},
			},
		},
		Required: []string{"todos"},
	}
}

// Kind returns KindTodo: read-only and sanitise-only, like update_plan.
func (Todo) Kind() Kind { return KindTodo }

// Subject has no permission subject.
func (Todo) Subject(args map[string]any) string { return "" }

// Mutates reports false: this tool performs no workspace I/O.
func (Todo) Mutates() bool { return false }

// Execute validates the list and returns the harness-composed summary. The
// full list rides in Meta so the agent loop adopts it into the shared list.
func (Todo) Execute(ctx context.Context, args map[string]any) (Result, error) {
	list, err := ParseTodoArg(args)
	if err != nil {
		return Result{}, err
	}
	return checklistResult(list, "todo list"), nil
}

var (
	_ Tool    = Todo{}
	_ Mutator = Todo{}
	_ NonPlan = Todo{}
)
