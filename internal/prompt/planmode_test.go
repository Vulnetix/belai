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

func TestPlanContractCoversThreePhases(t *testing.T) {
	for _, want := range []string{"Phase 1", "Phase 2", "Phase 3", "## Summary", "## Key Changes", "## Test Plan", "## Assumptions", "decision complete"} {
		if !strings.Contains(PlanContract, want) {
			t.Fatalf("contract missing %q", want)
		}
	}
}
