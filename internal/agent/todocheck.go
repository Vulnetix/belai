package agent

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/todos"
)

// todoCheck renders the TODO progress check every goal and agent pass carries
// beside its directive: where the todo list stands, and the instruction to bring
// it up to date with Todo in the same response as the tool calls that advance
// it. It is fixed wording plus harness-computed counts, so it is safe to seal;
// the model-authored todo text rides todoNote instead. Plan mode has its own
// wording (planCheck) because its checklist is the planning one.
func todoCheck(list todos.List, has bool) string {
	if !has || len(list.Items) == 0 {
		return "TODO check: no todo list is tracked yet. In the same response as your next tool calls, call Todo with the todos you will do, the first in_progress."
	}
	done, active := countTodos(list)
	if done == len(list.Items) {
		return "TODO check: every todo is marked completed. Correct the list with Todo only if a todo is wrong or missing, and focus this pass's tool calls on confirming the work or finishing, then give the final report."
	}
	return fmt.Sprintf("TODO check: %d of %d todos done, %d in progress. Mark finished todos with a [DONE:n] marker in the text of the response that carries your next tool calls (call Todo only when the todos themselves change), then spend this pass on the tool calls that complete the next unfinished todo. Do not reply with a list update alone.", done, len(list.Items), active)
}

// countTodos returns how many items are done and how many are active.
func countTodos(list todos.List) (done, active int) {
	for _, it := range list.Items {
		switch it.Status {
		case todos.StatusDone:
			done++
		case todos.StatusActive:
			active++
		}
	}
	return done, active
}

// planCheck is todoCheck for plan mode, whose checklist is the planning one and
// is reported with update_plan.
func planCheck(list todos.List, has bool) string {
	if !has || len(list.Items) == 0 {
		return "PLAN check: no research steps are tracked yet. In the same response as your next tool calls, call update_plan with the steps you will take, the first in_progress."
	}
	done, active := countTodos(list)
	if done == len(list.Items) {
		return "PLAN check: every research step is marked done. Correct the list with update_plan only if a step is wrong or missing, and focus this pass on writing the plan."
	}
	return fmt.Sprintf("PLAN check: %d of %d research steps done, %d in progress. Mark finished steps with a [DONE:n] marker in the text of the response that carries your next tool calls (call update_plan only when the steps themselves change). Do not reply with a list update alone.", done, len(list.Items), active)
}

// todoNote renders the tracked list for the directive's note. Step text is
// model-authored, so it never enters the sealed body.
func todoNote(list todos.List, has bool) string {
	if !has || len(list.Items) == 0 {
		return ""
	}
	return "Current TODO list:\n" + list.Render()
}

// withTodoCheck frames a pass-boundary directive with the TODO progress check
// appended to its sealed body and the current list (plus any extra
// model-derived note) carried as plain, sanitised note text.
func withTodoCheck(body string, list todos.List, has bool, notes ...string) []run.Turn {
	return withCheck(todoCheck(list, has), body, list, has, notes...)
}

// withPlanCheck is withTodoCheck for plan mode.
func withPlanCheck(body string, list todos.List, has bool, notes ...string) []run.Turn {
	return withCheck(planCheck(list, has), body, list, has, notes...)
}

func withCheck(check, body string, list todos.List, has bool, notes ...string) []run.Turn {
	var parts []string
	for _, n := range append(notes, todoNote(list, has)) {
		if strings.TrimSpace(n) != "" {
			parts = append(parts, n)
		}
	}
	return directiveTurnsWithNote(body+"\n\n"+check, strings.Join(parts, "\n\n"))
}
