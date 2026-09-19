package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/sanitize"
)

// PlanOnly is a marker implemented by tools that must only appear in the
// tool surface when the session is in plan mode. It keeps tools that are
// harmless in themselves but semantically meaningless (or worse, confusing)
// outside the plan pass loop out of agent and goal mode briefings.
type PlanOnly interface{ PlanOnly() bool }

// ExitPlanMode is the model's way of declaring a plan-mode turn complete.
// Calling it does not perform any workspace mutation: it is a signal to the
// harness that the plan in the plan argument should be handed to the user for
// review.
type ExitPlanMode struct{}

// ExitPlanModeSentinel is the fixed result string returned by the tool. It is
// not rendered as a normal tool result; passLoop recognises the call and
// short-circuits the remaining pass iterations.
const ExitPlanModeSentinel = "PLAN_EXIT"

// Definition describes the tool to the model.
func (ExitPlanMode) Definition() Definition {
	return Definition{
		Name: "ExitPlanMode",
		Description: "Declare the plan finished and hand it to the user for review. " +
			"The full plan goes in the plan argument as markdown with the sections " +
			"## Summary, ## Steps (numbered, with optional - Files: and - Verify: sub-bullets), " +
			"## Test Plan, ## Assumptions, and ## Risks. Your reply text is NOT the plan.",
		Properties: map[string]Property{
			"plan": {Type: "string", Description: "The full plan in markdown. Required."},
		},
		Required: []string{"plan"},
	}
}

// Kind returns a read-only kind so the registry keeps the tool through the
// ReadOnly() narrowing and so plan mode can advertise it.
func (ExitPlanMode) Kind() Kind { return KindRead }

// Subject returns the permission subject; as a read-only meta tool it has none.
func (ExitPlanMode) Subject(args map[string]any) string { return "" }

// Mutates reports false: this tool performs no I/O.
func (ExitPlanMode) Mutates() bool { return false }

// PlanOnly marks the tool so it can be filtered out of non-plan briefings.
func (ExitPlanMode) PlanOnly() bool { return true }

// Execute validates and returns the sentinel the pass loop watches for. The
// plan argument is sanitised and parsed; an empty plan or one with zero
// numbered steps is a tool error, so the model retries in the same pass.
func (ExitPlanMode) Execute(ctx context.Context, args map[string]any) (Result, error) {
	plan, _ := args["plan"].(string)
	if strings.TrimSpace(plan) == "" {
		return Result{}, fmt.Errorf("the plan argument is required: put the full plan in plan")
	}
	clean := sanitize.Sanitize(plan)
	if _, err := plans.ParseDoc(clean); err != nil {
		return Result{}, fmt.Errorf("invalid plan: %v", err)
	}
	return Result{Content: ExitPlanModeSentinel}, nil
}

// Ensure ExitPlanMode implements the marker and permission interfaces.
var (
	_ Tool     = ExitPlanMode{}
	_ PlanOnly = ExitPlanMode{}
	_ Mutator  = ExitPlanMode{}
)
