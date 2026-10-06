package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/todos"
)

// UpdatePlan is the trained checklist-progress tool (Codex's update_plan). It
// is plan mode's way of reporting research progress; goal, agent and code mode
// use the Todo tool instead (todo.go), which shares this file's parser. The
// [DONE:n] marker convention remains as a fallback parser for models that emit
// markers instead of calling the tool.
type UpdatePlan struct{}

// PlanOnly keeps update_plan off the goal, agent and code surfaces: outside
// plan mode the checklist is a Todo list and the model calls Todo.
func (UpdatePlan) PlanOnly() bool { return true }

// Definition describes the tool to the model. It deliberately documents the
// divergence from Codex: belai offers update_plan in plan mode only, where its
// plan pass loop tracks a planning checklist.
func (UpdatePlan) Definition() Definition {
	return Definition{
		Name: "update_plan",
		Description: "Report progress against the checklist of steps you are executing. " +
			"Each step carries a status: pending, in_progress, or completed. Keep it " +
			"current as you work — it tracks work, it does not replace it. " +
			"(Divergence from Codex: this tool is offered in plan mode only, where the " +
			"checklist being tracked is the planning one. It is optional and holds short research steps only: " +
			"the plan itself goes to ExitPlanMode, never into update_plan, and update_plan belongs in the same response as other calls.)",
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
func (UpdatePlan) Kind() Kind { return KindTodo }

// Subject has no permission subject.
func (UpdatePlan) Subject(args map[string]any) string { return "" }

// Mutates reports false: this tool performs no workspace I/O.
func (UpdatePlan) Mutates() bool { return false }

// Execute validates the checklist and returns a shaped, harness-composed
// progress summary.
func (UpdatePlan) Execute(ctx context.Context, args map[string]any) (Result, error) {
	list, err := ParsePlanArg(args)
	if err != nil {
		return Result{}, err
	}
	return checklistResult(list, "plan"), nil
}

// checklistResult is the harness-composed summary both checklist tools return.
// The full list rides in Meta so the agent loop can adopt it into the shared
// todo list.
func checklistResult(list todos.List, noun string) Result {
	done := 0
	for _, it := range list.Items {
		if it.Status == todos.StatusDone {
			done++
		}
	}
	return Result{
		Kind:    KindTodo,
		Content: fmt.Sprintf("%s updated: %d/%d", noun, done, len(list.Items)),
		Meta:    map[string]any{"todos": list},
	}
}

// planKeys are the argument names a model may use for the checklist itself.
// Codex's name is "plan"; the rest are what models actually send when they
// paraphrase the schema.
var planKeys = []string{"plan", "steps", "todos", "items", "tasks", "checklist"}

// stepKeys are the per-entry names a model may use for the step text. "step"
// is the trained one; the others cost nothing to accept and are the difference
// between a tracked checklist and a rejected call.
var stepKeys = []string{"step", "content", "description", "text", "title", "task", "name", "item", "label", "summary", "explanation"}

// statusKeys are the per-entry names a model may use for the step status.
var statusKeys = []string{"status", "state", "progress"}

// ParsePlanArg extracts the checklist from an update_plan argument map. It is
// the single definition of the accepted shape, shared by the tool and by the
// agent loop that adopts the list, so the two can never disagree about whether
// a call was usable.
//
// It is deliberately lenient about spelling and strict about substance: a step
// needs text, and a status it cannot read is pending rather than an error. A
// rejected update_plan costs a whole iteration and teaches the model nothing,
// and the checklist is bookkeeping — the harness measures progress from files
// on disk, never from this list.
func ParsePlanArg(args map[string]any) (todos.List, error) {
	return parseChecklist(args, "plan", "step")
}

// ParseTodoArg is ParsePlanArg for the Todo tool: the same accepted shapes,
// worded for a todo list, so a rejected call names the fields Todo declares.
func ParseTodoArg(args map[string]any) (todos.List, error) {
	return parseChecklist(args, "todos", "content")
}

// parseChecklist reads the checklist under any accepted name. listName and
// field are the names the calling tool declares, used only in the error.
func parseChecklist(args map[string]any, listName, field string) (todos.List, error) {
	raw, ok := planEntries(args)
	if !ok {
		return todos.List{}, fmt.Errorf("%s must be a non-empty array, each entry with a %s string and a status of pending, in_progress or completed", listName, field)
	}
	var items []todos.Item
	for i, r := range raw {
		step, status := planEntry(r)
		if step == "" {
			return todos.List{}, fmt.Errorf("%s[%d] has no %s text: each entry needs a %s string and a status of pending, in_progress or completed%s", listName, i, field, field, entryKeys(r))
		}
		items = append(items, todos.Item{N: len(items) + 1, Text: step, Status: status})
	}
	if len(items) == 0 {
		return todos.List{}, fmt.Errorf("%s must be a non-empty array", listName)
	}
	return todos.List{Items: items}, nil
}

// entryKeys names the keys of a rejected entry so the model can see which
// field it sent. Key names are the model's own text, so they are reduced to
// identifier characters and capped before they ride in an error.
func entryKeys(r any) string {
	m, ok := r.(map[string]any)
	if !ok || len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		clean := strings.Map(func(c rune) rune {
			if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				return c
			}
			return -1
		}, k)
		if clean != "" {
			keys = append(keys, clean)
		}
	}
	sort.Strings(keys)
	if len(keys) > 8 {
		keys = keys[:8]
	}
	return " (keys seen: " + strings.Join(keys, ", ") + ")"
}

// planEntries finds the checklist array under any of the accepted argument
// names, decoding a JSON string first: providers that serialise tool arguments
// as strings deliver the array that way.
func planEntries(args map[string]any) ([]any, bool) {
	for _, key := range planKeys {
		v, present := args[key]
		if !present {
			continue
		}
		if list, ok := v.([]any); ok && len(list) > 0 {
			return list, true
		}
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			continue
		}
		var decoded []any
		if err := json.Unmarshal([]byte(s), &decoded); err == nil && len(decoded) > 0 {
			return decoded, true
		}
	}
	return nil, false
}

// planEntry reads one checklist entry. An entry may be an object under any of
// the accepted step/status names, or a bare string, which is a pending step.
func planEntry(r any) (string, todos.Status) {
	switch v := r.(type) {
	case string:
		return strings.TrimSpace(v), todos.StatusPending
	case map[string]any:
		var step string
		for _, key := range stepKeys {
			if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
				step = strings.TrimSpace(s)
				break
			}
		}
		var status string
		for _, key := range statusKeys {
			if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
				status = s
				break
			}
		}
		return step, mapStatus(status)
	}
	return "", todos.StatusPending
}

// mapStatus maps the Codex status vocabulary, and the paraphrases models
// reach for, onto the todos statuses. An unreadable status is pending: the
// checklist is bookkeeping, and refusing the whole call over one word would
// cost an iteration and tell the model nothing it could act on.
func mapStatus(s string) todos.Status {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "-", "_"))) {
	case "in_progress", "inprogress", "active", "doing", "started", "current", "working":
		return todos.StatusActive
	case "completed", "complete", "done", "finished", "closed":
		return todos.StatusDone
	}
	return todos.StatusPending
}

// Ensure UpdatePlan implements the expected interfaces.
var (
	_ Tool     = UpdatePlan{}
	_ Mutator  = UpdatePlan{}
	_ PlanOnly = UpdatePlan{}
)
