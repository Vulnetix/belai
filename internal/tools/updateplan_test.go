package tools

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/todos"
)

func TestUpdatePlanStatusMapping(t *testing.T) {
	res, err := (UpdatePlan{}).Execute(nil, map[string]any{
		"plan": []any{
			map[string]any{"step": "design", "status": "completed"},
			map[string]any{"step": "implement", "status": "in_progress"},
			map[string]any{"step": "test", "status": "pending"},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "plan updated: 1/3" {
		t.Fatalf("Content = %q, want %q", res.Content, "plan updated: 1/3")
	}
	list, ok := res.Meta["todos"].(todos.List)
	if !ok {
		t.Fatalf("Meta todos missing: %v", res.Meta)
	}
	if len(list.Items) != 3 {
		t.Fatalf("items = %d", len(list.Items))
	}
	if list.Items[0].Status != todos.StatusDone || list.Items[1].Status != todos.StatusActive || list.Items[2].Status != todos.StatusPending {
		t.Fatalf("statuses = %v", list.Items)
	}
}

func TestUpdatePlanRejectsEmptyAndBadStatus(t *testing.T) {
	if _, err := (UpdatePlan{}).Execute(nil, map[string]any{"plan": []any{}}); err == nil {
		t.Fatal("empty plan must error")
	}
	_, err := (UpdatePlan{}).Execute(nil, map[string]any{
		"plan": []any{map[string]any{"step": "x", "status": "bogus"}},
	})
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("bad status must error, got %v", err)
	}
}

func TestUpdatePlanIsReadOnlyAndSanitiseOnly(t *testing.T) {
	if !KindUpdatePlan.ReadOnly() {
		t.Fatal("update_plan must be read-only")
	}
	if KindUpdatePlan.NeedsClassifier() {
		t.Fatal("update_plan result must skip the classifier")
	}
}

func TestExitPlanModeRequiresValidPlan(t *testing.T) {
	if _, err := (ExitPlanMode{}).Execute(nil, map[string]any{}); err == nil {
		t.Fatal("missing plan must error")
	}
	if _, err := (ExitPlanMode{}).Execute(nil, map[string]any{"plan": "   "}); err == nil {
		t.Fatal("blank plan must error")
	}
	_, err := (ExitPlanMode{}).Execute(nil, map[string]any{"plan": "## Summary\n\nNo steps.\n"})
	if err == nil {
		t.Fatal("stepless plan must error")
	}

	res, err := (ExitPlanMode{}).Execute(nil, map[string]any{"plan": "## Summary\n\nDo it.\n\n## Steps\n\n1. First\n"})
	if err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	if res.Content != ExitPlanModeSentinel {
		t.Fatalf("Content = %q, want sentinel", res.Content)
	}
}
