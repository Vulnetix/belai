package tools

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/todos"
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

func TestUpdatePlanRejectsEmptyAndStepless(t *testing.T) {
	if _, err := (UpdatePlan{}).Execute(nil, map[string]any{"plan": []any{}}); err == nil {
		t.Fatal("empty plan must error")
	}
	if _, err := (UpdatePlan{}).Execute(nil, map[string]any{}); err == nil {
		t.Fatal("missing plan must error")
	}
	_, err := (UpdatePlan{}).Execute(nil, map[string]any{
		"plan": []any{map[string]any{"status": "pending"}},
	})
	if err == nil || !strings.Contains(err.Error(), "step") {
		t.Fatalf("stepless entry must error naming the step, got %v", err)
	}
}

// A status the harness cannot read is pending, not a rejection: the checklist
// is bookkeeping, and refusing the call over one word costs an iteration and
// tells the model nothing it can act on.
func TestUpdatePlanUnknownStatusIsPending(t *testing.T) {
	res, err := (UpdatePlan{}).Execute(nil, map[string]any{
		"plan": []any{map[string]any{"step": "x", "status": "bogus"}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	list := res.Meta["todos"].(todos.List)
	if list.Items[0].Status != todos.StatusPending {
		t.Fatalf("status = %q, want pending", list.Items[0].Status)
	}
}

// Models paraphrase the schema. Every shape below is one a model actually
// reaches for, and rejecting it costs a whole iteration.
func TestParsePlanArgAcceptsModelParaphrases(t *testing.T) {
	cases := []struct {
		name   string
		args   map[string]any
		texts  []string
		status []todos.Status
	}{
		{
			name:   "description and state keys",
			args:   map[string]any{"plan": []any{map[string]any{"description": "design", "state": "in-progress"}}},
			texts:  []string{"design"},
			status: []todos.Status{todos.StatusActive},
		},
		{
			name:   "bare strings",
			args:   map[string]any{"plan": []any{"design", "implement"}},
			texts:  []string{"design", "implement"},
			status: []todos.Status{todos.StatusPending, todos.StatusPending},
		},
		{
			name:   "steps instead of plan",
			args:   map[string]any{"steps": []any{map[string]any{"title": "design", "status": "done"}}},
			texts:  []string{"design"},
			status: []todos.Status{todos.StatusDone},
		},
		{
			name:   "plan serialised as a JSON string",
			args:   map[string]any{"plan": `[{"step":"design","status":"completed"}]`},
			texts:  []string{"design"},
			status: []todos.Status{todos.StatusDone},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list, err := ParsePlanArg(tc.args)
			if err != nil {
				t.Fatalf("ParsePlanArg: %v", err)
			}
			if len(list.Items) != len(tc.texts) {
				t.Fatalf("items = %d, want %d", len(list.Items), len(tc.texts))
			}
			for i, it := range list.Items {
				if it.Text != tc.texts[i] || it.Status != tc.status[i] {
					t.Fatalf("item %d = %q/%q, want %q/%q", i, it.Text, it.Status, tc.texts[i], tc.status[i])
				}
				if it.N != i+1 {
					t.Fatalf("item %d has N = %d", i, it.N)
				}
			}
		})
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
