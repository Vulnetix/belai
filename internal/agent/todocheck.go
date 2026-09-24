package agent

import (
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/todos"
)

// todoCheck renders the TODO progress check every loop pass carries beside its
// directive: where the tracked list stands, and the instruction to bring it up
// to date with update_plan in the same response as the tool calls that
// advance it. It is fixed wording plus harness-computed counts, so it is safe
// to seal; the model-authored step text rides todoNote instead.
func todoCheck(list todos.List, has bool) string {
	if !has || len(list.Items) == 0 {
		return "TODO check: no step list is tracked yet. In the same response as your next tool calls, call update_plan with the steps you will execute, the first in_progress."
	}
	done, active := 0, 0
	for _, it := range list.Items {
		switch it.Status {
		case todos.StatusDone:
			done++
		case todos.StatusActive:
			active++
		}
	}
	if done == len(list.Items) {
		return "TODO check: every step is marked done. Correct the list with update_plan only if a step is wrong or missing, and focus this pass's tool calls on confirming the work or finishing."
	}
	return fmt.Sprintf("TODO check: %d of %d steps done, %d in progress. In the same response as your next tool calls, call update_plan to bring the list up to date: mark finished steps completed and the step you are working on in_progress (a [DONE:n] marker in your reply also completes step n). Then spend this pass on the tool calls that complete the next unfinished step. Do not reply with a list update alone.", done, len(list.Items), active)
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
	var parts []string
	for _, n := range append(notes, todoNote(list, has)) {
		if strings.TrimSpace(n) != "" {
			parts = append(parts, n)
		}
	}
	return directiveTurnsWithNote(body+"\n\n"+todoCheck(list, has), strings.Join(parts, "\n\n"))
}
