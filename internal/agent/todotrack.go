package agent

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/todos"
)

// The turn's todo list. Goal mode keeps its own ledger and plan mode its own,
// but agent and code mode keep none, and a pass that ends on a text-only reply
// dropped the list the model had reported. The tracker holds it for the whole
// turn, across every pass, so any mode can hold the turn to it: a reply that
// leaves a todo open is sent back (refuseOpenTodos), and the turn's list reaches
// the session record even when the turn is one pass long.
//
// It is fed from the model's own Todo and update_plan calls and from [DONE:n]
// markers in its own assistant text, never from a tool result, so a file that
// holds "[DONE:1]" cannot complete anything (todos.List.ApplyMarkers).

// maxTodoReprompts bounds how many times one turn is sent back for open todos.
// The bound is the harness's, not the model's: a model that will not finish its
// list is stopped and reported on, not argued with forever.
const maxTodoReprompts = 2

// openTodoDirective is the sealed instruction that sends a turn back. The
// counts are the harness's; the todo text rides the check's note.
const openTodoDirective = "You replied without a tool call, but %d of %d todos are still open. Finish them with tool calls now. If one no longer applies, mark it completed with Todo and say why in the final report. Do not stop with todos open, and do not give the final report until the list is done."

func (s *Session) resetTodoTracking() {
	s.turnTodos, s.turnHasTodos, s.turnTodosDirty, s.todoReprompts = todos.List{}, false, false, 0
}

// adoptTodos folds a list the model reported into the turn's list.
func (s *Session) adoptTodos(l todos.List) {
	if len(l.Items) == 0 {
		return
	}
	if !s.turnHasTodos {
		s.turnTodos, s.turnHasTodos = todos.New("", nil), true
	}
	s.turnTodos.Adopt(l.Items)
	s.turnTodosDirty = true
}

// noteAssistantText applies the [DONE:n] markers of the model's own reply.
func (s *Session) noteAssistantText(text string) {
	if !s.turnHasTodos || !strings.Contains(text, "[DONE:") {
		return
	}
	s.turnTodos.ApplyMarkers(text)
	s.turnTodosDirty = true
}

// flushTodos tells the UI and the session record the list changed.
func (s *Session) flushTodos(emit func(Event)) {
	if !s.turnTodosDirty || !s.turnHasTodos {
		return
	}
	s.turnTodosDirty = false
	list := s.turnTodos
	emit(Event{Kind: EventTodosKind, Todos: &list})
}

// refuseOpenTodos reports whether a text-only reply should be sent back
// because the turn's list still has open items, and how many. Plan mode is
// excluded (its list is research bookkeeping and ExitPlanMode ends it), as are
// the passes that must not loop or must not call tools: an explore subagent, a
// report-only turn, the kanban wrap-up, a read-only agent turn, and a session
// that cannot run a loop at all (a subagent).
func (s *Session) refuseOpenTodos(mode modes.Mode) (open, total int, refuse bool) {
	if !s.turnHasTodos || s.todoReprompts >= maxTodoReprompts || !s.allowPassLoop {
		return 0, 0, false
	}
	if mode == modes.ModePlan || s.planMode || s.turnReadOnly || s.exploreSubagent || s.reportOnly || s.kanbanWrapUpPass {
		return 0, 0, false
	}
	o := s.turnTodos.Open()
	if len(o) == 0 {
		return 0, 0, false
	}
	s.todoReprompts++
	return len(o), len(s.turnTodos.Items), true
}

// openTodoTurns is the instruction that sends the turn back for its open todos.
func (s *Session) openTodoTurns(open, total int) []run.Turn {
	return withTodoCheck(fmt.Sprintf(openTodoDirective, open, total), s.turnTodos, true)
}
