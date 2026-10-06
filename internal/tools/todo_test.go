package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/todos"
)

// The call that stalled a Code mode session: the model put `explanation` where
// the step text goes. It parses now, and the schema it should have followed
// reaches the model.
func TestTodoAcceptsTheCallThatUsedToFail(t *testing.T) {
	res, err := (Todo{}).Execute(nil, map[string]any{"todos": []any{
		map[string]any{"explanation": "Investigate the repository.", "status": "in_progress"},
		map[string]any{"content": "Fix the cache", "status": "pending"},
	}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	list := res.Meta["todos"].(todos.List)
	if len(list.Items) != 2 || list.Items[0].Text != "Investigate the repository." || list.Items[0].Status != todos.StatusActive {
		t.Fatalf("list = %+v", list)
	}
	if !strings.Contains(res.Content, "todo list updated: 0/2") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestTodoRejectionNamesTodoFieldsAndKeys(t *testing.T) {
	_, err := (Todo{}).Execute(nil, map[string]any{"todos": []any{map[string]any{"status": "pending", "bogus": "x"}}})
	if err == nil {
		t.Fatal("an entry with no text must be rejected")
	}
	for _, want := range []string{"todos[0]", "content", "keys seen: bogus, status"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q is missing %q", err, want)
		}
	}
	if _, err := (Todo{}).Execute(nil, map[string]any{}); err == nil || !strings.Contains(err.Error(), "todos must be a non-empty array") {
		t.Fatalf("empty call error = %v", err)
	}
}

// Nested shapes reach the wire: without them a model sees a bare array and
// invents the entry fields.
func TestSchemaCarriesNestedShapes(t *testing.T) {
	for _, tc := range []struct {
		def   Definition
		list  string
		field string
	}{
		{Todo{}.Definition(), "todos", "content"},
		{UpdatePlan{}.Definition(), "plan", "step"},
	} {
		props := tc.def.Schema()["properties"].(map[string]any)
		arr := props[tc.list].(map[string]any)
		items, ok := arr["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s: %s has no items in its schema: %v", tc.def.Name, tc.list, arr)
		}
		itemProps := items["properties"].(map[string]any)
		if _, ok := itemProps[tc.field]; !ok {
			t.Fatalf("%s: item properties = %v, want %q", tc.def.Name, itemProps, tc.field)
		}
		if _, ok := itemProps["status"]; !ok {
			t.Fatalf("%s: item properties lack status", tc.def.Name)
		}
		if req, _ := items["required"].([]string); len(req) != 2 {
			t.Fatalf("%s: item required = %v", tc.def.Name, items["required"])
		}
	}
	ask := AskUserQuestion{}.Definition().Schema()["properties"].(map[string]any)
	if q, ok := ask["questions"].(map[string]any); !ok || q["items"] == nil {
		t.Fatalf("AskUserQuestion questions lost their item shape: %v", ask["questions"])
	}
	// A flat tool keeps its flat schema.
	flat := (&Cd{}).Definition().Schema()["properties"].(map[string]any)
	for name, p := range flat {
		if _, nested := p.(map[string]any)["items"]; nested {
			t.Fatalf("flat property %s gained items", name)
		}
	}
}

// Todo belongs to goal, agent and code mode and update_plan to plan mode: each
// surface advertises one of them, and a profile allowlist naming either keeps
// the one its mode offers.
func TestChecklistToolIsPerMode(t *testing.T) {
	reg := NewRegistry(Todo{}, UpdatePlan{}, ExitPlanMode{})
	names := func(r *Registry) string { return strings.Join(r.Names(), ",") }

	if got := names(reg.WithoutPlanOnly()); got != "Todo" {
		t.Fatalf("agent surface = %s, want Todo", got)
	}
	got := names(reg.PlanWith(PlanSurface{}))
	if strings.Contains(got, "Todo") || !strings.Contains(got, "update_plan") {
		t.Fatalf("plan surface = %s, want update_plan and no Todo", got)
	}
	if got := names(reg.PlanWith(PlanSurface{GuardrailsOff: true})); strings.Contains(got, "Todo") {
		t.Fatalf("plan surface with guardrails off = %s, must not offer Todo", got)
	}
	if got := names(reg.Only("update_plan").WithoutPlanOnly()); got != "Todo" {
		t.Fatalf("a profile naming update_plan keeps Todo in agent mode, got %s", got)
	}
	if got := names(reg.Only("Todo").PlanWith(PlanSurface{})); got != "update_plan" {
		t.Fatalf("a profile naming Todo keeps update_plan in plan mode, got %s", got)
	}
}

func TestScriptsCannotCallTheChecklistTools(t *testing.T) {
	if NestedAllowed(Todo{}) || NestedAllowed(UpdatePlan{}) {
		t.Fatal("a script must not drive the checklist")
	}
}

func TestCodeScriptErrorIsARejectedCall(t *testing.T) {
	c := Code{Run: func(_ context.Context, script string) (string, error) {
		return "partial", errors.New("ReferenceError: x is not defined")
	}}
	res, err := c.Execute(context.Background(), map[string]any{"code": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Content, RejectedPrefix) || !strings.Contains(res.Content, "partial") {
		t.Fatalf("content = %q", res.Content)
	}
}
