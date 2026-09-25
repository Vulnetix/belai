package prompt

import (
	"strings"
	"testing"
)

func TestPlanDirectiveCadence(t *testing.T) {
	// Full on pass 1, reminder after, full again every five passes.
	for _, pass := range []int{1, 6, 11} {
		if got := PlanDirective(pass); got != PlanContract {
			t.Fatalf("pass %d: want full contract, got %q", pass, got)
		}
	}
	for _, pass := range []int{2, 3, 4, 5, 7} {
		if got := PlanDirective(pass); got != PlanReminder {
			t.Fatalf("pass %d: want reminder, got %q", pass, got)
		}
	}
}

// A plan executes after approval, where nobody answers a question and there is
// no plan mode to exit; a step that does either can never complete, and the
// execute loop spun on one. The contract also sizes the plan to the task.
func TestPlanContractForbidsUnexecutableStepsAndSizesToTask(t *testing.T) {
	for _, want := range []string{"Never write a step\nthat asks the user", "exits plan mode", "Size the plan and the research to the task"} {
		if !strings.Contains(PlanContract, want) {
			t.Fatalf("contract missing %q", want)
		}
	}
}

func TestPlanContractCoversThreePhases(t *testing.T) {
	for _, want := range []string{"Phase 1", "Phase 2", "Phase 3", "## Summary", "## Key Changes", "## Test Plan", "## Assumptions", "decision complete"} {
		if !strings.Contains(PlanContract, want) {
			t.Fatalf("contract missing %q", want)
		}
	}
}
