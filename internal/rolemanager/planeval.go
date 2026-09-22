package rolemanager

import (
	"context"
	"errors"
	"fmt"
)

// PlanSentinel is the strict single-token output of the plan evaluator. It is
// deliberately not prefixed GOAL_*: plan mode is its own mode, and reusing the
// goal evaluator's vocabulary would present a plan-mode pass boundary as a
// goal-mode one to the user.
type PlanSentinel string

const (
	// PlanComplete means the plan is researched and ready to execute.
	PlanComplete PlanSentinel = "PLAN_COMPLETE"
	// PlanPartial means the plan has advanced but is not yet ready; another
	// pass is granted.
	PlanPartial PlanSentinel = "PLAN_PARTIAL"
	// PlanNotStarted means no meaningful planning work has happened yet; the
	// harness responds with a planning directive.
	PlanNotStarted PlanSentinel = "PLAN_NOT_STARTED"
)

// ParsePlanSentinel maps a raw evaluator output to a PlanSentinel. It accepts
// a token that stands alone after normalizing away reasoning blocks and
// markdown wrappers, and rejects anything else. A malformed reply is never
// treated as completion: the fail-closed mapping is decided by the caller,
// which resolves a parse error to PlanPartial so completion can never be
// claimed by accident.
func ParsePlanSentinel(raw string) (PlanSentinel, error) {
	s, err := matchSentinel(raw, []string{
		string(PlanComplete),
		string(PlanPartial),
		string(PlanNotStarted),
	})
	if err != nil {
		return "", fmt.Errorf("malformed plan evaluator output %q: want a single sentinel token", raw)
	}
	return PlanSentinel(s), nil
}

// PlanEvalInput is the evidence the plan evaluator is shown.
type PlanEvalInput struct {
	// Context is the read-only exploration context gathered for the session
	// (explore-subagent findings). It has already been classified and admitted
	// as SAFE before it becomes the evaluator's evidence, so it is
	// harness-owned text, never a goal definition — plan mode has none.
	Context string
	// Todos is the current plan todo list rendered as plain text
	// (harness-owned).
	Todos string
	// Evidence is a digest of the pass's tool use. It is untrusted: the
	// caller sanitizes it before it becomes the classifier's user blob.
	Evidence string
}

// planEvalSystemPrompt instructs the evaluator to answer with exactly one
// plan sentinel token and nothing else.
const planEvalSystemPrompt = `You are a plan-progress evaluator for an LLM coding harness. You are shown the read-only exploration context gathered for the session, the harness's tracked plan todo list, and a digest of the work performed during the most recent pass. Decide whether the plan is complete and ready to execute, partially complete, or not yet started, and reply with a single token and nothing else — no punctuation, no explanation, no surrounding text.

Reply with exactly one of these tokens:
- PLAN_COMPLETE: every item in the plan todo list is researched and the plan is ready to execute.
- PLAN_PARTIAL: the plan has advanced but it is not yet complete.
- PLAN_NOT_STARTED: no meaningful planning work has happened yet.`

// BuildPlanEvalPayload constructs the plan-evaluator request. Tools, Skills,
// and Agent are always empty: the evaluator turn must never expose tools,
// skills, or an agent block.
func BuildPlanEvalPayload(in PlanEvalInput) ClassifierPayload {
	user := "Session exploration context:\n" + in.Context + "\n\nPlan todo list:\n" + in.Todos + "\n\nPass evidence digest:\n" + in.Evidence
	return ClassifierPayload{
		System:                 planEvalSystemPrompt,
		User:                   user,
		AllowReasoningFallback: true,
	}
}

// ErrMalformedPlanEval reports that the plan evaluator returned a non-sentinel
// reply. EvaluatePlan still returns PlanPartial alongside this error so a
// caller that ignores the error keeps the fail-closed verdict, while a loop
// driver can count consecutive malformed evaluations and stop a broken
// evaluator before it grants unbounded passes on garbage.
var ErrMalformedPlanEval = errors.New("malformed plan evaluator output")

// EvaluatePlan sends the plan evidence to the evaluator and parses the strict
// sentinel. A transport error is returned as ("", err). A malformed reply
// fails closed to (PlanPartial, ErrMalformedPlanEval): the verdict still
// grants a pass, but the loop driver detects the error to stop a provider
// that keeps returning garbage.
func EvaluatePlan(ctx context.Context, c Classifier, in PlanEvalInput) (PlanSentinel, error) {
	raw, err := c.Classify(ctx, BuildPlanEvalPayload(in))
	if err != nil {
		return "", err
	}
	s, err := ParsePlanSentinel(raw)
	if err != nil {
		record(EventPlanEval, string(PlanPartial), "", "malformed: "+traceSnippet(raw), 0)
		return PlanPartial, ErrMalformedPlanEval
	}
	record(EventPlanEval, string(s), "", "", 0)
	return s, nil
}
