package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/todos"
)

// UpdatePlan is the trained checklist-progress tool (Codex's update_plan). It
// is the primary way a model reports plan progress; the [DONE:n] marker
// convention remains as a fallback parser for models that emit markers
// instead of calling the tool.
type UpdatePlan struct{}

// Definition describes the tool to the model. It deliberately documents the
// divergence from Codex: belai accepts update_plan inside plan mode too,
// because its plan pass loop already tracks a planning checklist.
func (UpdatePlan) Definition() Definition {
	return Definition{
		Name: "update_plan",
		Description: "Report progress against the checklist of steps you are executing. " +
			"Each step carries a status: pending, in_progress, or completed. Keep it " +
			"current as you work — it tracks work, it does not replace it. " +
			"(Divergence from Codex: this tool is also accepted in plan mode, where the " +
			"checklist being tracked is the planning one.)",
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

// Execute validates the checklist and returns a shaped, harness-composed
// progress summary. The full checklist is carried in Meta so the agent loop
// can adopt it into the shared todo list.
func (UpdatePlan) Execute(ctx context.Context, args map[string]any) (Result, error) {
	list, err := ParsePlanArg(args)
	if err != nil {
		return Result{}, err
	}
	done := 0
	for _, it := range list.Items {
		if it.Status == todos.StatusDone {
			done++
		}
	}
	return Result{
		Kind:    KindUpdatePlan,
		Content: fmt.Sprintf("plan updated: %d/%d", done, len(list.Items)),
		Meta:    map[string]any{"todos": list},
	}, nil
}

// planKeys are the argument names a model may use for the checklist itself.
// Codex's name is "plan"; the rest are what models actually send when they
// paraphrase the schema.
var planKeys = []string{"plan", "steps", "todos", "items", "tasks", "checklist"}

// stepKeys are the per-entry names a model may use for the step text. "step"
// is the trained one; the others cost nothing to accept and are the difference
// between a tracked checklist and a rejected call.
var stepKeys = []string{"step", "description", "content", "text", "title", "task", "name", "item", "label", "summary"}

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
	raw, ok := planEntries(args)
	if !ok {
		return todos.List{}, fmt.Errorf("plan must be a non-empty array of steps, each with a step string and a status of pending, in_progress or completed")
	}
	var items []todos.Item
	for i, r := range raw {
		step, status := planEntry(r)
		if step == "" {
			return todos.List{}, fmt.Errorf("plan[%d] has no step text: each entry needs a step string and a status of pending, in_progress or completed", i)
		}
		items = append(items, todos.Item{N: len(items) + 1, Text: step, Status: status})
	}
	if len(items) == 0 {
		return todos.List{}, fmt.Errorf("plan must be a non-empty array of steps")
	}
	return todos.List{Items: items}, nil
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
	_ Tool    = UpdatePlan{}
	_ Mutator = UpdatePlan{}
)
