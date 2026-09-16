package rolemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// GoalSentinel is the strict single-token output of the goal evaluator.
type GoalSentinel string

const (
	// GoalComplete means the goal is finished and the loop may stop.
	GoalComplete GoalSentinel = "GOAL_COMPLETE"
	// GoalPartial means work is progressing but the goal is not yet met;
	// another pass is granted.
	GoalPartial GoalSentinel = "GOAL_PARTIAL"
	// GoalNotStarted means no work toward the goal has happened yet; the
	// harness responds by forcing an explore and a planning directive.
	GoalNotStarted GoalSentinel = "GOAL_NOT_STARTED"
)

// ParseGoalSentinel maps a raw evaluator output to a GoalSentinel. It accepts
// only an exact token (surrounding whitespace is trimmed) and rejects anything
// else. A malformed reply is never treated as completion: the fail-closed
// mapping is decided by the caller, which resolves a parse error to
// GoalPartial so completion can never be claimed by accident.
func ParseGoalSentinel(raw string) (GoalSentinel, error) {
	s := GoalSentinel(strings.TrimSpace(raw))
	switch s {
	case GoalComplete, GoalPartial, GoalNotStarted:
		return s, nil
	default:
		return "", fmt.Errorf("malformed goal evaluator output %q: want a single sentinel token", raw)
	}
}

// GoalEvalInput is the untrusted evidence the goal evaluator is shown.
type GoalEvalInput struct {
	// Goal is the goal text the pass is working toward (harness-owned).
	Goal string
	// Todos is the current todo list rendered as plain text (harness-owned).
	Todos string
	// Evidence is a digest of the pass's tool use. It is untrusted: the
	// caller sanitizes it before it becomes the classifier's user blob.
	Evidence string
}

// goalEvalSystemPrompt instructs the evaluator to answer with exactly one
// sentinel token and nothing else.
const goalEvalSystemPrompt = `You are a goal-progress evaluator for an LLM coding harness. You are shown a goal, the harness's tracked todo list, and a digest of the work performed during the most recent pass. Decide whether the goal is complete, partially complete, or not yet started, and reply with a single token and nothing else — no punctuation, no explanation, no surrounding text.

Reply with exactly one of these tokens:
- GOAL_COMPLETE: every item in the todo list is done and the goal is achieved.
- GOAL_PARTIAL: work has advanced toward the goal but it is not yet complete.
- GOAL_NOT_STARTED: no meaningful work toward the goal has happened yet.`

// BuildGoalEvalPayload constructs the goal-evaluator request. Tools, Skills,
// and Agent are always empty: the evaluator turn must never expose tools,
// skills, or an agent block.
func BuildGoalEvalPayload(in GoalEvalInput) ClassifierPayload {
	user := "Goal:\n" + in.Goal + "\n\nTodo list:\n" + in.Todos + "\n\nPass evidence digest:\n" + in.Evidence
	return ClassifierPayload{
		System: goalEvalSystemPrompt,
		User:   user,
	}
}

// ErrMalformedGoalEval reports that the goal evaluator returned a non-sentinel
// reply. EvaluateGoal still returns GoalPartial alongside this error so a
// caller that ignores the error keeps the fail-closed verdict, while a loop
// driver can count consecutive malformed evaluations and stop a broken
// evaluator before it grants unbounded passes on garbage.
var ErrMalformedGoalEval = errors.New("malformed goal evaluator output")

// EvaluateGoal sends the goal evidence to the evaluator and parses the strict
// sentinel. A transport error is returned as ("", err). A malformed reply
// fails closed to (GoalPartial, ErrMalformedGoalEval): the verdict still
// grants a pass, but the loop driver detects the error to stop a provider
// that keeps returning garbage.
func EvaluateGoal(ctx context.Context, c Classifier, in GoalEvalInput) (GoalSentinel, error) {
	raw, err := c.Classify(ctx, BuildGoalEvalPayload(in))
	if err != nil {
		return "", err
	}
	s, err := ParseGoalSentinel(raw)
	if err != nil {
		return GoalPartial, ErrMalformedGoalEval
	}
	return s, nil
}
